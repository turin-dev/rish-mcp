#!/usr/bin/env node
// rish-mcp-setup -- Node/npx port of server/cmd/setup (Go). Same flow,
// same reasoning for what it does and doesn't automate: see the comment
// at the top of server/cmd/setup/main.go. This exists so `npx rish-mcp-setup`
// works without anyone needing Go installed.
//
// Same "no TUI framework" choice as the Go version, for the same reason:
// this has to behave correctly when piped/tested non-interactively, which
// is how it's actually verified here (no real terminal in this environment).

import { createInterface } from "node:readline/promises";
import { emitKeypressEvents } from "node:readline";
import { stdin, stdout, exit, platform } from "node:process";
import { spawnSync } from "node:child_process";
import { existsSync, mkdirSync, chmodSync, rmSync, readdirSync, readFileSync } from "node:fs";
import { copyFile as fsCopyFile } from "node:fs/promises";
import { homedir } from "node:os";
import path from "node:path";
import { randomBytes } from "node:crypto";
import { downloadFile } from "./download.js";
import { parseArgs, resolveServerURL } from "./args.js";

// --- styling ---

const RESET = "\x1b[0m", BOLD = "\x1b[1m", DIM = "\x1b[2m";
const ACCENT = "\x1b[38;5;209m", GOOD = "\x1b[32m", BAD = "\x1b[31m";

const useColor = !process.env.NO_COLOR && stdout.isTTY;
const style = (s, code) => (useColor ? code + s + RESET : s);
const heading = (s) => style(s, BOLD + ACCENT);
const dim = (s) => style(s, DIM);
const good = (s) => style("✓ " + s, GOOD);
const bad = (s) => style("✗ " + s, BAD);
const TOTAL_STEPS = 6;
function step(n, title) {
  console.log();
  console.log(style(`[${n}/${TOTAL_STEPS}]`, BOLD + ACCENT) + " " + heading(title));
  console.log(style("─".repeat(4 + String(title).length), DIM));
}
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

// --- non-interactive mode (--yes / -y / RISH_MCP_YES=1) ---
// For an agent bootstrapping a fresh machine: no arrow-key menu, no
// blocking prompts. --action picks the menu entry (default "setup");
// every yes/no or default-value prompt takes its default instead of
// waiting on stdin; free-text prompts (tokens, URLs) fall back to "" ,
// which every call site already treats as "skip this optional bit".
const { cliArgs, help, version, nonInteractive, argValue } = parseArgs(process.argv.slice(2), process.env);
const packageVersion = JSON.parse(readFileSync(new URL("./package.json", import.meta.url), "utf8")).version;

if (help || version) {
  if (version) console.log(packageVersion);
  if (help) {
    console.log(`rish-mcp-setup ${packageVersion}\n\nUsage:\n  npx rish-mcp-setup [options]\n\nOptions:\n  --action <setup|apk|relay>  Run one action in non-interactive mode\n  --server <url>             Download from an explicitly trusted version server\n  --yes, -y                  Accept defaults and exit after one action\n  --help, -h                 Show this help\n  --version, -v              Show the package version\n\nEnvironment:\n  RISH_MCP_SERVER             Version-server URL; unset builds the APK locally\n  RISH_MCP_YES=1              Enable non-interactive mode\n\nRequires Node.js >= 18.`);
  }
  exit(0);
}

// Box width is computed off the raw (unstyled) text so ANSI escape codes
// never leak into the padding math -- row() takes the raw text separately
// from the styled version that actually gets printed.
function banner(title, subtitle) {
  const width = Math.max(title.length, subtitle.length) + 4;
  const top = style("┌" + "─".repeat(width) + "┐", ACCENT);
  const bottom = style("└" + "─".repeat(width) + "┘", ACCENT);
  function row(rawText, styledText) {
    const padding = " ".repeat(width - 2 - rawText.length);
    return style("│  ", ACCENT) + styledText + padding + style("│", ACCENT);
  }
  console.log(top);
  console.log(row(title, style(title, BOLD)));
  console.log(row(subtitle, dim(subtitle)));
  console.log(bottom);
}

// --- prompts ---

