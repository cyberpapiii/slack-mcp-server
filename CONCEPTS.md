# Concepts

Shared domain vocabulary for this project: entities, named processes, and status concepts with project-specific meaning. Seeded with core domain vocabulary, then grows as ce-compound and ce-compound-refresh capture process learnings; direct edits are fine. Glossary only, not a spec or catch-all.

## Runtime & deployment

### Plug
The MCP multiplexer that supervises this project's server as a child subprocess and exposes it to MCP clients. Plug distinguishes reloading its configuration from restarting a child: a config reload leaves a running server untouched, so picking up a newly built binary requires an explicit restart (disable, wait for teardown, enable) of the server.

### Deployment canary
An observable change in the server's live output, such as a new column header or a changed rendering of a known string, used to confirm from the outside that newly deployed code is actually serving. Complements process-level checks: a running server whose start time predates its binary's build time was never actually redeployed.

## Output

### Standard mode
The default output level for message-returning tools: compact CSV designed for agent consumption, keeping identifiers needed for follow-up actions while dropping verbose columns. May prepend a Legend and truncate long attachments with a Truncation receipt. Callers select modes per call via the `detail` parameter; a server-wide default applies when the call doesn't specify one.

### Full mode
The verbose output level: all columns including per-message user IDs and permalinks, with attachments rendered in full. The lossless recovery route when Standard mode has truncated something.

### Legend
Comment lines prepended to CSV output before the header: a channel map (`#channels:`), a user map (`#users:`, each distinct human speaker's ID, username, and real name), a link template for constructing message permalinks (`#link_template:`), the attachment fetch route (`#attachments:`), the pagination cursor (`#next_cursor:`), and a partial-result reason (`#partial:`). The user map is emitted only when the response is large enough for it to pay for itself; bots are excluded. The attachment line is conditioned on content rather than size: it appears whenever a row carries a file, including single-message reads.

### Attachment reference
How a file is named in an `AttachmentIDs` cell: `FileID (name, kind, size)`. The kind predicts what fetching it will yield rather than restating the file extension, so `image` and `text` mean `attachment_get_data` returns readable content while a raw filetype (`pdf`, `mp4`) means it returns base64 the reader cannot interpret. Size lets a reader weigh a fetch against the 5MB cap, above which `files_download` is the only route. The point is that skipping a file becomes an informed choice instead of a guess from a filename.

### Download root
The single directory `files_download` may write into, named by `SLACK_MCP_DOWNLOAD_DIR`. The caller names a file, never a path; the server derives the destination from the root and the file's ID. Unset means the tool refuses every call, so writing to the operator's disk stays something the operator turned on.

### Companion table
A second CSV table appended after a `#activity_items:` or `#saved_items:` line by tools that return both messages and the items that point at them. Its rows carry the IDs the matching mutation tool takes.

### Truncation receipt
The suffix appended to an attachment cut down to Standard mode's rendering budget, telling the reader the content was truncated. Recovery: re-fetch that message with `detail: full`, or call `conversations_get_message` with the channel and MsgID/timestamp. Attachments have no ID-addressable fetch path of their own.
