<div align="center">

# rish-mcp

**Give AI assistants secure, self-hosted MCP access to your Android device's `adb shell` — without root, Shizuku, or a permanently attached PC.**

[![CI](https://github.com/turin-dev/rish-mcp/actions/workflows/ci.yml/badge.svg?branch=master)](https://github.com/turin-dev/rish-mcp/actions/workflows/ci.yml)
[![CodeQL](https://github.com/turin-dev/rish-mcp/actions/workflows/codeql.yml/badge.svg?branch=master)](https://github.com/turin-dev/rish-mcp/actions/workflows/codeql.yml)
[![npm](https://img.shields.io/npm/v/rish-mcp-setup?logo=npm)](https://www.npmjs.com/package/rish-mcp-setup)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**English** · [한국어](README.ko.md)

</div>

> [!WARNING]
> **The rewrite is still in preview.** The Go relay and Android agent are usable for controlled testing, but the current Android rewrite has not completed the real-device stable-release gates. Read [Release channels](docs/RELEASES.md) before treating it as production-ready.

> [!CAUTION]
> GitHub releases `v0.2.0` through `v0.5.0` belong to the legacy Shizuku-based implementation. The rewrite uses the separate `agent-v*` release channel. The current signed preview is `agent-v0.1.0`.

## What is rish-mcp?

rish-mcp exposes an Android device's own shell (`uid 2000`, equivalent to `adb shell`) as MCP tools for AI clients.

The Android agent opens an **outbound-only WebSocket** to a relay you control. AI clients connect to that relay through **MCP over Streamable HTTP** and authenticate with a bearer token. The device does not need to accept inbound Internet connections, so the setup works behind NAT/CGNAT.

```text
                               outbound WebSocket
┌──────────────┐   MCP/HTTPS   ┌──────────────────────┐ ◀────────────────── ┌──────────────┐
│  AI client   │ ────────────▶ │   Go relay + MCP     │                     │ Android agent│
│ Claude, etc. │ ◀──────────── │  server/cmd/relay    │ ── shell command ─▶ │  adb shell   │
└──────────────┘                └──────────────────────┘ ◀── result/output ── └──────────────┘
                                         │
                                         │ version metadata / APK
                                         ▼
                               ┌──────────────────────┐
                               │ Public version server│
                               │ server/cmd/publicserver
                               └──────────────────────┘
```

The previous Node/TypeScript + Shizuku implementation is kept under [`before/`](before/) for reference. See [`plan.md`](plan.md) for the rewrite rationale and [`docs/DESIGN.md`](docs/DESIGN.md) for the architecture.

## Highlights

- **No root and no Shizuku** — the Android app talks directly to the device's own `adbd`.
- **MCP-native** — exposes `list_devices` and `run_shell` through a remote Streamable HTTP MCP endpoint.
- **Outbound-only agent connection** — no inbound port needs to be opened on the phone.
- **Self-hosted relay** — keep shell-access credentials and command traffic on infrastructure you control.
- **Go relay** — compact deployment, straightforward concurrency, and a single server binary per target.
- **Android Kotlin agent** — wireless-debugging pairing on Android 11+, with an `adb tcpip` fallback for older devices.
- **Separate public update service** — release metadata/APK serving stays outside the shell-access trust boundary.

## Project status

| Component | Status |
| --- | --- |
| Go relay (`server/cmd/relay`) — MCP, WebSocket relay, bearer auth + OAuth | ✅ Built and tested |
| Public version server (`server/cmd/publicserver`) | ✅ Built and tested |
| Android `AdbShellClient` — pairing and shell execution | ✅ Built; unit-testable parts tested |
| Android UI/service — `MainActivity`, `AgentService`, `ConnectionManager` | 🧪 Builds successfully; real-device validation still in progress |
| Signed rewrite APK | 🧪 `agent-v0.1.0` preview available |
| Docker packaging / Compose deployment | ✅ Available |
| Low-spec hybrid mode + FCM wake | ⛔ Planned; requires Firebase configuration |

## Quick start

### 1. Set up the relay

Run the installer on a machine with Docker. Re-running it updates the relay.

```bash
curl -fsSL https://rish-mcp.turin.my | sh
```

To read the script before running it, open [`rish-mcp.turin.my/relay`](https://rish-mcp.turin.my/relay). The npm utility does the same job: `npx rish-mcp-setup --yes --action server`.

The installer creates or reuses `AI_TOKEN` and `DEVICE_TOKEN`, stores them under `~/.config/rish-mcp/relay.env`, and runs the `rish-mcp-relay` container.

For reverse-proxy, Compose, and manual deployment options, see [`docs/USAGE.md`](docs/USAGE.md).

### 2. Install and pair the Android agent

Use a signed `agent-v*` artifact from [GitHub Releases](https://github.com/turin-dev/rish-mcp/releases) and follow the release notes.

On **Android 11+**, enable **Developer options → Wireless debugging → Pair device with pairing code**, then enter the pairing information in the rish-mcp app. On older Android versions, use the documented PC-assisted `adb tcpip` flow.

Detailed pairing instructions are in [`docs/USAGE.md`](docs/USAGE.md#3-pair-the-android-agent).

### 3. Connect an MCP client

Create a client configuration automatically:

```bash
npx rish-mcp-setup --yes --action client \
  --url https://mcp.example.com/mcp \
  --token "$AI_TOKEN"
```

Or configure a compatible MCP client manually:

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

For Claude CLI, the equivalent setup is:

```bash
claude mcp add --transport http phone https://mcp.example.com/mcp \
  --header "Authorization: Bearer <AI_TOKEN>"
```

> [!IMPORTANT]
> Put the bearer token in the `Authorization` header. Do not place access tokens in URLs or query strings.

## MCP tools

### `list_devices()`

Lists connected Android devices, including connection metadata such as agent version, connection age, and pending command count.

### `run_shell({ cmd, deviceId?, timeoutMs? })`

Runs a command as Android shell uid (`2000`) and returns stdout, stderr, exit code, and timing information. `deviceId` is only required when more than one device is connected.

```text
run_shell({ "cmd": "getprop ro.product.model" })
```

See [`docs/USAGE.md`](docs/USAGE.md#5-tool-reference) for the full tool contract.

## Repository layout

```text
app/                      Android Kotlin agent
server/cmd/relay/         MCP + WebSocket relay
server/cmd/publicserver/  Public version/APK server
cli/                      rish-mcp-setup npm package
docs/                     Design, usage, and release documentation
before/                   Legacy Shizuku + Node/TypeScript implementation
```

## Build from source

### Go servers

```bash
cd server
go build ./...
go test ./...
```

### Server containers

```bash
docker build --target relay -t rishmcp-relay server
docker build --target publicserver -t rishmcp-public server
```

### Android debug build

From the repository root:

```bash
docker build -t rishmcp-android-build -f app/Dockerfile.build app

docker run --rm -v "$PWD/app:/work" -w /work rishmcp-android-build \
  gradle --no-daemon testDebugUnitTest assembleDebug
```

Debug APK output:

```text
app/app/build/outputs/apk/debug/app-debug.apk
```

Official signed builds are produced by the tag-driven [Android release workflow](.github/workflows/release.yml) using strict `agent-vMAJOR.MINOR.PATCH` tags.

## Deployment

A Compose configuration for Traefik/Dokploy is included:

```bash
cp .env.example .env
# Edit MCP_HOST / PUBLIC_MCP_HOST and replace both secrets.
openssl rand -hex 32

docker network create dokploy-network  # once, if needed
docker compose up -d --build
curl -fsS "https://${MCP_HOST}/healthz"
```

The relay receives shell-access secrets and device traffic. The separate public server receives no relay token and only serves release metadata and the APK.

See [`docs/USAGE.md`](docs/USAGE.md) for environment variables, OAuth, reverse-proxy settings, and troubleshooting.

## Security model

> [!WARNING]
> `AI_TOKEN` effectively grants remote `adb shell` access to connected devices. Treat it like an SSH private key or other high-value credential.

- Commands run as **shell uid 2000**, not root.
- The Android agent initiates the connection to the relay; it does not expose an inbound shell service.
- Use **HTTPS/WSS** in real deployments.
- Keep `AI_TOKEN` and `DEVICE_TOKEN` secret and rotate them if they may have leaked.
- Scope is intentionally **single-user / owner-operated**, not a multi-tenant remote-device platform.

Please report vulnerabilities privately as described in [`SECURITY.md`](SECURITY.md).

## Release channels

| Channel | Meaning |
| --- | --- |
| `agent-v*` | Current Android rewrite artifacts |
| `v0.2.0`–`v0.5.0` | Legacy Shizuku application; incompatible with the rewrite |
| npm `rish-mcp-setup` | Relay/client setup utility only; it does **not** install the Android APK |

Promotion requirements and signing details live in [`docs/RELEASES.md`](docs/RELEASES.md).

## Documentation

| Document | Contents |
| --- | --- |
| [`docs/USAGE.md`](docs/USAGE.md) | Deployment, pairing, MCP tools, OAuth, protocol details, troubleshooting |
| [`docs/DESIGN.md`](docs/DESIGN.md) | Current architecture and implementation boundaries |
| [`docs/RELEASES.md`](docs/RELEASES.md) | Release channels, signing, and promotion gates |
| [`plan.md`](plan.md) | Rewrite rationale and project direction |
| [`cli/README.md`](cli/README.md) | `rish-mcp-setup` CLI reference |
| [`CONTRIBUTING.md`](CONTRIBUTING.md) | Contribution guide |

## Contributing

Issues and pull requests are welcome. Please read [`CONTRIBUTING.md`](CONTRIBUTING.md) before contributing.

## License

rish-mcp is licensed under the [MIT License](LICENSE).
