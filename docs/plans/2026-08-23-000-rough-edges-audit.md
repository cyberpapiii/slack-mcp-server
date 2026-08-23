---
title: Rough-Edges Audit
type: audit
date: 2026-08-23
topic: rough-edges-audit
commit: cf3a966
---

# slack-mcp-server rough-edges audit — 2026-08-23 (HEAD cf3a966)

Five parallel read-only audits (server, handler, provider/edge, repo/docs, agent UX). Top claims spot-verified by hand. No code changed.

## 0. Bottom line

Codebase is healthy (lint clean, tests pass, no TODOs), but it has grown by accretion: three commits' worth of tool files, 10 parallel tool-name lists, 6 `limit` policies, 4 env-bool parsers, 3 error classifiers, 2 pagination contracts. Most findings are "two ways to do one thing." Handful of real bugs. Biggest single win for agents: ~50 KB of `tools/list` is dead weight.

## 1. Real bugs / security (fix first)

| # | Where | What |
|---|---|---|
| B1 | `pkg/server/server.go:192` | `server.WithRecovery()` registered before the tool middlewares → outermost. Panics bypass `buildErrorRecoveryMiddleware` and surface as JSON-RPC -32603 (the very thing the middleware claims to prevent). Move it after the three `WithToolHandlerMiddleware` calls. |
| B2 | `pkg/server/core_tools.go:347`, `pkg/handler/usergroups.go:267` | `usergroups_me action=join/leave` mutates membership with no env gate at either layer; `usergroups_join/leave` are gated twice. Catalog already marks it legacy. Gate or delete. |
| B3 | `pkg/server/server.go:151-172`, `cmd/.../main.go:196` | `shouldAddTool` only consults env gates when `enabledTools` is empty; preset default `daily-power` means it never is. `SLACK_MCP_ADD_MESSAGE_TOOL=true` alone registers nothing. README + handler error text tell users to set a var that cannot help. `channelListGates` and ~200 test lines exercise a dead branch. |
| B4 | `pkg/handler/conversations_write.go:447` | Reactions gate `!inList(add) && !inList(remove)` — enabling `reactions_add` alone also enables `reactions_remove`. |
| B5 | `pkg/handler/channel_mutations.go:181-201` vs `tool_gates.go:71` | Two contradictory readings of `SLACK_MCP_ENABLED_TOOLS` in one package (additive vs exclusive allowlist). |
| B6 | `pkg/handler/channels.go:181-220` | `sort=popularity` applied after pagination; cursor attached to wrong row. |
| B7 | `pkg/handler/usergroups.go:262` | Overwrites `;`-joined Users with `,` → commas inside CSV cell. |
| B8 | `pkg/provider/api.go:224-246` | `isBrowserSessionAuthError` substring-matches `login`/`cookie`/`session`; one-way latch to degraded + osascript notify; self-matches its own error text. Match Slack error codes instead; add re-probe. |
| B9 | `pkg/provider/edge/edge.go:288-290` | Body leak on missing `Retry-After`; returns opaque error so `CallWithRetry` gives up. |
| B10 | `pkg/provider/api.go:590-622` | `mergeStandardConversationPages` unbounded loop, no seen-cursor guard (edge/search.go has one). |
| B11 | `pkg/provider/api.go:532-545,521-531,626,928` | Edge errors swallowed (no log, no degrade); `ClientUserBoot` gates but never degrades; `attachBrowserToOAuth` writes degraded state directly, skipping notify/status. |
| B12 | `pkg/limiter/limits.go:15` + all callers | `tier.Limiter()` returns a new limiter per call — N concurrent tools = N× burst. ~35 tools bypass limiter entirely. Make process-wide singletons. |
| B13 | `.github/workflows/unit-tests.yaml:6` | `pull_request_target` + checkout w/o ref → PR code never tested, elevated context. Use `pull_request`. |
| B14 | `Makefile:69-75`, `release.yaml` | `build-dxt` copies into `build/extension.dxt/server/` which no longer exists (deleted in 192fdfe). Release workflow red on tag. Also release/image workflows publish to korotovsky namespaces; `make release` pushes tag to `origin` = upstream. |
| B15 | `pkg/handler/saved.go:116-119,192` | Limiter error breaks inner loop only; collected items discarded, reported as "cancelled". |
| B16 | `pkg/handler/conversations_files.go:40` | Size cap checked against Slack-reported size, not downloaded buffer. |
| B17 | `pkg/handler/*` 8 sites | Typed `*ToolError` returned as Go error → agent loses code/retryable. `lists.go` mixes both in one function. |
| B18 | `pkg/provider/lists.go:18,307` | Hardcodes `https://slack.com/api/` → Slack Lists broken under `SLACK_MCP_GOVSLACK`. |
| B19 | `pkg/provider/oauth_store.go:96` | `syscall.Flock` with no build tag; breaks Windows build despite `_other.go` split. |
| B20 | `pkg/handler/conversations_unreads.go:601` | `go ForceRefreshChannels(context.Background())` per `conversations_open`, error dropped, unbounded. |

