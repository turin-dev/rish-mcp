# rish-mcp — usage guide

Companion to the [README](../README.md) and [`docs/DESIGN.md`](DESIGN.md).
This covers deploying the two Go binaries, pairing the Android agent without
Shizuku, the MCP tool reference, the OAuth flow, and the WS relay protocol.

- [1. How it fits together](#1-how-it-fits-together)
- [2. Deploy the relay](#2-deploy-the-relay)
- [3. Pair the Android agent](#3-pair-the-android-agent)
- [4. Connect an AI client](#4-connect-an-ai-client)
- [5. Tool reference](#5-tool-reference)
- [6. OAuth 2.0 reference](#6-oauth-20-reference)
- [7. Official version server](#7-official-version-server)
- [8. Troubleshooting](#8-troubleshooting)
- [Appendix A: WS relay protocol](#appendix-a-ws-relay-protocol)
- [Appendix B: environment variables](#appendix-b-environment-variables)

---

## 1. How it fits together

```
┌─────────┐  MCP run_shell    ┌──────────────────────┐   WS (outbound)   ┌──────────────┐
│   AI    │ ──HTTPS+auth────▶ │     Go relay + MCP    │ ◀── phone dials ──│  phone APK   │
│(Claude) │ ◀── stdout/code── │   (server/cmd/relay)  │ ── exec cmd ─────▶│ AdbShellClient│
└─────────┘                   └──────────────────────┘                   └──────────────┘
```

- **The phone never accepts inbound connections.** It dials *out* to the
  relay and holds one WebSocket open, so it works behind CGNAT.
- **Commands run as uid 2000 (shell)** — the same privilege level as
  `adb shell`. Root-only operations do not work.
- **Output is capped at 256 KB per stream** (stdout/stderr) on the phone;
  overflow sets a `truncated` flag rather than erroring.
- The old design's Shizuku dependency is gone: the phone pairs with its own
  `adbd` directly (§3), not through a separately-installed app.

---

## 2. Deploy the relay

### 2.1 Build

```bash
cd server
go build -o relay ./cmd/relay
go build -o publicserver ./cmd/publicserver
```

Or as containers:

```bash
docker build --target relay -t rishmcp-relay server
docker build --target publicserver -t rishmcp-public server
```

### 2.2 Configure env

See [Appendix B](#appendix-b-environment-variables) for the full list. At minimum:

```bash
AI_TOKEN=$(openssl rand -hex 32)      # master key for AI clients
DEVICE_TOKEN=$(openssl rand -hex 24)  # shared secret the phone presents
PUBLIC_URL=https://mcp.example.com    # external origin, for OAuth metadata/redirects
```

### 2.3 Run

```bash
docker run -d -p 8080:8080 \
  -e AI_TOKEN=$AI_TOKEN -e DEVICE_TOKEN=$DEVICE_TOKEN -e PUBLIC_URL=$PUBLIC_URL \
  rishmcp-relay
```

Front it with a reverse proxy that terminates TLS and forwards WebSocket
upgrades (nginx, Caddy, Traefik — any of them work; the old
[`before/docker-compose.yml`](../before/docker-compose.yml) used Traefik, not
yet re-created for this rewrite).

### 2.4 Verify

```bash
curl -s https://mcp.example.com/healthz
# {"ok":true,"devices":0}
```

---

## 3. Pair the Android agent

No Shizuku app to install. The APK pairs directly with the phone's own
`adbd`. See [`docs/DESIGN.md` §3.1](DESIGN.md#31-셸-접근-페어링-shizuku-대체)
for the full rationale; this is the practical walkthrough.

### 3.1 Android 11+ (wireless debugging pairing)

1. On the phone: **Settings → Developer options → Wireless debugging → Pair
   device with pairing code**. Note the port and 6-digit code shown.
2. In the rish-mcp app's **ADB shell access** card, enter that port + code,
   tap **Pair**.
3. Go back to the main **Wireless debugging** screen and note the port shown
   there (different from the pairing port — this one persists across
   reconnects). Enter it in **Connect port**, tap **Save port**.
4. Fill in **Relay URL** / **Device token** in the Configuration card, tap
   **Save & Start**.

### 3.2 Android 11 미만 (USB + `adb tcpip` bridge)

Wireless pairing doesn't exist before Android 11. Instead:

1. Connect the phone to a PC over USB with USB debugging enabled.
2. From the PC: `adb tcpip <port>` — this switches `adbd` to listen on that
   TCP port instead of (or alongside) USB.
3. In the app, enter that port under **Connect port** (no pairing
   port/code needed on this path) and **Save port**.

**Known limitation:** depending on the ROM, this setting may not survive a
reboot, requiring the PC+`adb tcpip` step to be repeated. This is a
documented, accepted limitation (`docs/DESIGN.md` §7), not something the app
works around.

### 3.3 Headless provisioning

The `am start` extras still work, now including `adbPort`:

```bash
adb shell am start -n kr.scin.rishmcp/.MainActivity \
  --es relay wss://mcp.example.com/agent --es token <DEVICE_TOKEN> \
  --ei adbPort <PORT> --ez autostart true
```

Pairing itself (entering the wireless pairing code) still needs a tap on the
device the first time — see `docs/DESIGN.md`'s explicit non-goal: full
headless/no-tap install is no longer a target now that Shizuku is gone.

---

## 4. Connect an AI client

Static bearer:

```bash
claude mcp add --transport http phone https://mcp.example.com/mcp \
  --header "Authorization: Bearer <AI_TOKEN>"
```

Or claude.ai custom connectors (OAuth) — see [§6](#6-oauth-20-reference).

---

## 5. Tool reference

Unchanged from the original design — this is the one part of the contract
that stayed fixed across the rewrite.

### `list_devices()`

```json
[
  { "id": "android-1a2b3c4d", "name": "SM-S911N", "kind": "android",
    "sdk": "36", "agentVersion": "0.1.0", "agentVersionCode": 1,
    "connectedForMs": 84213, "pending": 0 }
]
```

### `run_shell({ cmd, deviceId?, timeoutMs? })`

Runs `cmd` on the phone as uid 2000. Returns:

```
exit=<code> (<durationMs>ms)[ [output truncated]]
--- stdout ---
<stdout>
--- stderr ---     ← only present when stderr is non-empty
<stderr>
```

A non-zero exit code sets `isError: true`. `deviceId` is required only when
more than one device is connected.

---

## 6. OAuth 2.0 reference

Ported to Go (`server/internal/oauth`), same model as the original design:
single-user, no database, every issued token is an HMAC-signed string
derived from `AI_TOKEN`. Rotating `AI_TOKEN` revokes everything at once.

| Endpoint | Spec | Purpose |
|---|---|---|
| `GET /.well-known/oauth-authorization-server[/…]` | RFC 8414 | authorization-server metadata |
| `GET /.well-known/oauth-protected-resource[/…]` | RFC 9728 | resource metadata |
| `POST /oauth/register` | RFC 7591 | dynamic client registration |
| `GET /oauth/authorize` | OAuth 2.0 | consent page |
| `POST /oauth/authorize` | — | consent submit (checks `AI_TOKEN`) |
| `POST /oauth/token` | OAuth 2.0 | `authorization_code` + `refresh_token` |

PKCE (S256) is mandatory. Auth codes are single-use with a 5-minute TTL.
Consent submissions are rate-limited per IP (10 attempts / 5 min). Default
token lifetimes: access 1h, refresh 90d.

For claude.ai: **Settings → Connectors → Add custom connector**, URL
`https://mcp.example.com/mcp`, leave client ID/secret blank. You'll land on
the consent page — paste `AI_TOKEN` once.

---

## 7. Official version server

`server/cmd/publicserver` — a separate, secret-free binary/container.

```bash
curl -s https://dl.example.com/api/version/release
# {"versionName":"0.1.0","versionCode":1,"tag":"v0.1.0",
#  "sizeBytes":...,"sha256":"...","modifiedAt":"...","download":"/agent.apk"}

curl -sO https://dl.example.com/agent.apk    # no token needed
```

It polls GitHub for the newest release with a `.apk` asset, caches it, and
keeps serving the last-good copy if a fetch or parse fails. It has no route
to the relay and holds no tokens or device information.

---

## 8. Troubleshooting

| Symptom | Likely cause / fix |
|---|---|
| `healthz` shows `"devices":0` | Phone agent not connected. Check the app's ADB shell status row and relay/token config. |
| `run_shell` → `no device is connected to the relay` | Same as above — WS dropped, or never connected. |
| `run_shell` → `multiple devices connected; pass deviceId` | Call `list_devices` and pass the right `deviceId`. |
| App shows `ADB shell: pairing failed` | Wrong pairing port/code, or the pairing window expired — re-open Wireless debugging pairing and retry. |
| App shows `ADB shell: connect failed` | Connect port is stale (Wireless debugging port rotates on some ROMs after reboot) — recheck the port in Settings and re-save. |
| `401` on `POST /mcp` | Token signed under an old `AI_TOKEN` (rotated), or sending `DEVICE_TOKEN` by mistake. |
| `[output truncated]` | Output exceeded 256 KB/stream. Narrow the command. |

---

## Appendix A: WS relay protocol

Unchanged from the original design (`server/internal/relay`):

```json
// relay → device
{ "type": "exec", "reqId": "<uuid>", "cmd": "...", "timeoutMs": 60000 }

// device → relay
{ "type": "result", "reqId": "<uuid>", "code": 0,
  "stdout": "...", "stderr": "", "truncated": false, "durationMs": 127 }
```

Connection query params on `GET /agent`: `token`, `deviceId`, `name`, `sdk`,
`kind` (`android` or `watch`), `ver`, `vc`. Ping interval: 25s general / 60s
`kind=watch`; a device missing ~2.5 ping cycles is dropped.

## Appendix B: environment variables

### `server/cmd/relay`

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `AI_TOKEN` | ✅ | — | Bearer secret for AI clients; also the OAuth signing seed |
| `DEVICE_TOKEN` | ✅ | — | Shared secret the phone presents on `/agent` |
| `PUBLIC_URL` | for OAuth | `http://localhost:$PORT` | External origin for OAuth metadata/redirects |
| `PORT` | | `8080` | Listen port |
| `DEFAULT_TIMEOUT_MS` | | `60000` | Default per-command timeout |
| `MAX_TIMEOUT_MS` | | `600000` | Ceiling for a caller-supplied `timeoutMs` |

### `server/cmd/publicserver`

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `PORT` | | `8080` | Listen port |
| `DOWNLOADS_PER_HOUR` | | `30` | Per-IP cap on `/agent.apk` |
| `GITHUB_REPO` | | `turin-dev/rish-mcp` | Repo whose latest release supplies the APK |
| `RELEASE_CACHE_DIR` | | `/var/cache/rish-mcp` | Where the fetched APK is cached |
| `RELEASE_POLL_MS` | | `900000` | How often to check GitHub for a newer release |
| `GITHUB_API_BASE` | | `https://api.github.com` | Overridable for testing |
| `APK_PATH` | | — | Serve this local APK instead of fetching a release; disables GitHub polling |
