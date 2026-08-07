# Plan 019: Fix `edge.NewWithClient`'s tape/options bugs and give the edge client real fixture tests

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Worktree check (run zeroth)**: `git rev-parse --short HEAD` must be
> `adbae97` or a descendant (`git merge-base --is-ancestor adbae97 HEAD && echo ok`); otherwise STOP.
>
> **Drift check (run first)**:
> `git diff --stat adbae97..HEAD -- pkg/provider/edge/ pkg/provider/cache_test.go pkg/provider/edge/slacker_test.go`
> **Plan 008 (nil-guard + 429 retry) edits `edge.go` and should land first**
> — its tests belong in the file this plan creates or extends; coordinate by
> reading `edge.go`'s current state at execution time.

## Status

- **Priority**: P2
- **Effort**: M
- **Risk**: LOW (one production bug fix + tests; the bug fix removes an
  accidental side effect)
- **Depends on**: plan 008 (same file; land 008 first)
- **Category**: test coverage + latent bug
- **Planned at**: commit `adbae97`, 2026-08-07

## Why this matters

The edge client (`pkg/provider/edge`) is the fork's differentiating layer —
`client.counts`, saved/later, activity feed all flow through it — and it has
**zero meaningful unit tests**. Meanwhile `NewWithClient` has two real bugs:

1. It **unconditionally creates `tape.txt` in the current working directory**
   and installs a recording tape. Any future caller (including a test) that
   constructs a client this way starts silently recording request/response
   traffic — `PostFormRaw` bodies **include the xoxc token** — into an
   untracked file. That's a credential-on-disk hazard wired into a
   constructor.
2. It accepts `opt ...Option` but **never applies them** — options are
   silently dropped.

At planning time `NewWithClient` has no production callers (grep confirms:
only `New`/`NewWithInfo` are used), which is why the bugs are latent — and
why now, before tests start using the constructor, is the moment to fix it.

Also in scope: delete two tautological cache tests that assert their own
test-local mutex logic rather than production behavior, and promote a
test-local helper that shadows production logic.

## Current state

**The buggy constructor** — verified excerpt, `pkg/provider/edge/edge.go:76-95`:

```go
func NewWithClient(workspaceName string, teamID string, token string, cl *http.Client, opt ...Option) (*Client, error) {
	if teamID == "" {
		return nil, ErrNoTeamID
	}
	if token == "" {
		return nil, ErrNoToken
	}
	tape, err := os.Create("tape.txt")
	if err != nil {
		return nil, err
	}
	return &Client{
		cl:           cl,
		token:        token,
		teamID:       teamID,
		webclientAPI: fmt.Sprintf("https://%s.%s/api/", workspaceName, getSlackBaseDomain()),
		edgeAPI:      fmt.Sprintf("https://edgeapi.%s/cache/%s/", getSlackBaseDomain(), teamID),
		tape:         tape,
	}, nil
}
```

Both bugs are visible above: `os.Create("tape.txt")` runs unconditionally,
and `opt` is accepted but never ranged over.

Compare `NewWithInfo` (`edge.go:120-139`), the production path via `New`,
which does it right:

```go
func NewWithInfo(info *slack.AuthTestResponse, prov auth.Provider, opt ...Option) (*Client, error) {
	hcl, err := prov.HTTPClient()
	...
	c := &Client{ ...  tape: nopTape{} }
	for _, o := range opt {
		o(c)
	}
	return c, nil
}
```

`nopTape` already exists (`edge.go:107-115`) — that is the correct default.
Options available (`edge.go:47-58`): `type Option func(*Client)`,
`WithTape(tape io.WriteCloser)`, `OptionHTTPClient(client httpClient)`.

**Test seam**: `NewWithClient` is the cheap one — it takes plain strings and
an `*http.Client`, no `auth.Provider` needed. After Step 1, tests construct
with e.g.
`NewWithClient("testws", "T123", "xoxc-test", &http.Client{Transport: fake})`.

**Response parsing**: `ParseResponse` is a method on `*Client`, called from
`callEdgeAPI` (`edge.go:229-235`). Locate it by name; post-008 it has a
nil/error guard. Test it directly.

**Endpoints worth fixture tests** (JSON shapes hand-writable from the struct
definitions in the same package):
- `ClientCounts` (`client.go`) — feeds `conversations_unreads`.
- `SavedList` (`saved.go`) — feeds saved/later tools.
- `ActivityFeed` (`activity.go`) — feeds `activity_feed`.

**Tautological tests to delete**, `pkg/provider/cache_test.go`:
- `TestRefreshingFlagPreventsConcurrentRefreshes` (line ~365) — builds a
  local struct with its own atomic flag and asserts that *its own* flag
  logic works; never touches production code.
- `TestFetchSerializationWithMutex` (line ~523) — same pattern with a local
  mutex. Both pass vacuously forever.

**Shadow helper to promote**, `pkg/provider/edge/slacker_test.go:15`:
`collectResults` — a test-local reimplementation of the pagination-collect
logic in `slacker.go`. Tests currently verify the copy, not the original.
Extract the production loop's body into an unexported function in
`slacker.go` and have both the production caller and the test use it; delete
the test-local copy.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Build | `go build ./...` | exit 0 |
| All unit tests | `make test` | exit 0 |
| Edge tests | `go test -count=1 -run 'TestUnit' ./pkg/provider/edge/` | pass |
| Tape regression | `ls tape.txt` after running edge tests | "No such file" |
| Format | `gofmt -l pkg cmd` | no output |