## 2. Agent-facing token + UX (biggest payoff per line)

| # | What | Est. saving |
|---|---|---|
| T1 | `jsonschema_description` tags (32) are ignored by google/jsonschema-go (reads `jsonschema` only). Every output-schema field doc incl. prompt-injection guard never reaches agent. Rename tag or drop. | correctness |
| T2 | `dailyPowerOutputSchema` inlines ~700 B envelope into 49 tools, no `$ref`. `UserProfile` (36 fields, 8 avatar URLs) inlined 3×; `Message` 4×; `ListItem` 4×. Keep schemas only where agent must machine-parse (auth_status, prepare/execute). | 40-50 KB of ~108 KB tools/list |
| T3 | `toolDetailDescription` 436 ch × 7. Explains env var agent can't set. Cut to ~60 ch. | ~2.6 KB |
| T4 | 117 params in daily_power/custom_power files have **zero** descriptions (incl. `action` on 5 prepare/execute tools); core_tools describes 100% at ~110 ch. Two opposite conventions. | clarity |
| T5 | Boilerplate × N: channel-id sentence × 11 (two punctuation variants), cursor × 5, "Requires client confirmation" × 11 (duplicates destructiveHint). Extract consts. | ~7 KB |
| T6 | Two pagination contracts: cursor-in-last-CSV-row (history/replies/channels_list/files_list/search) vs `meta.next_cursor` (lists/scheduled/drafts/members/emoji/semantic/saved) vs none (activity_unreads, conversations_unreads, usergroups_*, channels_starred). Cursor param text wrong for 6 tools. Standardize on `meta.next_cursor`. | prevents loops |
| T7 | `saved_list`/`conversations_unreads` default path drops own primary keys (State/DateDue/UnreadCount) from text; only in structuredContent. `saved_update` unreachable from visible text. | correctness |
| T8 | `Channel` column means 3 things: `C123 (#name)` / bare ID / name only. Legend says "use leading ID" — false for unreads. | correctness |
| T9 | `fallbackJSON` at 27 sites + `auth_status` `MarshalIndent` → payload shipped twice (structured + pretty JSON). One-line human fallback instead. | ~2× per call |
| T10 | Redundant columns: saved `ItemID==ChannelID`; activity 3 timestamps/row; files_list 3 type renderings + full permalink; unreads struct lacks csv tags → Go field names as headers. | per-row |
| T11 | Param naming: `timestamp`/`ts`/`feed_ts`; `list_id` vs `id`; `limit` vs `max_*` ×4; `limit` is **string** on history/replies/files_list, number elsewhere (strict clients reject). | confusion |
| T12 | Overlaps: `usergroups_me`≡`usergroups_mine`+join+leave (4 tools, 1 capability); `conversations_unreads` vs `activity_unreads` never contrast each other; `search_semantic` desc says nothing about when to choose it, returns raw JSON, no `detail`; `conversations_draft_message` (preview, not saved) vs `drafts_create`. | wrong picks |
| T13 | `channels_list.channel_types` required, no default; `channels_me` defaults. First exploratory call errors. | friction |
| T14 | Errors: raw `%v` Go strings (`failed to marshal…`), panic text reaches agent, `channel %q not found` no remedy, degraded errors don't name `slack_auth_status`. Good pattern exists (`outcome_unknown` family) — apply everywhere. | actionability |
| T15 | `destructiveHint:true` on `conversations_add_message`/`reactions_add` (additive ops) → clients prompt on every post/emoji. | friction |
| T16 | CSV header casing split (PascalCase vs snake_case); `MsgID`/`Ts`/`FeedTs` same concept. | consistency |

## 3. Structural simplification

