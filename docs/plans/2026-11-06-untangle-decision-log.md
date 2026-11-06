# Untangle refactor: decision log (2026-11-06)

Branch `refactor/untangle`. Question asked of every piece: should these change together?
Extras chosen: **Comments** (drop comments that restate code) and **Tests** (drop tests that
cannot catch a bug, rewrite tests tied to internals). Fixes are listed above the problems they solve.

## Fixes and the problems they solve

### F1. Each handler family declares its own tools; the server only filters and registers
A family (`conversations`, `saved`, `lists`, ...) returns `[]ToolSpec{Name, Options, Handler}` next to
the code that parses those parameters. Names come from `capability` constants; phase, title, hints and
the access requirement come from the catalog row. `pkg/server` loops over specs, filters by the enabled
list and `provider.Satisfies(row.Requires)`, and registers each in the catalog's phase.
- `server/server.go:37-107` repeats all 71 catalog names as `Tool*` consts; `server_test.go:26-99` repeats them a third time.
- Descriptions/params live in `core_tools.go`, `daily_power_tools.go`, `custom_power_tools.go`, `server.go`, away from the handlers that parse them; defaults and maxima are hand-mirrored (`dnd.go:16` vs `mcp.Max(10080)`, `canvasOperations` vs enum, 40000 runes, list limit 200, unreads/activity/files/users/channels limits, `conversations_unreads.go:514` "keep in sync" comment).
- `conversations_write.go:309` `addMessageKnownParams` repeats the declared params (and lists stale `payload`).
- `daily_power_tools.go:97-106` dispatches `lists_item_delete` prepare/execute in the server; other approval tools dispatch in the handler.
- `server/tool_contract.go:37` `outputSchemaByTool` is a separate per-name map.
- `NewConversationsHandler(..., sendEnabled)` built twice (`core_tools.go:14`, `server.go:254`).
- `server/server.go:119-150` preset logic duplicated in `cmd/slack-mcp-auth/main.go:386-395` → move to `capability`.
- `custom_power_tools.go:38-47,78-85` writes `ChannelName`/`UserName` closures into exported handler fields after construction.
- `normalizeAnnotations` repeats the catalog's read-only rule.
- Hand-written tool-name literals in handlers (`browserSessionRequired("saved_list")`, approval `Binding.Tool`, permission messages).

### F2. One credential mode in provider, one requirement check used by registration, handlers and status
`provider` computes the mode once (bot / user OAuth / browser-only / OAuth+browser / demo) and answers
`Satisfies(capability.Requirement)`. Catalog rows carry `Requires` (none, user token, user OAuth,
browser session). `slack_auth_status` derives availability from the same rows.
- `client.go:55-62` mode split across `isOAuth`, `isBotToken`, `browserConfigured`, `browserState`; each reader rebuilds it (`browser_session.go:103,111,145,233`, `api.go:356-366`, `dnd.go:17`, `lists.go:53`, `auth_status.go:72`, `warmup.go:110`).
- Gates outside the table: `core_tools.go:188`, `server.go:295,309,343,392` (`IsBotToken`, `ConfiguredWithBrowserSession`), `daily_power_tools.go:40,47` (`api.Lists()`/`api.DND()` errors).
- `capability.Browser` decides nothing at registration; only `auth_status` and a test read it.
- 7 copies of the "user actor" check (`lists.go:198`, `scheduled.go:140`, `messages_files.go:261`, `canvas_drafts_search.go:262`, `channel_mutations.go:126`, `dnd.go:89`, `people_channels.go:245`).
- `auth_status.go:85-107,124,130` hand-typed availability map; reports `saved_later` as cache-dependent (it is not), tells user to restart Plug (re-probe recovers without restart).
- `api.go:151-160` call order `startManagedOAuth` → `attachBrowserToOAuth` → `startBrowserReprobe` must be known.
- `client.go:142-157` demo mode forces flags so every gate passes.
- `dnd.go:28,35`, `client.go:188`, `people_channels.go:319,342` repeat the bot-token gate per method.
- `channels.go:360-365` re-checks a registration-time gate.
- `warmup.go:104,111` hand copy of browser tool names.
- Mode used as "no browser" (OAuth+browser treated as OAuth-only): `cache_users.go:262,339`, `channels.go:379`, `browser_session.go:348`. **Behavior question, see Q1.**

