# rish-mcp

Expose an Android phone's **Shizuku shell** (uid 2000, like `adb shell`) to AIs as
an **MCP tool** — **without VPN, adb, or sshd**. The phone holds a single
**outbound** WebSocket to a relay on a public hostname you control; AIs call the relay's MCP endpoint.

```
┌─────────┐  MCP run_shell    ┌──────────────────────┐   WS (outbound)   ┌──────────────┐
│   AI    │ ──HTTPS+Bearer──▶ │     relay + MCP      │ ◀── phone dials ──│  phone APK   │
│(Claude) │ ◀── stdout/code── │   (Node, Dokploy)    │ ── exec cmd ─────▶│ Shizuku→shell │
└─────────┘                   └──────────────────────┘                   └──────────────┘
```

Phone has **zero inbound** exposure (works behind SKT CGNAT). No VPN, no adb, no sshd.

> [!IMPORTANT]
> `mcp.example.com` is a **placeholder**, not a service provided by this project,
> and Claude does not replace it automatically. Deploy the relay first, then
> replace it everywhere with the public hostname you configured as `MCP_HOST`.
> For example, if `MCP_HOST=phone.yourdomain.com`, the Claude connector URL is
> `https://phone.yourdomain.com/mcp`.

> 📖 **Full walkthrough** — deploy, install on the phone, connect every client
> (incl. the Claude mobile app via OAuth), tool reference, recipes,
> troubleshooting, and the threat model: **[docs/USAGE.md](docs/USAGE.md)**.

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

Tokens live in `server/.env` (gitignored). Choose a public hostname you control
and set it as `MCP_HOST`; all `mcp.example.com` URLs below are placeholders for
that hostname. For example:

```ini
MCP_HOST=phone.yourdomain.com
```

Deploy `docker-compose.yml` as a Dokploy **Compose** app with
`AI_TOKEN`/`DEVICE_TOKEN` env, routed by Traefik to that hostname. Then add a
Cloudflare A record such as `phone.yourdomain.com → <server-ip>`
(orange/proxied is fine for HTTPS+WSS over :443).

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

### Can I use it from the Claude app on my phone? (claude.ai custom connector)

**Yes.** The server ships a minimal built-in **OAuth 2.0** layer (dynamic client
registration + PKCE), which is what claude.ai custom connectors require — they
don't support static bearer tokens.

1. Find the public hostname you set as `MCP_HOST` when deploying the relay.
   Append `/mcp` to it: for example, `MCP_HOST=phone.yourdomain.com` becomes
   `https://phone.yourdomain.com/mcp`. Replace `yourdomain.com` with your
   actual domain; do **not** enter `mcp.example.com` as-is.
2. On **claude.ai → Settings → Connectors → Add custom connector**, enter that
   URL. No client ID/secret is needed.
3. Click **Connect** — you land on the rish-mcp consent page. Paste your
   `AI_TOKEN` (the same one from `.env`) once to authorize.
4. Done. The connector syncs to the Claude mobile/desktop apps automatically;
   enable it in the chat's connector menu. Claude calls the relay from
   Anthropic's cloud, so this works from anywhere — including the Claude app
   on the very phone being controlled.

**How it works.** The OAuth layer is single-user and keeps no database. The
consent page just checks the `AI_TOKEN` you paste; the access/refresh tokens it
then issues are stateless HMACs *derived from that same `AI_TOKEN`*, so rotating
`AI_TOKEN` instantly revokes every issued token. Dynamic client registration and
consent are open, but nothing is granted without typing the token, and the token
endpoint enforces PKCE (S256) and single-use codes. Set `PUBLIC_URL` to the
external `https://…` origin so the discovery metadata and redirects are correct
(the compose file derives it from `MCP_HOST`).

Static bearer auth still works in parallel, so **Claude Code** / **API** /
**curl** keep using `Authorization: Bearer <AI_TOKEN>` exactly as before.

## Security notes

- This grants shell-level remote execution on the phone to anyone holding `AI_TOKEN`.
  Treat it like an SSH private key. Rotate by changing env + restarting the stack.
- The phone only trusts the relay it dials; it never accepts inbound connections.
- Scope is the **owner's own device** for personal automation.