function exitOnClosedInput() {
  console.log();
  console.log(bad("input closed"));
  exit(1);
}

const rl = createInterface({ input: stdin, output: stdout });
let stdinClosed = false;
let intentionalClose = false;
// A pending rl.question() never settles on EOF, and with no more open
// handles left Node just exits the process on its own -- abandoning that
// pending promise, so code after `await rl.question()` never runs. Found by
// actually running this with piped input: it exited 0 with no message
// instead of reporting closed input. The 'close' event, unlike the
// question promise, reliably fires -- so the exit has to happen here.
//
// 'close' also fires on our OWN rl.close() at a clean exit (menu -> Exit),
// not just on EOF -- found by testing that path too, which was reporting
// "input closed" on a perfectly normal exit. intentionalClose distinguishes
// the two.
rl.on("close", () => {
  stdinClosed = true;
  // In non-interactive mode nothing ever reads stdin, so a stray EOF on it
  // (e.g. an agent running this with no pty attached at all) shouldn't
  // abort an otherwise-successful run -- found by testing --yes with no
  // stdin: it was killing the process mid-download, nowhere near a prompt.
  if (!intentionalClose && !nonInteractive) exitOnClosedInput();
});

async function prompt(label) {
  if (nonInteractive) {
    // Every downstream helper (promptDefault/promptYesNo) treats "" as
    // "take the default", and every bare prompt() call site already
    // treats "" as "skip this optional value" -- so short-circuiting
    // here alone is enough to make the whole flow non-blocking.
    console.log(label + " " + dim("(non-interactive, skipped)"));
    return "";
  }
  if (stdinClosed) exitOnClosedInput();
  const answer = await rl.question(label + " ");
  return answer.trim();
}

async function promptDefault(label, def) {
  const v = await prompt(`${label} ${dim("[" + def + "]")}`);
  return v === "" ? def : v;
}

async function promptYesNo(label, def) {
  const suffix = def ? "[Y/n]" : "[y/N]";
  const v = (await prompt(`${label} ${dim(suffix)}`)).toLowerCase();
  if (v === "") return def;
  return v === "y" || v === "yes";
}

// Arrow-key menu. Needs a real TTY in raw mode -- falls back to the old
// numbered prompt when there isn't one (piped input, e.g. this file's own
// test runs), so both stay testable without a live terminal.
async function selectMenu(title, options) {
  console.log();
  console.log(heading(title));

  if (nonInteractive) {
    const wanted = argValue("--action");
    const picked = wanted ? options.find((o) => o.value === wanted) : options[0];
    if (!picked) throw new Error(`--action ${wanted} isn't one of: ${options.map((o) => o.value).join(", ")}`);
    console.log(dim(`(non-interactive) → ${picked.label}`));
    return picked.value;
  }

  if (!stdin.isTTY) {
    options.forEach((o, i) => console.log(`  ${i + 1}) ${o.label}`));
    const choice = await promptDefault("Choice", "1");
    const picked = options[parseInt(choice, 10) - 1];
    return picked ? picked.value : null;
  }

  if (stdinClosed) exitOnClosedInput();

  return new Promise((resolve) => {
    let selected = 0;
    let firstDraw = true;

    function render() {
      if (!firstDraw) {
        stdout.write(`\x1b[${options.length}A`); // cursor up to the first option line
        stdout.write("\x1b[0J"); // clear from cursor to end of screen
      }
      firstDraw = false;
      options.forEach((o, i) => {
        const line = i === selected ? style(`❯ ${o.label}`, ACCENT) : `  ${o.label}`;
        console.log(line);
      });
    }

    function onKeypress(_str, key) {
      if (!key) return;
      if (key.name === "up") {
        selected = (selected - 1 + options.length) % options.length;
        render();
      } else if (key.name === "down") {
        selected = (selected + 1) % options.length;
        render();
      } else if (key.name === "return") {
        cleanup();
        resolve(options[selected].value);
      } else if (key.ctrl && key.name === "c") {
        cleanup();
        exit(130);
      }
    }

    function cleanup() {
      stdin.setRawMode(false);
      stdin.off("keypress", onKeypress);
    }

    render();
    emitKeypressEvents(stdin);
    stdin.setRawMode(true);
    stdin.on("keypress", onKeypress);
  });
}