### F3. Browser session owns the edge client; every edge call resolves it per call
- `custom_power_tools.go:84` + `canvas_drafts_search.go:257-263`: `Drafts()` captured once at startup; drafts never recover after re-probe and use a stale edge client (**bug**).
- Drafts registered without browser gate; saved checks per call (two mechanisms for one rule).
- `enterprise_merge.go:12,29` edge calls bypass `browserCall`, errors swallowed.
- Eight pasted `BrowserFeaturesAvailable()` + `browserSessionRequired("<literal>")` preambles (activity, unreads, saved).

### F4. One Slack HTTP call path and one Slack error type
- Six hand-written POST/429/size-cap/`ok` decoders: `edge/edge.go:161-238`, `canvas_drafts_search.go:198-225,492-510`, `people_channels.go:354-380`, `lists.go:311-350`, `oauth.go:84-145,383-454`; caps 4/8/8MB/none.
- `edge.APIError` vs `slack.SlackErrorResponse`: `capabilityError` only knows the latter, so drafts errors lose `Code` (**bug**); `isBrowserSessionAuthError` checks both.
- `ListsAPIError` vs `CapabilityAPIError` carry the same fields.
- `oauth.go:505-520`, `slack-mcp-auth/main.go:291`: refresher and identity checks use `http.DefaultClient`/`slack.New(token)` and skip proxy, CA, GovSlack.
- `transport.go:451-466` `RateLimited` and `SlackDomain`/`SlackAPIBase` live in transport only because edge cannot import provider.

### F5. One handler error path
Handlers return `(nil, err)`; one `toolError(err, mutationAttempted)` maps Slack errors; validation always
uses `invalidArguments`.
- Six mappers: `scheduledError`, `messageFilesError`, `peopleChannelsError`, `dndError`, `listToolError`, `canvasDraftSearchError`, plus `server.go:507` `typeRateLimit`.
- Ambiguous-mutation detection in 4 places (`results.go:13`, `scheduled.go:200`, `people_channels.go:351`, `canvas_drafts_search.go:573-584`).
- Two return conventions (`NewTypedErrorResult(err), nil` vs `nil, err`), mixed in `lists.go:158,162`.
- Untyped `errors.New` validation errors become `tool_error` and Warn logs. **Behavior question, see Q3.**
- `messageFilesInvalid`, hand-built `&ToolError{Code:"invalid_arguments"}` at 14+ sites.

### F6. One approval (prepare/execute) helper
- Five binding builders (`listDeleteBinding`, `scheduledBinding`, `deleteMessageBinding`, `draftDeleteBinding`, `archiveBinding`) each repeat identity check, two `CanonicalJSON`, `Provider:"local"`, tool literal.
- Action default drift (`""` vs `"prepare"`), duplicate action checks before `prepareOrExecute`.
- TTL `5*time.Minute` in `server.go:228`, `messages_files.go:75`, `approval/store.go:69`.
- `approval/store.go:96-98` comment describes a rule no caller follows.
- `Prepared.Binding`, `Consume`'s returned binding, `CanonicalJSON` wrapper, `Binding.Provider` never vary / never read.
- Each data struct re-declares `Phase/ApprovalToken/ExpiresAt`.

### F7. Configuration parsed once at startup; one boolean parser; channel policies parsed once
- Allowlist env read per call in 5 places (`conversations_write.go:354,439`, `tool_gates.go:51`, `messages_files.go:197`, `channel_mutations.go:19`); `main.go:172,216-239` keeps its own list and its own grammar.
- Gate runs on raw channel string in `messages_*`/`files_upload` but on the resolved ID in `conversations_add_message`.
- `SLACK_MCP_LOG_PARAMS` parsed in `logging.go:13` and `server.go:525`.
- Four boolean parsers (`text_processor.go:201,288`, `main.go:178`, `sse_auth.go:43`) beside `envutil.IsTruthy`.
- `getenv` test seam covers only some provider reads; token env read in 5 places with different precedence (`api.go:92-100`, `oauth.go:464-496`, `browser_session.go:432`, `keychain_check.go:24`, `client.go:77`).
- `sse_auth.go:72` reads the API key per call.

