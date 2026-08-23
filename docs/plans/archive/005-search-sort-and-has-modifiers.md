# Plan 005: Expose search recency sort and pass `has:` modifiers through instead of searching them literally

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat adbae97..HEAD -- pkg/handler/conversations.go pkg/server/server.go pkg/handler/conversations_test.go`
> If any in-scope file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Priority**: P2
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none
- **Category**: direction
- **Planned at**: commit `adbae97`, 2026-07-02

## Why this matters

Two common agent intents are unreachable or silently wrong in `conversations_search_messages`:

1. **`has:` modifiers are searched as literal text.** The query splitter whitelists 8 filter keys; `has:link`, `has:reaction`, `has:pin` etc. fail the whitelist and are treated as free-text tokens — Slack then searches for the literal string "has:link", returning junk instead of messages containing links. This is silent wrongness, not a missing feature.
2. **Sort is hardcoded to relevance.** "Find the *most recent* message about X" cannot be expressed; the handler always passes Slack's default (score) sort.

Both fixes are additive params/pass-throughs with defaults preserving today's behavior.

## Current state

Relevant files:

- `pkg/handler/conversations.go` — search param parsing (`parseParamsToolSearch`, line 2480), the filter-key whitelist (`validFilterKeys`, lines 37–46), the query splitter/builder (`splitQuery` line 3005, `buildQuery` line 3028), the `searchParams` struct (line 107), and the search handler (`ConversationsSearchHandler`, line 905).
- `pkg/server/server.go` — the search tool registration (`conversationsSearchTool`, lines 342–388).
- `pkg/handler/conversations_test.go` — handler tests (package `handler`, testify assert/require, `TestUnit` prefix for unit tests). **No tests currently cover `splitQuery`/`buildQuery`** — this plan adds the first ones.

The whitelist (`conversations.go:37-46`):

```go
var validFilterKeys = map[string]struct{}{
	"is":     {},
	"in":     {},
	"from":   {},
	"with":   {},
	"before": {},
	"after":  {},
	"on":     {},
	"during": {},
}
```

The splitter — an unlisted `key:value` token falls into free text (`conversations.go:3005-3017`):

```go
func splitQuery(q string) (freeText []string, filters map[string][]string) {
	filters = make(map[string][]string)
	for _, tok := range strings.Fields(q) {
		parts := strings.SplitN(tok, ":", 2)
		if len(parts) == 2 && isFilterKey(parts[0]) {
			key := strings.ToLower(parts[0])
			filters[key] = append(filters[key], parts[1])
		} else {
			freeText = append(freeText, tok)
		}
	}
	return
}
```

The builder — **re-emits only a hardcoded key list**; a key present in `filters` but absent here is silently dropped (`conversations.go:3028-3037`). This is why the whitelist and this slice MUST change together:

```go
func buildQuery(freeText []string, filters map[string][]string) string {
	var out []string
	out = append(out, freeText...)
	for _, key := range []string{"is", "in", "from", "with", "before", "after", "on", "during"} {
		for _, val := range filters[key] {
			out = append(out, fmt.Sprintf("%s:%s", key, val))
		}
	}
	return strings.Join(out, " ")
}
```

The `searchParams` struct (`conversations.go:107-111`):

```go
type searchParams struct {
	query string
	limit int
	page  int
}
```

The hardcoded sort in the handler (`conversations.go:919-925`):

```go
	searchParams := slack.SearchParameters{
		Sort:          slack.DEFAULT_SEARCH_SORT,
		SortDirection: slack.DEFAULT_SEARCH_SORT_DIR,
		Highlight:     false,
		Count:         params.limit,
		Page:          params.page,
	}
```

(slack-go defines `DEFAULT_SEARCH_SORT = "score"` and `DEFAULT_SEARCH_SORT_DIR = "desc"`; the API accepts `Sort: "timestamp"` for recency. Verify the constant names in `go doc github.com/slack-go/slack.SearchParameters` if in doubt.)

How the parser adds programmatic filters — the pattern for a new `filter_has` param (`conversations.go:2484-2486`):

```go
	if req.GetBool("filter_threads_only", false) {
		addFilter(filters, "is", "thread")
	}
