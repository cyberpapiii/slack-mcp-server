# Plan 030: Rate-limiter architecture — findings and a decision to make

> **This is not an executable plan. Do not dispatch an executor against it.**
>
> It is a design write-up: what the rate limiting in this repo actually does
> today, where that diverges from what Slack meters, and four options with a
> recommendation. One finding in it is a concrete bug with a one-line fix, but
> that fix is an **observable behavior change** (it makes channel listing
> roughly ten times slower), so it is presented as a decision rather than
> written up as a build plan. Once the maintainer picks an option, the chosen
> option becomes plan 031+.

## Status

- **Priority**: P2 (one finding), P3 (the rest)
- **Effort**: XS to L depending on option chosen
- **Risk**: MEDIUM — every option here trades throughput for 429 avoidance
- **Depends on**: nothing. Plan 029 fixes one instance of finding 1 inside
  `edge.UsersList`; this document covers the other twelve call sites.
- **Category**: performance / architecture
- **Analyzed at**: commit `b507108`, the Track B tip, 2026-08-07. Every line
  number below was read there. `pkg/limiter/` and all of `pkg/provider/edge/`
  are byte-identical at the Track A tip `420e4d1`; `pkg/handler/*` and
  `pkg/provider/api.go` are **not**, so their line numbers will have shifted
  if you look at them from Track A. Re-grep rather than trusting the numbers.

## What the limiter package is

`pkg/limiter/limits.go` is fifteen lines:

```go
type tier struct {
	// once every
	t time.Duration
	// burst
	b int
}

func (t tier) Limiter() *rate.Limiter {
	return rate.NewLimiter(rate.Every(t.t), t.b)
}

var (
	// tier1 = tier{t: 1 * time.Minute, b: 2}
	Tier2      = tier{t: 3 * time.Second, b: 3}
	Tier2boost = tier{t: 300 * time.Millisecond, b: 5}
	Tier3      = tier{t: 1200 * time.Millisecond, b: 4}
	// tier4      = tier{t: 60 * time.Millisecond, b: 5}
)
```

Sustained rates that works out to:

| Tier | Interval | Burst | Sustained |
|---|---|---|---|
| `Tier2` | 3s | 3 | 20 req/min |
| `Tier3` | 1.2s | 4 | 50 req/min |
| `Tier2boost` | 300ms | 5 | **200 req/min** |

`pkg/limiter/retry.go` adds a generic `CallWithRetry(ctx, rl, maxRetries,
retryAfter, fn)` that waits on the limiter, calls `fn`, and on a retryable
error sleeps for the duration `retryAfter` reports before trying again.

## The thirteen call sites

Enumerated with `grep -rn "limiter\." pkg --include="*.go" | grep -v _test.go`.

| # | Site | Slack method it paces | Tier used | Wrapped in `CallWithRetry`? |
|---|---|---|---|---|
| 1 | `pkg/handler/activity.go:161` | `conversations.replies` | `Tier3` | no — bare `Wait` |
| 2 | `pkg/handler/conversations.go:971` | `search.messages` | `Tier2` | yes |
| 3 | `pkg/handler/conversations.go:1496` | `conversations.history` | `Tier3` | yes |
| 4 | `pkg/handler/conversations.go:1584` | `conversations.info` **and** `conversations.history` — one limiter, two methods (used at `:1665` and `:1726`) | `Tier3` | yes |
| 5 | `pkg/handler/saved.go:64` | edge `saved.list` | `Tier3` | no — bare `Wait` at `:77` and `:112` |
| 6 | `pkg/provider/api.go:1596` | `conversations.list` — the **standard** Web API, via `ap.client.GetConversationsContext` | **`Tier2boost`** | no — bare `Wait` |
| 7 | `pkg/provider/edge/dms.go:41` | edge `client.dms` | `Tier2boost` | no |
| 8 | `pkg/provider/edge/search.go:145` | edge search | `Tier2boost` | no |
| 9 | `pkg/provider/edge/client.go:142` | edge `client.dms` | `Tier2boost` | no |
| 10 | `pkg/provider/edge/userlist.go:123` | edge `users/list` | `Tier3` | no |
| 11 | `pkg/provider/edge/userlist.go:239` | edge `users/list` | `Tier3` | no |
| 12 | `pkg/provider/edge/userlist.go:268` | edge `conversations.view` | `Tier3` | no |
| 13 | *(sites 11 and 12 run concurrently under one `errgroup`)* | — | — | — |

