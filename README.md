# rish-mcp

Expose an Android phone's **Shizuku shell** (uid 2000, like `adb shell`) to AIs as
an **MCP tool** — **without VPN, adb, or sshd**. The phone holds a single
**outbound** WebSocket to a relay on `example.com`; AIs call the relay's MCP endpoint.

```
┌─────────┐  MCP run_shell    ┌──────────────────────┐   WS (outbound)   ┌──────────────┐
│   AI    │ ──HTTPS+Bearer──▶ │  example.com relay+MCP  │ ◀── phone dials ──│  phone APK   │
│(Claude) │ ◀── stdout/code── │   (Node, Dokploy)    │ ── exec cmd ─────▶│ Shizuku→shell │
└─────────┘                   └──────────────────────┘                   └──────────────┘
```

Phone has **zero inbound** exposure (works behind SKT CGNAT). No VPN, no adb, no sshd.

## Components

- `server/` — Node/TS. Streamable-HTTP **MCP server** (`run_shell`, `list_devices`)
  + **WS relay** the phone connects to. Bearer auth for AIs, shared token for the phone.
- `app/` — Android (Kotlin). One installable **APK**: binds a Shizuku `UserService`
  to run commands as shell uid, a foreground service holds the outbound WS, auto-starts on boot.

## Build

```bash
# server (typecheck + e2e smoke test with a fake agent)
cd server && npm install && npx tsc && node test/smoke.mjs

# APK (Android SDK + Gradle run inside Docker; host stays clean)
cd app && ./build-apk.sh        # -> app/rish-mcp-agent.apk
```

## Deploy (relay+MCP)

Tokens live in `server/.env` (gitignored). Deploy `docker-compose.yml` as a Dokploy
**Compose** app with `AI_TOKEN`/`DEVICE_TOKEN` env, routed by Traefik to `mcp.example.com`.
Add a Cloudflare A record `mcp.example.com → <server-ip>` (orange/proxied is fine for
HTTPS+WSS over :443).

## Install on phone

Fully **headless** when you already have a Shizuku shell (`rish` / `adb`). `-g`
auto-grants Shizuku's `API_V23` permission, and `am` extras provision the agent —
no taps, no typing on the device:

```bash
SIZE   # not needed; stream the apk over stdin as the shell user
TOKEN=<DEVICE_TOKEN>

# 1. push + install with runtime perms granted (grants Shizuku too)
rish -c 'cat > /data/local/tmp/r.apk' < rish-mcp-agent.apk
rish -c 'pm install -r -g /data/local/tmp/r.apk; rm -f /data/local/tmp/r.apk'

# 2. provision relay URL + token and start the agent
rish -c "am start -n kr.scin.rishmcp/.MainActivity \
  --es relay wss://mcp.example.com/agent --es token $TOKEN --ez autostart true"
```

The foreground service connects out to the relay and survives reboot (BootReceiver).
To re-point an already-running agent at a new relay, `am force-stop kr.scin.rishmcp`
first, then re-run step 2 (the service reads config on fresh start).

Manual alternative: open the app, **Grant Shizuku permission**, paste relay URL +
token, **Save & Start**.

## Use from an AI (MCP client)

Any MCP client that speaks **Streamable HTTP** and can send an `Authorization`
header works. Generic config (Claude Code `.mcp.json`, and most other clients):

```json
{
  "mcpServers": {
    "phone": {
      "type": "http",
      "url": "https://mcp.example.com/mcp",
      "headers": { "Authorization": "Bearer <AI_TOKEN>" }
    }
  }
}
```

Or with the Claude Code CLI:

```bash
claude mcp add --transport http phone https://mcp.example.com/mcp \
  --header "Authorization: Bearer <AI_TOKEN>"
```

Then the AI has two tools:

- `list_devices()` — phones currently connected to the relay
- `run_shell({cmd, deviceId?, timeoutMs?})` — run a command as shell uid;
  returns stdout, stderr and the exit code. `deviceId` is optional when
  exactly one phone is online.

### Example prompts

Once connected, just ask in natural language — the AI translates to shell:

> "What's my phone's battery level?" → `dumpsys battery`
> "Is my phone's screen on?" → `dumpsys power | grep -i wakefulness`
> "What apps did I install this month?" → `pm list packages -3 …`
> "Take a screenshot and describe it" → `screencap -p /sdcard/…` + pull
> "Silence my phone" / "open Maps" → `cmd notification set_dnd on` / `am start …`

Anything an `adb shell` (uid 2000) can do works: `pm`, `am`, `dumpsys`,
`settings`, `cmd`, `input`, `screencap`, `logcat`, file access under
`/sdcard`, etc. Root-only things do **not** work.

### Quick sanity check (no AI needed)

```bash
curl -s https://mcp.example.com/healthz
# {"ok":true,"devices":1}   ← devices ≥ 1 means the phone is connected

curl -s https://mcp.example.com/mcp \
  -H "Authorization: Bearer $AI_TOKEN" \
  -H "Content-Type: application/json" -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"run_shell","arguments":{"cmd":"getprop ro.product.model"}}}'
```

### Can I use it from the Claude app on my phone?

**Not yet, out of the box.** Claude's mobile apps do use custom connectors
(remote MCP) added at claude.ai → Settings → Connectors — the connector syncs
to the phone app and Claude connects to your relay from Anthropic's cloud. But
claude.ai custom connectors currently only support **OAuth or no auth**; there
is no field for a static bearer token, and `?token=` query params are not
supported either. rish-mcp authenticates AIs with a static bearer token, so it
works today with:

- **Claude Code** (CLI/desktop/web) — supports custom headers, see above
- **Claude API / Agent SDK** — MCP connector supports bearer headers
- any other MCP client that can send an `Authorization` header

Running the server **without auth** to make claude.ai accept it is a bad idea —
that would hand shell access on your phone to anyone who finds the URL. An
OAuth layer in front of `/mcp` would close the gap; PRs welcome.

## Security notes

- This grants shell-level remote execution on the phone to anyone holding `AI_TOKEN`.
  Treat it like an SSH private key. Rotate by changing env + restarting the stack.
- The phone only trusts the relay it dials; it never accepts inbound connections.
- Scope is the **owner's own device** for personal automation.
