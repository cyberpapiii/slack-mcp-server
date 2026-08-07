---
title: "Silent stale-binary deploys: Plug 'reload' and a disable/enable race kept the old MCP server running"
date: 2026-07-01
category: integration-issues
module: deploy-local
problem_type: integration_issue
component: development_workflow
severity: high
symptoms:
  - "`make deploy-local` exited 0 but the old slack-mcp-server process kept serving requests"
  - "A live `conversations_history` MCP call returned the OLD compact CSV header (User,Channel,Text,Time,Reactions,Cursor) instead of the just-merged format"
  - "`ps -eo pid,lstart,command` showed the running process start time predating the binary build timestamp (e.g. 19:05:49 process vs 19:06:13 build)"
  - "`plug reload` reloaded config but did not restart the running server subprocess"
  - "Back-to-back `plug server disable slack && plug server enable slack` raced, leaving the old process alive"
root_cause: async_timing
resolution_type: workflow_improvement
related_components:
  - tooling
tags:
  - deploy-local
  - makefile
  - plug
  - mcp-stdio
  - stale-binary
  - race-condition
  - process-supervisor
  - staleness-check
---

# Silent stale-binary deploys: Plug 'reload' and a disable/enable race kept the old MCP server running

## Problem

Local deploys of `slack-mcp-server` silently failed to take effect: `make deploy-local` reported success, but the old server process kept serving MCP requests while the freshly built binary sat unused on disk. The root cause was that the deploy recipe never actually restarted the running server subprocess, first because it used the wrong Plug verb, and then because a corrected restart raced against Plug's supervisor.

## Symptoms

- After deploying a change that replaced the compact CSV columns, a live `conversations_history` MCP call still returned the OLD header (`User,Channel,Text,Time,Reactions,Cursor`) instead of the new one (`User,Channel,Text,Time,MsgID,ThreadTs,Reactions,AttachmentIDs,Files,Cursor`). The observable output format was the deployment canary, and it never changed.
- A later fix still showed a just-fixed string in live output: a truncation receipt rendered with CSV-doubled quotes (`""full""`) that the new binary renders as `full`. The bug that had supposedly been deployed was still on screen.
- The deploy commands all exited 0. `plug reload` succeeded, and both `plug server disable slack` and `plug server enable slack` succeeded, so nothing in the command output signaled a problem.
- The decisive symptom was in process metadata, not stdout. `ps -eo pid,lstart,command | grep "[s]lack-mcp-server --transport"` showed the serving process had started at 16:58, hours before the 18:50 rebuild. Later, the running process's start time (`ps -o lstart=`: 19:05:49) PREDATED the new binary's build timestamp (19:06:13, from the `-ldflags` BuildTime). A running process cannot be older than the binary it supposedly executes, which proved the swap had not happened.

## What Didn't Work

### Layer 1: `plug reload` does not restart server subprocesses

The original recipe was:

```makefile
go build ... -o ./bin/slack-mcp-server ./cmd/slack-mcp-server
plug reload && echo "Plug reloaded"
```

`plug reload` re-reads Plug's config from disk but leaves running stdio server children untouched. The new binary was built, the config was re-read, and the echo printed "Plug reloaded", but the process that had been spawned at 16:58 kept serving the old CSV header indefinitely.

How it was detected: a live `conversations_history` call still returned the old header after a successful deploy. `ps -eo pid,lstart,command | grep "[s]lack-mcp-server --transport"` showed the serving process predated the rebuild by hours. The manual workaround that actually swapped the process during debugging was `plug server disable slack`, wait about two seconds, then `plug server enable slack`, which kills and respawns the subprocess.

### Layer 2: back-to-back `disable && enable` races the supervisor

The first Makefile fix encoded the manual workaround but dropped the pause:

```makefile
plug server disable slack && plug server enable slack
```

With no delay between them, the `disable` had not finished tearing down the child when `enable` ran, so Plug kept the old process alive. Both commands exited 0 and the echo printed "restarted", so the failure was completely invisible in the command output, strictly worse than failing loudly because it looked like success.

