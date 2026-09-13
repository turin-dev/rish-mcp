#!/usr/bin/env node

import { createInterface } from "node:readline/promises";
import { stdin, stdout, exit, platform } from "node:process";
import { spawnSync } from "node:child_process";
import { chmodSync, existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import path from "node:path";
import { randomBytes } from "node:crypto";
import { parseArgs } from "./args.js";

const RELAY_IMAGE = "ghcr.io/turin-dev/rish-mcp-relay:latest";
const RELAY_CONTAINER = "rish-mcp-relay";
const DEFAULT_PORT = 8080;

const RESET = "\x1b[0m";
const BOLD = "\x1b[1m";
const DIM = "\x1b[2m";
const ACCENT = "\x1b[38;5;209m";
const GOOD = "\x1b[32m";
const BAD = "\x1b[31m";
const useColor = !process.env.NO_COLOR && stdout.isTTY;
const style = (value, code) => (useColor ? code + value + RESET : value);
const heading = (value) => style(value, BOLD + ACCENT);
const dim = (value) => style(value, DIM);
const good = (value) => style("✓ " + value, GOOD);
const bad = (value) => style("✗ " + value, BAD);

const { help, version, nonInteractive, argValue } = parseArgs(process.argv.slice(2), process.env);
const packageVersion = JSON.parse(readFileSync(new URL("./package.json", import.meta.url), "utf8")).version;

function printHelp() {
  console.log(`rish-mcp-setup ${packageVersion}

Install the rish-mcp relay server or create an MCP client configuration.
Android APK download/build/install is intentionally not part of the npm package.

Usage:
  npx rish-mcp-setup
  npx rish-mcp-setup --action server
  npx rish-mcp-setup --action client

Options:
  --action <server|client>    Run one action
  --url <url>                 MCP endpoint for client config
  --token <token>             AI bearer token for client config
  --ai-token <token>          AI token used by the relay server
  --device-token <token>      Android device token used by the relay server
  --port <port>               Host port for the relay (default: 8080)
  --yes, -y                   Accept defaults / run non-interactively
  --help, -h                  Show this help
  --version, -v               Show the package version

Environment:
  RISH_MCP_URL                Default MCP endpoint for client config
  AI_TOKEN                    Relay/client AI bearer token
  DEVICE_TOKEN                Relay Android-device token
  RISH_MCP_RELAY_PORT         Relay host port
  RISH_MCP_YES=1              Enable non-interactive mode

Requires Node.js >= 18. Server install also requires Docker.`);
}

if (version) {
  console.log(packageVersion);
  if (!help) exit(0);
}
if (help) {
  printHelp();
  exit(0);
}

const rl = createInterface({ input: stdin, output: stdout });

async function prompt(label) {
  if (nonInteractive) return "";
  return (await rl.question(label + " ")).trim();
}

async function promptDefault(label, value) {
  const answer = await prompt(`${label} ${dim("[" + value + "]")}`);
  return answer || value;
}

function run(command, args, options = {}) {
  return spawnSync(command, args, { encoding: "utf8", ...options });
}

function which(command) {
  const finder = platform === "win32" ? "where" : "which";
  const result = run(finder, [command]);
  if (result.status !== 0) return null;
  return result.stdout.split(/\r?\n/)[0].trim() || null;
}

function randomToken() {
  return randomBytes(32).toString("hex");
}

function configDir() {
  const dir = path.join(homedir(), ".config", "rish-mcp");
  mkdirSync(dir, { recursive: true });
  return dir;
}

function parsePort(raw) {
  const value = Number(raw);
  if (!Number.isInteger(value) || value < 1 || value > 65535) {
    throw new Error(`invalid port: ${raw}`);
  }
  return value;
}

function readEnvFile(filePath) {
  if (!existsSync(filePath)) return {};
  const result = {};
  for (const line of readFileSync(filePath, "utf8").split(/\r?\n/)) {
    const trimmed = line.trim();
    if (!trimmed || trimmed.startsWith("#")) continue;
    const index = trimmed.indexOf("=");
    if (index === -1) continue;
    result[trimmed.slice(0, index)] = trimmed.slice(index + 1);
  }
  return result;
}

async function chooseAction() {
  const requested = argValue("--action");
  if (requested) {
    if (!['server', 'client'].includes(requested)) {
      throw new Error(`--action must be server or client (got ${requested})`);
    }
    return requested;
  }
  if (nonInteractive) return "server";

  console.log();
  console.log(heading("rish-mcp setup"));
  console.log(dim("npm installs only the relay server or MCP client config; APK handling lives outside npm."));
  console.log();
  console.log("  1) Install/update relay server");
  console.log("  2) Configure MCP client");
  console.log("  3) Exit");
  const answer = await promptDefault("Choice", "1");
  if (answer === "1") return "server";
  if (answer === "2") return "client";
  return "exit";
}

async function installServer() {
  if (!which("docker")) {
    throw new Error("Docker is required to install the relay server");
  }

  const port = parsePort(
    argValue("--port") || process.env.RISH_MCP_RELAY_PORT || String(DEFAULT_PORT),
  );
  const relayEnvPath = path.join(configDir(), "relay.env");
  const previous = readEnvFile(relayEnvPath);

  let aiToken = argValue("--ai-token") || process.env.AI_TOKEN || previous.AI_TOKEN || "";
  let deviceToken = argValue("--device-token") || process.env.DEVICE_TOKEN || previous.DEVICE_TOKEN || "";

  if (!aiToken) aiToken = randomToken();
  if (!deviceToken) deviceToken = randomToken();

  writeFileSync(
    relayEnvPath,
    `AI_TOKEN=${aiToken}\nDEVICE_TOKEN=${deviceToken}\n`,
    { mode: 0o600 },
  );
  if (platform !== "win32") chmodSync(relayEnvPath, 0o600);

  console.log(dim(`Pulling ${RELAY_IMAGE}`));
  const pull = run("docker", ["pull", RELAY_IMAGE], { stdio: "inherit" });
  if (pull.status !== 0) throw new Error("docker pull failed");

  run("docker", ["rm", "-f", RELAY_CONTAINER], { stdio: "ignore" });

  console.log(dim(`Starting ${RELAY_CONTAINER} on port ${port}`));
  const start = run(
    "docker",
    [
      "run",
      "-d",
      "--name",
      RELAY_CONTAINER,
      "--restart",
      "unless-stopped",
      "--env-file",
      relayEnvPath,
      "-p",
      `${port}:8080`,
      RELAY_IMAGE,
    ],
    { stdio: "inherit" },
  );
  if (start.status !== 0) throw new Error("docker run failed");

  console.log();
  console.log(good("relay server installed"));
  console.log(`MCP URL:      http://localhost:${port}/mcp`);
  console.log(`AI_TOKEN:     ${aiToken}`);
  console.log(`DEVICE_TOKEN: ${deviceToken}`);
  console.log(`Secrets:      ${relayEnvPath}`);
  console.log(dim("Keep both tokens secret. The Android app needs DEVICE_TOKEN; MCP clients use AI_TOKEN."));
}

async function configureClient() {
  const relayEnvPath = path.join(configDir(), "relay.env");
  const localRelay = readEnvFile(relayEnvPath);

  const defaultUrl = argValue("--url") || process.env.RISH_MCP_URL || "http://localhost:8080/mcp";
  const url = nonInteractive ? defaultUrl : await promptDefault("MCP URL", defaultUrl);

  let token = argValue("--token") || process.env.AI_TOKEN || localRelay.AI_TOKEN || "";
  if (!token && !nonInteractive) token = await prompt("AI_TOKEN:");
  if (!token) {
    throw new Error("AI token is required; pass --token or set AI_TOKEN");
  }

  const config = {
    mcpServers: {
      phone: {
        type: "http",
        url,
        headers: {
          Authorization: `Bearer ${token}`,
        },
      },
    },
  };

  const clientPath = path.join(configDir(), "client.json");
  writeFileSync(clientPath, JSON.stringify(config, null, 2) + "\n", { mode: 0o600 });
  if (platform !== "win32") chmodSync(clientPath, 0o600);

  console.log();
  console.log(good("MCP client configuration created"));
  console.log(`Saved to: ${clientPath}`);
  console.log();
  console.log(JSON.stringify(config, null, 2));
  console.log();
  console.log(dim("Copy this mcpServers entry into your MCP-compatible AI client's configuration."));
}

try {
  const action = await chooseAction();
  if (action === "server") await installServer();
  if (action === "client") await configureClient();
} catch (error) {
  console.error();
  console.error(bad(error instanceof Error ? error.message : String(error)));
  process.exitCode = 1;
} finally {
  rl.close();
}
