---
title: "Overlapping cache warms multiplied Slack API load 24x; replaced per-path locks/CAS with one shared refresh flight"
date: 2026-08-07
category: performance-issues
module: provider-cache
problem_type: performance_issue
component: pkg/provider
severity: medium
symptoms:
  - "24 overlapping foreground `RefreshUsers` callers produced 24 users API calls plus 24 Slack Connect enrichment calls (measured 5/5 runs)"
  - "24 overlapping cold `RefreshChannels` callers produced 24 channel API calls (measured 5/5 runs)"
  - "A caller with a 25 ms deadline blocked ~100 ms behind a slow predecessor's mutex before noticing its context was dead"
root_cause: missing_coalescing
resolution_type: code_fix
related_components:
  - cache_warmup
tags:
  - cache
  - coalescing
  - single-flight
  - mutex
  - slack-api
  - rate-limit
  - battle-test
---

# Overlapping cache warms multiplied Slack API load 24x; replaced per-path locks/CAS with one shared refresh flight

## Problem

The users and channels cache warm paths in `pkg/provider/api.go` serialized
overlapping refreshes without sharing results. Each caller that entered the
fetch path performed its own full Slack API round trip after the previous
caller released the lock, so N overlapping warms cost N dependency calls.
Warm-up retries, tool-triggered refreshes, and background refreshes could all
overlap on an empty cache, multiplying Slack API and rate-limit load.

## Symptoms

Measured by the adversary harness (`sh scripts/battle/adversary/run.sh`),
five runs each, before the fix:

- Users: 24 overlapping callers → 24 users API calls + 24 Slack Connect
  enrichment calls per cohort.
- Channels: 24 overlapping callers → 24 channel API calls per cohort.
- Deadline blindness: a second warm with a 25 ms deadline waited 100–101 ms
  for the first caller's mutex, then entered the dependency with a dead
  context.

## What Didn't Work

The pre-existing coordination looked like protection but wasn't:

- `fetchUsersMu` / `fetchChannelsMu` serialized callers. Serialization is not
  coalescing: each caller still ran a fresh fetch after acquiring the lock.
- A `refreshingUsers` / `refreshingChannels` CAS guard deduplicated only the
  background-spawn path, not foreground callers. The two mechanisms
  coordinated different subsets of callers and neither shared a completed
  result.

## Solution

Subtract-first: both the mutex and the CAS guard were removed, replaced by a
single shared refresh flight per cache (`usersRefreshCall`,
`channelsRefreshCall` in `pkg/provider/api.go`). One leader runs the
dependency call; overlapping callers wait on the shared result. Waiters
select on their own `ctx.Done()`, so a cancelled waiter exits at its own
deadline without cancelling the shared flight. A failed leader wakes live
waiters and one retries, preserving the old serialized failure semantics.

After, same probes: users 24 → 1 API call + 1 enrichment call; channels
24 → 1 API call (95.8% reduction, 5/5 runs each). The 25 ms waiter returned
in ~25–26 ms instead of ~101 ms. Net code: coordination primitives removed,
no new dependency or layer added.

## Why This Works

A shared flight makes the completed result the unit of coordination instead
of the critical section. Followers no longer pay for the leader's work twice,
and because waiting is decoupled from executing, a waiter's deadline governs
only its wait. The failure path deliberately does not coalesce: retry storms
under persistent failure behave exactly as before the change, which kept the
behavior contract frozen.

## Prevention

- A mutex around a fetch is a smell when callers want the same result:
  serialization multiplies dependency cost by the number of waiters. Reach
  for a shared/single flight instead.
- Split defenses (a CAS for background, a mutex for foreground) indicate the
  coordination lives at the wrong layer; unify on one primitive covering all
  callers.
- Measure caller-to-dependency-call ratio under deliberate overlap (the
  adversary probes assert 24 → 1). A unit test that counts dependency calls
  under concurrency turns this class of regression into a red test.

## Related Issues

- Battle-test program evidence: `testdata/battle/adversary/provider-races.log`
  and `scripts/battle/adversary/README.md` (probe descriptions).
- Two characterized-but-unfixed follow-ups are deliberately open (human
  gates): leader-deadline owning the shared flight (`leader-flight-ctx`) and
  MCP CallTool cancel propagation (`cancel-propagation`).
