# Plan 008: Harden the edge client — nil-response guard and a working 429 retry

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
> **Drift check (run first)**: `git diff --stat adbae97..HEAD -- pkg/provider/edge/edge.go`
> On any change, compare the "Current state" excerpts against the live code;
> mismatch = STOP condition.

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `adbae97`, 2026-08-07

## Why this matters

Two defects in the shared HTTP plumbing every browser-session ("edge") API
call passes through:

1. **Nil-response panic that can kill the process.** `callEdgeAPI` forwards a
   nil `*http.Response` to `ParseResponse` when the transport error is
   `io.EOF` — which `http.Client.Do` returns (wrapped in `*url.Error`)
   whenever the server closes a pooled keep-alive connection, a routine
   transient event. `ParseResponse` dereferences the response on its first
   line. Inside a tool handler the panic is absorbed by `WithRecovery()`
   (`pkg/server/server.go:142`), but on the cache-warmup path
   (`cmd/slack-mcp-server/warmup.go` goroutine → `RefreshUsers` →
   `GetSlackConnect` → `ClientUserBoot` → `callEdgeAPI`) it is a bare
   goroutine panic that takes down the whole MCP server.
2. **The 429 retry always fails.** On rate-limit, `do()` sleeps and re-issues
   the *same* `*http.Request` whose one-shot body reader was drained by the
   first attempt — so the retry posts an empty body and gets a confusing
   Slack argument error, exactly under the load where the retry matters. The
   first 429 response body is also never closed (leaked connection), and the
   sleep ignores context cancellation.

## Current state

- `pkg/provider/edge/edge.go` — the edge client core. All excerpts below are
  from commit `adbae97`.

The nil-forwarding path:

```go
// edge.go:228-234
func (cl *Client) callEdgeAPI(ctx context.Context, v any, endpoint string, req PostRequest) error {
	r, err := cl.PostJSON(ctx, endpoint, req)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return cl.ParseResponse(v, r)
}
```

```go
// edge.go:270-274
func (cl *Client) ParseResponse(req any, r *http.Response) error {
	if r.StatusCode < http.StatusOK || http.StatusMultipleChoices <= r.StatusCode {
		return fmt.Errorf("error: status code: %s", r.Status)
	}
	defer r.Body.Close()
```

The retry path (inside the package-level `do` function; surrounding lines ~300-320):

```go
	if resp.StatusCode == http.StatusTooManyRequests {
		wait, err := parseRetryAfter(resp)
		if err != nil {
			return nil, err
		}
		lg.InfoContext(ctx, "got rate limited, waiting", "delay", wait)

		time.Sleep(wait)
		resp, err = cl.Do(req)
```

Request bodies are one-shot readers, and **`req.GetBody` is always nil today**
— this is the crux of the fix (corrected 2026-08-07 after a first execution
attempt proved the naive approach is a no-op):

```go
// edge.go:209-225 (PostJSON)
	data, err := json.Marshal(req)
	...
	tape := cl.recorder(bytes.NewReader(data))
	defer cl.record([]byte("\n\n"))
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, cl.edgeAPI+path, tape)
```

```go
// edge.go:255-266 (PostFormRaw)
	if form["token"] == nil {
		form.Set("token", cl.token)
	}
	r := cl.recorder(strings.NewReader(form.Encode()))
	defer cl.record([]byte("\n\n"))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, r)
```

```go
// edge.go:389-394
func (cl *Client) recorder(r io.Reader) io.Reader {
	if cl.tape == nil {
		return r
	}
	return io.TeeReader(r, cl.tape)
}
```

`cl.tape` is never nil in practice — `NewWithInfo` defaults it to
`nopTape{}` (a non-nil interface value) and `NewWithClient` sets a real file.
So the reader handed to `http.NewRequestWithContext` always has concrete type
`*io.teeReader`. Go's constructor only auto-populates `req.GetBody` from a
type switch on `*bytes.Buffer`, `*bytes.Reader`, `*strings.Reader`; a
`TeeReader` wrapping one of those hits the `default:` case and `GetBody` stays
`nil`. **Therefore `GetBody` must be set explicitly at the two construction
sites** (Step 2) — a `if req.GetBody != nil { ... }` guard in `do` alone is a
silent no-op.