Four of thirteen sites retry on a 429. The other nine call `lim.Wait(ctx)` and
then let a rate-limit error propagate as an ordinary failure.

## Finding 1 — every limiter is per-invocation, so there is no global budget

`tier.Limiter()` calls `rate.NewLimiter`. It returns a **new** limiter on every
call. All thirteen sites call it inside the function that uses it, so each
limiter lives exactly as long as one loop.

Two consequences:

- **No cross-call throttling.** Two tool calls in flight at once get two
  independent budgets. Slack meters per token and per method, so the workspace
  sees the sum.
- **Every invocation starts with a full burst.** A fresh `rate.Limiter` begins
  with `b` tokens available immediately. So the first 3–5 requests of *every*
  handler call are unthrottled, no matter how recently the previous call
  finished. A handler invoked in a tight loop is effectively unlimited.

**Is the concurrency real, or theoretical?** Real. Three independent sources:

- `pkg/provider/api.go:1287` and `pkg/provider/api.go:1495` launch background
  cache-refresh goroutines that run while handlers are serving.
- `pkg/provider/edge/userlist.go:193` fans out into an `errgroup` — two
  goroutines, sites 11 and 12, each with its own `Tier3` limiter, hitting the
  same token at twice the intended rate. (Plan 029 fixes this one instance,
  along with a data race in the same function.)
- `pkg/provider/edge/slacker.go:53` runs a `sync.WaitGroup` fan-out.

## Finding 2 — one limiter is shared across two *different* Slack methods

Site 4, `pkg/handler/conversations.go:1584`, creates a single `Tier3` limiter
and then uses it for both `conversations.info` (`:1665`) and
`conversations.history` (`:1726`).

Slack's budgets are per method. Two methods that are each individually Tier 3
have two separate 50/min allowances, not a shared one. Pacing them through one
limiter **over-throttles** — the code voluntarily runs at half the rate Slack
would permit.

This is the mirror image of finding 3: the same one-limiter-per-call-site habit
is too strict here and too loose there, because the limiter is keyed to a code
location rather than to a Slack method.

## Finding 3 — `conversations.list` is paced at roughly ten times its tier

Site 6 is the sharpest one. `pkg/provider/api.go:1596`:

```go
	lim := limiter.Tier2boost.Limiter()

	for {
		if err := lim.Wait(ctx); err != nil {
			ap.logger.Error("Rate limiter wait failed", zap.Error(err))
			return nil
		}

		channels, nextcur, err = ap.client.GetConversationsContext(ctx, params)
```

`ap.client` is the standard slack-go Web API client, so this is
`conversations.list` — a documented, metered Slack Web API method, not an edge
endpoint. `Tier2boost` paces it at 200 req/min.

`Tier2boost` exists for the undocumented internal edge API, which does not
publish tiers and empirically tolerates more. Applying it to a standard Web API
method is very likely a copy-paste from the edge call sites (7, 8, 9), which
all legitimately use it.

If `conversations.list` is Tier 2 (20+/min), this site is paced about **10×**
over. On a workspace with many channels, `conversations.list` paginates — so
this is exactly the path that would generate a burst of 429s.

**Verify the tier before acting.** The tier assignments in this document are
from Slack's published Web API rate-limit table as I understand it, not from a
fetch of the live docs. Before changing anything on the strength of finding 3,
confirm `conversations.list`'s current tier at
`https://docs.slack.com/apis/web-api/rate-limits`. If it is Tier 3 rather than
Tier 2, the gap is 4× rather than 10× — still wrong, but less urgent.

**This is why finding 3 is not already a build plan.** Correcting it to `Tier2`
would slow channel listing by roughly an order of magnitude on workspaces
large enough to paginate. That is a user-visible latency change, and after the
plan-009 reversal earlier in this session the standing rule is that observable
behavior changes get flagged for a decision before they get planned. Your call.