### F8. One message pipeline shared by every message-returning tool
- `conversations.go:357-416` search converter is a drifted copy of history's (no reactions, no `FilesToText` fallback, different bot rule, different warning).
- `ActivityHandler`/`SavedHandler` hold `*ConversationsHandler` to reach unexported `convertMessagesFromHistory`, `render`, `channelDisplayName`.
- Two "fetch one message" strategies (`conversations_write.go:287`, `saved.go:220-283`), swallowed reply errors at `saved.go:257,264`.
- Draft vs add-message parsers ~70% copied; markdown-fallback decision duplicated.
- files_get vs files_download duplicate URL/size/fetch.

### F9. Provider lookup API instead of map access
`ChannelByRef`, `UserByRef`, `ChannelLabel`, `UserLabel`, `DMChannelFor`, one Slack ID classifier.
- Four channel resolvers (`conversations_read.go:221-243`, `conversations.go:222-267,480-497`, `conversations_unreads.go:674-682`), two user resolvers.
- Seven case-sensitive prefix sniffers; `G` claimed twice.
- `<@U123>` never resolves (`conversations.go:467-473`) (**bug**).
- `conversations_unreads.go:306-395` re-derives names and renders `#@mpdm-…` (**bug**).
- `files_list` documents `#name` but passes it raw (**bug**).
- `UsersSearchHandler` O(users×channels) DM scan; handlers index `Channels`/`UsersInv` maps directly.
- "RealName else Name" labeling three times; `channelDisplayName` rebuilt in server.
- `PatchUser` callers must re-read `ProvideUsersMap()`.

### F10. One tabular result builder
- `marshalRowsToCSV`, `renderActivityItems`, `renderSavedItems`, `marshalFilesToCSV`, canvas/semantic copies; raw gocsv bypasses in `reactions_get`, `users_search`.
- Three page-cursor encodings.
- Five time formats for one concept (`formatUnixTs`, `unixTime`, `formatJSONTime` in local zone, `slackTsTime`, inline).

### F11. Cache lifecycle owned by provider
- `warmup.go:35` and `cache_revalidate.go:66` both encode "users before channels"; 15m timeout copied (`warmup.go:21`, `api.go:24`).
- Warm-up loop uses `time.Sleep`/`context.Background`, ignores `Close`.
- mtime-TTL test written twice (`cache_revalidate.go:19`, `cachefile.go:129`).
- `refreshUsersInternal(force)` always false; `GetChannels(types)` exported, only called with all types; `usersMu` never read-locked.
- `GetSlackConnect` decides from the previous snapshot, unbatched, nil deref risk.
- `channel_map.go:46` 13-positional-param `mapChannel` exists for one re-map.
- `cachefile.go:66` / `api.go:232` cache-dir fallback twice; lock path spelled twice.

### F12. OAuth credential code in its own package
- PKCE, code exchange, commit used only by `slack-mcp-auth` but live in `provider`; the CLI links the whole server.
- Identity check ("auth.test, compare team+user") in 5 places.
- `oauth.go:210-224` vs `:263-275` duplicate commit path inside `Current`.

### F13. Edge speaks slack-go types at its boundary
- `rusq/slack` second Slack type library in 5 edge files; four channel mappers; `enterprise_merge.go:47` `Created` ×1000 (latent bug).
- Handlers depend on edge wire types (`edge.SavedItem`, `ClientCountsResponse`, `fasttime.Time`); saved `item_id`=channel encoding decoded in 4 places.
- `fasttime.SlackString` zero → negative string, worked around in `conversations_unreads.go:280`.
- Draft blocks as `[]map[string]any` + second block-kit renderer; rich_text builder copied (`drafts.go:124`, `saved.go:392`).
- Edge logs via `slog.Default()` (goes to /dev/null under Plug).

### F14. Packages own one concern
- `transport` imports `text` only for `HumanizeCertificates`.
- `text.IsUnfurlingEnabled` (policy) and `text.Workspace` (URL parse, repeated with logs in 3 callers).
- `handler` imports `server/auth` for resource auth; mcp-go v1.5.0 has `WithResourceHandlerMiddleware`. `ApiProvider.transport`/`ServerTransport()` exist only for this.
- `limiter.Tier` shallow wrapper; `CallWithRetry` `retryAfter` param always `provider.SlackRetryAfter` (which lives in `cache_channels.go`); `unpaced` workaround.
- `slackcreds.UserAgent` is overwritten by `UserAgentTransport` on every request; edge's UA line has no effect.
- Feature providers built three ways; `MCPSlackClient` per-feature pass-throughs; unreachable "does not support" errors.

