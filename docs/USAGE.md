# rish-mcp — usage guide

Companion to the [README](../README.md) and [`docs/DESIGN.md`](DESIGN.md).
This covers deploying the two Go binaries, authorizing the Android agent's
Shizuku/ADB shell backends, the MCP tool reference, OAuth, and the WS protocol.

- [1. How it fits together](#1-how-it-fits-together)
- [2. Deploy the relay](#2-deploy-the-relay)
- [3. Authorize the Android agent](#3-authorize-the-android-agent)
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
│(Claude) │ ◀── stdout/code── │   (server/cmd/relay)  │ ── exec cmd ─────▶│Shizuku/ADB   │
└─────────┘                   └──────────────────────┘                   └──────────────┘
```

- **The phone never accepts inbound connections.** It dials *out* to the
  relay and holds one WebSocket open, so it works behind CGNAT.
- **Commands run as uid 2000 (shell)** — the same privilege level as
  `adb shell`. Root-only operations do not work.
- **Output is capped at 256 KB per stream** (stdout/stderr) on the phone;
  overflow sets a `truncated` flag rather than erroring.
- Shizuku is preferred after an explicit owner permission grant. A paired
  on-device ADB connection is used automatically when Shizuku is unavailable.

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
TRUSTED_PROXIES=172.18.0.1          # optional proxy IP(s), comma-separated
```

### 2.3 Docker Compose with Traefik (recommended)

The repository includes [`docker-compose.yml`](../docker-compose.yml), which
builds the two Go Docker targets and keeps the relay trust boundary separate
from the public APK server. It expects an external Traefik/Dokploy network:

```bash
cp .env.example .env
# Edit MCP_HOST and PUBLIC_MCP_HOST, then replace both token values.
# Generate secrets with: openssl rand -hex 32

docker network create dokploy-network  # once, if it does not exist
docker compose up -d --build
```

The relay is routed at `MCP_HOST` and receives `AI_TOKEN`/`DEVICE_TOKEN`.
Set `TRUSTED_PROXIES` to the direct IP address(es) of the reverse proxy (comma-separated)
when Traefik/nginx supplies `X-Forwarded-For`; with it empty, the server uses the
socket peer address and ignores client-supplied forwarding headers. Configure the
same variable on both relay and publicserver when both sit behind the same proxy.
The publicserver is routed at `PUBLIC_MCP_HOST`, has no tokens, and serves
only `/healthz`, `/api/version/release`, and `/agent.apk`. Traefik must expose
the `websecure` entrypoint and the `letsencrypt` certificate resolver used by
the labels in the Compose file. The Android WebSocket upgrade is handled by
Traefik automatically.

### 2.4 Run one container manually

For local testing without Traefik, run the relay directly:

```bash
docker run --rm -p 8080:8080 \
  -e AI_TOKEN="$AI_TOKEN" -e DEVICE_TOKEN="$DEVICE_TOKEN" \
  -e PUBLIC_URL="$PUBLIC_URL" \
  rishmcp-relay
```

For production, put a TLS reverse proxy in front of the container (nginx,
Caddy, or Traefik) and forward WebSocket upgrades on `/agent`.

### 2.5 Verify

```bash
curl -s https://mcp.example.com/healthz
# {"ok":true,"devices":0}
```

---

## 3. Authorize the Android agent

The app supports two uid-2000 shell transports. Shizuku is recommended,
especially on Wear OS and Android versions where wireless-debugging ports
change after reboot. ADB remains a fully supported fallback.

### 3.1 Shizuku (recommended)

1. Install and start [Shizuku](https://shizuku.rikka.app/) using its normal
   wireless-debugging, USB, or rooted-device instructions.
2. Open rish-mcp and tap **Grant Shizuku** in the **Shell access** card.
3. Accept Shizuku's permission prompt, fill in **Relay URL** / **Device
   token**, then tap **Save & Start**.

The foreground service binds a Shizuku UserService running as shell uid 2000.
If Shizuku later stops, rish-mcp uses an already-paired ADB backend for new
commands. A command that was already dispatched is never automatically
replayed on another backend.

rish-mcp deliberately rejects a Shizuku server running as root (uid 0). Start
Shizuku in normal ADB mode; the product's security contract is shell uid 2000,
not opportunistic root escalation.

### 3.2 Android 11+ ADB fallback (wireless debugging pairing)

1. On the phone: **Settings → Developer options → Wireless debugging → Pair
   device with pairing code**. Note the port and 6-digit code shown.
2. In the rish-mcp app's **Shell access** card, enter that port + code,
   tap **Pair**.
3. Go back to the main **Wireless debugging** screen and note the port shown
   there (different from the pairing port — this one persists across
   reconnects). Enter it in **Connect port**, tap **Save port**.
4. Fill in **Relay URL** / **Device token** in the Configuration card, tap
   **Save & Start**.

### 3.3 Android 11 미만 ADB fallback (USB + `adb tcpip` bridge)

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

### 3.4 Headless provisioning

The `am start` extras still work, now including `adbPort`:

```bash
adb shell am start -n kr.scin.rishmcp/.ProvisioningActivity \
  --es relay wss://mcp.example.com/agent --es token <DEVICE_TOKEN> \
  --ei adbPort <PORT> --ez autostart true
```

ADB wireless pairing still needs a tap on the device the first time. A device
where the owner already granted rish-mcp in Shizuku can use that backend
without configuring `adbPort`.

The dedicated activity is protected by Android's privileged `DUMP` permission,
which adb shell holds. The normal launcher activity ignores extras;
on Android 14+ the app also checks that the launching uid is exactly 2000.

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
    "sdk": "36", "agentVersion": "1.0.0", "agentVersionCode": 10000,
    "shellBackend": "shizuku",
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
# {"versionName":"1.0.0","versionCode":10000,"tag":"agent-v1.0.0",
#  "sizeBytes":...,"sha256":"...","modifiedAt":"...","download":"/agent.apk"}

curl -sO https://dl.example.com/agent.apk    # no token needed
```

It lists stable GitHub releases in the configured channel and selects the
highest semantic version, rather than trusting GitHub creation order or the
repository-wide `latest` release. A channel tag must be exactly
`RELEASE_TAG_PREFIX` + `MAJOR.MINOR.PATCH`; the default prefix is `agent-v`, so
a rewrite release is tagged, for example, `agent-v1.0.0`. This separate channel
intentionally excludes historical `v0.2`–`v0.5` releases, which contain the
legacy Shizuku app rather than the rewrite agent.

Release-list pagination is scanned to a safety bound of 1,000 entries. If the
final page cannot be reached within that bound, the refresh is rejected as a
whole instead of publishing a potentially lower version from a partial list.

Before publication, the tag suffix must exactly match the APK's embedded
`versionName`, its Android `versionCode` must be greater than the currently
served rewrite release, and the tag version itself must increase. Downloads
larger than 128 MiB are rejected. A failed download, parse, validation, or
metadata update leaves the last-good release in service. The 128 MiB file cap
also applies when restoring a cache or reading the `APK_PATH` override.
Validated APKs use immutable SHA-256-based cache filenames; `release.json` is
the small mutable pointer recovered after an ordinary process interruption
(not a power-loss durability guarantee). On startup, non-current
content-addressed APKs are removed only after a valid current pointer is loaded
and no unresolved metadata backup remains. Treat the cache as single-writer:
run only one public-server process per `RELEASE_CACHE_DIR`. `APK_PATH` remains
the local development override and disables GitHub polling. The public server
has no route to the relay and holds no tokens or device information.

---

## 8. Troubleshooting

| Symptom | Likely cause / fix |
|---|---|
| `healthz` shows `"devices":0` | Phone agent not connected. Check the app's Shell status row and relay/token config. |
| App shows `Shizuku permission needed` | Start Shizuku, tap **Grant Shizuku**, and approve the owner permission prompt. |
| App shows `Shizuku not running` | Start Shizuku again; the foreground service rebinds automatically. A configured ADB backend remains available as fallback. |
| App shows `root mode rejected` | Restart Shizuku in ADB mode. rish-mcp intentionally refuses a uid-0 UserService. |
| `run_shell` → `no device is connected to the relay` | Same as above — WS dropped, or never connected. |
| `run_shell` → `multiple devices connected; pass deviceId` | Call `list_devices` and pass the right `deviceId`. |
| App shows `ADB shell: pairing failed` | Wrong pairing port/code, or the pairing window expired — re-open Wireless debugging pairing and retry. |
| App shows `ADB shell: connect failed` | Connect port is stale (Wireless debugging port rotates on some ROMs after reboot) — recheck the port in Settings and re-save. |
| `401` on `POST /mcp` | Token signed under an old `AI_TOKEN` (rotated), or sending `DEVICE_TOKEN` by mistake. |
| `[output truncated]` | Output exceeded 256 KB/stream. Narrow the command. |

---

## Appendix A: WS relay protocol

The command/result contract is unchanged; 1.0 adds a metadata-only status
frame so `list_devices` follows backend transitions without reconnecting:

```json
// relay → device
{ "type": "exec", "reqId": "<uuid>", "cmd": "...", "timeoutMs": 60000 }

// device → relay
{ "type": "result", "reqId": "<uuid>", "code": 0,
  "stdout": "...", "stderr": "", "truncated": false, "durationMs": 127 }

// device → relay when the active backend changes
{ "type": "status", "backend": "shizuku" }
```

Connection query params on `GET /agent`: `token`, `deviceId`, `name`, `sdk`,
`kind` (`android` or `watch`), `ver`, `vc`, `backend`. Ping interval: 25s general / 60s
`kind=watch`; a device missing ~2.5 ping cycles is dropped.

## Appendix B: environment variables

### `server/cmd/relay`

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `AI_TOKEN` | ✅ | — | Bearer secret for AI clients; also the OAuth signing seed |
| `DEVICE_TOKEN` | ✅ | — | Shared secret the phone presents on `/agent` |
| `PUBLIC_URL` | for OAuth | `http://localhost:$PORT` | External origin for OAuth metadata/redirects |
| `TRUSTED_PROXIES` | | empty | Comma-separated proxy IPs allowed to set `X-Forwarded-For` |
| `PORT` | | `8080` | Listen port |
| `DEFAULT_TIMEOUT_MS` | | `60000` | Default per-command timeout |
| `MAX_TIMEOUT_MS` | | `600000` | Ceiling for a caller-supplied `timeoutMs` |

### `server/cmd/publicserver`

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `PORT` | | `8080` | Listen port |
| `TRUSTED_PROXIES` | | empty | Comma-separated proxy IPs allowed to set `X-Forwarded-For` |
| `DOWNLOADS_PER_HOUR` | | `30` | Per-IP cap on `/agent.apk` |
| `GITHUB_REPO` | | `turin-dev/rish-mcp` | Repo whose compatible release supplies the APK |
| `RELEASE_TAG_PREFIX` | | `agent-v` | Rewrite release channel; accepted tags are exactly `<prefix>MAJOR.MINOR.PATCH`, and the highest compatible version is selected |
| `RELEASE_CACHE_DIR` | | `/var/cache/rish-mcp` | Where the fetched APK is cached |
| `RELEASE_POLL_MS` | | `900000` | How often to check GitHub for a newer release |
| `GITHUB_API_BASE` | | `https://api.github.com` | Overridable for testing |
| `APK_PATH` | | — | Serve this local APK (maximum 128 MiB) instead of fetching a release; disables GitHub polling |
