# Plan 028: Finish the Makefile purity work — `build-all-platforms` still mutates, `clean` still deletes tracked-looking files

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. Do NOT edit anything under `plans/`; the reviewer
> maintains the index.
>
> **Worktree check (run zeroth)**: `git rev-parse --short HEAD` must be
> `569e010`; otherwise STOP. This plan is on **Track A**, stacked on plan 018 —
> not on the Track B stack where the other current plans live.

## Status

- **Priority**: P3
- **Effort**: XS
- **Risk**: LOW
- **Depends on**: plan 018 (this finishes what 018 started)
- **Category**: DX / tooling
- **Planned at**: commit `569e010`, 2026-08-07

## Why this matters

Plan 018 made `make build` read-only — it no longer runs `go mod tidy` and
`gofmt -w` behind the developer's back — and moved those into an explicit
`make prepare`. But 018's scope named only the `build` target, so its sibling
`build-all-platforms` still carries the same mutating prerequisites. That is
the **release** path, so a release build can still rewrite `go.mod`/`go.sum`
and reformat the tree as a side effect.

Separately, `make clean` `rm -rf`s two paths that look like source files:
`./npm/slack-mcp-server/LICENSE` and `./npm/slack-mcp-server/README.md`. They
are untracked today — copied in by the npm packaging step — so nothing is lost.
But the safety of `clean` rests entirely on those two files never becoming
tracked, and nothing enforces that.

## Current state

Verified at `569e010` (i.e. with plan 018 already applied).

```make
CLEAN_TARGETS :=
CLEAN_TARGETS += '$(BINARY_NAME)'
CLEAN_TARGETS += $(foreach os,$(OSES),$(foreach arch,$(ARCHS),./build/$(BINARY_NAME)-$(os)-$(arch)$(if $(findstring windows,$(os)),.exe,)))
CLEAN_TARGETS += $(foreach os,$(OSES),$(foreach arch,$(ARCHS),./build/extension.dxt/server/$(BINARY_NAME)-$(os)-$(arch)))
CLEAN_TARGETS += $(foreach os,$(OSES),$(foreach arch,$(ARCHS),./npm/$(BINARY_NAME)-$(os)-$(arch)/bin/))
CLEAN_TARGETS += $(foreach os,$(OSES),$(foreach arch,$(ARCHS),./npm/$(BINARY_NAME)-$(os)-$(arch)/.npmrc))
CLEAN_TARGETS += ./npm/slack-mcp-server/.npmrc ./npm/slack-mcp-server/LICENSE ./npm/slack-mcp-server/README.md build/extension.dxt/manifest.json build/extension.dxt/icon.png
CLEAN_TARGETS += ./build/slack-mcp-server.dxt ./build/slack-mcp-server-$(NPM_VERSION).dxt
```

```make
.PHONY: clean
clean: ## Clean up all build artifacts
	rm -rf $(CLEAN_TARGETS)

.PHONY: build
build: clean ## Build the project (read-only: run `make prepare` for tidy+format)
	go build $(COMMON_BUILD_ARGS) -o ./build/$(BINARY_NAME) ./cmd/slack-mcp-server

.PHONY: build-all-platforms
build-all-platforms: clean tidy format ## Build the project for all platforms
	$(foreach os,$(OSES),$(foreach arch,$(ARCHS), \
		GOOS=$(os) GOARCH=$(arch) go build $(COMMON_BUILD_ARGS) -o ./build/$(BINARY_NAME)-$(os)-$(arch)$(if $(findstring windows,$(os)),.exe,) ./cmd/slack-mcp-server; \
	))
```

