# Plan 017: Pass the degradation-notification text to osascript as an argument, not interpolated source

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
> **Drift check (run first)**: `git diff --stat adbae97..HEAD -- pkg/provider/api.go`
> On any change, locate the code by the excerpt below; unlocatable = STOP.

## Status

- **Priority**: P3
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none
- **Category**: security hygiene / robustness
- **Planned at**: commit `adbae97`, 2026-08-07

## Why this matters

The macOS degradation notifier builds AppleScript **source code** by
string-interpolating a message that includes upstream error text. The
escaping handles only double quotes — a backslash (or `\"` sequence) in the
error text produces either broken AppleScript (notification silently fails,
exactly when the user most needs to know browser auth degraded) or, in
principle, injected AppleScript. Slack error strings and Go error wrapping
routinely contain quotes and backslashes (JSON fragments, Windows-style
paths in proxied errors). The robust fix is the standard one: never build
source from data — pass the message as an **argv argument** to a fixed
script. This also makes the construction unit-testable.

## Current state

`pkg/provider/api.go` at commit `adbae97` (**corrected 2026-08-07** — the
first version of this plan quoted a wrong excerpt; this is the verified code):

```go
// api.go:241-247
func notifyBrowserDegradation(reason string, logger *zap.Logger) {
	if err := exec.Command("osascript", "-e", fmt.Sprintf(`display notification "%s" with title "Slack MCP fallback active"`,
		strings.ReplaceAll(reason, `"`, `\"`),
	)).Run(); err != nil {
		logger.Debug("Failed to emit browser degradation notification", zap.Error(err))
	}
}
```

Surrounding facts you must preserve:

- There is NO `runtime.GOOS != "darwin"` guard. Do not add one — that would
  be a behavior change outside this plan's scope.
- The function is reached through an indirection variable used as a test
  seam: `var browserDegradationNotifier = notifyBrowserDegradation`
  (`api.go:63`). Single production caller:
  `browserDegradationNotifier(reasonText, c.logger)` (`api.go:520`).
- `pkg/provider/api_edge_fallback_test.go:127` monkey-patches that variable
  with a `func(reason string, logger *zap.Logger)` literal. **The signature
  must stay exactly `(reason string, logger *zap.Logger)`** or that test
  breaks.
- Execution is synchronous `.Run()` with the error logged at Debug — keep
  that; it is not fire-and-forget.
- Title text is `Slack MCP fallback active` with no subtitle — keep it
  byte-identical.

Repo conventions: unit tests `TestUnit*` in `pkg/provider/*_test.go`; no
existing test for this function.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Build | `go build ./...` | exit 0 |
| All unit tests | `make test` | exit 0 |
| Targeted | `go test -count=1 -run 'TestUnitOsascriptNotificationArgs' ./pkg/provider/` | pass |
| Format | `gofmt -l pkg cmd` | no output |
| Manual (optional, macOS) | run the two-line snippet in Step 3 | a notification appears |

## Scope

**In scope**:
- `pkg/provider/api.go` — `notifyBrowserDegradation` only.
- A `TestUnit*` test in the provider test files.

**Out of scope**:
- When/whether to notify (call sites unchanged).
- Non-macOS notification mechanisms.
- Any other `exec.Command` use in the repo (none problematic at planning time).

## Git workflow

- Branch: `advisor/017-osascript-notifier-args`
- One commit; imperative subject. Do NOT push.

## Steps

### Step 1: Split construction from execution

Replace the body with a fixed script that reads its message from argv, plus
a pure helper the test can cover:

```go
// osascriptNotificationArgs builds the argv for a degradation notification.
// The message travels as an argument to a fixed script — never interpolated
// into AppleScript source — so no escaping is needed.
func osascriptNotificationArgs(reason string) []string {
	const maxRunes = 200
	if r := []rune(reason); len(r) > maxRunes {
		reason = string(r[:maxRunes]) + "…"
	}
	const script = `on run argv
	display notification (item 1 of argv) with title "Slack MCP fallback active"
end run`
	return []string{"-e", script, reason}
}

func notifyBrowserDegradation(reason string, logger *zap.Logger) {
	if err := exec.Command("osascript", osascriptNotificationArgs(reason)...).Run(); err != nil {
		logger.Debug("Failed to emit browser degradation notification", zap.Error(err))
	}
}
```

Truncate on `[]rune`, as shown — byte slicing can split a UTF-8 rune. Do NOT
add a dependency on the text package for this; the inline form above is the
whole fix. Signature, title string, `.Run()`, and the Debug log all stay
exactly as they are today.

**Verify**: `go build ./...` → exit 0

### Step 2: Unit test

`TestUnitOsascriptNotificationArgs` (table-driven), asserting on the
returned slice:

- Reason containing `"` and `\` and backticks → appears **verbatim** as the
  last element (no escaping — that's the point), and the script element is
  byte-identical to the fixed constant (no interpolation happened).
- 500-char reason → last element ≤ ~204 bytes, ends with `…`, and is valid
  UTF-8 (`utf8.ValidString`) — including when the cut lands mid-rune (use a
  multi-byte-rune input).
- Empty reason → still three elements, empty message.

**Verify**: `go test -count=1 -run 'TestUnitOsascriptNotificationArgs' ./pkg/provider/` → pass

### Step 3: Optional manual smoke (macOS only — this machine qualifies)

```
osascript -e 'on run argv
	display notification (item 1 of argv) with title "Slack MCP fallback active"
end run' 'test "quoted" \back\slash'
```

A notification should appear with the literal quotes and backslashes. This
runs osascript directly and mutates nothing in the repo.

### Step 4: Full suite

**Verify**: `make test` → exit 0; `gofmt -l pkg cmd` → no output

## Test plan

Step 2. The load-bearing assertion: the script string is constant regardless
of input.

## Done criteria

- [ ] `make test` exits 0; new test passes
- [ ] `grep -n 'fmt.Sprintf' pkg/provider/api.go` shows no osascript/AppleScript construction site
- [ ] `git status` shows only in-scope files modified
- [ ] `plans/README.md` status row updated

## STOP conditions

- The excerpt doesn't match (drift).
- You find additional string-built `exec.Command` scripts in the repo while
  grepping — do not fix them here; list them in your report.
- macOS smoke test shows `on run argv` form not delivering notifications on
  this OS version — report; do not fall back to interpolation.

## Maintenance notes

- Reviewer: confirm the message is never concatenated into the `-e` payload
  in the final diff.
- Future notification variants (different titles/subtitles) should extend
  the argv pattern (`item 2 of argv` etc.), never return to Sprintf.