// --- adb ---

function run(cmd, args) {
  return spawnSync(cmd, args, { encoding: "utf8" });
}

function which(cmd) {
  const finder = platform === "win32" ? "where" : "which";
  const res = run(finder, [cmd]);
  if (res.status !== 0) return null;
  return res.stdout.split(/\r?\n/)[0].trim() || null;
}

function cacheDir() {
  const dir = path.join(homedir(), ".rish-mcp");
  mkdirSync(dir, { recursive: true });
  return dir;
}

function adbBinaryName() {
  return platform === "win32" ? path.join("platform-tools", "adb.exe") : path.join("platform-tools", "adb");
}

function platformToolsURL() {
  if (platform === "win32") return "https://dl.google.com/android/repository/platform-tools-latest-windows.zip";
  if (platform === "darwin") return "https://dl.google.com/android/repository/platform-tools-latest-darwin.zip";
  if (platform === "linux") return "https://dl.google.com/android/repository/platform-tools-latest-linux.zip";
  throw new Error(`unsupported OS ${platform} -- install adb yourself and re-run`);
}

async function downloadPlatformTools(dir) {
  const url = platformToolsURL();
  console.log(dim("downloading " + url));
  const zipPath = path.join(dir, "platform-tools.zip");
  await downloadFile(url, zipPath);
  try {
    const { default: AdmZip } = await import("adm-zip");
    new AdmZip(zipPath).extractAllTo(dir, true);
  } finally {
    rmSync(zipPath, { force: true });
  }
  const adbPath = path.join(dir, adbBinaryName());
  if (platform !== "win32") chmodSync(adbPath, 0o755);
  if (!existsSync(adbPath)) throw new Error("adb missing from extracted archive");
  return adbPath;
}

async function ensureADB() {
  const found = which("adb");
  if (found) return found;
  const dir = cacheDir();
  const cached = path.join(dir, adbBinaryName());
  if (existsSync(cached)) return cached;
  console.log(dim("adb not found on PATH."));
  if (!(await promptYesNo("Download Android platform-tools now?", true))) {
    throw new Error("adb is required");
  }
  return downloadPlatformTools(dir);
}

function listDevices(adbPath) {
  const res = run(adbPath, ["devices"]);
  if (res.status !== 0) throw new Error("adb devices failed: " + res.stderr);
  return res.stdout
    .split(/\r?\n/)
    .map((l) => l.trim())
    .filter((l) => l && !l.startsWith("List of devices"))
    .map((l) => l.split(/\s+/))
    .filter((f) => f.length === 2 && f[1] === "device")
    .map((f) => f[0]);
}

// --- APK acquisition ---

function findRepoRoot() {
  let dir = process.cwd();
  for (;;) {
    if (existsSync(path.join(dir, "app", "Dockerfile.build"))) return dir;
    const parent = path.dirname(dir);
    if (parent === dir) {
      throw new Error(
        "couldn't find a rish-mcp checkout in this or any parent directory (looking for app/Dockerfile.build).\n" +
          "  git clone https://github.com/turin-dev/rish-mcp.git && cd rish-mcp\n" +
          "then run npx rish-mcp-setup again from there."
      );
    }
    dir = parent;
  }
}

async function ensureGoogleServicesJSON(appDir) {
  const target = path.join(appDir, "app", "google-services.json");
  if (existsSync(target)) {
    console.log(good("google-services.json found -- FCM wake path will be built in"));
    return;
  }
  console.log(dim("No app/app/google-services.json -- building without the FCM low-spec wake path."));
  console.log(dim("(Regular devices work fine without it; this only affects Wear OS-style wake.)"));
  if (!(await promptYesNo("Do you have your own Firebase project's google-services.json to add?", false))) return;
  const src = await prompt("Path to it:");
  if (!src) return;
  await fsCopyFile(src, target);
  console.log(good("copied -- FCM wake path will be built in"));
}