```

Existing param registrations to model (`server.go:375-377`):

```go
		mcp.WithBoolean("filter_threads_only",
			mcp.Description("If true, the response will include only messages from threads. Default is boolean false."),
		),
```

Conventions: zap structured logging; param validation errors via `errors.New`/`fmt.Errorf` before any API call; `is:` is ALREADY a valid key, so `is:pinned`/`is:saved` pass through today — do not touch `is` handling.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Build | `go build ./...` | exit 0 |
| Vet | `go vet ./...` | exit 0 |
| Tests | `go test -count=1 -skip="Integration" ./...` | all pass |
| Format check | `gofmt -l pkg/ cmd/` | no output |

## Scope

**In scope** (the only files you should modify):
- `pkg/handler/conversations.go`
- `pkg/server/server.go` (search tool registration block only)
- `pkg/handler/conversations_test.go`
- `plans/README.md` — status row

**Out of scope** (do NOT touch, even though they look related):
- The message rendering / `detail` / legend pipeline — unrelated to search filters.
- `validFilterKeys` entries other than adding `"has"` — widening to arbitrary keys changes literal-search behavior users may rely on.
- `docs/agent-presets.md`, Plug config — maintainer's deploy concern.

## Git workflow

- Branch: `advisor/005-search-sort-and-has-modifiers`
- Commit style: imperative summary line, body explaining why (see `git log --oneline -10`).
- Do NOT push or open a PR. This fork never pushes to origin.

## Steps

### Step 1: Add `has` to the whitelist AND the builder's key list

In `pkg/handler/conversations.go`:
- Add `"has": {},` to `validFilterKeys` (after `"during"`).
- Add `"has"` to the end of the hardcoded slice in `buildQuery`: `[]string{"is", "in", "from", "with", "before", "after", "on", "during", "has"}`.

These MUST land together (see Current state — a whitelisted key missing from `buildQuery` is silently dropped, which is worse than today's literal search).

**Verify**: `go build ./...` → exit 0, and both greps match:
`grep -n '"has"' pkg/handler/conversations.go` → 2 matches (whitelist + builder).

### Step 2: Add a `filter_has` param

In `parseParamsToolSearch` (`conversations.go:2480`), after the `filter_threads_only` block, add:

```go
	if has := req.GetString("filter_has", ""); has != "" {
		addFilter(filters, "has", has)
	}
```

In the search tool registration in `pkg/server/server.go` (after the `filter_threads_only` param), add:

```go
		mcp.WithString("filter_has",
			mcp.Description("Filter messages by content type. One of: 'link', 'reaction', 'pin', 'file', or an emoji name like ':eyes:'. Maps to Slack's has: search modifier. If not provided, no content-type filter is applied."),
		),
```

**Verify**: `go build ./...` → exit 0.

### Step 3: Add a `sort` param threaded into the Slack call

1. Extend the `searchParams` struct (`conversations.go:107`) with `sort string`.
2. In `parseParamsToolSearch`, parse and validate it (near the `limit`/`cursor` parsing at the end):

```go
	sort := req.GetString("sort", "score")
	if sort != "score" && sort != "timestamp" {
		return nil, fmt.Errorf("invalid sort: %q (must be 'score' or 'timestamp')", sort)
	}
```

and include `sort: sort` in the returned `&searchParams{...}` literal.
3. In `ConversationsSearchHandler` (`conversations.go:919`), replace `Sort: slack.DEFAULT_SEARCH_SORT,` with `Sort: params.sort,` (leave `SortDirection` as-is — `desc` is correct for both: best-first and newest-first).
4. In the registration in `server.go`, add after the `limit` param:

```go
		mcp.WithString("sort",
			mcp.DefaultString("score"),
			mcp.Description("Sort order: 'score' (default, relevance) or 'timestamp' (most recent first)."),
		),
```

**Verify**: `go build ./... && go vet ./...` → exit 0.

### Step 4: Unit tests

In `pkg/handler/conversations_test.go` (same package, so unexported functions are reachable), add:

```go
func TestUnitSplitQueryHasModifier(t *testing.T) {
	free, filters := splitQuery("quarterly report has:link from:@bob")
	assert.Equal(t, []string{"quarterly", "report"}, free)
	assert.Equal(t, []string{"link"}, filters["has"])
	assert.Equal(t, []string{"@bob"}, filters["from"])
}