### F15. Delete dead code
- `results.go:100 ResultText` (tests only), `refresh.go:40 inFlight` (tests only), `BrowserCredentialStore.Save` (tests only), `BrowserTokenRecord.TeamID/UserID`, `edge.Client.teamID`, `postFormRaw`, `values(omitempty)`, `intString`, dead branches in `parseResponse`/`callEdgeAPI`, unused pagination fields, `runtime/trace` calls, `version.BinaryName`, `fasttime_386.go` split, `transport.dialProxy` + uTLS proxy path, duplicate ClientHello name switch, `AttachmentToText` export, `auth_status.go:75` overwritten value, unreachable `membershipAction` default, unreachable `limitByExpression("")` branch, unreachable content-type defaults, `AuthFromRequest(logger)` unused param, 16 ignored env vars in `scripts/toolslist-snapshot.sh`.

## Special cases kept (the system still needs them)
- Edge pacing/re-probe intervals, Keychain wait warning: external systems.
- `saved.markCompleted` never called bare (Slack completes every open to-do).
- `date_due=0` carried as string (Slack treats it as clear).

## Behavior decisions (asked before changing, 2026-11-06)
- Q1. OAuth+browser was treated as OAuth-only for user search, starred source, mute info, Slack Connect
  enrichment. **Decision: use the browser session whenever it is healthy.**
- Q2. history/replies omitted `limit`: schema said default `1d`, code gave 50 messages. Rob deferred.
  **Decision: keep 50 messages and make the schema say so.** A message count bounds the response size;
  a 1-day window is empty on a quiet channel and unbounded on a busy one.
- Q3. Rob deferred; all four accepted: validation errors return `invalid_arguments`; search rows use the
  history converter (gain reactions/attachments, email-file text); `reactions_get`/`users_search` go
  through the CSV builder (empty lists return a header-only CSV); a failed mark after a successful post
  returns success with a warning instead of an error (a retry would post twice).

## Progress (2026-11-06)
Landed on `refactor/untangle`: F1, F4 (W1), F5, F6, F7, F12 (W2), the provider error unification, and the
W1 leftovers (oauthcred on slackhttp; edge and Lists take a required HTTP client). Running in worktrees:
W3 (F2, F3, Q1, F11, F14 server side), W4 (F8, F9, F10, F13 handler side, Q2, F14 text side).

Observable changes so far, besides the Q1–Q3 decisions:
- `channels_archive` and `drafts_delete` require `action`, as their schemas said (they defaulted to prepare).
- An invalid `detail` value is `invalid_arguments`.
- Allowlist denials name the refused tool (`reactions_add`, not "reactions tools").
- `SLACK_MCP_COMPACT_OUTPUT=off` selects full, like `0`/`false`/`no` (one falsy parser).
- Credential env values are trimmed, so a whitespace-only `SLACK_MCP_OAUTH_KEYCHAIN_ACCOUNT` counts as unset.
- Error codes follow one table: Lists `missing_scope` is `permission_denied`, a missing Lists item or draft is
  `not_found`, an unknown Slack code is `slack_error` everywhere (was `slack_api_error` or the raw code), and a
  5xx or `fatal_error`/`internal_error` after any write is `outcome_unknown`.
- Edge errors and logs: edge logs reach the server log (they went to `slog.Default()`); HTTP status errors use
  slack-go's `StatusCodeError` text; one 64 MiB response cap; any 429 is retryable, including `Retry-After: 0`.

### Merged W3 and W4 (2026-11-06)
Observable changes:
- `slack_auth_status.capability_availability` lists every tool (`available`, `not_enabled`,
  `user_token_required`, `user_oauth_required`, `browser_session_required`, `browser_session_degraded`,
  `available_after_cache_warmup`); the old feature keys are gone; "restart Plug" only for a browser-only
  session, which has no re-probe.
- The browser-session gate runs before argument checks for browser tools.
- Drafts recover after a browser re-probe (bug); a session degraded at startup answers
  `browser_session_required` until it recovers.
- Enterprise edge failures are logged at Debug before the Web API fallback, and can mark the session degraded.
- Q2 schema: history/replies `limit` default is `"50"`.
- Q3: `reactions_get` and `users_search` return a header-only CSV when empty. Search rows go through the
  history converter, but slack-go's `SearchMessage` carries no files or reactions, so those columns stay empty
  for search rows (would need our own `search.messages` decode).