| # | What |
|---|---|
| S1 | **Table-driven tool registry.** Four tool files are commit strata not a design axis (`channels_*` split across 3 files; `newDailyPowerTool` used in all 4; `registerCustomPowerTools` nested inside `registerDailyPowerLifecycleTools`). Six registration idioms. Ten parallel tool-name lists, only 3 drift-tested; handler `requireToolEnabled` uses bare string literals (24 pairs) vs constants at registration. One `{name, gate, phase, service, desc, params, handler}` table → drift tests become one `ElementsMatch`. |
| S2 | **`api.go` 2010 lines → ~150.** Split seams: cachefile / bootstrap / browser_session / client / enterprise_merge / cache_users / cache_channels / channel_map; managed-OAuth rotation belongs in oauth.go. `newWithXOXP`≈`newWithXOXC` 95% identical; users/channels refresh coordinator verbatim duplicate (~120 lines); 8 browser-gated methods one generic `browserCall[T]`. |
| S3 | **Finish WebAPI() pinning.** `canvas_drafts_search.go:102` (`canvasSlackAPI`) and `people_channels.go:139` (5 of 7 passthroughs) still duck-type. 12 pure passthrough methods on `MCPSlackClient` deletable. `DNDClient`, `draftsEdgeAPI`, `ListsClient.Token` unused. `SlackAPI` interface → rename `BrowserSlackAPI`, hide cache-internal methods. |
| S4 | **One of each:** limit policy (6 now), env-bool parser (4), error classifier (3 + 1 raw), 429→RateLimitedError conversion (3), json-to-string helper (3), present-string helper (2), Retry-After parser (3), base-URL strategy (3), HTTP client source (2), browser-degraded preamble (5 copies). `slackRetryAfter` byte-identical in handler and provider. |
| S5 | **Shared helpers in topic files:** `fallbackJSON` in scheduled.go (27 users), `decodeArguments` in lists.go, `requiredChannelID` in channel_mutations.go, `validUnreadsChannelTypes` in conversations_users.go (hand-synced with `channelTypePriority`). Move to `results.go`/`params.go`. |
| S6 | **Orphan doc comments** from the conversations.go split (7 sites) document functions now in other files. `conversations_test.go` (2072 lines) not split with source. |
| S7 | **Dead code:** `pkg/capability` VerifyInventory subsystem (~120 lines, test-only), `official()`, `OfficialAction`, `PlanDependent`, `BrowserOptional`, `MigrationPlanned` (never assigned → `LegacyFullLocalTools` filter tautology → `ValidateEnabledTools` validates nothing); `edge/userlist.go` + `ConversationsView` (~330 lines, zero prod callers); `edge` exports with no external caller (unexport all but Client/NewWithInfo/response types); `isKeychainItemNotFound` + `os/exec` import; `browser_auth_runtime.json` write-only (no reader); `getBotInfo` always-true; `lookupLimitReached` unreachable; `main.go:243` dead branch; `effectiveOAuth()` vestigial. |
| S8 | **Env as control plane:** `main.go:204` `os.Setenv(SLACK_MCP_ENABLED_TOOLS)` so handlers can re-derive policy — makes handler gates untestable in parallel. Thread resolved policy struct. |
| S9 | **Library `logger.Fatal`:** 9 sites in `pkg/provider/api.go`, 2 in `pkg/server/server.go`, 4 in `pkg/transport`. `newWithXOXC` proves error-return shape works. |
| S10 | **`commandRunner` keychain shim** builds fake `security(1)` argv then re-parses it into Security.framework calls; deprecated `SecKeychainItemCreateFromContent` etc. Replace with `keychainStore` interface. `SaveIfGeneration` relies on caller holding flock, not expressed in signature. |
| S11 | `edge` logs via `log/slog` (escapes zap config, stderr under stdio). |
| S12 | Confirmation tier in catalog decorative; `usergroups_users_update`/`saved_clear_completed` marked Preview but lack `action` param; nothing asserts. |

## 4. Reliability / perf