`build` is clean (plan 018's work). `build-all-platforms: clean tidy format` is
not. `npm-copy-binaries` depends on `build-all-platforms`, so the release chain
inherits the mutation.

The `prepare` target plan 018 added, and the `tidy` / `format` targets, are
further down the file — locate them by name before editing.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Target list | `make help` | lists targets, exit 0 |
| Dry run | `make -n build-all-platforms` | prints go build lines, **no** `go mod tidy` / `gofmt` |
| Dry run | `make -n clean` | prints the `rm -rf`, no LICENSE/README paths |
| Build | `go build ./...` | exit 0 |
| Tests | `make test` | exit 0 |

**Do not actually run `make build-all-platforms`** — it writes six binaries.
`make -n` is the verification here.

## Scope

**In scope**: `Makefile` only, plus any doc line that documents these two
targets (grep `build-all-platforms` and `make clean` across `docs/`,
`README.md`, `AGENTS.md`, `CONTRIBUTING*`).

**Out of scope**:
- `build`, `prepare`, `tidy`, `format`, `test`, `lint` — plan 018 settled
  those. Do not touch them.
- The npm publishing flow itself.
- Whether `LICENSE`/`README.md` *should* be copied into the npm package —
  that mechanism stays exactly as it is.

## Git workflow

- Branch: `advisor/028-makefile-purity`, based on `569e010`.
- One commit, imperative subject. Do NOT push.

## Steps

### Step 1: Drop the mutating prerequisites from `build-all-platforms`

Change:

```make
build-all-platforms: clean tidy format ## Build the project for all platforms
```

to depend on `clean` only, and update the `##` help text to match the wording
plan 018 used on `build` — read `build`'s help string and mirror it, so the two
targets describe themselves consistently.

**Verify**: `make -n build-all-platforms 2>&1 | grep -c 'mod tidy\|gofmt'` → `0`

### Step 2: Stop `clean` from deleting the copied npm docs

Remove `./npm/slack-mcp-server/LICENSE` and `./npm/slack-mcp-server/README.md`
from the `CLEAN_TARGETS` line that lists them, leaving
`./npm/slack-mcp-server/.npmrc` and the two `build/extension.dxt/` entries on
that line.

Then find the target that copies them in (grep `LICENSE` in the Makefile — it
is in the npm packaging chain) and have **that** target remove them itself
before re-copying, or leave them in place. Read the target first and pick
whichever fits its existing structure; a stale copied file being overwritten on
the next run is not a problem worth new machinery.

If removing them from `CLEAN_TARGETS` would leave a genuinely stale artifact
that breaks a subsequent `npm-publish`, STOP and report instead — in that case
the right fix is a narrower `clean-npm` target, which is more than this plan
should decide alone.

**Verify**: `make -n clean` → the printed `rm -rf` contains neither
`npm/slack-mcp-server/LICENSE` nor `npm/slack-mcp-server/README.md`

### Step 3: Docs

Grep for anything documenting these targets:

```
grep -rn 'build-all-platforms\|make clean' docs/ README.md AGENTS.md 2>/dev/null
```

If a doc says `build-all-platforms` formats or tidies, fix it. If nothing
mentions it, note that in your report and skip.

**Verify**: re-read each hit

### Step 4: Confirm nothing else regressed

**Verify**: `make test` → exit 0; `make -n build` still shows no `tidy`/`gofmt`
(plan 018's work intact)

## Test plan

There is no unit test for a Makefile. The verification is the `make -n` dry
runs in Steps 1 and 2 — record their actual output in your report.

## Done criteria

- [ ] `make -n build-all-platforms` shows no `go mod tidy` and no `gofmt`
- [ ] `make -n clean` no longer lists the two npm doc paths
- [ ] `make -n build` is unchanged from `569e010` (018's work intact)
- [ ] `make test` exits 0
- [ ] `git status` shows only `Makefile` (and any doc file) modified
- [ ] `git diff 569e010..HEAD --stat` touches no `.go` file

## STOP conditions

- Removing `tidy format` from `build-all-platforms` breaks the npm or dxt
  chain because something downstream depended on the formatting side effect —
  report which target and how you found out.
- The npm packaging target turns out to *require* `clean` to have removed
  `LICENSE`/`README.md` (e.g. it copies with a flag that fails on an existing
  file) — report; do not invent a `clean-npm` target.
- `git status` shows `go.mod`/`go.sum` modified at any point — that means you
  ran a mutating target. Restore them with `git checkout -- go.mod go.sum` and
  report it.

## Maintenance notes

- After this plan, no `build*` target mutates the working tree; `make prepare`
  is the single explicit place for `tidy` + `format`. Keep it that way — a new
  build target must not grow those prerequisites back.
- The npm `LICENSE`/`README.md` copies remain untracked. If they are ever
  committed, revisit whether the packaging step should copy at all.
- Reviewer: confirm the diff is Makefile-only and that plan 018's `build` and
  `prepare` targets are byte-identical to `569e010`.
