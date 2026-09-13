# rish-mcp-setup

`rish-mcp-setup` is the npm setup utility for the **server side** of rish-mcp and for creating an **MCP client configuration**.

It intentionally does **not** download, build, install, or update the Android APK. Android agent releases are handled separately through the signed GitHub release channel.

```bash
npx rish-mcp-setup
```

The menu contains only:

1. **Install/update relay server** — pulls `ghcr.io/turin-dev/rish-mcp-relay:latest`, creates persistent `AI_TOKEN` and `DEVICE_TOKEN` values when needed, stores them under `~/.config/rish-mcp/relay.env`, and starts the relay with Docker.
2. **Configure MCP client** — creates a standard `mcpServers` JSON entry pointing at the relay and stores it under `~/.config/rish-mcp/client.json`.
3. **Exit**

## Non-interactive usage

Install or update the relay server:

```bash
npx rish-mcp-setup --yes --action server
```

Create an MCP client configuration:

```bash
npx rish-mcp-setup --yes --action client \
  --url https://mcp.example.com/mcp \
  --token "$AI_TOKEN"
```

## Server options

```bash
npx rish-mcp-setup --action server \
  --port 8080 \
  --ai-token "$AI_TOKEN" \
  --device-token "$DEVICE_TOKEN"
```

If tokens are omitted, the installer reuses values from `~/.config/rish-mcp/relay.env` when available and otherwise creates cryptographically random tokens. The relay is installed as the `rish-mcp-relay` Docker container with `--restart unless-stopped`.

Environment equivalents:

- `AI_TOKEN`
- `DEVICE_TOKEN`
- `RISH_MCP_RELAY_PORT`
- `RISH_MCP_YES=1`

Docker is required for the server action.

## Client options

```bash
npx rish-mcp-setup --action client \
  --url https://mcp.example.com/mcp \
  --token "$AI_TOKEN"
```

The generated configuration looks like this:

```json
{
  "mcpServers": {
    "phone": {
      "type": "http",
      "url": "https://mcp.example.com/mcp",
      "headers": {
        "Authorization": "Bearer <AI_TOKEN>"
      }
    }
  }
}
```

If the relay was installed on the same machine, the client action can reuse the locally stored `AI_TOKEN` automatically. The generated file contains a bearer token, so it is written with owner-only permissions where supported.

## Android agent

The npm package has no APK code or Android tooling dependencies. Do not use it to install the Android app.

Use the signed `agent-v*` GitHub release channel for Android artifacts and follow the release notes for pairing and device setup.

## Other commands

```bash
npx rish-mcp-setup --help
npx rish-mcp-setup --version
```

Node.js 18 or newer is required.

## License

MIT