async function buildLocally() {
  if (!which("docker")) {
    throw new Error("docker not found -- install Docker Desktop, or pass --server to download a prebuilt APK instead");
  }
  const repoRoot = findRepoRoot();
  const appDir = path.join(repoRoot, "app");
  await ensureGoogleServicesJSON(appDir);

  console.log(dim("docker build -t rishmcp-android-build -f Dockerfile.build " + appDir));
  const build = spawnSync("docker", ["build", "-t", "rishmcp-android-build", "-f", "Dockerfile.build", "."], {
    cwd: appDir,
    stdio: "inherit",
  });
  if (build.status !== 0) throw new Error("docker build failed");

  console.log(dim("running the build inside the image and copying the APK out"));
  const runRes = spawnSync(
    "docker",
    ["run", "--rm", "-v", `${appDir}:/work`, "rishmcp-android-build", "bash", "-c", "cd /work && gradle assembleDebug --no-daemon"],
    { stdio: "inherit" }
  );
  if (runRes.status !== 0) throw new Error("gradle build failed");

  const outDir = path.join(appDir, "app", "build", "outputs", "apk", "debug");
  if (!existsSync(outDir)) throw new Error(`no build output at ${outDir}`);
  const apk = readdirSync(outDir).find((f) => f.endsWith(".apk"));
  if (!apk) throw new Error(`no .apk found in ${outDir}`);
  return path.join(outDir, apk);
}

async function acquireAPK(serverURL) {
  const dir = cacheDir();
  if (!serverURL) {
    console.log(dim("No --server / RISH_MCP_SERVER set -- building locally instead."));
    return buildLocally();
  }
  console.log("A version server is configured: " + serverURL);
  if (!(await promptYesNo("Download the official prebuilt APK from it?", true))) {
    return buildLocally();
  }
  const apkURL = serverURL.replace(/\/+$/, "") + "/agent.apk";
  const dest = path.join(dir, "rish-mcp-agent.apk");
  console.log(dim("downloading " + apkURL));
  await downloadFile(apkURL, dest);
  return dest;
}

// --- relay ---

function randomToken() {
  return randomBytes(24).toString("hex");
}

const RELAY_IMAGE = "ghcr.io/turin-dev/rish-mcp-relay:latest";

async function runStartRelay() {
  let repoRoot = null;
  try {
    repoRoot = findRepoRoot();
  } catch {
    // No local checkout -- that's fine, we fall back to the prebuilt image
    // below instead of requiring one just to run the relay.
  }

  let aiToken = process.env.AI_TOKEN || "";
  if (!aiToken) {
    if (await promptYesNo("No AI_TOKEN set. Generate a random one?", true)) {
      aiToken = randomToken();
      console.log(dim("AI_TOKEN=" + aiToken));
    } else {
      aiToken = await prompt("AI_TOKEN:");
    }
  }
  let deviceToken = process.env.DEVICE_TOKEN || "";
  if (!deviceToken) {
    if (await promptYesNo("No DEVICE_TOKEN set. Generate a random one?", true)) {
      deviceToken = randomToken();
      console.log(dim("DEVICE_TOKEN=" + deviceToken));
    } else {
      deviceToken = await prompt("DEVICE_TOKEN:");
    }
  }
  if (!aiToken || !deviceToken) throw new Error("AI_TOKEN and DEVICE_TOKEN are both required");
  const port = await promptDefault("Port", "8080");

  const env = { ...process.env, AI_TOKEN: aiToken, DEVICE_TOKEN: deviceToken, PORT: port };

  if (repoRoot) {
    const serverDir = path.join(repoRoot, "server");
    if (which("go")) {
      console.log(dim("go run ./cmd/relay"));
      console.log(good("starting relay on :" + port + " -- Ctrl+C to stop"));
      const res = spawnSync("go", ["run", "./cmd/relay"], { cwd: serverDir, env, stdio: "inherit" });
      if (res.status !== 0) throw new Error("relay exited with an error");
      return;
    }
    if (which("docker")) {
      console.log(dim("docker build --target relay -t rishmcp-relay " + serverDir));
      const build = spawnSync("docker", ["build", "--target", "relay", "-t", "rishmcp-relay", "."], { cwd: serverDir, stdio: "inherit" });
      if (build.status !== 0) throw new Error("docker build failed");
      console.log(good("starting relay on :" + port + " -- Ctrl+C to stop"));
      const res = spawnSync(
        "docker",
        ["run", "--rm", "-p", `${port}:${port}`, "-e", "AI_TOKEN=" + aiToken, "-e", "DEVICE_TOKEN=" + deviceToken, "-e", "PORT=" + port, "rishmcp-relay"],
        { stdio: "inherit" }
      );
      if (res.status !== 0) throw new Error("relay exited with an error");
      return;
    }
  }

  // No local checkout (or no go/docker to build it with) -- fall back to
  // the prebuilt image, which needs neither.
  if (!which("docker")) {
    throw new Error(
      repoRoot
        ? "neither go nor docker found -- install one to run the relay"
        : "no local checkout and docker not found -- install Docker to run the prebuilt relay image, or clone the repo and install Go"
    );
  }
  if (!repoRoot) console.log(dim("No local checkout found -- pulling the prebuilt image instead."));
  console.log(dim("docker pull " + RELAY_IMAGE));
  const pull = spawnSync("docker", ["pull", RELAY_IMAGE], { stdio: "inherit" });
  if (pull.status !== 0) throw new Error("docker pull failed");
  console.log(good("starting relay on :" + port + " -- Ctrl+C to stop"));
  const res = spawnSync(
    "docker",
    ["run", "--rm", "-p", `${port}:${port}`, "-e", "AI_TOKEN=" + aiToken, "-e", "DEVICE_TOKEN=" + deviceToken, "-e", "PORT=" + port, RELAY_IMAGE],
    { stdio: "inherit" }
  );
  if (res.status !== 0) throw new Error("relay exited with an error");
}

