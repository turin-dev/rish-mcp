# rish-mcp-setup

Interactive installer for the [rish-mcp](https://github.com/turin-dev/rish-mcp) Android agent — the thing that lets an AI run shell commands on a real Android device over MCP.

```
npx rish-mcp-setup
```

No install, no Go toolchain, no Android SDK required just to run it.

## What it does

An arrow-key menu with four options:

1. **Full device setup** — makes sure `adb` is available (downloads platform-tools if it isn't), waits for a device to show up on `adb devices`, bridges pre-Android-11 devices over `adb tcpip`, gets an APK onto the device (downloaded from a configured version server, or built locally via Docker), installs it, and pre-fills the Relay URL / device token via the app's own intent-extras provisioning — nothing gets baked into the build.
2. **Just build/download the APK** — the APK-acquisition step on its own.
3. **Start a relay server** — runs `server/cmd/relay` from a local checkout if you have one (via `go run` or a local Docker build), or falls back to pulling the prebuilt `ghcr.io/turin-dev/rish-mcp-relay` image if you don't.
4. **Exit**

One thing it deliberately does **not** do: drive the Android 11+ wireless-pairing handshake itself. That happens on-device, inside the app — the whole point of rish-mcp not needing Shizuku or a PC in the loop. A PC's `adb` is only load-bearing for installing the APK and for the pre-Android-11 `adb tcpip` bridge; this tool covers exactly those two things, then hands off to the app's own pairing screen.

## Options

```
npx rish-mcp-setup --server https://your-version-server.example.com
```

`--server` (or the `RISH_MCP_SERVER` env var) points at a rish-mcp version server to download a prebuilt APK from. Without it, building locally falls back to Docker.

## Also available as a Go binary

Same tool, same flow, ported 1:1 — see [`server/cmd/setup`](https://github.com/turin-dev/rish-mcp/tree/revive/server/cmd/setup) if you'd rather have a single static binary than a Node dependency.

## License

MIT
