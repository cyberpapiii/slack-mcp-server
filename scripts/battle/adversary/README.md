# ADV-001 adversary scenarios

Run all in-process scenarios from repository root:

```sh
sh scripts/battle/adversary/run.sh
```

The runner temporarily copies package-local test probes into `pkg/provider`
and `pkg/server`, runs them with the race detector, and removes them on every
exit path. It also perturbs each GT-001 fixture, requires compare mode to fail,
and verifies that update mode replaces a perturbed fixture without a review
prompt. Original fixtures are restored before exit.

Evidence is written to `testdata/battle/adversary/*.log`.

Exit codes:

- `0`: all characterization probes ran and all expected fixture failures were
  detected;
- `1`: a race/probe failed, an expected fixture failure was not detected, or
  update mode did not restore the canonical fixture;
- `2`: a generated temporary test path already existed, so nothing was
  overwritten.

This runner does not use Slack credentials or network access.
