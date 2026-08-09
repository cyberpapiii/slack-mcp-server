# Slack daily-power local rollout — 2026-08-09

- Source: `3ff349e5ef1c57bae0f3fb87041d95c9f24ada9e`
- Candidate and installed binary SHA-256: `6d20e62d02d1c6f7dfa1bc77b6b7f23871e6d242210d42c19fbf7a5b3eac9a41`
- Previous binary SHA-256: `1206b844cf1cecd78231baedbb3ace2d0a92677aa27633a8a2da7276fe3d04b5`
- Installed target: `/Users/robdezendorf/Documents/GitHub/slack-mcp-server/bin/slack-mcp-server`
- Rollback artifacts: `/Users/robdezendorf/Library/Application Support/plug/backups/slack-daily-power-20260809T0815Z/`
- Post-change Plug config SHA-256: `56b71b0df5c81f564b797c638d75d0eebffcbab9025b7897294067d9ef8c8a6d`
- Running process: PID 26693, started 2026-08-09 05:13:03 EDT, parent Plug PID 52074
- Server version: `v1.3.0-136-g3ff349e-dirty`; the dirty suffix comes from preserved user-owned ignored-file deletions and `default.profraw`, not uncommitted plan code.
- Catalog version: `2026-08-09.2`
- Local actor: team `T039Z3X554H`, user `U03BMAR2R50`, user/browser-session mode
- OAuth rotation: disabled; standard OAuth is not configured
- Official Slack connector identity: unverified because the official connector is not connected in this runtime
- Host curation/confirmation support: unverified, so no local mutation tools were exposed
- Requested daily allowlist: ten read-only tools
- Live inventory: eight read-only tools; `dnd_get` and `lists_items_list` stayed unavailable because standard OAuth is not configured and Lists support is unverified
- Live inventory fingerprint: `5bafd5203bd84f1dcd3f28e4e7844097346969033b87cb3a5b96db2912058b38`
- Read canaries: `slack_auth_status` and `usergroups_list` succeeded through Plug
- Write canaries: skipped; no user confirmation was requested for a concrete Slack mutation
- Repository verification: `make lint && make test` passed after the final review fixes
- Connected-client refresh: Plug reports the correct eight-tool live inventory, but this existing Codex session still retains its pre-rollout Slack tool catalog; a fresh session or MCP reconnect is required before client-side inventory proof can pass

The first automatic config-watcher restart raced ahead of binary installation and failed initialization. A separated per-server disable/enable recovered it. The final deployment used `make deploy-local`; Plug reported all 12 servers healthy, loaded the expected binary, and advertised only read-only Slack tools.

## CI remediation redeploy

The personal-fork Trivy run identified fixed vulnerabilities in the inherited `golang.org/x/crypto`, `x/net`, and `x/text` versions. Commit `a3ca91c4e7d22f410ca8f230ba3e21876f99b419` upgraded those modules and their aligned `x/*` dependencies; `make lint && make test` passed and the next Trivy run succeeded.

- Installed binary SHA-256: `c54392cf8059d5fe0ab9f8e7f166058fe041f56ecf4d44b070733ceed7376a56`
- Running process: PID 30105, started 2026-08-09 05:24:11 EDT, parent Plug PID 52074
- Plug health: 12/12 servers healthy; Slack advertises the same eight read-only tools
- Post-restart canaries: `slack_auth_status` and `usergroups_list` succeeded through Plug with the same team and human user
- Remaining CI residual: the personal fork has no Slack, OpenAI, or ngrok integration secrets, so its inherited live integration job fails before exercising this change
