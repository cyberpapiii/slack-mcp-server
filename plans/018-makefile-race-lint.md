# Plan 018: Add `-race` to tests, a `lint` target, and stop `make build` mutating the tree

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
> **Drift check (run first)**: `git diff --stat adbae97..HEAD -- Makefile AGENTS.md`
> On any change, locate targets by name; unlocatable = STOP.

## Status

- **Priority**: P2
- **Effort**: S
- **Risk**: LOW (tooling only — no source changes)
- **Depends on**: **Plan 013 (PatchUser CAS) must land first** — a known
  lost-update pattern exists in `PatchUser` at planning time; `-race` plus
  013's concurrent test would otherwise make the default test target
  fail/flake. If 013 is not merged when you start, STOP.
- **Category**: DX / verification baseline
- **Planned at**: commit `adbae97`, 2026-08-07

## Why this matters

Three gaps in the verification story:

1. **No `-race` anywhere.** The codebase is goroutine-heavy (background
   cache refresh, SWR snapshots, warmup) and its concurrency bugs are
   exactly the kind `-race` catches. The suite runs in <1s; the race
   detector's overhead is irrelevant at that scale.
2. **No vet/lint gate at all.** Not in the Makefile, no CI on this fork —
   `go vet` catches real bug classes (printf mismatches, lock copies) for
   free.
3. **`make build` mutates the working tree**: `build: clean tidy format`
   runs `go mod tidy` and `gofmt -w`, so "just build it" can dirty a
   worktree mid-review — an advisor/executor workflow hazard in this repo
   specifically (plans forbid unplanned mutations).

## Current state

`Makefile` at commit `adbae97` (relevant targets, abridged — read the real
file first; it also has `deploy-local`, `clean`, etc.):

```make
build: clean tidy format
	go build -o bin/slack-mcp-server ./cmd/slack-mcp-server

test:
	go test -count=1 -v -skip="Integration" ./...

tidy:
	go mod tidy

format:
	gofmt -w pkg cmd
```

(Exact recipe lines may differ slightly — match by target name. The load-
bearing facts: `test` has no `-race`; `build` depends on `tidy format`;
there is no `lint`/`vet` target.)

`AGENTS.md` has a build/test commands section (near the top) that executor
agents read — it must stay in sync.

Toolchain: Go 1.25 (`go.mod`). `-race` is supported on darwin/arm64 and
requires CGO available (standard on this machine — Xcode CLT present, since
`make build` already works).

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Baseline | `make test` | exit 0 BEFORE any edits (record time) |
| After edit | `make test` | exit 0, now with `-race` |
| New target | `make lint` | exit 0 on a clean tree |
| Build purity | `git status --porcelain` unchanged before/after `make build` | identical output |

## Scope

**In scope**:
- `Makefile`
- `AGENTS.md` (the build/test commands section only)

**Out of scope**:
- Installing golangci-lint or ANY new tool — the lint target uses only the
  Go toolchain (`go vet`, `gofmt -l`, `go mod tidy -diff`).
- CI workflows (fork has none; upstream's are upstream's).
- Source code (if `-race` or `go vet` FAILS, that's a STOP, not a fix).
- The `deploy-local` target.

## Git workflow

- Branch: `advisor/018-makefile-race-lint`
- One commit; imperative subject. Do NOT push.

## Steps

### Step 0: Preconditions

- Confirm plan 013 merged: `git log --oneline master | head -20` should show
  its commit (subject mentions PatchUser/CAS). Absent → STOP.
- Baseline: `make test` → exit 0. Failing baseline → STOP.

### Step 1: `-race` on the test target

```make
test:
	go test -count=1 -race -v -skip="Integration" ./...
```

**Verify**: `make test` → exit 0. If it reports a data race, STOP — copy the
race report into your handoff; the fix belongs in a source-plan, not here.

### Step 2: `lint` target

```make
lint:
	go vet ./...
	@fmt_out=$$(gofmt -l pkg cmd); if [ -n "$$fmt_out" ]; then echo "gofmt needed:"; echo "$$fmt_out"; exit 1; fi
	go mod tidy -diff
```

`go mod tidy -diff` (Go ≥1.23) exits non-zero if tidy would change
go.mod/go.sum, WITHOUT writing. Add `lint` to `.PHONY` alongside the
existing phony declarations (match the file's existing `.PHONY` style).

**Verify**: `make lint` → exit 0. If `go vet` reports issues, STOP and
report them (they're findings, not yours to fix).

### Step 3: Make `build` read-only

Change `build: clean tidy format` → `build: clean`, and add a convenience
target for the old behavior:

```make
prepare: tidy format
```

**Verify**:
```
git status --porcelain > /tmp/pre.txt 2>&1 || true
make build
git status --porcelain | diff /tmp/pre.txt -
```
→ no diff (bin/ is gitignored — confirm with `git check-ignore bin/slack-mcp-server`).

### Step 4: Sync AGENTS.md

In the build/test commands section: note `make test` now includes the race
detector, add `make lint`, and note `make build` no longer runs
tidy/format (`make prepare` does).

**Verify**: `grep -n 'lint\|prepare' AGENTS.md` → matches in the commands section

## Test plan

The Makefile is its own test: Step 1–3 verifies are the gates. No Go tests
change.

## Done criteria

- [ ] `make test` exits 0 and its output shows `-race` in effect (the go
      test command echo line)
- [ ] `make lint` exits 0
- [ ] `make build` leaves `git status --porcelain` unchanged
- [ ] AGENTS.md commands section updated
- [ ] `git status` shows only `Makefile` and `AGENTS.md` modified
- [ ] `plans/README.md` status row updated

## STOP conditions

- Plan 013 not merged (Step 0).
- `-race` surfaces any race (Step 1) or `go vet` any finding (Step 2) —
  report verbatim output; these become new source plans.
- `go mod tidy -diff` unsupported by the installed Go (pre-1.23 toolchain
  somehow active) — report; do not substitute a mutating tidy.
- `bin/` turns out NOT to be gitignored — report (Step 3's purity check
  would fail through no fault of the recipe change).

## Maintenance notes

- Anyone adding integration-style concurrency tests should know `-race`
  roughly 10×es their runtime — fine at today's <1s suite, revisit if the
  suite grows past ~30s.
- If this fork ever adds CI, `make lint && make test` is the whole gate.
- Reviewer: confirm no target silently lost a prerequisite it needed
  (`clean` must remain on `build` so stale binaries don't ship — that was
  the Plug stale-binary incident, commit `adbae97`'s subject).
