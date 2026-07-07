# rish-mcp — detailed usage guide

This is the long-form companion to the [README](../README.md). It walks through
the whole path end to end: deploy the relay, install the agent on the phone,
connect an AI client (including the **Claude mobile app** via OAuth), and covers
the tool reference, OAuth model, recipes, troubleshooting, and the threat model.

- [1. How it fits together](#1-how-it-fits-together)
- [2. Deploy the relay + MCP server](#2-deploy-the-relay--mcp-server)
- [3. Install the agent on the phone](#3-install-the-agent-on-the-phone)
- [4. Connect an AI client](#4-connect-an-ai-client)
  - [4.1 Claude Code](#41-claude-code-cli--desktop--web)
  - [4.2 Claude API / Agent SDK](#42-claude-api--agent-sdk)
  - [4.3 claude.ai custom connector (OAuth) — incl. the phone app](#43-claudeai-custom-connector-oauth--incl-the-phone-app)
  - [4.4 Any other MCP client](#44-any-other-mcp-client)
- [5. Tool reference](#5-tool-reference)
- [6. OAuth 2.0 reference](#6-oauth-20-reference)
- [7. Recipes: natural language → shell](#7-recipes-natural-language--shell)
- [8. Troubleshooting](#8-troubleshooting)
- [9. Security & threat model](#9-security--threat-model)
- [10. Rotating & revoking access](#10-rotating--revoking-access)
- [Appendix A: WS relay protocol](#appendix-a-ws-relay-protocol)
- [Appendix B: environment variables](#appendix-b-environment-variables)

---

## 1. How it fits together

```
┌─────────┐  MCP run_shell    ┌──────────────────────┐   WS (outbound)   ┌──────────────┐
│   AI    │ ──HTTPS+auth────▶ │  relay + MCP server  │ ◀── phone dials ──│  phone APK   │
│(Claude) │ ◀── stdout/code── │   (Node, :8080)      │ ── exec cmd ─────▶│ Shizuku→shell │
└─────────┘                   └──────────────────────┘                   └──────────────┘
      ▲                              ▲        ▲
      │ OAuth or Bearer AI_TOKEN     │        │ DEVICE_TOKEN (query param on the WS)
      └── you control this token ────┘        └── shared secret baked into the agent
```

Three parties, two trust boundaries:

| Party | Talks to | Auth it presents |
|---|---|---|
| **AI / MCP client** | `POST /mcp` (HTTPS) | `Authorization: Bearer <AI_TOKEN>` **or** an OAuth access token |
| **Phone agent** | `GET /agent` (WebSocket, outbound only) | `?token=<DEVICE_TOKEN>` |
| **You (owner)** | consent page at `/oauth/authorize` | you paste `AI_TOKEN` once |

Key properties:

- **The phone never accepts inbound connections.** It dials *out* to the relay
  and holds one WebSocket open, so it works behind CGNAT (e.g. SKT mobile),
  with no VPN, no `adb`, no `sshd`, no port forwarding.
- **The relay is stateless per request.** Each `POST /mcp` builds a fresh MCP
  server instance; there are no MCP sessions to manage.
- **Commands run as uid 2000 (shell)** via Shizuku's `UserService` — exactly the
  privilege level of `adb shell`. Root-only operations do **not** work.
- Output is capped at **256 KB per stream** (stdout and stderr each) on the
  phone; overflow sets a `truncated` flag rather than erroring.

---

## 2. Deploy the relay + MCP server

### 2.1 Prerequisites

- A small always-on host with Docker (any VPS works).
- A hostname you control pointing at it, e.g. `mcp.example.com`, terminating TLS.
  The examples assume **Traefik** in front, but any reverse proxy that supports
  **WebSocket upgrades** works (nginx, Caddy). TLS is required — claude.ai only
  connects to `https://` connectors, and phones dial `wss://`.

### 2.2 Configure env

Copy `.env.example` to `.env` and fill it in (it's gitignored):

```bash
cp .env.example .env
# generate strong secrets:
echo "AI_TOKEN=$(openssl rand -hex 32)"      >> .env   # then edit the placeholder out
echo "DEVICE_TOKEN=$(openssl rand -hex 24)"  >> .env
```

```ini
MCP_HOST=mcp.example.com          # public hostname (Traefik routes this to :8080)
AI_TOKEN=<64 hex chars>           # the "master key" for AI clients — treat like an SSH key
DEVICE_TOKEN=<48 hex chars>       # shared secret the phone presents on the WS
# PUBLIC_URL is derived from MCP_HOST by docker-compose; only set it manually
# if you run the server outside compose.
```

See [Appendix B](#appendix-b-environment-variables) for every variable.

### 2.3 Bring it up

```bash
docker compose up -d --build
```

`docker-compose.yml` sets `PUBLIC_URL=https://${MCP_HOST}` (so OAuth metadata and
redirects are correct), mounts the built APK read-only for OTA, and attaches the
Traefik router. The container listens on `:8080` internally; only Traefik is
public.

### 2.4 DNS + TLS

Point an `A` record at the host: `mcp.example.com → <server-ip>`. Behind
Cloudflare, **orange-cloud (proxied) is fine** — HTTPS and WSS both ride `:443`,
including the phone's `/agent` WebSocket. Traefik obtains the certificate via its
configured resolver (Let's Encrypt in the sample labels).

### 2.5 Verify

```bash
curl -s https://mcp.example.com/healthz
# {"ok":true,"devices":0}   ← 0 until a phone connects (next section)
```

If you get a TLS or 404 error, the proxy/DNS isn't routing yet — fix that before
moving on.

---

## 3. Install the agent on the phone

The phone side is one APK. It needs **Shizuku** running (the app that hands out
`adb shell`-level access without root). Install and start Shizuku first — via
wireless debugging, a computer, or root.

### 3.1 Build the APK

```bash
cd app && ./build-apk.sh        # runs the Android SDK + Gradle inside Docker
# -> app/rish-mcp-agent.apk
```

### 3.2 Headless install (recommended, no taps on the phone)

If you already have a Shizuku shell on the device (`rish` or `adb shell`), you
can install and provision without touching the screen. `-g` grants Shizuku's
runtime permission; the `am` extras provision the relay URL and token.

```bash
TOKEN=<DEVICE_TOKEN>

# 1. push + install with runtime perms granted
rish -c 'cat > /data/local/tmp/r.apk' < app/rish-mcp-agent.apk
rish -c 'pm install -r -g /data/local/tmp/r.apk; rm -f /data/local/tmp/r.apk'

# 2. provision + start the foreground agent
rish -c "am start -n kr.scin.rishmcp/.MainActivity \
  --es relay wss://mcp.example.com/agent --es token $TOKEN --ez autostart true"
```

Provisioning extras understood by `MainActivity`:

| Extra | Type | Meaning |
|---|---|---|
| `relay` | string (`--es`) | WebSocket URL, e.g. `wss://mcp.example.com/agent` |
| `token` | string (`--es`) | the `DEVICE_TOKEN` |
| `autostart` | bool (`--ez`) | start the foreground service immediately |

To **re-point** an already-running agent at a new relay/token:

```bash
rish -c 'am force-stop kr.scin.rishmcp'   # it reads config fresh on next start
rish -c "am start -n kr.scin.rishmcp/.MainActivity --es relay wss://NEW/agent --es token NEW --ez autostart true"
```

### 3.3 Manual install

Sideload the APK, open the app, tap **Grant Shizuku permission**, paste the relay
URL + `DEVICE_TOKEN`, then **Save & Start**.

### 3.4 What the agent does

- Holds one outbound `wss://…/agent?token=…&deviceId=…&name=…&sdk=…` connection.
- Runs as a **foreground service** (persistent notification) so Android won't kill
  it, and **auto-starts on boot** via `BootReceiver`.
- A watchdog reconnects on network changes or if Shizuku drops.
- The relay pings every 25 s to keep the socket warm; a reconnect with the same
  `deviceId` transparently replaces the stale socket.

### 3.5 Confirm it's connected

```bash
curl -s https://mcp.example.com/healthz
# {"ok":true,"devices":1}   ← 1 means the phone's WebSocket is up
```

Or ask any connected AI to call `list_devices` (see below).

### 3.6 OTA self-update

The relay serves the mounted APK at `GET /agent.apk?t=<DEVICE_TOKEN>`, so a phone
can update itself over the same channel — no `adb`/`sshd`:

```bash
rish -c 'curl -sfL "https://mcp.example.com/agent.apk?t=<DEVICE_TOKEN>" -o /data/local/tmp/r.apk \
  && pm install -r -g /data/local/tmp/r.apk && rm -f /data/local/tmp/r.apk'
```

---

## 4. Connect an AI client

Two authentication styles are supported **in parallel**:

- **Static bearer** — send `Authorization: Bearer <AI_TOKEN>`. Simplest; used by
  Claude Code, the API, curl, and any client that lets you set a header.
- **OAuth 2.0** — for clients that *only* speak OAuth, most importantly
  **claude.ai custom connectors** (which drive the Claude web + mobile + desktop
  apps). See [§4.3](#43-claudeai-custom-connector-oauth--incl-the-phone-app).

### 4.1 Claude Code (CLI / desktop / web)

One command:

```bash
claude mcp add --transport http phone https://mcp.example.com/mcp \
  --header "Authorization: Bearer <AI_TOKEN>"
```

Or drop it into `.mcp.json` (project) / your user MCP config:

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

### 4.2 Claude API / Agent SDK

Use the MCP connector with a bearer header (the API calls your relay from
Anthropic's servers, so the URL must be publicly reachable):

```jsonc
// messages request → "mcp_servers"
{
  "type": "url",
  "url": "https://mcp.example.com/mcp",
  "name": "phone",
  "authorization_token": "<AI_TOKEN>"
}
```

### 4.3 claude.ai custom connector (OAuth) — incl. the phone app

claude.ai's custom-connector UI has **no field for a static bearer token**, so
rish-mcp ships a small built-in OAuth 2.0 authorization server that the connector
flow drives automatically. You never register a client or copy a client
secret — the only thing you type is your `AI_TOKEN`, once, on a consent page.

**Steps:**

1. Go to **claude.ai → Settings → Connectors → Add custom connector**.
2. **Name:** anything (e.g. "My phone"). **URL:** `https://mcp.example.com/mcp`.
   Leave the advanced OAuth client ID/secret fields **blank**.
3. Click **Add / Connect**. Claude discovers the OAuth endpoints, registers
   itself dynamically, and sends you to the rish-mcp **consent page**.
4. On the consent page, paste your `AI_TOKEN` and click **Authorize**.
5. You're redirected back and the connector shows **Connected**. Enable it for a
   chat via the connector/tools menu.

Because Claude connects to your relay from **Anthropic's cloud** (not from the
device), and the connector **syncs across web, desktop, and the mobile apps**,
this works from anywhere — including the delightful case of the **Claude app on
the very phone it is controlling**.

> **Do not** run the relay with auth disabled just to satisfy a client. That
> would expose shell access to your phone to anyone who finds the URL. The OAuth
> layer exists precisely so you never have to.

Details of the flow and its security properties are in [§6](#6-oauth-20-reference).

### 4.4 Any other MCP client

Anything that speaks **Streamable HTTP MCP** and can send an `Authorization`
header works with the static-bearer style. Point it at `https://mcp.example.com/mcp`.

### 4.5 Quick check without any AI

```bash
AI=<AI_TOKEN>
curl -s https://mcp.example.com/mcp \
  -H "Authorization: Bearer $AI" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call",
       "params":{"name":"run_shell","arguments":{"cmd":"getprop ro.product.model"}}}'
# -> exit=0 (…ms) / --- stdout --- / SM-S911N
```

---

## 5. Tool reference

The server advertises exactly two tools.

### `list_devices()`

No arguments. Returns a JSON array of connected phones:

```json
[
  { "id": "s23-1a2b3c4d", "name": "phone", "sdk": "36",
    "connectedForMs": 84213, "pending": 0 }
]
```

- `id` — device id; pass it to `run_shell` as `deviceId` when more than one phone
  is connected.
- `pending` — commands currently in flight to that device.

Call this first if you're unsure which phone is online.

### `run_shell({ cmd, deviceId?, timeoutMs? })`

Runs `cmd` on the phone as uid 2000 (via `sh -c`), like `adb shell`.

| Param | Type | Default | Notes |
|---|---|---|---|
| `cmd` | string (required) | — | the shell command line, e.g. `dumpsys battery` |
| `deviceId` | string | the sole device | **required** only when >1 phone is connected |
| `timeoutMs` | int | `DEFAULT_TIMEOUT_MS` (60 000) | per-command; capped at `MAX_TIMEOUT_MS` (600 000) |

**Return shape** (text content):

```
exit=<code> (<durationMs>ms)[ [output truncated]]
--- stdout ---
<stdout>
--- stderr ---     ← only present when stderr is non-empty
<stderr>
```

Behavior notes:

- A **non-zero exit code** sets the MCP result's `isError: true` — the command
  still ran; the flag just signals failure to the model.
- **Timeout:** the phone force-kills the process at `timeoutMs` (exit `-1`); the
  relay adds a 2 s grace before giving up on the round trip.
- **Truncation:** stdout/stderr are each capped at 256 KB on the phone; overflow
  (or a killed process) sets the `[output truncated]` marker.
- **Device selection:** omit `deviceId` when exactly one phone is connected. With
  none connected you get `no phone is connected to the relay`; with several,
  `multiple devices connected; pass deviceId`.

---

## 6. OAuth 2.0 reference

The server implements just enough of OAuth for MCP clients, and no more. It is
**single-user by design**: "logging in" means typing the relay's `AI_TOKEN` once.

### Endpoints

| Endpoint | Spec | Purpose |
|---|---|---|
| `GET /.well-known/oauth-authorization-server[/…]` | RFC 8414 | authorization-server metadata |
| `GET /.well-known/oauth-protected-resource[/…]` | RFC 9728 | resource metadata (the `/mcp` resource → issuer) |
| `POST /oauth/register` | RFC 7591 | dynamic client registration (public clients) |
| `GET /oauth/authorize` | OAuth 2.0 | renders the consent page |
| `POST /oauth/authorize` | — | consent submit (checks `AI_TOKEN`), redirects with a code |
| `POST /oauth/token` | OAuth 2.0 | `authorization_code` + `refresh_token` grants |

A `401` from `POST /mcp` carries `WWW-Authenticate: Bearer resource_metadata="…/.well-known/oauth-protected-resource"`, which is how an OAuth-capable client discovers the flow.

### The flow

```
client → POST /oauth/register {redirect_uris}            → client_id
client → GET  /oauth/authorize?client_id&redirect_uri
              &code_challenge&code_challenge_method=S256   → consent page
you    → POST /oauth/authorize  (paste AI_TOKEN)           → 302 redirect_uri?code=…&state=…
client → POST /oauth/token grant=authorization_code
              code, code_verifier, redirect_uri            → {access_token, refresh_token}
client → POST /mcp  Authorization: Bearer <access_token>   → tools work
client → POST /oauth/token grant=refresh_token …           → fresh access_token
```

### Token & security model

- **No database.** Every issued value (`client_id`, auth code, access token,
  refresh token) is a self-describing string signed with an HMAC key **derived
  from `AI_TOKEN`**. The server keeps no per-client state.
- **Rotating `AI_TOKEN` revokes everything at once** — the signing key changes, so
  all previously issued tokens fail verification immediately.
- **PKCE (S256) is mandatory.** Authorization without a valid `code_challenge`
  is rejected; the token endpoint verifies the `code_verifier`.
- **Auth codes are single-use** (5-minute TTL) and bound to the exact
  `redirect_uri` and PKCE challenge.
- **Consent is the only place the real secret is compared**, and it is rate-limited
  per IP (10 attempts / 5 min).
- Registration and the consent page are open (public clients only), but
  possessing a `client_id` grants nothing without the token-paste step.
- Default token lifetimes: access **1 h**, refresh **90 d** (both configurable in
  `OAuthProvider`).

This is intentionally minimal — good enough to let first-party Claude clients
connect to *your own* single-tenant relay. It is not a multi-tenant IdP.

---

## 7. Recipes: natural language → shell

Once connected, you just ask in plain language and Claude picks the command.
Everything an `adb shell` can do is available: `pm`, `am`, `dumpsys`, `settings`,
`cmd`, `input`, `screencap`, `logcat`, and file access under `/sdcard`.

| You ask | Roughly runs |
|---|---|
| "What's my battery level and is it charging?" | `dumpsys battery` |
| "Is the screen on right now?" | `dumpsys power \| grep -i wakefulness` |
| "List the apps I installed myself." | `pm list packages -3` |
| "How much free storage do I have?" | `df -h /data` |
| "Take a screenshot." | `screencap -p /sdcard/s.png` (then read/pull it) |
| "Silence the phone." | `cmd notification set_dnd on` |
| "Open Google Maps." | `am start -a android.intent.action.VIEW -d geo:0,0` |
| "What Wi-Fi am I on?" | `dumpsys wifi \| grep -i ssid` |
| "Show the last 50 lines of logcat for MyApp." | `logcat -d -t 50 \| grep MyApp` |
| "Type 'hello' into the focused field." | `input text hello` |

Root-only things (writing outside app/shell-accessible paths, editing protected
system settings) won't work — that's the uid-2000 boundary, same as `adb`.

---

## 8. Troubleshooting

| Symptom | Likely cause / fix |
|---|---|
| `healthz` shows `"devices":0` | Phone agent not connected. Check Shizuku is running, the foreground notification is present, and `relay`/`token` are correct. Re-provision (§3.2). |
| `run_shell` → `no phone is connected to the relay` | Same as above — the WS dropped. The watchdog should reconnect; force with `am force-stop` + restart. |
| `multiple devices connected; pass deviceId` | Call `list_devices` and pass the right `deviceId`. |
| claude.ai connector never reaches the consent page | `PUBLIC_URL` wrong → metadata/redirect mismatch. It must equal the exact external origin (`https://mcp.example.com`). Check `GET /.well-known/oauth-authorization-server`. |
| claude.ai says the connector is unreachable | Relay not public over HTTPS, or the proxy blocks the well-known paths / `POST` bodies. Verify with the curl checks in §2.5 and §4.5. |
| `401` on `POST /mcp` with a token you expect to work | Token was signed under an **old** `AI_TOKEN` (rotated), or you're sending the `DEVICE_TOKEN` by mistake. Re-issue via the OAuth flow or use the current `AI_TOKEN`. |
| `[output truncated]` | Output exceeded 256 KB/stream. Narrow the command (`head`, `grep`, `-t N`). |
| Command exits `-1` after a delay | Hit the timeout; raise `timeoutMs` (up to `MAX_TIMEOUT_MS`) or make the command return faster. |
| WebSocket won't upgrade | Proxy not forwarding `Upgrade`/`Connection` headers for `/agent`. Enable WS support on the reverse proxy. |

Server logs: `docker compose logs -f rish-mcp`.

---

## 9. Security & threat model

- **`AI_TOKEN` is a master key.** Anyone holding it — or any live OAuth token
  derived from it — can run shell commands (uid 2000) on your phone. Treat it
  exactly like an SSH private key. Never commit it; `.env` is gitignored.
- **`DEVICE_TOKEN` gates who may register as a phone.** Leaking it lets an
  attacker impersonate a device (present a fake phone), not run commands on yours.
- **The phone trusts only the relay it dials.** It never listens for inbound
  connections, so there's no attack surface on the device itself over the network.
- **The relay is a high-value target**: it can command every connected phone.
  Keep it patched, keep TLS on, and don't expose `:8080` directly — only via the
  proxy.
- **Scope is the owner's own device** for personal automation. Don't point this
  at devices you don't own or don't have authorization to control.
- **Blast radius of a compromised token** = whatever `adb shell` can do: read
  `/sdcard`, list/inspect apps, change many settings, drive the UI, capture the
  screen. Not root, but not nothing. Rotate promptly if you suspect exposure
  (§10).

---

## 10. Rotating & revoking access

Because all AI-side credentials derive from `AI_TOKEN`, rotation is one step and
revokes **everything** (static bearer users and every OAuth token/refresh token):

```bash
# 1. new secret
sed -i "s/^AI_TOKEN=.*/AI_TOKEN=$(openssl rand -hex 32)/" .env
# 2. restart to load it
docker compose up -d
```

After this:

- Update Claude Code / API configs with the new `AI_TOKEN`.
- Re-authorize the claude.ai connector (it will hit the consent page again; paste
  the new token).

To rotate the **phone** credential, change `DEVICE_TOKEN` in `.env`, restart, then
re-provision the agent (§3.2) with the new token.

---

## Appendix A: WS relay protocol

The phone↔relay messages are newline-free JSON frames over the WebSocket.

**Relay → phone (run a command):**

```json
{ "type": "exec", "reqId": "<uuid>", "cmd": "getprop ro.product.model", "timeoutMs": 60000 }
```

**Phone → relay (result):**

```json
{ "type": "result", "reqId": "<uuid>", "code": 0,
  "stdout": "SM-S911N\n", "stderr": "", "truncated": false, "durationMs": 127 }
```

Connection query params on `GET /agent`: `token` (required, = `DEVICE_TOKEN`),
`deviceId` (optional; server assigns a UUID if absent), `name`, `sdk`. The relay
pings every 25 s; a reconnect with an existing `deviceId` replaces the old socket
and fails its in-flight commands with `device reconnected`.

## Appendix B: environment variables

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `AI_TOKEN` | ✅ | — | Bearer secret for AI clients; also the OAuth signing seed and consent password |
| `DEVICE_TOKEN` | ✅ | — | Shared secret the phone presents on the `/agent` WebSocket |
| `PUBLIC_URL` | for OAuth | `http://localhost:<PORT>` | External `https://` origin used in OAuth metadata + redirects. Compose sets it from `MCP_HOST` |
| `MCP_HOST` | compose | — | Public hostname; Traefik routing + derives `PUBLIC_URL` |
| `PORT` | | `8080` | Internal listen port |
| `DEFAULT_TIMEOUT_MS` | | `60000` | Default per-command timeout |
| `MAX_TIMEOUT_MS` | | `600000` | Ceiling for a caller-supplied `timeoutMs` |
| `APK_PATH` | | `/srv/agent.apk` | Path the OTA endpoint serves |
