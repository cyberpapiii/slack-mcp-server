# Plan 025: Make every write-tool gate validate its value (today `=false` enables the tool)

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. Do NOT edit anything under `plans/`; the reviewer
> maintains the index.
>
> **Worktree check (run zeroth)**: `git rev-parse --short HEAD` must be
> `c037e11`; otherwise STOP.

## Status

- **Priority**: P1 (security-relevant: affects write-capable tools)
- **Effort**: S
- **Risk**: MEDIUM — this is a **deliberate behavior change**. Anyone who set
  a gate to a non-truthy value and got the tool *enabled* will now get it
  disabled. That is the point of the fix, and it fails safe (toward disabled).
- **Depends on**: plan 024 (same files; stack after it)
- **Category**: security / correctness
- **Planned at**: commit `727b517`, 2026-08-07

## Why this matters

Five environment variables gate write-capable or data-exfiltrating tools. Two
of them validate their value. Three test only "is it non-empty".

So today:

```
SLACK_MCP_CHANNEL_MEMBERSHIP_TOOL=false   → conversations_join/leave ENABLED
SLACK_MCP_USERGROUPS_WRITE_TOOL=off       → usergroup writes ENABLED
SLACK_MCP_FILES_LIST_TOOL=no              → files_list ENABLED
```

Each of those tools' own error message says *"set the … environment variable to
true or 1"*, so an operator who writes `=false` reasonably expects "off" and
gets "on". An operator disabling a tool is doing it for a reason; silently
inverting that is the failure mode worth fixing.

`SLACK_MCP_ATTACHMENT_TOOL` and `SLACK_MCP_MARK_TOOL` already do this right and
are the pattern to copy.

## Current state

All excerpts verified at `727b517`.

### The correct pattern — `SLACK_MCP_MARK_TOOL` (`pkg/handler/conversations.go:2570-2585`)

```go
	toolConfig := os.Getenv("SLACK_MCP_MARK_TOOL")
	if toolConfig == "" {
		ch.logger.Error("Mark tool disabled by default")
		return nil, errors.New(
			"by default, the conversations_mark tool is disabled to prevent accidental marking of messages as read. " +
				"To enable it, set the SLACK_MCP_MARK_TOOL environment variable to true or 1, " +
				"e.g. 'SLACK_MCP_MARK_TOOL=true'",
		)
	}
	if toolConfig != "1" && toolConfig != "true" && toolConfig != "yes" {
		ch.logger.Error("Mark tool disabled by config", zap.String("config", toolConfig))
		return nil, errors.New(
			"the conversations_mark tool is disabled. " +
				"To enable it, set the SLACK_MCP_MARK_TOOL environment variable to true or 1",
		)
	}
```

`SLACK_MCP_ATTACHMENT_TOOL` does the same at `conversations.go:2510-2526`, with
the extra wrinkle that an entry in `SLACK_MCP_ENABLED_TOOLS` substitutes
`toolConfig = "true"`.

The accepted set in both places is exactly `"true"`, `"1"`, `"yes"`.

### The broken gate — `requireToolEnabled` (`pkg/handler/conversations.go:2310-2318`)

```go
// requireToolEnabled reports whether a call-time-gated tool is enabled: either
// its dedicated envVarName is set to a non-empty value, or toolName is
// explicitly listed in the SLACK_MCP_ENABLED_TOOLS allowlist.
func requireToolEnabled(envVarName, toolName string) bool {
	if os.Getenv(envVarName) != "" {
		return true
	}
	return isToolInEnabledList(os.Getenv("SLACK_MCP_ENABLED_TOOLS"), toolName)
}
```

Callers (all verified):

```
pkg/handler/conversations.go:1831  SLACK_MCP_CHANNEL_MEMBERSHIP_TOOL / conversations_leave
pkg/handler/conversations.go:1868  SLACK_MCP_CHANNEL_MEMBERSHIP_TOOL / conversations_join
pkg/handler/usergroups.go:115      SLACK_MCP_USERGROUPS_WRITE_TOOL   / usergroups_create
pkg/handler/usergroups.go:166      SLACK_MCP_USERGROUPS_WRITE_TOOL   / usergroups_update
pkg/handler/usergroups.go:228      SLACK_MCP_USERGROUPS_WRITE_TOOL   / usergroups_users_update
```