## Finding 4 — a 429 does not slow anything down

`CallWithRetry` handles a 429 by sleeping for the server's `Retry-After` and
trying again:

```go
		backoff := retryAfter(err)
		if backoff <= 0 {
			return result, err
		}
		if attempt == maxRetries {
			return result, err
		}
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(backoff):
		}
```

The `rate.Limiter` is never adjusted. After the sleep the loop resumes at
exactly the rate that just earned a 429. With `maxRetries` hardcoded to `2` at
all four sites, the pattern under sustained pressure is: hit the wall, wait,
hit the wall, wait, give up.

The giving-up is at least visible — `pkg/handler/conversations.go:1471` surfaces
a real warning to the caller:

> `WARNING: %d channels were skipped due to Slack rate limiting (even after retries) — results are degraded. Try again after a brief cooldown.`

So the failure mode is honest. It is just not self-correcting.

Related, smaller: there is no jitter anywhere. Goroutines that block on the
same `Retry-After` wake together and retry in lockstep.

## Finding 5 — the edge layer already retries, and the limiter wraps it again

`pkg/provider/edge/edge.go:286` documents its own one-shot retry:

> tries to handle the rate limiting by waiting for the Retry-After only once,
> if it receives another rate limit error, it returns `slack.RateLimitedError`

So an edge call wrapped in `CallWithRetry` gets two layers of Retry-After
waiting with different policies. Nothing is broken by this — the outer layer
sees the `slack.RateLimitedError` the inner layer gives up with, which is the
correct handoff — but the total wait for a request is harder to reason about
than either layer suggests on its own, and `maxRetries: 2` outside a layer that
already retried once means up to six attempts, not three.

## Options

### A — Do nothing

Accept the current behavior. The 429 path is already visible to the user and
degrades rather than corrupts.

Argues for: this is a personal fork driving one Cursor client over stdio. A
single user's concurrency is low, and no 429 problem has actually been
reported. Every other option costs latency in exchange for avoiding a problem
that may not be occurring.

Argues against: finding 3 is a real mis-assignment regardless of whether it has
bitten yet, and finding 2 is leaving free throughput on the table.

### B — Shared per-tier singletons

Add `tier.Shared()` returning a package-level `*rate.Limiter` per tier, built
once via `sync.Once`. Change the thirteen call sites to use it.

- Effort: S. Roughly thirteen one-line changes plus fifteen lines in
  `limits.go`.
- Gets: a real process-wide budget; no more free burst per invocation.
- Costs: **makes finding 2 worse, everywhere.** All `Tier3` users would then
  share one 50/min budget across `conversations.replies`,
  `conversations.info`, `conversations.history`, edge `users/list` and edge
  `conversations.view` — five methods with five separate Slack budgets funneled
  through one. That is a large, across-the-board slowdown in exchange for
  correctness the user may not need.

This is the cheap option and it is the wrong one. Sharing the limiter is only
correct once the limiter is keyed to something that matches what Slack meters,
which is option C.

### C — Per-method limiter registry

Key limiters by Slack method, not by call site:

```go
// limiter.For returns the process-wide limiter for a Slack API method.
// Slack meters per token and per method, so one limiter per method is the
// unit that matches what is actually being metered.
func For(method string) *rate.Limiter
```

backed by a `map[string]tier` of method → tier and a `sync.Map` of method →
shared limiter. Call sites become `limiter.For("conversations.history")`.

- Effort: M. New registry (~50 lines), a method table (~15 entries), thirteen
  call-site changes, and a test that every method named at a call site has a
  table entry.
- Gets: findings 1, 2 and 3 all fall out of it. Finding 3 in particular stops
  being a judgement call — `conversations.list` gets whatever the table says,
  and the table is one place to audit against Slack's docs.
- Costs: the same latency change as finding 3 implies, since a correct table
  is what produces it. Plus a new failure mode: a call site naming a method
  missing from the table. Guard it — either a compile-time enum of method
  constants, or a test that walks the call sites.

### D — C plus adaptive backoff

Option C, and additionally feed 429s back into the limiter: on a
`RateLimitedError`, either `SetLimit` the method's limiter down for a cooldown
window, or gate the method behind a "not before T" timestamp so *all*
goroutines targeting it wait, not just the one that got the 429. Add jitter to
retry sleeps.