func TestUnitBuildQueryEmitsHas(t *testing.T) {
	filters := map[string][]string{"has": {"link"}, "in": {"#general"}}
	q := buildQuery([]string{"report"}, filters)
	assert.Contains(t, q, "has:link")
	assert.Contains(t, q, "in:#general")
	assert.Contains(t, q, "report")
}

func TestUnitBuildQueryUnknownKeyStillDropped(t *testing.T) {
	// documents the invariant: keys outside the ordered list don't survive buildQuery
	q := buildQuery(nil, map[string][]string{"bogus": {"x"}})
	assert.Equal(t, "", q)
}
```

Also add a `sort` validation test if `parseParamsToolSearch` can be called with a nil-provider handler — it CAN for queries that skip channel/user formatting (no `filter_in_channel`, `filter_users_*` params), since those are the only provider-touching paths:

```go
func TestUnitSearchSortValidation(t *testing.T) {
	ch := &ConversationsHandler{logger: zap.NewNop()}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"search_query": "hello", "sort": "bogus"}
	_, err := ch.parseParamsToolSearch(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sort")
}
```

Adapt request construction to the local idiom used by existing `TestUnit` tests in `pkg/handler/` if it differs. If `parseParamsToolSearch` unavoidably dereferences the provider before reaching sort validation, move the sort validation earlier in the function (top, next to the query parsing) rather than dropping the test.

**Verify**: `go test -count=1 -skip="Integration" ./pkg/handler/` → all pass, including 4 new tests.

## Test plan

- `TestUnitSplitQueryHasModifier` — `has:x` is now classified as a filter, not free text (the bug this plan fixes).
- `TestUnitBuildQueryEmitsHas` — the builder re-emits `has:` (guards the two-place invariant).
- `TestUnitBuildQueryUnknownKeyStillDropped` — documents existing drop behavior for unlisted keys.
- `TestUnitSearchSortValidation` — invalid `sort` value errors before any API call.
- Structural pattern: existing `TestUnit*` tests in `pkg/handler/conversations_test.go` (testify, `TestUnit` prefix).
- Verification: `go test -count=1 -skip="Integration" ./...` → all pass.

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `go build ./...` exits 0
- [ ] `go vet ./...` exits 0
- [ ] `go test -count=1 -skip="Integration" ./...` exits 0, including 4 new tests
- [ ] `gofmt -l pkg/ cmd/` prints nothing
- [ ] `grep -n '"has"' pkg/handler/conversations.go` → 2 matches
- [ ] `grep -n "DEFAULT_SEARCH_SORT," pkg/handler/conversations.go` → no match (sort now comes from params)
- [ ] `grep -c "filter_has\|\"sort\"" pkg/server/server.go` ≥ 2 (both new params registered)
- [ ] No files outside the in-scope list modified (`git status`)
- [ ] `plans/README.md` status row updated

## STOP conditions

Stop and report back (do not improvise) if:

- The drift check shows in-scope files changed and the excerpts no longer match (especially `validFilterKeys`, `splitQuery`, `buildQuery`).
- slack-go's `SearchParameters` has no `Sort` field or rejects `"timestamp"` per its docs (`go doc github.com/slack-go/slack.SearchParameters`) — the vendored version may differ.
- Sort validation cannot run before provider-touching code without restructuring more than moving the validation block.
- A step's verification fails twice after a reasonable fix attempt.

## Maintenance notes

- The whitelist (`validFilterKeys`) and `buildQuery`'s ordered slice are a two-place invariant; `TestUnitBuildQueryEmitsHas` guards it, but any future filter key must be added to both (a reviewer should check this on any search change).
- Deliberately NOT added: other Slack modifiers (`hasmy:`, `is:saved` already passes via `is:`). Widen only with a concrete agent need.
- Maintainer's post-merge live check: search a known channel with `filter_has: link` and with `sort: timestamp` through the Plug-exposed tool and confirm sensible results (per AGENTS.md preference for live verification). Requires `make deploy-local`.
- Plug's cached tool schema may not show the new params immediately; passing them works regardless (observed previously with the `detail` param).
