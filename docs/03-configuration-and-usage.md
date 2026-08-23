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
| `--transport` or `-t`       | Yes        | Select transport for the MCP Server, possible values are: `stdio`, `sse`, `http`                                                                                                                                            |
| `--enabled-tools` or `-e`   | No         | Comma-separated allowlist of tools to register (same as `SLACK_MCP_ENABLED_TOOLS`). Overrides the preset. Tool names: README "Tools". |
| `--tool-preset`             | No         | `daily-power` (default, read-only) or `legacy-full` (same as `SLACK_MCP_TOOL_PRESET`). |
| `--no-cache`                | No         | Skip the users/channels cache warm-up; `#channel` and `@user` lookups then need IDs. |

### Environment variables

The full table lives in the [README](../README.md#environment-variables); it is the only copy.

### Tool registration and permissions

Canonical rules: **`AGENTS.md` "Tool surface"**. Summary:

- A tool registers iff its name is in `SLACK_MCP_ENABLED_TOOLS` (or, when that is unset, in the `SLACK_MCP_TOOL_PRESET` list; `daily-power` is the read-only default).
- `SLACK_MCP_ADD_MESSAGE_TOOL`, `SLACK_MCP_REACTION_TOOL`, and `SLACK_MCP_CHANNEL_MANAGEMENT_TOOL` are channel allow/block lists checked per call; they do not enable or disable tools.
- Usergroups write tools still need the `usergroups:write` OAuth scope.

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

Register the tool with `SLACK_MCP_ENABLED_TOOLS` (or the `legacy-full` preset) and restrict it with `SLACK_MCP_ADD_MESSAGE_TOOL`:

```json
{
  "env": {
    "SLACK_MCP_XOXP_TOKEN": "xoxp-...",
    "SLACK_MCP_ENABLED_TOOLS": "conversations_history,conversations_add_message",
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

Applies to `conversations_add_message` / `SLACK_MCP_ADD_MESSAGE_TOOL`; `reactions_*` and channel management follow the same shape with their own variable.

| Tool in enabled list? | `SLACK_MCP_ADD_MESSAGE_TOOL` | Registered? | Channel restriction |
|-----------------------|------------------------------|-------------|---------------------|
| no                    | any                          | No          | N/A                 |
| yes                   | unset or `true`              | Yes         | None                |
| yes                   | `C123,C456`                  | Yes         | Only listed channels |
| yes                   | `!C123`                      | Yes         | Every channel except C123 |
| yes                   | `false`                      | Yes         | Every channel denied (startup warns) |