| # | What |
|---|---|
| R1 | Background refresh + managed OAuth loop use `context.Background()`, no shutdown, 15-min outlive. |
| R2 | Failed-leader retry storm: N waiters → N serialized refreshes when Slack unhappy. |
| R3 | `atomicWriteFile` no fsync; `MarshalIndent` for 50k-user cache (3× bytes); `getCacheDir` silently falls back to `.`. |
| R4 | IM name mapping gap: channels refreshed before users → all no-user IMs collide on `ChannelsInv["@"]`, persists until full refresh. |
| R5 | `searchUsersInCache` iterates Go map → non-deterministic results; dead `&& IsOAuth()` branch. |
| R6 | `ProvideHTTPClient` builds fresh Transport per call (`users_set_profile` cold pool every call; `Lists()` leaks pool). uTLS path closes conn per request (no reuse). |
| R7 | `startupJitter` runs twice on OAuth+browser path (up to 6 s). |
| R8 | `usergroups_me` join/leave calls `GetUserGroupMembersContext` twice as fake CAS; `scheduled_message_cancel` runs `findScheduled` on both prepare and execute (up to 20 list calls); `GetDraft` crawls 100 pages before every `UpdateDraft`; `ListEmoji` refetches + re-sorts whole map per page. |
| R9 | `normalizeLinks` quadratic and wrong for duplicate links (`LastIndex` decides, `Replace(…,1)` rewrites first). |
| R10 | `h.identity()` nil-deref in 5 constructors (others guard). |
| R11 | `warmupMaxAttempts` is a threshold not max; no ctx/shutdown/jitter. |
| R12 | Logging: per-request Warn for deprecated SSE key; auth failure triple-logged at Error; logger middleware never logs err (wrapped inside recovery); cache-write `Info` every refresh. |

## 5. Repo / docs hygiene

| # | What |
|---|---|
| D1 | CI never runs `make lint`. Release workflow pins Go 1.25 vs go-version-file elsewhere. |
| D2 | Upstream residue the fork never exercises: `npm/`, `manifest-dxt.json`, `docker-compose.toolkit.yml` (still recommends fatal `SERVER_CA_TOOLKIT`), `docker-compose.dev.yml`, `docker-compose.yml` (points at upstream image), `images/*.gif`, `.gitignore` stale DXT paths. |
| D3 | `plans/` 488 K, 32 merged plans, referenced nowhere; `docs/plans/` and `docs/solutions/` each hold 1 file. Three locations, pick one. |
| D4 | Docs drift: "31 tools" ×3 (README:303, docs/03:254, AGENTS:154) vs 69; README documents 18/69 tools with no "partial" note; preset system (`SLACK_MCP_TOOL_PRESET`, default `daily-power`) absent from README/docs/03; docs/02-03 install paths all install upstream v1.3.0; ~16 fork env vars only in AGENTS.md; `.env.dist` says "two channel-allowlist gates" (three); `AGENTS.md` gate table understates 4 gates (notably `ADD_MESSAGE_TOOL` also gates schedule/update/**delete**); catalog version in 3 places, 3 values; `docs/agent-presets.md:105` stale warm-up retry description; CONCEPTS "Canonical capability" contradicts "self-contained" claim; README debug cmd references nonexistent `mcp/mcp-server.go`; "over 50 commits" (153). |
| D5 | Tests: `pkg/transport` (345 lines TLS/proxy/CA) zero tests; integration gating by name substring not build tag; `openai-go` + `ngrok` direct deps for integration tests only pull big indirect tree; entire handler write path (add_message, reactions, mark, join/leave/open), usergroups create/update/users_update, channels handlers, history/replies/search have zero handler-level tests — `isChannelAllowedForConfig` tested, enforcement never. |
| D6 | `cmd/slack-mcp-auth` invisible outside docs/01 (not in AGENTS build block or code map). macOS-only `open` undocumented. |
| D7 | `main.go`: sse/http arms ~30 duplicated lines; `panic(err)` ×2 vs `logger.Fatal`; `LOG_COLOR` bypasses `IsTruthy`; `validateToolConfig` skips `CHANNEL_MANAGEMENT_TOOL`. |

## 6. Suggested order

1. Bugs B1–B5, B8, B12, B13 (small, high leverage; B3 is a design decision: make env gates additive or delete the branch + fix docs).
2. T1–T3, T5, T9 (token: ~50 KB off tools/list, mechanical).
3. T6 (pagination contract) + T4 (missing param descs) + T11 (param names) — one "agent contract" pass; changes observable behavior, needs sign-off.
4. S1 table registry (unlocks drift tests, deletes ~300 lines).
5. S3 + S7 dead code (delete before S2 so less to move).
6. S2 api.go split; S4 helper consolidation.
7. D2/D3/D4 repo + docs sweep (half-day, no code risk).
8. R-series as encountered.
