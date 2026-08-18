# rish-mcp

[![CI](https://github.com/turin-dev/rish-mcp/actions/workflows/ci.yml/badge.svg?branch=master)](https://github.com/turin-dev/rish-mcp/actions/workflows/ci.yml)
[![CodeQL](https://github.com/turin-dev/rish-mcp/actions/workflows/codeql.yml/badge.svg?branch=master)](https://github.com/turin-dev/rish-mcp/actions/workflows/codeql.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Expose an Android device's own shell (uid 2000, like `adb shell`) to AIs as an
**MCP tool** — without VPN, adb-from-a-PC-forever, or sshd. The device holds a
single **outbound** WebSocket to a relay on a public hostname you control; AIs
call the relay's MCP endpoint.

```
┌─────────┐   MCP   ┌──────────────────┐ ◀── outbound WS ── ┌──────────────────┐
│   AI    │──HTTPS─▶│  Go relay + MCP  │                    │   Android app    │
│(Claude) │ ◀───────│     server       │── exec / result ──▶│ Shizuku → ADB fb │
└─────────┘         └──────────────────┘                    └──────────────────┘
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

> [!CAUTION]
> GitHub releases `v0.2.0` through `v0.5.0` contain the legacy Shizuku-based
> application. They are not compatible release artifacts for this rewrite.
> A signed, real-device-verified rewrite APK has not been published yet. Until
> one is available, build the Android app from this checkout; see
> [Release channels](docs/RELEASES.md) for the versioning boundary and gates.
> The current source version is **1.0.0** (Android `versionCode` 10000), but that is
> a source milestone—not an assertion that a signed `agent-v1.0.0` release is
> already available.

## Why rewrite

The old agent required [Shizuku](https://shizuku.rikka.app/) and had no
fallback. The rewrite first moved to an on-device ADB client; 1.0 combines the
two: Shizuku is the preferred owner-authorized backend, while paired ADB keeps
the app usable when Shizuku is absent or stopped. The relay was independently
rewritten in Go for a smaller, testable trust boundary.

## What's different this time

- **Two shell backends.** Shizuku is preferred after an explicit permission
  grant. Paired on-device ADB is the automatic fallback — wireless-debugging
  pairing on Android 11+, or a PC+`adb tcpip` bridge below that.
- **Go relay**, not Node/TS — same MCP tool contracts (`run_shell`,
  `list_devices`), same WS relay protocol, same OAuth model, rewritten for
  concurrency/memory efficiency and a single static binary.
- **One honest connection model.** Every device currently keeps an outbound
  WebSocket. The unused Firebase stub and SDK were removed; FCM wake will only
  return if both the relay sender and a real Firebase project are implemented.

## Status

| Piece | State |
|---|---|
| Go relay (`server/cmd/relay`) — MCP tools, WS relay, static bearer + OAuth | ✅ built, tested |
| Official version server (`server/cmd/publicserver`) | ✅ built, tested |
| Android Shizuku + ADB fallback backends | ✅ built, router/policy tested — **not verified against a real device** |
| `ConnectionManager` / `AgentService` / `MainActivity` | ✅ duplicate-reconnect and command-overload guards; Docker build tested |
| Low-spec push wake | not shipped — no dead Firebase dependency or misleading stub in the APK |
| Docker packaging for the Go binaries | ✅ `server/Dockerfile` (`--target relay` / `--target publicserver`) |
| docker-compose / reverse-proxy deploy config | ✅ `docker-compose.yml` (Traefik/Dokploy) |
| Signed rewrite APK release | ⛔ not published — legacy releases are incompatible |

## Components

- `server/cmd/relay` — Go. Streamable-HTTP **MCP server** (`run_shell`,
  `list_devices`) + **WS relay** the Android device connects to. Static
  bearer or OAuth for AIs, shared token for the device.
- `server/cmd/publicserver` — Go. Separate, secret-free binary: reports the
  current agent version and serves the APK. No route to the relay.
- `app/` — Android (Kotlin). One installable APK: runs commands as shell uid
  through Shizuku when authorized, otherwise through a paired local `adbd`;
  a foreground service holds the outbound WS and auto-starts on boot.

## Quick start: local Android build and setup

The currently published npm CLI still knows about the legacy download server,
so explicitly disable remote APK download and build from this checkout:

```bash
git clone https://github.com/turin-dev/rish-mcp.git
cd rish-mcp
npx rish-mcp-setup --server=
```

This requires Node.js 18+, Docker, and `adb`; it does not install the CLI
globally. The empty `--server=` value is deliberate and forces a local build.
For scripts, add `--yes --action setup|apk|relay`. See
[`cli/README.md`](cli/README.md) for all options and prerequisites.

## Build

```bash
# server (both binaries)
cd server && go build ./... && go test ./...

# server Docker images
docker build --target relay -t rishmcp-relay server
docker build --target publicserver -t rishmcp-public server

# Android unit tests + debug APK (run from the repository root)
docker build -t rishmcp-android-build -f app/Dockerfile.build app
docker run --rm -v "$PWD/app:/work" -w /work rishmcp-android-build \
  gradle --no-daemon lintDebug testDebugUnitTest assembleDebug
# output: app/app/build/outputs/apk/debug/app-debug.apk
```

The rewrite does not yet have a supported release-signing command. Do not
publish the debug APK as an official release; the acceptance and signing gates
are documented in [`docs/RELEASES.md`](docs/RELEASES.md).

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

- `list_devices()` — connected devices, active shell backend, agent version,
  connection age, and pending-command count.
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
- Shizuku access is optional and only becomes active after the device owner
  grants this app permission; otherwise the paired ADB backend is used.
- Root-mode Shizuku is deliberately rejected. The agent binds only when the
  Shizuku server reports uid 2000, preserving the documented shell ceiling.
- Scope is the **owner's own device** for personal automation, same as
  before — see `plan.md`'s explicit "multi-tenant 아님" non-goal.

Please report vulnerabilities privately as described in
[`SECURITY.md`](SECURITY.md). Contributions are welcome under the
[`MIT License`](LICENSE); see [`CONTRIBUTING.md`](CONTRIBUTING.md) before
opening a pull request.
