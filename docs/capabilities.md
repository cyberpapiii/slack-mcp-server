# Slack capability ownership

Catalog version: `2026-08-09.1`

The `daily-power` surface combines Slack's official MCP with this local server.
Each user intent has one canonical provider. `pkg/capability/catalog.go` is the
source of truth for provider ownership, local presets, OAuth scopes,
confirmation tiers, result types, and migration state.

## Provider defaults

Slack's official MCP owns ordinary message and file search, channel and thread
reading, message send and persisted-draft creation, message schedule/edit/delete,
file upload/read, conversation creation and membership, reaction add/inspect,
profiles, custom emoji, and canvases.

The local server owns exact-message retrieval, unread and read-progress state,
reaction removal, Activity, Later, user-group management, and diagnostics.
U1's generated `daily-power` allowlist exposes only no-confirmation reads. Local
mutations remain cataloged but hidden until host confirmation is live-proven.
Planned local additions cover only remaining gaps: scheduled-message list and
cancel, channel metadata and archive, Lists, DND, and the isolated browser-only
persisted-draft lifecycle.

`legacy-full` preserves all current local tools for migration. It is not a
canonical combined surface because it includes official-owned duplicates.

## Host policy

The active MCP host must filter the merged inventory by catalog capability ID,
refresh that policy after provider reconnect and `tools/list_changed`, and show
only the catalog owner. Local allowlists cannot hide tools supplied by another
server.

The host must require immediate user confirmation for every Slack mutation
except creating or editing a non-sending draft. Delete, archive, cancel, clear,
and bulk-replacement actions must preview their exact targets first. Tool
annotations and registration gates are defense metadata, not proof of user
approval.

Do not expose huddles, clips, Slack Connect administration, workflows, or
workspace administration through `daily-power`.

## Feasibility record

The official inventory fixture is
`pkg/capability/testdata/official-tools-2026-08-09.json`. It records the schemas
and behavior canaries observed during planning. Catalog tests fail when an
official canonical action disappears or loses its object input schema,
structured result, or verified behavior.

Before enabling combined writes, export the host-visible inventory and compare
it with the fixture and local catalog. The verifier rejects missing or duplicate
owners, wrong providers, excluded administration families, catalog-version
drift, and official/local team or user mismatch.

`slack_auth_status` reports catalog version, local provider identity, browser
health, and plan-dependent availability. `unverified` is intentionally not
equivalent to available. Lists, combined host curation, exact official/local
identity parity, and confirmation cancellation still require live proof.

If host filtering or confirmation cannot be proved, keep mutation tools hidden.
Do not silently route a write to another provider.
