# rish-mcp

Expose an Android device's own shell (uid 2000, like `adb shell`) to AIs as an
**MCP tool** — without VPN, adb-from-a-PC-forever, or sshd. The device holds a
single **outbound** WebSocket to a relay on a public hostname you control; AIs
call the relay's MCP endpoint.

```
                 상시 WS (일반 기기)
┌─────────┐   MCP   ┌──────────────────┐ ◀───────────────── ┌──────────────┐
│   AI    │──HTTPS─▶│  Go relay + MCP  │                    │ Android 앱   │
│(Claude) │ ◀───────│     서버         │──FCM 웨이크업──────▶│ (저사양 기기) │
└─────────┘         └──────────────────┘   (Google FCM 경유) └──────────────┘
                             │
                             │ 버전/체크섬 조회, APK 배포
                             ▼
                   ┌──────────────────┐
                   │  공식 버전 서버   │  (별도 바이너리/컨테이너, 무토큰)
                   └──────────────────┘
```

> **This is a from-scratch rewrite in progress.** The previous Shizuku +
> Node/TS implementation lives under [`before/`](before/) for reference. See
> [`plan.md`](plan.md) for why it was rebuilt and [`docs/DESIGN.md`](docs/DESIGN.md)
> for the current architecture and what's actually implemented vs. still
> pending — read that before assuming anything below is production-ready.

## Why rewrite

The old agent depended on [Shizuku](https://shizuku.rikka.app/) — a separate
app the user had to install, understand, and grant permission to, which also
meant devices that didn't support or know about Shizuku couldn't use rish-mcp
at all. See [`plan.md`](plan.md) for the full rationale (Shizuku dependency,
Wear OS performance, server code quality, no official version endpoint).

## What's different this time

- **No Shizuku.** The Android app pairs with its own `adbd` directly —
  wireless-debugging pairing on Android 11+, a PC+`adb tcpip` bridge below
  that. See [`docs/DESIGN.md` §3.1](docs/DESIGN.md#31-셸-접근-페어링-shizuku-대체).
- **Go relay**, not Node/TS — same MCP tool contracts (`run_shell`,
  `list_devices`), same WS relay protocol, same OAuth model, rewritten for
  concurrency/memory efficiency and a single static binary.
- **Hybrid connection model** (planned): normal phones/tablets keep an
  always-on WebSocket; low-spec devices (Wear OS) are meant to move to an
  FCM-wake + short session model instead. **Not implemented yet** — it needs
  a Firebase project this repo doesn't have configured. Every device
  currently uses the always-on path.

## Status

| Piece | State |
|---|---|
| Go relay (`server/cmd/relay`) — MCP tools, WS relay, static bearer + OAuth | ✅ built, tested |
| Official version server (`server/cmd/publicserver`) | ✅ built, tested |
| Android `AdbShellClient` (ADB pairing, shell exec) | ✅ built, tested (unit-testable parts only — no device to pair against in this environment) |
| `ConnectionManager` / `AgentService` / `MainActivity` (pairing UI) | ✅ built, compiles — **not verified against a real device** |
| Low-spec hybrid connection + FCM wake | ⛔ blocked — needs a Firebase project (see `docs/DESIGN.md` §7) |
| Docker packaging for the Go binaries | ✅ `server/Dockerfile` (`--target relay` / `--target publicserver`) |
| docker-compose / reverse-proxy deploy config | ✅ `docker-compose.yml` (Traefik/Dokploy) |

## Components

- `server/cmd/relay` — Go. Streamable-HTTP **MCP server** (`run_shell`,
  `list_devices`) + **WS relay** the Android device connects to. Static
  bearer or OAuth for AIs, shared token for the device.
- `server/cmd/publicserver` — Go. Separate, secret-free binary: reports the
  current agent version and serves the APK. No route to the relay.
- `app/` — Android (Kotlin). One installable APK: pairs with the device's own
  `adbd` to run commands as shell uid, a foreground service holds the
  outbound WS, auto-starts on boot.

## Quick start: Android setup CLI

If you want the guided installer rather than building the APK and configuring
`adb` by hand, run it directly with `npx`:

```bash
npx rish-mcp-setup
```

It requires Node.js 18 or newer and does not install globally. For scripts, use
`--yes --action setup|apk|relay`; see [`cli/README.md`](cli/README.md) for all
options and environment variables.

## Build

```bash
# server (both binaries)
cd server && go build ./... && go test ./...

# server Docker images
docker build --target relay -t rishmcp-relay server
docker build --target publicserver -t rishmcp-public server

# Android APK (Android SDK + Gradle run inside Docker; host stays clean)
cd app && docker build -t rishmcp-android-build -f Dockerfile.build .
# then run gradle assembleDebug/assembleRelease inside that image — see
# before/app/build-apk.sh for the release-signing flow this hasn't been
# re-ported to yet.
```

## Deploy

For a Dokploy host with an external Traefik network, copy `.env.example` to
`.env`, set the two hostnames and generate `AI_TOKEN`/`DEVICE_TOKEN`, then run:

```bash
cp .env.example .env
# edit .env and replace both secrets
openssl rand -hex 32

docker network create dokploy-network  # once, if it does not exist
docker compose up -d --build
curl -fsS "https://${MCP_HOST}/healthz"
```

`docker-compose.yml` keeps the trust boundary explicit: the relay receives
shell-access secrets and serves MCP/agent traffic, while the separate
publicserver serves only release metadata and the APK. For all variables and
manual Docker deployment, see [`docs/USAGE.md`](docs/USAGE.md).

## Use from an AI (MCP client)

Same tool surface as before — this part of the contract didn't change:

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

- `list_devices()` — connected devices, with agent version + update flag.
- `run_shell({cmd, deviceId?, timeoutMs?})` — run a command as shell uid;
  returns stdout, stderr, exit code.

Full tool reference, OAuth flow, and the WS relay protocol are documented in
[`docs/USAGE.md`](docs/USAGE.md).

## Security notes

- `AI_TOKEN` is a master key for shell access to the device. Treat it like an
  SSH private key.
- The Android device only trusts the relay it dials; it never accepts
  inbound connections.
- No root is required or used — shell access is uid 2000, same ceiling as
  `adb shell`.
- Scope is the **owner's own device** for personal automation, same as
  before — see `plan.md`'s explicit "multi-tenant 아님" non-goal.