The injectable-client seam for tests already exists:

```go
// edge.go:56-60
func OptionHTTPClient(client httpClient) func(*Client) {
	return func(cl *Client) {
		cl.cl = client
	}
}
```

(`httpClient` is an interface with a `Do(*http.Request)` method — confirm its
exact shape in the file.)

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Build | `go build ./...` | exit 0 |
| All unit tests | `make test` | exit 0 |
| Targeted | `go test -count=1 -run 'TestUnitEdge' ./pkg/provider/edge/` | pass |
| Format | `gofmt -l pkg cmd` | no output |

## Scope

**In scope**:
- `pkg/provider/edge/edge.go` (`callEdgeAPI`, `ParseResponse` top guard, the
  429 block in `do`)
- New test file `pkg/provider/edge/edge_test.go`

**Out of scope**:
- `NewWithClient`'s `tape.txt` bug and option-application bug — plan 019.
- `pkg/limiter/` retry logic — different subsystem.
- Any change to the edge API endpoints, DTOs, or `pkg/provider/api.go`.

## Git workflow

- Branch: `advisor/008-edge-client-nil-guard-and-429-retry`
- One commit, imperative subject. Do NOT push.

## Steps

### Step 1: Nil guards

In `callEdgeAPI`: after the `PostJSON` call, if `r == nil`, return `err` (or a
wrapped `fmt.Errorf("edge %s: no response: %w", endpoint, err)` when err is
non-nil; if both are nil return an explicit error — that state should be
impossible but must not fall through). The `io.EOF` special-case may remain
only for the case where a response object actually exists.

In `ParseResponse`: add a first-line guard `if r == nil { return errors.New("nil response") }`.

**Verify**: `go build ./...` → exit 0

### Step 2a: Make `GetBody` actually exist

Set `GetBody` explicitly at both request-construction sites, right after the
`http.NewRequestWithContext` error check. Capture the payload bytes in a
variable first so the closure replays the exact same bytes.

In `PostJSON` (`data` is already in scope):

```go
	r.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(data)), nil
	}
```

In `PostFormRaw`, hoist the encode so it isn't recomputed:

```go
	encoded := form.Encode()
	r := cl.recorder(strings.NewReader(encoded))
	...
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(encoded)), nil
	}
```

Deliberate: the replayed body is **not** re-wrapped in `cl.recorder(...)`, so
the tape records the body once, not once per attempt. The tape is a debug
transcript and the retried bytes are identical by construction — record that
reasoning in the commit message. Do NOT change `recorder`, `record`, or the
`tape` field.

**Verify**: `go build ./...` → exit 0

### Step 2b: Fix the 429 retry in `do`

Before re-issuing the request:
1. Drain and close the 429 response body (`io.Copy(io.Discard, resp.Body)`;
   `resp.Body.Close()`).
2. Rebuild the body: `if req.GetBody != nil { req.Body, err = req.GetBody(); if err != nil { return nil, err } }`.
   After Step 2a this branch is live for both construction paths.
3. Replace `time.Sleep(wait)` with a context-aware wait:

```go
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(wait):
	}
```

Apply the same treatment to the *second* rate-limit branch (the code after
"still rate limited" — read it fully; if it also sleeps or re-issues, it needs
the same body/close/ctx handling; if it gives up, just ensure the body is
closed).

**Verify**: `go build ./...` → exit 0

### Step 3: Tests

New `pkg/provider/edge/edge_test.go` (internal package test), using a
`roundTripperFunc`-style fake passed via `OptionHTTPClient` on a `Client`
built with `NewWithInfo` (see `edge.go:120`; it defaults the tape to
`nopTape{}` — do NOT use `NewWithClient`, which has a known side effect,
see plan 019).

`NewWithInfo(info *slack.AuthTestResponse, prov auth.Provider, opt ...Option)`
needs a fake provider. `auth.Provider` is
`github.com/rusq/slackdump/v3/auth` and has exactly five methods.

