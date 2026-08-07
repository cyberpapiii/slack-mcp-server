# Battle contract harness

Compare committed MCP behavior goldens, then run baseline microbenchmarks:

```bash
sh scripts/battle/run.sh
```

Capture intentional fixture updates for review:

```bash
sh scripts/battle/run.sh --update
```

`--update` rewrites only `testdata/battle/contracts/`. Review every fixture
diff before committing. No live Slack credentials or network access are used.

Benchmarks cover:

- canonical enabled-tool validation;
- standard message CSV and legend rendering for 100 messages;
- message text normalization with links and mentions.

Committed benchmark output lives in `testdata/battle/baseline/`.