- Effort: L. Also needs a decision about recovery — how the rate climbs back.
- Gets: self-correcting behavior under sustained pressure; the current
  hit-wall-wait-hit-wall-give-up pattern disappears.
- Costs: genuinely more machinery, and adaptive limiters are easy to get
  subtly wrong in ways that only show up under load you cannot easily
  reproduce locally. Hard to test without a fake clock.

## Recommendation

**A for now, with finding 3 raised as a separate yes/no.**

Reasoning: this is a single-user fork driving one MCP client. The concurrency
that makes finding 1 bite is real but low-volume, and the 429 path already
degrades visibly rather than silently. Options C and D are real engineering
against a problem that has not been observed in practice, and both of them
*cost* latency — the correct rate is slower than the current rate almost
everywhere.

What I would actually do:

1. **Land plan 029.** It fixes a data race, which is a correctness bug
   independent of any of this, and it removes one instance of finding 1 as a
   side effect. No latency cost — the shared limiter there replaces two
   limiters that were running at 2× the intended rate.
2. **Decide finding 3 explicitly.** Verify `conversations.list`'s tier against
   Slack's docs. If it is Tier 2, changing site 6 from `Tier2boost` to `Tier2`
   is a one-line fix — but confirm you are willing to pay roughly 10× the
   latency on channel listing for large workspaces first. If you would rather
   keep the speed and accept occasional 429s, that is a legitimate answer for a
   personal fork, and it should be a comment at that line saying so
   deliberately rather than an accident.
3. **Revisit C only if 429s actually show up.** The warning string at
   `pkg/handler/conversations.go:1471` is the signal to watch. If you start
   seeing it, C is the right response and this document is the starting point.

Do **not** do B. It is the cheapest change and the one most likely to make
things worse.

## Open question for the maintainer — ANSWERED by plan 031

Finding 3 was originally raised here as a three-way decision, on the stated
grounds that correcting the tier would cost "roughly 10× the latency on channel
listing". **That framing was wrong in absolute terms and is retracted.**

The ratio is right, but the loop issues only a handful of requests:
`params.Limit` is 999 and Slack caps `conversations.list` at 1000 per page, so
a 5,000-channel workspace is 5 requests and a 20,000-channel workspace is 21.
Measured against the tier definitions, the real cost is:

| Workspace | Pages | `Tier2boost` | `Tier2` | Delta |
|---|---|---|---|---|
| 5,000 channels | 5 | ~0s (within burst) | ~6s | ~6s |
| 20,000 channels | 21 | ~5s | ~57s | ~52s |

Seconds to under a minute, on a background refresh, once per cache TTL — not
the minutes implied above. That is nowhere near enough to justify running 10×
over a documented tier.

What settled it was finding out what a 429 here actually *costs*. Following the
partial result out of `getChannelsMultiType` showed that a single mid-pagination
failure silently truncates the channel cache in memory **and on disk**, and the
truncation survives restarts. Written up, with the fix, as **plan 031**.

So finding 3 is decided: move to `Tier2`, add retry, and make a partial fetch
loud. Plan 031 does all three.

**Everything else in this document still stands as written** — in particular
finding 1 (per-invocation limiters everywhere) is untouched outside the two
call sites plans 029 and 031 happen to visit, and options B/C/D and the
recommendation against B are unchanged.

## Lesson worth keeping

The original write-up reasoned about a rate *ratio* and stopped there. The
number that mattered was the *request count*, which was one `grep` away — and
it changed the recommendation. When a rate-limit change looks expensive, count
the requests before pricing it.

## Maintenance notes

- The tier assignments in the call-site table were read from the code; the
  Slack-side tiers they are compared against are from memory of Slack's
  published rate-limit table and are **not verified against a live fetch**.
  Verify before acting on any of them.
- If a new call site is added, the thing to check is not "does it have a
  limiter" but "does its limiter correspond to a Slack method". The current
  code answers the first question everywhere and the second question nowhere.
- Plan 029's maintenance note points back here. If 029 lands and this document
  is later acted on, site 13 in the table above is already fixed.