**Two different `slack` packages are involved** — this is not a typo, and
getting it wrong is a compile error:

- `NewWithInfo`'s `info` parameter is `*"github.com/slack-go/slack".AuthTestResponse`
  (that's the `slack` imported by `edge.go`).
- `auth.Provider.Test` returns `*"github.com/rusq/slack".AuthTestResponse`.

So the test file needs an aliased import, e.g. `rslack "github.com/rusq/slack"`:

```go
type fakeAuthProvider struct{ cl *http.Client }

func (f fakeAuthProvider) SlackToken() string      { return "xoxc-test" }
func (f fakeAuthProvider) Cookies() []*http.Cookie { return nil }
func (f fakeAuthProvider) Validate() error         { return nil }
func (f fakeAuthProvider) Test(context.Context) (*rslack.AuthTestResponse, error) {
	return nil, nil
}
func (f fakeAuthProvider) HTTPClient() (*http.Client, error) { return f.cl, nil }
```

Pass `&slack.AuthTestResponse{TeamID: "T123", URL: "https://testws.slack.com/"}`
(the slack-go one) as `info`. Tests to write:

1. `TestUnitEdgeCallEdgeAPINilResponse` — fake `Do` returns `(nil,
   &url.Error{Err: io.EOF})`; assert `callEdgeAPI` returns a non-nil error
   and does not panic.
2. `TestUnitEdgeParseResponseNil` — `ParseResponse(&struct{}{}, nil)` returns
   an error.
3. `TestUnitEdgeRetryRebuildsBody` — fake returns 429 (with `Retry-After: 0`
   header and a non-nil `Body`) on the first call, then 200 `{"ok":true}`;
   capture the body content seen on each call; assert the second call's body
   equals the first call's body and is non-empty; assert the 429 body was
   closed (use a closable recorder).
4. `TestUnitEdgeRetryContextCancel` — 429 with a large `Retry-After`, a
   context cancelled after ~10ms; assert return within ~1s with
   `context.Canceled`.

Drive these through an exported call (`PostJSON`/`callEdgeAPI` are internal —
internal test package makes them callable directly).

**Verify**: `go test -count=1 -run 'TestUnitEdge' ./pkg/provider/edge/` → all 4 pass

### Step 4: Full suite

**Verify**: `make test` → exit 0; `gofmt -l pkg cmd` → no output

## Test plan

Covered by Step 3: nil-response (the crash), retry-body (the broken retry),
context cancellation, plus the happy path implicitly via case 3's second call.
These are the first-ever unit tests in `pkg/provider/edge` — keep them free of
real tokens and real endpoints (fixtures are hand-written JSON).

## Done criteria

- [ ] `make test` exits 0; the 4 new tests exist and pass
- [ ] `grep -n "time.Sleep" pkg/provider/edge/edge.go` shows no sleep in the 429 path
- [ ] `grep -n "GetBody" pkg/provider/edge/edge.go` shows assignments in BOTH
      `PostJSON` and `PostFormRaw`, plus the read in `do`
- [ ] `ParseResponse` has a nil guard (read the diff)
- [ ] `git status` shows only in-scope files modified
- [ ] `plans/README.md` status row updated

## STOP conditions

- Excerpts don't match the live code (drift).
- `TestUnitEdgeRetryRebuildsBody` still sees an empty second body **after**
  Step 2a — that would mean a third body-wrapping layer exists that this plan
  hasn't accounted for. Report the concrete types involved; do not invent a
  buffering layer inside `do`.
- The `httpClient` interface shape prevents the fake from setting per-call
  behavior without production changes.
- Any test requires a real token or network call.

## Maintenance notes

- Plan 019 (edge fixture tests) builds on this file's seams and depends on
  Step 1's guards; land 008 before 019.
- Reviewer: scrutinize double-close risks in the retry path (body closed
  exactly once per response) and that the success path's body handling is
  unchanged.
- Deferred: a general retry/backoff policy for edge calls (only the existing
  single 429 retry is being repaired, not redesigned).