## Scope

**In scope**:
- `pkg/provider/edge/edge.go` (`NewWithClient` only)
- New `pkg/provider/edge/edge_test.go` (or extend the file plan 008 created)
- `pkg/provider/edge/slacker.go` + `slacker_test.go` (helper promotion)
- `pkg/provider/cache_test.go` (two deletions)

**Out of scope**:
- The tape mechanism itself as an opt-in debug tool — keep the `tape` type;
  just stop installing it by default. (A future `OptionTape(w io.Writer)` was
  surfaced as a direction idea; do NOT build it here.)
- Live-Slack integration tests (`TestIntegration*`) — untouched.
- Exported-but-unused edge methods (`ClientDMs`, etc.) — plan 022 territory,
  and even there they're retained pending a direction decision.

## Git workflow

- Branch: `advisor/019-edge-fixture-tests`
- Commits: fix, then tests, then cleanups — or one commit; imperative
  subjects. Do NOT push.

## Steps

### Step 1: Fix `NewWithClient`

- Delete the `os.Create("tape.txt")` block; set `tape: nopTape{}` instead,
  exactly as `NewWithInfo` does. Callers that want recording can pass
  `WithTape(...)`, which the next bullet makes work.
- Apply options: build the `*Client` into a variable, then
  `for _, o := range opt { o(c) }` before returning — copy the pattern from
  `NewWithInfo` verbatim.
- Grep `rg -n 'NewWithClient' pkg cmd` — expect no production callers (see
  STOP if found).

**Verify**: `go build ./...` → exit 0; `rg -n 'os.Create' pkg/provider/edge/` → no matches

### Step 2: Fixture tests for parsing and endpoints

In `edge_test.go`, add a `roundTripperFunc`:

```go
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
```

Then, constructing clients via
`NewWithClient("testws", "T123", "xoxc-test", &http.Client{Transport: fake})`
(this also exercises Step 1):

- `TestUnitParseResponse`: table over — 200 with `{"ok":true,...}` → nil
  error; 200 with `{"ok":false,"error":"invalid_auth"}` → error containing
  `invalid_auth`; non-2xx status → status error; plus the nil-response case
  if plan 008 didn't already cover it (read the existing tests first; don't
  duplicate).
- `TestUnitClientCounts`: fake returns a hand-written `client.counts` JSON
  body (build it by reading the response struct fields in `client.go` —
  channels/mpims/ims arrays with `id`, `last_read`, `mention_count`,
  `has_unreads`); assert the decoded struct's values round-trip.
- `TestUnitSavedList` and `TestUnitActivityFeed`: same pattern against their
  response structs. Keep fixtures minimal-but-realistic (2 items each);
  inline `const` strings, not testdata files, matching the package's
  existing style (no testdata dir exists).
- In one test, assert the fake saw the expected form values (token present,
  endpoint path correct) — that pins the request-building contract.

**Verify**: `go test -count=1 -run 'TestUnit' ./pkg/provider/edge/` → pass; `ls tape.txt` in repo root → no such file

### Step 3: Promote `collectResults`

Read `slacker_test.go:15` and the corresponding production loop in
`slacker.go`. Extract the production logic into an unexported func (name it
after what it does, e.g. `collectPages`), call it from the production site,
rewrite the test against it, delete the test-local copy. Behavior-preserving
refactor — the diff to production logic should be extract-only.

**Verify**: `go test -count=1 ./pkg/provider/edge/` → pass

### Step 4: Delete the tautological cache tests

Remove `TestRefreshingFlagPreventsConcurrentRefreshes` and
`TestFetchSerializationWithMutex` from `pkg/provider/cache_test.go`
(verify by reading them first that they still construct their own local
types rather than exercising production code — that was true at planning).

**Verify**: `make test` → exit 0 (nothing else referenced them)

### Step 5: Full suite

**Verify**: `make test` → exit 0; `gofmt -l pkg cmd` → no output

## Test plan

Steps 2–3 ARE the test plan. Load-bearing: no `tape.txt` side effect;
options applied; parse/endpoint contracts pinned by fixtures.

## Done criteria

- [ ] `make test` exits 0; new `TestUnit*` edge tests pass
- [ ] `rg -n 'os.Create' pkg/provider/edge/` → no matches
- [ ] No `tape.txt` exists in the repo root after the suite runs
- [ ] `NewWithClient` applies its options (read the diff)
- [ ] The two tautological cache tests are gone; `collectResults` shadow copy is gone
- [ ] `git status` shows only in-scope files modified
- [ ] `plans/README.md` status row updated

## STOP conditions

- Excerpts don't match (drift) — especially if plan 008 restructured
  `NewWithClient` already; reconcile by reading, and skip any step 008
  already did.
- `rg -n 'NewWithClient'` finds a production caller — someone may depend on
  the tape; report before removing it.
- The `collectResults` extraction turns out NOT to be behavior-preserving
  (the test copy and production loop differ) — that difference is a
  finding; report it, don't silently pick one.

## Maintenance notes

- If tape-based debugging is ever wanted again, add `OptionTape(w io.Writer)`
  — opt-in, caller-owned writer, and NEVER default-on (the recorded bodies
  contain the xoxc token).
- Reviewer: check fixture JSON against a real (redacted) response if one is
  handy — hand-written fixtures can encode wrong assumptions; the structs'
  json tags are the source of truth used here.
- Plan 020 (unreads characterization) builds on `ClientCounts` shapes from
  this plan's fixtures — keep field names consistent.