Every one of those five sites' error text says "set … to true or 1".

### The broken registration gate — `shouldAddTool` (`pkg/server/server.go:118-135`)

```go
func shouldAddTool(name string, enabledTools []string, envVarName string) bool {
	if envVarName == "" {
		if len(enabledTools) == 0 {
			return true
		}
		return slices.Contains(enabledTools, name)
	}
	if len(enabledTools) > 0 && slices.Contains(enabledTools, name) {
		return true
	}
	if len(enabledTools) == 0 {
		return os.Getenv(envVarName) != ""
	}
	return false
}
```

The `os.Getenv(envVarName) != ""` on the second-to-last line is the same bug at
registration time — **but only for the boolean gates**.

**Corrected 2026-08-07.** The first version of this plan listed only the five
boolean gate vars as callers and told the executor to change that shared line
outright. That was wrong, and the executor STOPped on it. The full caller set,
verified by `grep -n 'shouldAddTool(' pkg/server/server.go` at `c037e11`:

| Env var | Call sites | Kind |
|---|---|---|
| `SLACK_MCP_ATTACHMENT_TOOL` | 331 | boolean |
| `SLACK_MCP_FILES_LIST_TOOL` | 409 | boolean |
| `SLACK_MCP_MARK_TOOL` | 434 | boolean |
| `SLACK_MCP_CHANNEL_MEMBERSHIP_TOOL` | 449, 461 | boolean |
| `SLACK_MCP_USERGROUPS_WRITE_TOOL` | 510, 531, 555 | boolean |
| **`SLACK_MCP_ADD_MESSAGE_TOOL`** | **244** | **channel list** |
| **`SLACK_MCP_REACTION_TOOL`** | **293, 312** | **channel list** |

(Every other `shouldAddTool` call passes `""` as `envVarName` and takes the
early-return path, unaffected by this plan.)

The last two are **not booleans**. `SLACK_MCP_REACTION_TOOL`'s own error text
(`pkg/handler/conversations.go`, in `parseParamsToolReaction`) reads:

```
"To enable them, set the SLACK_MCP_REACTION_TOOL environment variable to true, 1, or comma separated list of channels
to limit where the MCP can manage reactions, e.g. 'SLACK_MCP_REACTION_TOOL=C1234567890,D0987654321', 'SLACK_MCP_REACTION_TOOL=!C1234567890'
to enable all except one or 'SLACK_MCP_REACTION_TOOL=true' for all channels and DMs"
```

`SLACK_MCP_ADD_MESSAGE_TOOL` works the same way. For those two, a value like
`C1234567890,D0987654321` or `!C1234567890` is a legitimate "on" — applying
truthiness would silently unregister the tool. Step 3 below therefore
distinguishes the two kinds rather than changing the shared line.

Note the two-layer design that already exists and must be preserved: when
`SLACK_MCP_ENABLED_TOOLS` is non-empty, the per-tool env var is **not**
consulted at registration — the allowlist wins. Do not change that.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Build | `go build ./...` | exit 0 |
| All unit tests | `make test` | exit 0 |
| Targeted | `go test -count=1 -run 'TestUnit' ./pkg/handler/ ./pkg/server/...` | pass |
| Format | `gofmt -l pkg cmd` | no output |

## Scope

**In scope**: `pkg/handler/conversations.go`, `pkg/server/server.go`, their
test files, and `docs/` + `.env.dist` only where they state the accepted
values.

**Out of scope**:
- `SLACK_MCP_ENABLED_TOOLS` parsing and the allowlist-wins precedence.
- Changing the **semantics** of `SLACK_MCP_ADD_MESSAGE_TOOL` or
  `SLACK_MCP_REACTION_TOOL`. Both are **channel lists**, not booleans. Step 3
  adds them to an explicit exemption set precisely so their behavior stays
  byte-identical. Do not apply truthiness to either.
- Any change to which tools are gated. Same five vars, same five tools.
- `SLACK_MCP_ALLOW_UNAUTHENTICATED` (plan 016 owns it; it is strict-`true` by
  design).

## Git workflow

- Branch: `advisor/025-tool-gate-truthiness`, based on `c037e11`.
- One commit, imperative subject. Do NOT push.

## Steps

### Step 1: One shared truthiness helper

Add an unexported helper next to `isToolInEnabledList` in
`pkg/handler/conversations.go`:

```go
// isTruthyEnv reports whether a gate environment variable's value means
// "enabled". The accepted set matches the SLACK_MCP_ATTACHMENT_TOOL and
// SLACK_MCP_MARK_TOOL checks and the error text every gate prints: true, 1,
// yes. Comparison is case-insensitive and ignores surrounding whitespace so
// that `=True` and `= true` behave as an operator expects.
func isTruthyEnv(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "1", "yes":
		return true
	}
	return false
}
```

`strings` is already imported in that file.

**Verify**: `go build ./...` → exit 0

### Step 2: Use it in `requireToolEnabled`

Replace the `os.Getenv(envVarName) != ""` test with
`isTruthyEnv(os.Getenv(envVarName))` and update the doc comment, which
currently says "set to a non-empty value" — that phrasing is exactly the bug.

Do not change the `SLACK_MCP_ENABLED_TOOLS` fallback line.

**Verify**: `go build ./...` → exit 0

### Step 3: Use it in `shouldAddTool` — boolean gates only

`pkg/server/server.go` is a different package and cannot call the handler
helper. Add an equivalent unexported `isTruthyEnv` in `pkg/server/server.go`
with the same accepted set and a comment pointing at the handler copy.

Duplicating six lines across two packages is the right call here — the
alternative is a new shared package for one predicate. Say so in a comment on
both copies so a future reader knows they must stay in sync.

Then add, next to it, an explicit list of the channel-list gates:

```go
// channelListGates are gate variables whose value is a channel allowlist, not
// a boolean — e.g. "C1234567890,D0987654321" or "!C1234567890". For these, any
// non-empty value means "enabled", because the value IS the configuration.
// Every other gate variable is a boolean and goes through isTruthyEnv.
var channelListGates = map[string]bool{
	"SLACK_MCP_ADD_MESSAGE_TOOL": true,
	"SLACK_MCP_REACTION_TOOL":    true,
}
```

Replace `return os.Getenv(envVarName) != ""` with:

```go
	value := os.Getenv(envVarName)
	if channelListGates[envVarName] {
		return value != ""
	}
	return isTruthyEnv(value)
```

So `SLACK_MCP_ADD_MESSAGE_TOOL` and `SLACK_MCP_REACTION_TOOL` keep exactly
today's behavior, and the five boolean gates get validated.

Note that `"true"` and `"1"` are already valid channel-list values meaning "all
channels", so the two kinds do not conflict — a channel-list gate set to
`true` still registers, as it does today.

**Verify**: `go build ./...` → exit 0; existing `shouldAddTool` tests in
`pkg/server/server_test.go` covering `ADD_MESSAGE` and `REACTION` still pass
**unmodified** — if you had to touch them, you broke a channel-list gate, so
STOP.

### Step 4: Make the two existing validators use the helper

`parseParamsToolFilesGet` (`conversations.go:2523`) and `parseParamsToolMark`
(`conversations.go:2579`) each spell out
`x != "true" && x != "1" && x != "yes"`. Replace both with `!isTruthyEnv(x)`.

This is a small behavior widening — those two gates become case-insensitive and
whitespace-tolerant, matching the other three. Keep every error message string
byte-identical.

**Verify**: `go build ./...` → exit 0

### Step 5: Tests

There is an existing `TestUnitRequireToolEnabled` in
`pkg/handler/conversations_test.go` (at `727b517` it begins around line 1050 —
locate it by name). Read it fully; it almost certainly asserts the current
non-empty behavior and **must be updated deliberately**. Extend it to a table
covering, with `t.Setenv`:

- unset → false
- `"true"`, `"1"`, `"yes"`, `"TRUE"`, `"  true  "` → true
- `"false"`, `"0"`, `"no"`, `"off"`, `""`, `"maybe"` → **false** (this is the
  fix; `"false"` returning true is the bug)
- unset gate + tool named in `SLACK_MCP_ENABLED_TOOLS` → true
- gate set to `"false"` + tool named in `SLACK_MCP_ENABLED_TOOLS` → true
  (allowlist still wins; assert this so the precedence is pinned)

`pkg/server` already has `server_test.go` and `tool_phases_test.go`, and
`shouldAddTool` is already covered there (`TestShouldAddTool_WriteTool_Reactions`,
`TestShouldAddTool_RegistrationTimeGates`, and an `ADD_MESSAGE` table). Extend
the existing coverage — do not create a new file. Add at minimum:

