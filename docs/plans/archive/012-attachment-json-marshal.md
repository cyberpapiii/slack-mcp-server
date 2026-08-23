# Plan 012: Emit valid JSON from `attachment_get_data` via `encoding/json`

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
> **Drift check (run first)**: `git diff --stat adbae97..HEAD -- pkg/handler/conversations.go`
> On any change, locate the code by the excerpts below; unlocatable = STOP.

## Status

- **Priority**: P2
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none (execute after 009/011 if all run — same file, serialize merges)
- **Category**: bug
- **Planned at**: commit `adbae97`, 2026-08-07

## Why this matters

`attachment_get_data` builds its JSON tool result by `fmt.Sprintf` on a
template, escaped by a hand-rolled `escapeJSON` that handles only
`\ " \n \r \t`. Every other C0 control byte (NUL, `\v`, `\f`, and — the
common one — `\x1b` ANSI escapes in log files) passes through raw, producing
**invalid JSON** the MCP client cannot parse. The tool fails on exactly the
files people fetch it for (logs, CSVs). `fileInfo.ID` is interpolated with no
escaping at all. `encoding/json` escapes a strict superset and preserves the
output shape for well-behaved input.

## Current state

`pkg/handler/conversations.go`, inside `FilesGetHandler`, at commit `adbae97`:

Image path:

```go
// conversations.go:675-682
	if isImageMimetype(fileInfo.Mimetype) {
		imageData := base64.StdEncoding.EncodeToString(content)
		metadata := fmt.Sprintf(`{"file_id":"%s","filename":"%s","mimetype":"%s","size":%d}`,
			fileInfo.ID,
			escapeJSON(fileInfo.Name),
			escapeJSON(fileInfo.Mimetype),
			len(content))
		return mcp.NewToolResultImage(metadata, imageData, fileInfo.Mimetype), nil
	}
```

Text/binary path:

```go
// conversations.go:685-703
	encoding := "none"
	var contentStr string
	if isTextMimetype(fileInfo.Mimetype) {
		contentStr = string(content)
	} else {
		contentStr = base64.StdEncoding.EncodeToString(content)
		encoding = "base64"
	}
	result := fmt.Sprintf(`{"file_id":"%s","filename":"%s","mimetype":"%s","size":%d,"encoding":"%s","content":"%s"}`,
		fileInfo.ID,
		escapeJSON(fileInfo.Name),
		escapeJSON(fileInfo.Mimetype),
		len(content),
		encoding,
		escapeJSON(contentStr))
	return mcp.NewToolResultText(result), nil
```

The incomplete escaper:

```go
// conversations.go:809-816
func escapeJSON(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\r", `\r`)
	s = strings.ReplaceAll(s, "\t", `\t`)
	return s
}
```

Run `grep -n 'escapeJSON' pkg cmd -r` — at planning time its only callers are
the two blocks above.

Conventions: unit tests `TestUnit*` in `pkg/handler/conversations_test.go`
(pattern: `TestUnitFormatThreadTs`, line 736).

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Build | `go build ./...` | exit 0 |
| All unit tests | `make test` | exit 0 |
| Targeted | `go test -count=1 -run 'TestUnitFileResult' ./pkg/handler/` | pass |
| Format | `gofmt -l pkg cmd` | no output |

## Scope

**In scope**:
- `pkg/handler/conversations.go` (the two result-building blocks, `escapeJSON` deletion, one new helper)
- `pkg/handler/conversations_test.go`

**Out of scope**:
- The download logic, mimetype classification (`isImageMimetype`/`isTextMimetype`), size cap, or tool schema.
- Any other handler's output format.

## Git workflow

- Branch: `advisor/012-attachment-json-marshal`
- One commit; imperative subject. Do NOT push.

## Steps

### Step 1: Add a marshaled result helper

Near the handler, add:

```go
type fileResultPayload struct {
	FileID   string `json:"file_id"`
	Filename string `json:"filename"`
	Mimetype string `json:"mimetype"`
	Size     int    `json:"size"`
	Encoding string `json:"encoding,omitempty"`
	Content  string `json:"content,omitempty"`
}
```

and a `marshalFileResult(p fileResultPayload) (string, error)` that
`json.Marshal`s it. Note: the image-metadata payload has no
`encoding`/`content` keys today — `omitempty` preserves that shape only if
the image path passes empty strings for both; confirm the emitted keys match
the current shapes exactly (image: 4 keys; text: 6 keys). If `encoding:"none"`
must survive (it does — the text path emits it), `omitempty` on `Encoding`
is wrong for the text path; simplest correct approach: two structs, or set
`Encoding` always and drop `omitempty` for it while keeping the image path on
a separate 4-field struct. Choose the two-struct form if in doubt — shape
fidelity beats cleverness here.

**Verify**: `go build ./...` → exit 0

### Step 2: Route both paths through the helper and delete `escapeJSON`

Replace both `fmt.Sprintf` blocks with the marshal call (propagating a
marshal error as a handler error, matching surrounding error style). Delete
`escapeJSON` (lines 809-816). One behavior note: for a text file whose bytes
are not valid UTF-8, `json.Marshal` replaces invalid sequences with U+FFFD —
same class of degradation the current code produces; acceptable.

**Verify**: `go build ./...` → exit 0 and `grep -rn 'escapeJSON' pkg cmd` → no matches

### Step 3: Tests

In `conversations_test.go`, `TestUnitFileResultPayload` (table-driven):

- Filename with `"` and `\` and a control byte `\x1b` → output passes
  `json.Valid`, and unmarshaling round-trips the exact filename.
- Content containing `\x00` and ANSI escapes → `json.Valid` passes;
  round-trip equality.
- Image-shape payload emits exactly the keys
  `file_id, filename, mimetype, size` (unmarshal into `map[string]any`,
  assert key set) and the text shape emits exactly the 6 documented keys
  with `encoding` present even when `"none"`.

**Verify**: `go test -count=1 -run 'TestUnitFileResult' ./pkg/handler/` → pass

### Step 4: Full suite

**Verify**: `make test` → exit 0; `gofmt -l pkg cmd` → no output

## Test plan

Step 3 covers the regression (control bytes), quoting, and shape fidelity for
both payload forms.

## Done criteria

- [ ] `make test` exits 0; new tests pass
- [ ] `grep -rn 'escapeJSON' pkg cmd` → no matches
- [ ] Both result paths use `json.Marshal` (read the diff)
- [ ] `git status` shows only in-scope files modified
- [ ] `plans/README.md` status row updated

## STOP conditions

- Excerpts don't match (drift).
- `escapeJSON` has callers beyond the two blocks (grep first — if any exist,
  report; they were not present at planning).
- Preserving the exact current key order matters to some consumer you find
  evidence of (JSON key order is not semantic; only stop if you find a test
  or client asserting order).

## Maintenance notes

- Reviewer: diff the emitted key sets against the tool's documented output
  (README section for `attachment_get_data`, if present) — the shape is the
  contract.
- Future: if a streaming/chunked variant of this tool is ever added, reuse
  `fileResultPayload` rather than reintroducing string templates.
- Memory-copy reduction (the ~3 in-flight copies of file content) was
  surfaced in the audit and deliberately left out of scope.
