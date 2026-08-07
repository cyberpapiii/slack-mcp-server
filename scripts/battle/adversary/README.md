# Adversary scenarios (ADV-001 through ADV-004)

Run all in-process scenarios from the repository root:

```sh
sh scripts/battle/adversary/run.sh
```

No Slack credentials or network access. The runner temporarily copies the
package-local probes (`provider_adversary_test.go.in`,
`server_adversary_test.go.in`) into `pkg/provider` and `pkg/server` as
`adv001_generated_test.go`, runs them with `-race -count=5`, and removes them
on every exit path. The `.go.in` extension keeps the probes out of `make test`;
they only exist as tests while the runner executes.

## What it probes

Provider (`TestADV001`–`TestADV004`, `pkg/provider`):

- users warm coalescing: 24 overlapping `RefreshUsers` callers must produce
  exactly 1 users API call + 1 Slack Connect enrichment call (was 24+24
  before HRD-001);
- channels warm coalescing: 24 overlapping `RefreshChannels` callers must
  produce exactly 1 channel API call (was 24 before HRD-003);
- waiter cancellation: a waiter with a 25 ms deadline exits near its deadline
  without cancelling the shared flight;
- leader-deadline characterization: a short-lived leader fails the shared
  attempt once; healthy waiters share one retry (known open gate
  `leader-flight-ctx`, documented not fixed);
- persistent-failure characterization: when the dependency always fails,
  every caller retries serially — the coalesce win applies to success paths
  only;
- hostile never-returning dependency: background timeout fires, flight
  clears, and a later refresh spawns cleanly.

Server (`TestADV001`, `pkg/server`):

- concurrent stdio tool flood: 48 goroutines, 960 calls per run (4,800
  across five runs), synthetic errors preserved as MCP error results;
- 128 concurrent cache-dependent tool registrations, no duplicates;
- cancelled `CallTool` characterization: the handler keeps running past
  150 ms after client cancel (known open gate `cancel-propagation`,
  documented not fixed).

Fixtures: the runner perturbs each GT-001 contract golden, requires compare
mode to fail with `contract drift`, and verifies update mode auto-accepts a
perturbed fixture (documented capture risk). Original fixtures are restored
before exit.

## Output

Evidence is written to `testdata/battle/adversary/*.log`. The logs are
committed snapshots of the last accepted run; a fresh run rewrites them with
new timing jitter. Restore them afterwards unless you intend to commit new
evidence:

```sh
git checkout -- testdata/battle/adversary/
```

Exit codes:

- `0`: all probes ran, all counts held, and all expected fixture failures
  were detected;
- `1`: a race/probe failed, an expected fixture failure was not detected, or
  update mode did not restore the canonical fixture;
- `2`: a generated temporary test path already existed, so nothing was
  overwritten.