- a boolean gate (`SLACK_MCP_CHANNEL_MEMBERSHIP_TOOL`) set to `"false"` with an
  empty `SLACK_MCP_ENABLED_TOOLS` → false
- the same gate set to `"true"` → true
- **`SLACK_MCP_ADD_MESSAGE_TOOL="C123,C456"` → true** and
  **`SLACK_MCP_REACTION_TOOL="!C123"` → true** — the channel-list regression
  guards. The existing subtests already assert this; confirm they still pass
  unmodified and add the `!C123` negation case if it is not covered.
- the existing allowlist-wins path, unchanged

**Verify**: `go test -count=1 -run 'TestUnit' ./pkg/handler/ ./pkg/server/...` → pass

### Step 6: Docs

Plan 021 rewrote the gate documentation to describe the code's *actual*
behavior (non-empty enables). That documentation is now wrong in the other
direction. Grep for it and fix:

```
grep -rn 'non-empty\|CHANNEL_MEMBERSHIP_TOOL\|USERGROUPS_WRITE_TOOL\|FILES_LIST_TOOL' docs/ AGENTS.md .env.dist README.md
```

The known hits at `c037e11` are `.env.dist:42, 46, 50` ("Any non-empty value.")
and `AGENTS.md:66, 67, 68` plus `AGENTS.md:70` ("a gated tool registers only if
its own env var is non-empty"). Each should now say the accepted values for the
**five boolean gates** are `true`, `1`, or `yes` (case-insensitive), and that
any other value — including `false` — leaves the tool disabled.

`AGENTS.md:70`'s blanket claim needs a carve-out: it is still true for
`SLACK_MCP_ADD_MESSAGE_TOOL` and `SLACK_MCP_REACTION_TOOL`.

**Do not touch `AGENTS.md:63`** — it documents `SLACK_MCP_REACTION_TOOL` as
accepting `true`/`1` or a channel allowlist, which remains correct.

**Verify**: re-read each hit; `go build ./...` → exit 0

### Step 7: Full suite

**Verify**: `make test` → exit 0; `gofmt -l pkg cmd` → no output

## Test plan

Step 5. The single load-bearing assertion: `SLACK_MCP_CHANNEL_MEMBERSHIP_TOOL=false`
must leave `conversations_join` / `conversations_leave` **disabled**.

## Done criteria

- [ ] `make test` exits 0
- [ ] `grep -n 'os.Getenv(envVarName) != ""' pkg/server/server.go` → no output
- [ ] `grep -n 'os.Getenv(envVarName) != ""' pkg/handler/conversations.go` → no output
- [ ] `SLACK_MCP_ADD_MESSAGE_TOOL` handling is unchanged (read the diff)
- [ ] Docs no longer claim any non-empty value enables these gates
- [ ] `git status` shows only in-scope files modified

## STOP conditions

- `TestUnitRequireToolEnabled` asserts the non-empty behavior with a comment
  indicating it is *intentional* — report before changing it.
- A gate var turns out to be read somewhere else with different semantics
  (grep each of the seven names across `pkg/` and `cmd/` before editing) —
  report any site this plan did not list.
- `grep -n 'shouldAddTool(' pkg/server/server.go` shows a gate variable not in
  this plan's caller table — an eighth gate would need classifying as boolean
  or channel-list before you touch the shared line. STOP and report it.
- `SLACK_MCP_ADD_MESSAGE_TOOL` or `SLACK_MCP_REACTION_TOOL` appears in
  `requireToolEnabled`'s call sites — it should not; both are handled at
  registration only. STOP if either does.
- Any existing `pkg/server` test covering `ADD_MESSAGE` or `REACTION` fails.
  That means the exemption set is not working; do not edit the test to pass.

## Maintenance notes

- Two copies of `isTruthyEnv` now exist (`pkg/handler`, `pkg/server`) by
  deliberate choice. If a third package ever needs one, that is the signal to
  extract a shared package rather than adding a third copy.
- This is the fix for the "three gates accept any value" finding recorded in
  `plans/README.md` under the 2026-08-07 surfaced-but-not-selected section.
- Reviewer: confirm the change fails *safe* — every value that previously
  enabled a tool and no longer does should be a value no one would write
  meaning "on".
