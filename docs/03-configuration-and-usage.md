## 3. Configuration and usage

You can configure the MCP server using command line arguments and environment variables.

### Using DXT

For [Claude Desktop](https://claude.ai/download) users, you can use the DXT extension to run the MCP server without needing to edit the `claude_desktop_config.json` file directly. Download the [latest version](https://github.com/korotovsky/slack-mcp-server/releases/latest/download/slack-mcp-server.dxt) of the DXT Extension from [releases](https://github.com/korotovsky/slack-mcp-server/releases) page.

1. Open Claude Desktop and go to the `Settings` menu.
2. Click on the `Extensions` tab.
3. Drag and drop the downloaded .dxt file to install it and click "Install".
4. Fill in all required configuration fields:
    - Authentication method: `xoxc/xoxd`, `xoxp`, or `xoxb`.
    - Value for `SLACK_MCP_XOXC_TOKEN` and `SLACK_MCP_XOXD_TOKEN` for the `xoxc/xoxd` method, `SLACK_MCP_XOXP_TOKEN` for `xoxp`, or `SLACK_MCP_XOXB_TOKEN` for `xoxb`.
    - You may also enable `Add Message Tool` to allow posting messages to channels.
    - You may also change the User-Agent if you have Enterprise Slack.
5. Enable MCP Server.

> [!IMPORTANT]
> If you hit startup issues, you may need to disable the bundled node in Claude Desktop and let it use node from the host machine. This is a known DXT bug: https://github.com/anthropics/dxt/issues/45#issuecomment-3050284228

### Using the Cursor installer

You can install the MCP server with the Cursor One-Click method. Below are prepared configurations:

 - `npx` and `xoxc/xoxd` method: [![Install MCP Server](https://cursor.com/deeplink/mcp-install-light.svg)](cursor://anysphere.cursor-deeplink/mcp/install?name=slack-mcp-server&config=eyJjb21tYW5kIjogIm5weCAteSBzbGFjay1tY3Atc2VydmVyQGxhdGVzdCAtLXRyYW5zcG9ydCBzdGRpbyIsImVudiI6IHsiU0xBQ0tfTUNQX1hPWENfVE9LRU4iOiAieG94Yy0uLi4iLCAiU0xBQ0tfTUNQX1hPWERfVE9LRU4iOiAieG94ZC0uLi4ifSwiZGlzYWJsZWQiOiBmYWxzZSwiYXV0b0FwcHJvdmUiOiBbXX0%3D)
 - `npx` and `xoxp` method: [![Install MCP Server](https://cursor.com/deeplink/mcp-install-light.svg)](cursor://anysphere.cursor-deeplink/mcp/install?name=slack-mcp-server&config=eyJjb21tYW5kIjogIm5weCAteSBzbGFjay1tY3Atc2VydmVyQGxhdGVzdCAtLXRyYW5zcG9ydCBzdGRpbyIsImVudiI6IHsiU0xBQ0tfTUNQX1hPWFBfVE9LRU4iOiAieG94cC0uLi4ifSwiZGlzYWJsZWQiOiBmYWxzZSwiYXV0b0FwcHJvdmUiOiBbXX0%3D)
 - `npx` and `xoxb` method: [![Install MCP Server](https://cursor.com/deeplink/mcp-install-light.svg)](cursor://anysphere.cursor-deeplink/mcp/install?name=slack-mcp-server&config=eyJjb21tYW5kIjogIm5weCAteSBzbGFjay1tY3Atc2VydmVyQGxhdGVzdCAtLXRyYW5zcG9ydCBzdGRpbyIsImVudiI6IHsiU0xBQ0tfTUNQX1hPWEJfVE9LRU4iOiAieG94Yi0uLi4ifSwiZGlzYWJsZWQiOiBmYWxzZSwiYXV0b0FwcHJvdmUiOiBbXX0%3D)

> [!IMPORTANT]
> The tokens in these configurations are examples. Replace them with your own.

### Using npx

If you have npm installed, this is the fastest way to get started with `slack-mcp-server` on Claude Desktop.

Open your `claude_desktop_config.json` and add the mcp server to the list of `mcpServers`:

> [!WARNING]  
> If you are using Enterprise Slack, you may set the `SLACK_MCP_USER_AGENT` environment variable to match the User-Agent string of the browser you extracted `xoxc` and `xoxd` from, and enable `SLACK_MCP_CUSTOM_TLS` so the custom TLS handshake looks like a real browser's. Some environments with higher security policies require this for the server to work properly.

**Option 1: Using XOXP Token**
``` json
{
  "mcpServers": {
    "slack": {
      "command": "npx",
      "args": [
        "-y",
        "slack-mcp-server@latest",
        "--transport",
        "stdio"
      ],
      "env": {
        "SLACK_MCP_XOXP_TOKEN": "xoxp-..."
      }
    }
  }
}
```

**Option 2: Using XOXB Token (Bot)**
``` json
{
  "mcpServers": {
    "slack": {
      "command": "npx",
      "args": [
        "-y",
        "slack-mcp-server@latest",
        "--transport",
        "stdio"
      ],
      "env": {
        "SLACK_MCP_XOXB_TOKEN": "xoxb-..."
      }
    }
  }
}
```

**Option 3: Using XOXC/XOXD Tokens**
``` json
{
  "mcpServers": {
    "slack": {
      "command": "npx",
      "args": [
        "-y",
        "slack-mcp-server@latest",
        "--transport",
        "stdio"
      ],
      "env": {
        "SLACK_MCP_XOXC_TOKEN": "xoxc-...",
        "SLACK_MCP_XOXD_TOKEN": "xoxd-..."
      }
    }
  }
}
```

<details>
<summary>Or, stdio transport with docker.</summary>

**Option 1: Using XOXP Token**
```json
{
  "mcpServers": {
    "slack": {
      "command": "docker",
      "args": [
        "run",
        "-i",
        "--rm",
        "-e",
        "SLACK_MCP_XOXP_TOKEN",
        "ghcr.io/korotovsky/slack-mcp-server",
        "--transport",
        "stdio"
      ],
      "env": {
        "SLACK_MCP_XOXP_TOKEN": "xoxp-..."
      }
    }
  }
}
```

**Option 2: Using XOXC/XOXD Tokens**
```json
{
  "mcpServers": {
    "slack": {
      "command": "docker",
      "args": [
        "run",
        "-i",
        "--rm",
        "-e",
        "SLACK_MCP_XOXC_TOKEN",
        "-e",
        "SLACK_MCP_XOXD_TOKEN",
        "ghcr.io/korotovsky/slack-mcp-server",
        "--transport",
        "stdio"
      ],
      "env": {
        "SLACK_MCP_XOXC_TOKEN": "xoxc-...",
        "SLACK_MCP_XOXD_TOKEN": "xoxd-..."
      }
    }
  }
}
```

Please see [Docker](#Using-Docker) for more information.
</details>

### Using npx with `sse` transport:

To run it in `sse` mode, use the `mcp-remote` wrapper for Claude Desktop and deploy/expose the MCP server somewhere, e.g. with `ngrok` or `docker-compose`.

```json
{
  "mcpServers": {
    "slack": {
      "command": "npx",
      "args": [
        "-y",
        "mcp-remote",
        "https://x.y.z.q:3001/sse",
        "--header",
        "Authorization: Bearer ${SLACK_MCP_API_KEY}"
      ],
      "env": {
        "SLACK_MCP_API_KEY": "my-$$e-$ecret"
      }
    }
  }
}
```

<details>
<summary>Or, sse transport for Windows.</summary>

```json
{
  "mcpServers": {
    "slack": {
      "command": "C:\\Progra~1\\nodejs\\npx.cmd",
      "args": [
        "-y",
        "mcp-remote",
        "https://x.y.z.q:3001/sse",
        "--header",
        "Authorization: Bearer ${SLACK_MCP_API_KEY}"
      ],
      "env": {
        "SLACK_MCP_API_KEY": "my-$$e-$ecret"
      }
    }
  }
}
```
</details>

### TLS and exposing to the internet

Two reasons you might need to set up HTTPS for your SSE endpoint:
- `mcp-remote` handles only https schemes;
- TLS is good practice for any service exposed to the internet.

You could use `ngrok`:

```bash
ngrok http 3001
```

and then use the endpoint `https://903d-xxx-xxxx-xxxx-10b4.ngrok-free.app` for your `mcp-remote` argument.

**The server refuses to start in `sse`/`http` mode unless `SLACK_MCP_API_KEY` is set.** Never combine an `ngrok` (or any public) exposure with the `SLACK_MCP_ALLOW_UNAUTHENTICATED` opt-out. That opt-out is for local loopback development only.

### Using Docker

For detailed information about all environment variables, see [Environment Variables](https://github.com/korotovsky/slack-mcp-server?tab=readme-ov-file#environment-variables).

```bash
export SLACK_MCP_XOXC_TOKEN=xoxc-...
export SLACK_MCP_XOXD_TOKEN=xoxd-...

docker pull ghcr.io/korotovsky/slack-mcp-server:latest
docker run -i --rm \
  -e SLACK_MCP_XOXC_TOKEN \
  -e SLACK_MCP_XOXD_TOKEN \
  ghcr.io/korotovsky/slack-mcp-server:latest --transport stdio
```

Or, the docker-compose way:

```bash
wget -O docker-compose.yml https://github.com/korotovsky/slack-mcp-server/releases/latest/download/docker-compose.yml
wget -O .env https://github.com/korotovsky/slack-mcp-server/releases/latest/download/default.env.dist
nano .env # Edit .env file with your tokens from step 1 of the setup guide
docker network create app-tier
docker-compose up -d
```

### Console arguments

| Argument                    | Required ? | Description                                                                                                                                                                                                         |
|-----------------------------|------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `--transport` or `-t`       | Yes        | Select transport for the MCP Server, possible values are: `stdio`, `sse`                                                                                                                                            |
| `--enabled-tools` or `-e`   | No         | Comma-separated list of tools to register. If not set, all tools are registered. Runtime permissions (e.g., `SLACK_MCP_ADD_MESSAGE_TOOL`) are still enforced. Available tools: `conversations_history`, `conversations_replies`, `conversations_get_message`, `conversations_add_message`, `conversations_open`, `conversations_draft_message`, `reactions_add`, `reactions_remove`, `reactions_get`, `attachment_get_data`, `conversations_search_messages`, `conversations_join`, `conversations_leave`, `conversations_unreads`, `conversations_mark`, `channels_list`, `channels_me`, `channels_starred`, `usergroups_list`, `usergroups_me`, `usergroups_create`, `usergroups_update`, `usergroups_users_update`, `users_search`, `activity_unreads`, `activity_mark_read`, `saved_list`, `saved_update`, `saved_clear_completed`, `files_list`, `slack_auth_status`. |

### Environment variables

| Variable                          | Required? | Default                   | Description                                                                                                                                                                                                                                                                               |
|-----------------------------------|-----------|---------------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `SLACK_MCP_XOXC_TOKEN`            | Yes*      | `nil`                     | Slack browser token (`xoxc-...`)                                                                                                                                                                                                                                                          |
| `SLACK_MCP_XOXD_TOKEN`            | Yes*      | `nil`                     | Slack browser cookie `d` (`xoxd-...`)                                                                                                                                                                                                                                                     |
| `SLACK_MCP_XOXP_TOKEN`            | Yes*      | `nil`                     | User OAuth token (`xoxp-...`), an alternative to xoxc/xoxd                                                                                                                                                                                                                                  |
| `SLACK_MCP_PORT`                  | No        | `13080`                   | Port for the MCP server to listen on                                                                                                                                                                                                                                                      |
| `SLACK_MCP_HOST`                  | No        | `127.0.0.1`               | Host for the MCP server to listen on                                                                                                                                                                                                                                                      |
| `SLACK_MCP_API_KEY`           | No        | `nil`                     | Bearer token for SSE and HTTP transports                                                                                                                                                                                                                                                            |
| `SLACK_MCP_ALLOW_UNAUTHENTICATED` | No    | `nil` (unset)             | Only honored by `sse`/`http` transports. When `SLACK_MCP_API_KEY` is not set, the server refuses to start unless this is set to exactly `true`. Strongly discouraged outside of local loopback development. It disables authentication entirely.                                        |
| `SLACK_MCP_PROXY`                 | No        | `nil`                     | Proxy URL for outgoing requests                                                                                                                                                                                                                                                           |
| `SLACK_MCP_USER_AGENT`            | No        | `nil`                     | Custom User-Agent (for Enterprise Slack environments)                                                                                                                                                                                                                                     |
| `SLACK_MCP_CUSTOM_TLS`            | No        | `nil`                     | Send custom TLS-handshake to Slack servers based on `SLACK_MCP_USER_AGENT` or default User-Agent. (for Enterprise Slack environments)                                                                                                                                                     |
| `SLACK_MCP_SERVER_CA`             | No        | `nil`                     | Path to CA certificate                                                                                                                                                                                                                                                                    |
| `SLACK_MCP_SERVER_CA_TOOLKIT`     | No        | `nil`                     | Inject HTTPToolkit CA certificate to root trust-store for MitM debugging                                                                                                                                                                                                                  |
| `SLACK_MCP_SERVER_CA_INSECURE`    | No        | `false`                   | Trust all insecure requests (NOT RECOMMENDED)                                                                                                                                                                                                                                             |
| `SLACK_MCP_ADD_MESSAGE_TOOL`      | No        | `nil`                     | Enable message posting via `conversations_add_message` by setting it to `true` for all channels, a comma-separated list of channel IDs to whitelist specific channels, or use `!` before a channel ID to allow all except specified ones. If empty, the tool is only registered when explicitly listed in `SLACK_MCP_ENABLED_TOOLS`. |
| `SLACK_MCP_ADD_MESSAGE_MARK`      | No        | `nil`                     | When `conversations_add_message` is enabled (via `SLACK_MCP_ADD_MESSAGE_TOOL` or `SLACK_MCP_ENABLED_TOOLS`), setting this to `true` will automatically mark sent messages as read.                                                                                                        |
| `SLACK_MCP_ADD_MESSAGE_UNFURLING` | No        | `nil`                     | Enable to let Slack unfurl posted links, or set a comma-separated list of domains, e.g. `github.com,slack.com`, to whitelist unfurling only for them. If the text contains both a whitelisted and an unknown domain, unfurling is disabled for security reasons.                                         |
| `SLACK_MCP_USERS_CACHE`           | No        | `.users_cache.json`       | Path to the users cache file. Used to cache Slack user information to avoid repeated API calls on startup.                                                                                                                                                                                |
| `SLACK_MCP_CHANNELS_CACHE`        | No        | `.channels_cache_v2.json` | Path to the channels cache file. Used to cache Slack channel information to avoid repeated API calls on startup.                                                                                                                                                                          |
| `SLACK_MCP_CACHE_TTL`             | No        | `24h`                     | Cache time-to-live. Supports duration format (`24h`, `30m`) or seconds (`3600`). Set to `0` to disable TTL (cache forever). When the cache expires, the server serves stale data immediately while a background refresh fetches fresh data.                                                       |
| `SLACK_MCP_MIN_REFRESH_INTERVAL`  | No        | `30s`                     | Minimum interval between forced cache refreshes. Prevents API abuse from repeated force-refresh requests. Supports duration format (`30s`, `1m`) or seconds (`60`). Set to `0` to disable rate limiting.                                                                                  |
| `SLACK_MCP_LOG_LEVEL`             | No        | `info`                    | Log-level for stdout or stderr. Valid values are: `debug`, `info`, `warn`, `error`, `panic` and `fatal`                                                                                                                                                                                   |
| `SLACK_MCP_ENABLED_TOOLS`         | No        | `nil`                     | Comma-separated list of tools to register. If empty, all read-only tools and usergroups tools are registered; write tools (`conversations_add_message`, `reactions_add`, `reactions_remove`, `attachment_get_data`) require their specific env var to be set OR must be explicitly listed here. When a write tool is listed here, it's enabled without channel restrictions. Available tools: `conversations_history`, `conversations_replies`, `conversations_get_message`, `conversations_add_message`, `conversations_open`, `conversations_draft_message`, `reactions_add`, `reactions_remove`, `reactions_get`, `attachment_get_data`, `conversations_search_messages`, `conversations_join`, `conversations_leave`, `conversations_unreads`, `conversations_mark`, `channels_list`, `channels_me`, `channels_starred`, `usergroups_list`, `usergroups_me`, `usergroups_create`, `usergroups_update`, `usergroups_users_update`, `users_search`, `activity_unreads`, `activity_mark_read`, `saved_list`, `saved_update`, `saved_clear_completed`, `files_list`, `slack_auth_status`. |

### Tool registration and permissions

#### Overview

Tools are controlled at two levels:
- **Registration** (`SLACK_MCP_ENABLED_TOOLS`): determines which tools are visible to MCP clients
- **Runtime permissions** (tool-specific env vars like `SLACK_MCP_ADD_MESSAGE_TOOL`): channel restrictions for write tools

Write tools (`conversations_add_message`, `reactions_add`, `reactions_remove`, `attachment_get_data`) are **not registered by default** to prevent accidental exposure. To enable them, you must either:
1. Set their specific environment variable (e.g., `SLACK_MCP_ADD_MESSAGE_TOOL`), or
2. Explicitly list them in `SLACK_MCP_ENABLED_TOOLS`

Usergroups read tools (`usergroups_list`, `usergroups_me`) are **registered by default**. Write tools (`usergroups_create`, `usergroups_update`, `usergroups_users_update`) require `SLACK_MCP_USERGROUPS_WRITE_TOOL` (or listing them in `SLACK_MCP_ENABLED_TOOLS`). All need the matching OAuth scopes (`usergroups:read` / `usergroups:write`).

#### Examples

**Example 1: Read-only mode (default)**

By default, only read-only tools are available. No write tools are registered.

```json
{
  "env": {
    "SLACK_MCP_XOXP_TOKEN": "xoxp-..."
  }
}
```

**Example 2: Enable messaging to specific channels**

Use `SLACK_MCP_ADD_MESSAGE_TOOL` to enable messaging with channel restrictions:

```json
{
  "env": {
    "SLACK_MCP_XOXP_TOKEN": "xoxp-...",
    "SLACK_MCP_ADD_MESSAGE_TOOL": "C123456789,C987654321"
  }
}
```

**Example 3: Enable messaging without channel restrictions**

Use `SLACK_MCP_ENABLED_TOOLS` to register write tools without restrictions:

```json
{
  "env": {
    "SLACK_MCP_XOXP_TOKEN": "xoxp-...",
    "SLACK_MCP_ENABLED_TOOLS": "conversations_history,conversations_add_message,reactions_add"
  }
}
```

**Example 4: Minimal read-only setup**

Expose only specific tools:

```json
{
  "env": {
    "SLACK_MCP_XOXP_TOKEN": "xoxp-...",
    "SLACK_MCP_ENABLED_TOOLS": "channels_list,conversations_history"
  }
}
```

#### Behavior matrix

| `ENABLED_TOOLS` | Tool-specific env var | Write tool registered? | Channel restrictions |
|-----------------|----------------------|------------------------|---------------------|
| empty/not set   | not set              | No                     | N/A                 |
| empty/not set   | `true`               | Yes                    | None                |
| empty/not set   | `C123,C456`          | Yes                    | Only listed channels |
| includes tool   | not set              | Yes                    | None                |
| includes tool   | `C123,C456`          | Yes                    | Only listed channels |
| excludes tool   | any                  | No                     | N/A                 |
