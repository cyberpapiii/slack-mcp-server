# Plan 013: Make `PatchUser`'s snapshot update lose-proof under concurrency

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

- **Priority**: P2
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none. Land BEFORE plan 018 (which adds `-race` to `make test` and would flag this).
- **Category**: bug (data race / lost update)
- **Planned at**: commit `adbae97`, 2026-08-07

## Why this matters

`PatchUser` performs an unsynchronized load → copy → store on the users
snapshot (`atomic.Pointer[UsersCache]`). Two concurrent tool calls (the
SSE/HTTP transports serve tools in parallel, and even under stdio the
background SWR refresh goroutine stores snapshots concurrently) can
interleave so the second `Store` discards the first's patched user — or a
patch clobbers a freshly rebuilt full snapshot with a stale copy plus one
user. The symptom is silent: a user resolves once, then reverts to a raw
`Uxxxx` ID, and the per-call resolver's `attemptedIDs` suppresses a re-fetch.
A compare-and-swap retry loop fixes it in a few lines.

## Current state

`pkg/provider/api.go` at commit `adbae97`:

```go
// api.go:1180-1212 (abridged)
func (ap *ApiProvider) PatchUser(ctx context.Context, userID string) (*slack.User, error) {
	usersInfo, err := ap.client.GetUsersInfo(userID)
	...
	user := (*usersInfo)[0]
	current := ap.usersSnapshot.Load()

	newSnapshot := &UsersCache{
		Users:    make(map[string]slack.User, len(current.Users)+1),
		UsersInv: make(map[string]string, len(current.UsersInv)+1),
	}
	for k, v := range current.Users { newSnapshot.Users[k] = v }
	for k, v := range current.UsersInv { newSnapshot.UsersInv[k] = v }
	newSnapshot.Users[user.ID] = user
	newSnapshot.UsersInv[user.Name] = user.ID

	ap.usersSnapshot.Store(newSnapshot)
	...
}
```

- `usersSnapshot` is `atomic.Pointer[UsersCache]` (`api.go:382`), which has a
  `CompareAndSwap` method.
- Concurrent writers: `fetchAndStoreUsers` (`api.go:1335` intermediate store
  and the final-snapshot store around `:1360`) via the background refresh
  (`spawnBackgroundUsersRefresh`), and other `PatchUser` calls.
- Existing test seam: `TestUnitPatchUser` in `pkg/provider/api_patch_test.go`
  already fakes the client and calls `PatchUser` — extend that file.
- Convention note: `usersMu` exists but is documented as protecting
  `lastForcedUsersRefresh` (`api.go:387`) — do not overload it.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Build | `go build ./...` | exit 0 |
| All unit tests | `make test` | exit 0 |
| Targeted, race-checked | `go test -count=1 -race -run 'TestUnitPatchUser' ./pkg/provider/` | pass, no race report |
| Format | `gofmt -l pkg cmd` | no output |

## Scope

**In scope**:
- `pkg/provider/api.go` — the tail of `PatchUser` only.
- `pkg/provider/api_patch_test.go`

**Out of scope**:
- Hoisting the per-call `userResolver` or batching `users.info` calls
  (surfaced in the audit as a performance finding; deliberately not planned).
- `fetchAndStoreUsers`' own stores (they build full snapshots from source
  data; a CAS in PatchUser is sufficient to stop patches clobbering them).
- The channels snapshot (no equivalent patch path).

## Git workflow

- Branch: `advisor/013-patchuser-cas`
- One commit; imperative subject. Do NOT push.

## Steps

### Step 1: CAS retry loop

Replace the `Load` → build → `Store` tail with:

```go
	for {
		current := ap.usersSnapshot.Load()
		newSnapshot := &UsersCache{ /* same copy as today, from current */ }
		// ... copy maps, add user ...
		if ap.usersSnapshot.CompareAndSwap(current, newSnapshot) {
			break
		}
		// lost the race to another writer; rebuild from the fresh snapshot
	}
```

Keep the debug log after the loop. The copy work moves inside the loop so a
retry rebuilds from the winner's snapshot.

**Verify**: `go build ./...` → exit 0

### Step 2: Concurrency test

In `api_patch_test.go`, add `TestUnitPatchUserConcurrent`: seed a snapshot
with N existing users, fake `GetUsersInfo` to return a distinct user per
requested ID, run ~8 goroutines each patching a different user ID
(`sync.WaitGroup`), then assert the final snapshot contains all 8 patched
users plus the seed users. Model the fake on the existing `TestUnitPatchUser`.

**Verify**: `go test -count=1 -race -run 'TestUnitPatchUser' ./pkg/provider/` → both tests pass, no race detected

### Step 3: Full suite

**Verify**: `make test` → exit 0; `gofmt -l pkg cmd` → no output

## Test plan

Step 2's concurrent test is the regression test — without the CAS it fails
intermittently (lost updates) and `-race` may flag the pattern; with the CAS
it must pass deterministically under `-race`.

## Done criteria

- [ ] `make test` exits 0
- [ ] `go test -race -run 'TestUnitPatchUser' ./pkg/provider/` passes with no race report
- [ ] `PatchUser` uses `CompareAndSwap` in a retry loop (read the diff)
- [ ] `git status` shows only in-scope files modified
- [ ] `plans/README.md` status row updated

## STOP conditions

- The excerpt doesn't match (drift).
- `usersSnapshot` is not `atomic.Pointer[UsersCache]` (CAS unavailable) —
  report; the fallback design (a dedicated mutex) is a reviewer decision.
- The concurrent test is flaky *with* the CAS in place — that indicates a
  second unsynchronized writer you should name in your report, not paper over.

## Maintenance notes

- Plan 018 adds `-race` to the default test target; this plan must merge
  first or 018's gate may fail on the old code.
- Reviewer: confirm the retry loop re-copies from the *fresh* `Load()` on
  each iteration (a loop that reuses the stale `current` spins forever).
- Future: if a batch `PatchUsers(ids...)` is ever added (the audit's
  perf finding), it must use this same CAS pattern.