// --- device setup (steps 1-6) ---

async function runDeviceSetup(serverURL) {
  let adbPath;
  try {
    adbPath = await ensureADB();
  } catch (e) {
    throw new Error("couldn't get adb: " + e.message);
  }
  console.log(good("adb ready: " + adbPath));

  if (!serverURL && !which("docker")) {
    console.log(bad("no way to get an APK: Docker isn't installed, and no --server / RISH_MCP_SERVER is set."));
    console.log(dim("Either install Docker Desktop (used to build the app without needing the Android SDK locally),"));
    console.log(dim("or pass --server <url> to download a prebuilt APK instead."));
    return;
  }

  step(1, "connect your device");
  console.log("Plug the phone in over USB and enable USB debugging (Settings → Developer");
  console.log("options → USB debugging), or make sure it's already reachable over adb.");
  const maxAttempts = nonInteractive ? 15 : Infinity; // ~30s of polling, not a busy-loop, in agent mode
  let deviceFound = false;
  for (let attempt = 0; attempt < maxAttempts; attempt++) {
    if (nonInteractive) {
      if (attempt > 0) await sleep(2000);
    } else {
      await prompt(dim("press enter once it's connected"));
    }
    let devices;
    try {
      devices = listDevices(adbPath);
    } catch (e) {
      console.log(bad(e.message));
      continue;
    }
    if (devices.length === 0) {
      console.log(bad("no device seen by `adb devices` yet -- check the USB debugging prompt on the phone"));
      continue;
    }
    console.log(good(`found device: ${devices[0]}`));
    deviceFound = true;
    break;
  }
  if (!deviceFound) {
    throw new Error(`no device showed up on \`adb devices\` after ${maxAttempts} attempts (non-interactive mode doesn't wait forever)`);
  }

  step(2, "Android version");
  const is11Plus = await promptYesNo("Is the device on Android 11 or newer?", true);
  let bridgePort = "";
  if (!is11Plus) {
    console.log(dim("Wireless pairing doesn't exist before Android 11, so we bridge over USB instead."));
    bridgePort = await promptDefault("TCP port for the adb bridge", "5555");
    const res = run(adbPath, ["tcpip", bridgePort]);
    if (res.status !== 0) {
      throw new Error("adb tcpip failed: " + (res.stderr || res.stdout).trim());
    }
    console.log(good(`adbd now listening on port ${bridgePort} -- you can unplug the USB cable`));
  }

  step(3, "get the APK");
  const apkPath = await acquireAPK(serverURL);
  console.log(good("APK ready: " + apkPath));

  step(4, "install");
  const install = run(adbPath, ["install", "-r", apkPath]);
  const installOut = (install.stdout || "") + (install.stderr || "");
  if (install.status !== 0 || !installOut.includes("Success")) {
    throw new Error("adb install failed:\n" + installOut);
  }
  console.log(good("installed"));

  step(5, "configure the app");
  console.log(dim("These get sent to the app as launch extras, not baked into the build --"));
  console.log(dim("see docs/USAGE.md §3.3 (headless provisioning)."));
  const relayURL = await prompt("Relay URL (e.g. wss://mcp.example.com/agent):");
  const deviceToken = await prompt("Device token:");
  if (relayURL && deviceToken) {
    const amArgs = [
      "shell", "am", "start", "-n", "kr.scin.rishmcp/.MainActivity",
      "--es", "relay", relayURL,
      "--es", "token", deviceToken,
      "--ez", "autostart", "true",
    ];
    if (bridgePort) amArgs.push("--ei", "adbPort", bridgePort);
    const res = run(adbPath, amArgs);
    if (res.status !== 0) {
      console.log(bad("am start failed: " + (res.stderr || res.stdout).trim()));
    } else {
      console.log(good("app launched with relay + token pre-filled"));
    }
  } else {
    console.log(dim("skipped -- you can fill these in from the app's Configuration card instead"));
  }

  step(6, "pair the app");
  if (is11Plus) {
    console.log("On the phone: Settings → Developer options → Wireless debugging → Pair device");
    console.log('with pairing code. Enter that port + 6-digit code in the app\'s "ADB shell');
    console.log('access" card, tap Pair. Then note the (different) port on the main Wireless');
    console.log('debugging screen, enter it under "Connect port", tap Save port.');
  } else {
    console.log(`The bridge port (${bridgePort}) is already listening and, if you filled in step 5,`);
    console.log("already saved as the app's Connect port.");
  }
  if (!relayURL || !deviceToken) {
    console.log("Fill in Relay URL + Device token in the Configuration card and tap Save & Start.");
  }
  console.log();
  console.log(good("done"));
}