How it was detected: live MCP output still showed the just-fixed `""full""` truncation string, and the decisive check was that the running process's start time (19:05:49) predated the new binary's build time (19:06:13). The earlier manual bounces had only worked because a human-scale pause happened to sit between the disable and the enable; automating the sequence removed that incidental delay and resurrected the bug.

## Solution

The working `deploy-local` target restarts with a real delay between disable and enable, then verifies the swap by checking the new process exists and printing its start time so a human can compare it against the build time:

Before:

```makefile
go build ... -o ./bin/slack-mcp-server ./cmd/slack-mcp-server
plug reload && echo "Plug reloaded"
```

After:

```makefile
go build $(COMMON_BUILD_ARGS) -o ./bin/slack-mcp-server ./cmd/slack-mcp-server
@echo "Built ./bin/slack-mcp-server"
@if command -v plug >/dev/null 2>&1; then \
    plug server disable slack && sleep 2 && plug server enable slack \
        && echo "Plug slack server restarted with new binary"; \
else \
    echo "plug not in PATH: restart Plug manually"; \
fi
@sleep 3; NEW_PID=$$(pgrep -f 'bin/slack-mcp-server --transport' | head -1); \
if [ -n "$$NEW_PID" ]; then \
    echo "slack-mcp-server running as PID $$NEW_PID (started $$(ps -o lstart= -p $$NEW_PID))"; \
else \
    echo "WARNING: no slack-mcp-server process found, check plug status"; \
fi
```

Verified live: the new process start time (19:07:05) postdates the build (19:07:02), and MCP output shows the new CSV format.

## Why This Works

The bug is entirely about supervisor lifecycle semantics. Plug distinguishes "reload config" from "restart child", and the two failure layers each got one half wrong:

- `plug reload` only re-reads configuration. It has no reason to signal a healthy, already-running stdio child, so the old process survives with the old code. Config-reload verbs are not restart verbs.
- `disable` followed immediately by `enable` fails because `disable` is not synchronous with respect to child teardown. Plug's supervisor needs wall-clock time to actually reap the subprocess; if `enable` arrives before the teardown completes, the supervisor sees the server still registered and keeps the existing process. The `sleep 2` gives the supervisor time to tear the child down before `enable` re-registers it, so the enable spawns a genuinely new process running the new binary.

The trailing `sleep 3` plus `pgrep`/`ps -o lstart=` block turns a silent failure into a loud one. Because the build step already printed the binary's build time, a human (or a future automated check) can compare it against the printed process start time. The invariant is simple and decisive: a running process cannot have started before the binary it executes was built, so if the start time predates the build time, the deploy did not happen.

## Prevention

- A deploy target must verify the process actually swapped, not just that its commands exited 0. Exit codes describe whether the command ran, not whether the running system changed. Add an explicit post-deploy process check that prints the PID and start time.
- Use "process start time must postdate binary build time" as a cheap, decisive deployment invariant. If `lstart < BuildTime`, the deploy did not take effect, full stop. A one-liner to surface both facts:

  ```bash
  NEW_PID=$(pgrep -f 'bin/slack-mcp-server --transport' | head -1)
  ps -eo pid,lstart,command | grep "[s]lack-mcp-server --transport"
  # compare the printed lstart against the binary's -ldflags BuildTime
  ```

- Give deploys an observable output marker that doubles as a canary. A changed CSV header or a changed rendering of a known string lets a smoke test confirm the new code is live from the outside, independent of process metadata.
- Treat supervisor "reload" verbs with suspicion. They frequently mean "reload configuration", not "restart children". Confirm what a verb actually does before trusting it to pick up a new binary; when in doubt, use an explicit disable/enable (restart) path.
- Beware fixes validated manually with incidental human-scale delays. The manual disable/enable bounce worked only because a human paused between the two commands. Automation removes that pause and can resurrect a bug that "worked when I did it by hand". When encoding a manual procedure, make any required delay or ordering explicit rather than relying on human timing.

## Related Issues

- None. This is the first entry in `docs/solutions/`. GitHub issue search skipped: the failure is in local Plug deployment tooling, which does not exist in the upstream repository.
- Related repo docs: the deploy procedure is documented in `docs/agent-presets.md` (header note) and the `deploy-local` target lives in the root `Makefile`.
