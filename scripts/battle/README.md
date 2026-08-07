# Battle harness

Regression harness from the 2026-08-07 battle-test program. Two entrypoints,
both offline (no Slack credentials, no network):

| Entrypoint | Purpose | Green means |
|---|---|---|
| `sh scripts/battle/run.sh` | Contract compare + baseline benchmarks | Behavior contract unchanged; benchmarks ran |
| `sh scripts/battle/adversary/run.sh` | Concurrency/failure characterization probes | Coalesce invariants hold; race detector clean; fixtures reject perturbation |

Run both from the repository root after any change to `pkg/server`,
`pkg/handler`, `pkg/provider`, or `pkg/text`. They are independent of
`make lint && make test` and cover behavior those targets do not.

## Contract compare

```bash
sh scripts/battle/run.sh
```

Compares live output against committed goldens in `testdata/battle/contracts/`:

- `tool-registration-surface.txt` — the 31-tool ordered surface
  (`ValidToolNames`, asserted by `TestBattleContractToolRegistrationSurface`);
- `message-csv-legend.txt` — standard CSV rows, `#users:`/`#link_template:`
  legends, bot exclusion (`TestBattleContractMessageCSVLegend`);
- `error-taxonomy.json` — handler errors stay MCP `isError=true`, never
  protocol errors (`TestBattleContractErrorTaxonomy`).

Any mismatch fails with `contract drift`.

## Updating goldens (caution)

```bash
sh scripts/battle/run.sh --update
```

`--update` (equivalently `UPDATE_BATTLE_GOLDENS=1`) rewrites
`testdata/battle/contracts/` with current output and **exits 0 with no review
prompt** — it will happily capture an unintended contract change as the new
truth (measured in ADV-001, `golden-update-autoaccept`). Only run it when you
*intend* to change the behavior contract, and review every fixture diff before
committing. Never run `--update` to "fix" an unexplained compare failure.

## Baseline benchmarks

`run.sh` also runs the three `BenchmarkBattle*` microbenchmarks with
`-benchmem -count=5`. The accepted baseline (medians, raw runs, host, and
command) is committed at `testdata/battle/baseline/2026-08-07.md`, measured at
commit `20257e08`. Benchmarks are informational on every run; there is no
automated threshold. To claim a performance delta, run five repetitions on an
idle host and compare medians against the committed baseline file; append a
new dated file rather than editing the old one.

## Adversary probes

```bash
sh scripts/battle/adversary/run.sh
```

See `scripts/battle/adversary/README.md`. Covers concurrent tool-call flood,
users/channels warm coalescing (24 callers → 1 dependency call), waiter
cancellation, hostile slow dependencies, cancel/restart, and fixture
perturbation. Writes evidence to `testdata/battle/adversary/*.log`; the runner
regenerates those logs, so discard the churn (`git checkout -- testdata/battle/adversary/`)
unless a probe found something worth committing.

## Deploying after changes

These scripts validate source only. The live binary served through Plug is a
separate layer: after landing changes run `make deploy-local` (or rebuild
`bin/slack-mcp-server` and restart Plug's `slack` server) so Cursor picks up
the new code.