// --- menu ---

async function main() {
  // Fail closed: legacy GitHub releases contain the Shizuku implementation.
  // Build the rewrite locally unless the user explicitly trusts a compatible
  // version server via --server or RISH_MCP_SERVER.
  const serverURL = resolveServerURL(cliArgs, process.env);

  banner("rish-mcp setup", "adb + pairing + build + relay, all from one place.");

  const menuOptions = [
    { label: "📱  Full device setup — adb, pairing, install the app", value: "setup" },
    { label: "📦  Just build/download the APK", value: "apk" },
    { label: "🛰  Start a relay server", value: "relay" },
    { label: "🚪  Exit", value: "exit" },
  ];

  for (;;) {
    let choice;
    try {
      choice = await selectMenu("What do you want to do?", menuOptions);
      if (choice === "setup") await runDeviceSetup(serverURL);
      else if (choice === "apk") {
        const apkPath = await acquireAPK(serverURL);
        console.log(good("APK ready: " + apkPath));
      } else if (choice === "relay") await runStartRelay();
      else if (choice === "exit" || choice === null) break;
    } catch (e) {
      console.log(bad(e.message));
      if (nonInteractive) {
        intentionalClose = true;
        rl.close();
        exit(1);
      }
    }

    // Non-interactive mode does exactly the one --action and stops --
    // there's nobody to ask "back to the menu?", and that prompt
    // short-circuiting to its default would otherwise loop forever.
    if (nonInteractive) break;
    if (choice !== "exit" && !(await promptYesNo("Back to the menu?", true))) break;
  }
  intentionalClose = true;
  rl.close();
}

main().catch((error) => {
  console.error(bad(error instanceof Error ? error.message : String(error)));
  intentionalClose = true;
  rl.close();
  exit(1);
});
