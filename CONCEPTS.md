# Concepts

Shared domain vocabulary for this project: entities, named processes, and status concepts with project-specific meaning. Seeded with core domain vocabulary, then grows as ce-compound and ce-compound-refresh capture process learnings; direct edits are fine. Glossary only, not a spec or catch-all.

## Runtime & deployment

### Plug
The MCP multiplexer that supervises this project's server as a child subprocess and exposes it to MCP clients. Plug distinguishes reloading its configuration from restarting a child: a config reload leaves a running server untouched, so picking up a newly built binary requires an explicit restart (disable, wait for teardown, enable) of the server.

### Deployment canary
An observable change in the server's live output, such as a new column header or a changed rendering of a known string, used to confirm from the outside that newly deployed code is actually serving. Complements process-level checks: a running server whose start time predates its binary's build time was never actually redeployed.

## Output modes

### Standard mode
The default output level for message-returning tools: compact CSV designed for agent consumption, keeping identifiers needed for follow-up actions while dropping verbose columns. May prepend a Legend and truncate long attachments with a Truncation receipt. Callers select modes per call via the `detail` parameter; a server-wide default applies when the call doesn't specify one.

### Full mode
The verbose output level: all columns including per-message user IDs and permalinks, with attachments rendered in full. The lossless recovery route when Standard mode has truncated something.

### Legend
Comment lines prepended to Standard-mode output before the CSV header: a user map (each distinct human speaker's ID, username, and real name) and a link template for constructing message permalinks from the channel and message ID columns. Emitted only when the response is large enough for the mapping to pay for itself; bots are excluded from the user map.

### Truncation receipt
The suffix appended to an attachment cut down to Standard mode's rendering budget, telling the reader the content was truncated. Recovery: re-fetch that message with `detail: full`, or call `conversations_get_message` with the channel and MsgID/timestamp. Attachments have no ID-addressable fetch path of their own.