- One cursor codec: `files_list` `#next_cursor` is base64 `page:N`; old bare-number cursors are rejected.
- One time format (RFC 3339 UTC): `files_list Created`, `usergroups_* DateCreate/DateUpdate` (were local time),
  `saved_list DateCreated/DateDue` (were `2006-01-02 15:04`).
- `saved_list` fetches each item's one message like `conversations_get_message`; `max_messages_per_item` has no
  effect (the old window only ever held the saved message).
- Lookups: `<@U123>`, `@U123` and cached emails resolve; a name like `Uwe` is no longer taken for a user ID;
  `files_list` resolves `#name`; unreads name group DMs `@mpdm-…` (was `#@mpdm-…`); DMs are labelled by the
  peer's current username.
- Drafts render through `text.BlocksToText` (links `text (url)`, `:emoji:`, bullets, blocks joined by `\n`).
- `SLACK_MCP_ADD_MESSAGE_UNFURLING` reads `off`/mixed-case booleans as booleans, not as a one-domain allowlist.
- IDs with a lowercase body after `C`/`U` are rejected locally.
- The channels/users resources no longer call auth.test per read.

### Review rounds 1–2 (W5, W6, W9, W10) and the comments/tests passes
Observable changes:
- Tools no longer wait for the cache unless they look a name up; a cold-cache `#name`/`@name` returns
  `cache_warming` (retryable). IDs work at once.
- `conversations_get_message`, `reactions_get` and `saved_list` match the exact timestamp; a missing message is
  `not_found` (reactions_get used to answer a successful "No message found" text). Local not-found codes are
  `not_found`, matching Slack's own misses.
- Bad `channel_types`/`query_targets`/`sort`, argument decoding and Lists input are `invalid_arguments`;
  #general/shared-channel refusals `permission_denied`; archive conflicts `conflict`; a read deadline
  `network_error`.
- Six user-only write tools (`conversations_mark`, `messages_delete`, `users_set_profile`, `users_set_status`,
  `scheduled_message_cancel`, `channels_archive`) require a user token, so bot tokens no longer list them.
- `reactions_get` asks for the history scopes it uses (not `reactions:read`). `slack-mcp-auth` resolves tools
  like the server (`--enabled-tools`, `--preset`, same env defaults); the server warns when the login lacks scopes.
- Error hints name a tool only if it is served. The search description no longer promises message-URL lookup.
  `saved_list` drops `max_messages_per_item` and names the failure code in its placeholder.
- Clients own pacing and retries: 2 rate-limit retries everywhere (adds them to history/replies),
  `conversations.replies` paced everywhere, Enterprise merge pages paced.
- Channel writes, join, leave and open keep the channel cache current.
- `canvases_read` falls back to the preview only for `not_allowed_token_type`/`missing_scope`.
- A relative `SLACK_MCP_DOWNLOAD_DIR` fails at startup; downloads stop at the size cap while streaming.
- `emoji_list` `#next_cursor` uses the one cursor codec; old numeric cursors are rejected.
- Mark-after-post is skipped for a bot identity. Config warnings (bad cache TTL, log level, allowlists) reach the
  log file. The OAuth refresh lock refuses a cwd-relative path.

Review rounds 3–5 (fixed directly on `refactor/untangle`):
- Unfurl allowlist covers links in blocks and `messages_schedule`, fails closed on a URL it cannot parse, counts
  private-suffix domains (`*.github.io`), and normalizes hosts one way.
- With a channel allow/block list set, its tools require a conversation ID (C/G/D); names and user IDs return
  `invalid_arguments`. A list entry that is not a conversation ID fails at startup. The draft preview uses the
  same gate.
- Bad cursors are `invalid_cursor` everywhere. `files_list` with no files returns a header-only CSV.
- The HTTP client fails a call only after 30s without progress (was: any call over 30s), so large transfers
  finish; the custom-TLS path now honors cancellation.
- `slack_auth_status` reports a failed managed OAuth load on every credential path.
- One tool-selection rule (`capability.Select`) for both binaries; a preset flag beats an inherited tool list.
- Search queries never reach logs; failed requests log at Debug.
- `auth.test` takes a context (browser re-probe cancels on shutdown).

Product decisions (Rob deferred to judgment, 2026-11-06):
- `activity_unreads` now returns the message row for a top-level @mention (one message, not its replies); before,
  only thread items got rows and a mention showed up only in the item table.
- `reactions_add` returns structured content (`add_reaction`/`added`) like `reactions_remove`, with the same text.
