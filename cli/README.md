# rish-mcp-setup

Interactive installer for the [rish-mcp](https://github.com/turin-dev/rish-mcp) Android agent — the thing that lets an AI run shell commands on a real Android device over MCP.

```bash
git clone https://github.com/turin-dev/rish-mcp.git
cd rish-mcp
npx rish-mcp-setup --server=
```

The empty `--server=` value forces a local APK build. This is currently
required because GitHub releases through `v0.5.0` contain the legacy
legacy application, not the Go rewrite's isolated `agent-v` channel. No global install, Go
toolchain, or host Android SDK is required, but local APK builds require Docker.

## What it does

An arrow-key menu with four options:

1. **Full device setup** — makes sure `adb` is available (downloads platform-tools if it isn't), waits for a device to show up on `adb devices`, bridges pre-Android-11 devices over `adb tcpip`, gets an APK onto the device (downloaded from a configured version server, or built locally via Docker), installs it, and pre-fills the Relay URL / device token via the app's own intent-extras provisioning — nothing gets baked into the build.
2. **Just build/download the APK** — the APK-acquisition step on its own.
3. **Start a relay server** — runs `server/cmd/relay` from a local checkout if you have one (via `go run` or a local Docker build), or falls back to pulling the prebuilt `ghcr.io/turin-dev/rish-mcp-relay` image if you don't.
4. **Exit**

One thing it deliberately does **not** do: drive the Android 11+ wireless-pairing handshake itself. That happens on-device inside the app. The app now prefers an explicitly authorized, ADB-mode Shizuku service and keeps on-device ADB as its fallback. A PC's `adb` is only load-bearing for installing the APK and for the pre-Android-11 `adb tcpip` bridge; this tool covers those steps, then hands off to the app's Shell access screen.

## Options

```bash
npx rish-mcp-setup --server https://your-version-server.example.com
```

`--server` (or the `RISH_MCP_SERVER` env var) points at an explicitly trusted,
rewrite-compatible rish-mcp version server. Without it, the safe default is to
build locally with Docker. The legacy public APK must not be used with the
current rewrite; see [`../docs/RELEASES.md`](../docs/RELEASES.md).

### Non-interactive setup

For scripts or a fresh machine where no one can answer prompts, pass `--yes`
(or `-y`) and choose the menu action with `--action`:

```bash
# Full device setup; accepts defaults and uses RISH_MCP_* environment values.
npx rish-mcp-setup --yes --action setup --server=

# Only acquire an APK.
npx rish-mcp-setup --yes --action apk --server=

# Start the relay from a checkout, or use the published fallback image.
npx rish-mcp-setup --yes --action relay
```

The equivalent environment switch is `RISH_MCP_YES=1`. Use `--help` to see
all flags, or `--version` to print the installed package version:

```bash
npx rish-mcp-setup --help
npx rish-mcp-setup --version
```

The CLI requires Node.js 18 or newer. Help and version output do not load the
ZIP extraction dependency or contact the network. Downloads have a two-minute
timeout and replace cached files only after the new file completes. Non-interactive
mode still needs the real prerequisites for the selected action: Docker or a local
APK/build for APK acquisition, and `adb` plus an authorized device for full setup.

## Also available as a Go binary

Same tool, same flow, ported 1:1 — see [`server/cmd/setup`](../server/cmd/setup)
if you'd rather have a single static binary than a Node dependency.

## License

MIT
