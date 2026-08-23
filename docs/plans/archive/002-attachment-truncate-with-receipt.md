# Plan 002: Render bot-attachment content in standard mode with budgeted truncation and an explicit re-fetch receipt

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat 7781aaf..HEAD -- pkg/text/`
> Plan 001 intentionally changes these files (signatures gain an OutputMode
> argument) — that drift is expected. Compare against the post-001 shape
> described below; on any *other* mismatch, treat it as a STOP condition.

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW (pure rendering change, isolated in `pkg/text`)
- **Depends on**: plans/001-per-call-detail-parameter.md
- **Category**: dx
- **Planned at**: commit `7781aaf`, 2026-07-01

## Why this matters

Slack integrations (alerting, CI, Jira/GitHub bots) often put their entire payload in message *attachments* — the message text is empty or a stub, and the real content lives in the attachment's fields and body. In the default compact mode, this server renders an attachment as only `Title (URL)` (`attachmentToCompactText`), which makes an agent triaging an alerts channel blind to what the alert actually says. Full mode renders everything but often doubles message size (the code comments say so). This plan makes standard mode render attachment content up to a character budget — fields first, since they carry the densest signal in bot attachments — and, when it truncates, append an explicit marker telling the agent how to recover the rest (re-fetch with `detail: full`, the parameter added by plan 001). Result: lossy display, lossless reachability, no silent data destruction.

## Current state

All in `pkg/text/text_processor.go`. **After plan 001 lands**, `AttachmentToText` has the signature `AttachmentToText(att slack.Attachment, mode OutputMode) string` and dispatches to the two private renderers. The renderers themselves are unchanged by plan 001:

- `attachmentToCompactText` (line 27 at `7781aaf`) — the function this plan rewrites:
  ```go
  // attachmentToCompactText keeps link previews short: title plus URL when available.
  // Full mode appends author, body, fields, and footer which often doubles message size.
  func attachmentToCompactText(att slack.Attachment) string {
  	var result string
  	switch {
  	case att.Title != "" && att.TitleLink != "":
  		result = fmt.Sprintf("%s (%s)", att.Title, att.TitleLink)
  	case att.Title != "":
  		result = att.Title
  	case att.TitleLink != "":
  		result = att.TitleLink
  	default:
  		return ""
  	}
  	result = strings.ReplaceAll(result, "\n", " ")
  	result = strings.ReplaceAll(result, "\r", " ")
  	return strings.TrimSpace(result)
  }
  ```
- `attachmentToFullText` (line 45) — renders `Title`, `Pretext`, `Text`, all `Fields` as `Title: Value` pairs joined by `; `, `Footer`, and `Blocks`, then strips newlines/tabs. Use it as the reference for how fields are formatted.
- `AttachmentsTo2CSV` (line 165) — wraps per-attachment text into the message text; unchanged by this plan except that it already passes `mode` through after plan 001.
- Existing tests for these renderers live in `pkg/text/text_processor_test.go` (attachment tests around lines 267–449); they are table-driven with `testify`.

Key constraint discovered during planning: **attachments are not independently fetchable.** The `attachment_get_data` tool (`pkg/handler/conversations.go:632`, `FilesGetHandler`) downloads *files* by file ID — attachments (link unfurls, bot payloads) have no ID-addressable fetch path. The only lossless recovery is re-fetching the same message with `detail: full`. The truncation marker must therefore say exactly that.

## Commands you will need

| Purpose   | Command              | Expected on success |
|-----------|----------------------|---------------------|
| Build     | `go build ./...`     | exit 0              |
| Tests     | `make test` (= `go test -count=1 -v -skip="Integration" ./...`) | all pass |
| Package tests only | `go test ./pkg/text/` | ok |

## Scope

**In scope** (the only files you should modify):
- `pkg/text/text_processor.go`
- `pkg/text/text_processor_test.go`

**Out of scope** (do NOT touch, even though they look related):
- `attachmentToFullText` — full mode stays byte-identical; it is the recovery path and must not change under an agent's feet.
- `pkg/handler/*` — no handler changes; the budget applies per attachment inside the renderer, not per message.
- `FilesToText` / email-file rendering in the same file — different mechanism, different plan if ever.
- The `maxFileSizeBytes` limit and `FilesGetHandler` — file downloads are unrelated to attachment rendering.

## Git workflow

- Branch: `advisor/002-attachment-truncate-with-receipt`
- Commit style: imperative sentence, capitalized — e.g. `Render attachment fields in standard mode with truncation receipt`.
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Rewrite `attachmentToCompactText` with a field budget

Replace the body with logic that builds, in priority order: title+link (as today), then each `Fields` entry as `Title: Value` (matching `attachmentToFullText`'s field formatting), then `Text`, then `Pretext` — accumulating into a single `; `-joined string, newlines flattened to spaces, until adding the next part would exceed the budget.

```go
// attachmentCompactBudget caps per-attachment rendered length in standard mode.
// Fields render before body text because bot attachments (alerts, CI, Jira)
// carry their densest signal in fields. When content is cut, an explicit
// receipt tells the agent how to recover it losslessly.
const attachmentCompactBudget = 300

const attachmentTruncationReceipt = " …[attachment truncated — re-fetch this message with detail: \"full\"]"
```

Rules:
- If everything fits within `attachmentCompactBudget`, output has no receipt and (for a title-and-link-only attachment) must be byte-identical to today's output — existing behavior is a strict subset.
- If any part is dropped or cut mid-part, append `attachmentTruncationReceipt` (the receipt does not count against the budget).
- Cut whole parts, not mid-word: if the next part doesn't fit, stop (exception: if *nothing* beyond the title fits and the title itself exceeds the budget, hard-cut the title at the budget and append the receipt).
- An attachment with no title, no link, no fields, and no text still returns `""` as today.

**Verify**: `go test ./pkg/text/` → existing attachment tests still pass (those asserting title+link output for simple attachments must be unaffected).

### Step 2: Tests

Write the tests in the Test plan, run the full gate.

**Verify**: `make test` → all pass; `go build ./...` → exit 0.

## Test plan

New table-driven tests in `pkg/text/text_processor_test.go`, modeled on the existing attachment tests in that file:

- `TestUnitAttachmentCompactIncludesFields`: attachment with title, link, and two short fields (e.g. `sev: P1`, `svc: api-gw`) → output contains all four elements, no receipt.
- `TestUnitAttachmentCompactTruncatesWithReceipt`: attachment whose fields + text total well over 300 chars → output length (excluding receipt) ≤ 300 + a small tolerance for the last whole part boundary rule you chose — assert the receipt suffix is present and the first field IS present (priority order held).
- `TestUnitAttachmentCompactNoReceiptWhenFits`: everything under budget → receipt absent.
- `TestUnitAttachmentCompactTitleOnlyUnchanged`: title+link only → exactly `Title (https://link)` — the legacy shape, proving backward compatibility.
- `TestUnitAttachmentCompactOversizedTitle`: 400-char title, nothing else → hard-cut at budget, receipt present.
- Keep/verify one `ModeFull` assertion confirming full-mode output is unchanged for the truncation-test fixture.

Verification: `make test` → all pass, including ≥5 new tests.

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `go build ./...` exits 0
- [ ] `make test` exits 0; the 5 new `TestUnitAttachmentCompact*` tests exist and pass
- [ ] `grep -n "attachmentCompactBudget" pkg/text/text_processor.go` shows the constant with its doc comment
- [ ] `grep -rn "attachmentTruncationReceipt" pkg/handler/` returns no matches (renderer-internal; not leaked)
- [ ] `attachmentToFullText` is unmodified (`git diff pkg/text/text_processor.go` shows no hunks in that function)
- [ ] No files outside the in-scope list are modified (`git status`)
- [ ] `plans/README.md` status row updated

## STOP conditions

Stop and report back (do not improvise) if:

- Plan 001 has not landed (no `OutputMode` type in `pkg/text`) — this plan's receipt text references the `detail` parameter, which must exist.
- `attachmentToCompactText`'s current body doesn't match the excerpt above (beyond plan 001's signature changes).
- Existing tests assert compact output for attachments *with fields or body text* in the old title-only shape and the maintainer intent is ambiguous — list the conflicting tests and report rather than rewriting their expectations silently.

## Maintenance notes

- The 300-char budget is a judgment call; if agents report still-blind alerts, raise it or make it env-tunable (`SLACK_MCP_ATTACHMENT_BUDGET`) — deferred until there's evidence.
- If a future Slack API or tool ever makes attachments ID-addressable, the receipt should point at that instead of a full re-fetch; revisit `attachmentTruncationReceipt` then.
- Reviewer should scrutinize: that per-attachment budgeting can't blow up messages with many attachments (a message with 10 attachments can now reach ~3KB rendered — acceptable, but worth a conscious yes).
