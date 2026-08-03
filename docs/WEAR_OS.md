# Wear OS support

`rish-mcp` 0.4 uses the **same APK** on normal Android devices and Wear OS watches.
There is no separate watch build or package name.

## What changes automatically on a watch

The agent detects `android.hardware.type.watch` at runtime and switches to a watch-friendly profile:

- watches use `res/layout-watch/activity_main.xml` with larger safe margins and stacked controls (the `watch` UI-mode qualifier, so square watches get it too);
- new device IDs use `watch-xxxxxxxx` instead of the handheld `android-xxxxxxxx` prefix;
- the relay receives `kind=watch`, and `list_devices()` exposes that form factor;
- the WebSocket ping interval is relaxed from 20 to 60 seconds on both ends (the relay pings watch connections every 60 seconds instead of every 25);
- the local Shizuku/reconnect heartbeat is relaxed from 30 seconds to 90 seconds;
- Bluetooth/companion-network connectivity is shown as `phone/bluetooth` when Android exposes that transport.

Pings are stretched, never switched off. The client's ping timeout is what surfaces a
half-open socket, and the relay drops any connection that misses ~2.5 ping cycles — a
watch that loses its network is reconnected rather than silently sitting in the
registry as a device that no longer answers.

Existing installs keep their already-generated device ID when upgraded.

## Requirements

- Wear OS capable of installing the APK (Wear OS 3+ is the practical target; the app's Android `minSdk` remains 26).
- Shizuku installed and **running** on the watch.
- Shizuku permission granted to `kr.scin.rishmcp`.
- Internet connectivity from the watch. Wi-Fi/LTE is not required if the watch can reach the internet through its paired phone.
- A deployed rish-mcp relay reachable over HTTPS/WSS.

The MCP agent only receives the same `uid 2000` shell privileges that Shizuku provides. It does not add root privileges.

## Install

Build the normal APK:

```bash
cd app
./build-apk.sh
```

Install it using whichever ADB path you have to the watch:

```bash
adb install -r -g rish-mcp-agent.apk
```

You can then open the watch UI, grant Shizuku access, enter the relay URL/token and tap **Save & Start**.

If you already have shell access to the watch, headless provisioning is identical to a phone:

```bash
TOKEN=<DEVICE_TOKEN>

adb shell am start -n kr.scin.rishmcp/.MainActivity \
  --es relay wss://mcp.example.com/agent \
  --es token "$TOKEN" \
  --ez autostart true
```

The same command works through `rish -c` when `rish` is available on the controlling Android device.

## Verify

From an MCP client:

```text
list_devices()
```

A watch should appear similar to:

```json
[
  {
    "id": "watch-a1b2c3d4",
    "name": "SM-Lxxx",
    "kind": "watch",
    "sdk": "36",
    "connectedForMs": 12345,
    "pending": 0
  }
]
```

Then test shell execution:

```text
run_shell({"cmd":"id && getprop ro.product.model"})
```

The `id` output should show shell uid 2000.

## Notes about background operation

The agent still runs as a foreground service and can restart after `BOOT_COMPLETED` when it was previously enabled. Wear OS may apply more aggressive battery management than a phone, so real-device testing is still important.

The watch profile intentionally reduces keepalive frequency, but an always-connected WebSocket will still consume more power than an on-demand design. If battery life is more important than instant remote commands, a future mode could connect only on demand or on a periodic window.

## Troubleshooting

### `Shizuku: not running`

The APK is installed correctly, but Shizuku itself is not active on the watch. Start Shizuku first; the agent automatically rebinds when the binder appears.

### `Shizuku: permission needed`

Open the app and tap **Grant Shizuku**, or grant the permission through a shell-based provisioning flow.

### `Network: none`

Confirm the watch itself can access the internet. A paired phone can provide the path, but pairing alone does not guarantee that every network state exposes a usable default network.

### Relay shows the watch as `android`

The server is older than the 0.4 agent changes. Update/redeploy the relay; old servers still accept the connection but ignore the new `kind=watch` query parameter.
