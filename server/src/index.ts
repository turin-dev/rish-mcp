import { createServer } from "node:http";
import { randomUUID } from "node:crypto";
import { existsSync, statSync } from "node:fs";
import express, { type Request, type Response } from "express";
import { WebSocketServer, type WebSocket } from "ws";
import { z } from "zod";
import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { StreamableHTTPServerTransport } from "@modelcontextprotocol/sdk/server/streamableHttp.js";
import { registry, type Device } from "./relay.js";

// ---------------------------------------------------------------------------
// Config (all via env; see .env.example)
// ---------------------------------------------------------------------------
const PORT = Number(process.env.PORT ?? 8080);
const AI_TOKEN = requireEnv("AI_TOKEN"); // bearer the AI/MCP client must present
const DEVICE_TOKEN = requireEnv("DEVICE_TOKEN"); // shared secret the phone presents
const DEFAULT_TIMEOUT_MS = Number(process.env.DEFAULT_TIMEOUT_MS ?? 60_000);
const MAX_TIMEOUT_MS = Number(process.env.MAX_TIMEOUT_MS ?? 600_000);

function requireEnv(name: string): string {
  const v = process.env[name];
  if (!v) {
    console.error(`FATAL: missing required env ${name}`);
    process.exit(1);
  }
  return v;
}

// ---------------------------------------------------------------------------
// MCP server: one fresh instance per request (stateless streamable HTTP)
// ---------------------------------------------------------------------------
function buildMcpServer(): McpServer {
  const server = new McpServer(
    { name: "rish-mcp", version: "0.1.0" },
    { capabilities: { tools: {} } },
  );

  server.registerTool(
    "run_shell",
    {
      title: "Run a shell command on the phone",
      description:
        "Executes a shell command on the connected Android phone with Shizuku " +
        "shell privileges (uid 2000), like an adb shell. Returns stdout, stderr " +
        "and the exit code. Use list_devices first if unsure which phone is online.",
      inputSchema: {
        cmd: z.string().min(1).describe("The shell command line to run, e.g. 'getprop ro.product.model'"),
        deviceId: z
          .string()
          .optional()
          .describe("Target device id; optional when exactly one phone is connected"),
        timeoutMs: z
          .number()
          .int()
          .positive()
          .max(MAX_TIMEOUT_MS)
          .optional()
          .describe(`Per-command timeout in ms (default ${DEFAULT_TIMEOUT_MS}, max ${MAX_TIMEOUT_MS})`),
      },
    },
    async ({ cmd, deviceId, timeoutMs }) => {
      try {
        const r = await registry.exec(deviceId, cmd, timeoutMs ?? DEFAULT_TIMEOUT_MS);
        const body =
          `exit=${r.code} (${r.durationMs}ms)` +
          (r.truncated ? " [output truncated]" : "") +
          `\n--- stdout ---\n${r.stdout}` +
          (r.stderr ? `\n--- stderr ---\n${r.stderr}` : "");
        return { content: [{ type: "text", text: body }], isError: r.code !== 0 };
      } catch (e) {
        return {
          content: [{ type: "text", text: `error: ${e instanceof Error ? e.message : String(e)}` }],
          isError: true,
        };
      }
    },
  );

  server.registerTool(
    "list_devices",
    {
      title: "List connected phones",
      description: "Lists Android phones currently connected to the relay and ready to run commands.",
      inputSchema: {},
    },
    async () => {
      const devices = registry.list().map((d) => ({
        id: d.id,
        name: d.name,
        sdk: d.sdk,
        connectedForMs: Date.now() - d.connectedAt,
        pending: d.pending.size,
      }));
      return { content: [{ type: "text", text: JSON.stringify(devices, null, 2) }] };
    },
  );

  return server;
}

// ---------------------------------------------------------------------------
// HTTP app
// ---------------------------------------------------------------------------
const app = express();
app.use(express.json({ limit: "1mb" }));

app.get("/healthz", (_req, res) => {
  res.json({ ok: true, devices: registry.list().length });
});

// OTA: serve the agent APK so a phone can self-update via curl (no sshd needed).
// Token-gated with the device token; path is mounted read-only into the container.
const APK_PATH = process.env.APK_PATH ?? "/srv/agent.apk";
app.get("/agent.apk", (req: Request, res: Response) => {
  if ((req.query.t ?? "") !== DEVICE_TOKEN) {
    res.status(401).type("text/plain").send("unauthorized");
    return;
  }
  if (!existsSync(APK_PATH)) {
    res.status(404).type("text/plain").send("apk not available");
    return;
  }
  res.setHeader("Content-Type", "application/vnd.android.package-archive");
  res.setHeader("Content-Length", String(statSync(APK_PATH).size));
  res.sendFile(APK_PATH);
});

function checkAiAuth(req: Request, res: Response): boolean {
  const auth = req.header("authorization") ?? "";
  const token = auth.startsWith("Bearer ") ? auth.slice(7) : "";
  if (token !== AI_TOKEN) {
    res.status(401).json({
      jsonrpc: "2.0",
      error: { code: -32001, message: "unauthorized" },
      id: null,
    });
    return false;
  }
  return true;
}

// Stateless MCP endpoint: a new server+transport per POST, no session needed.
app.post("/mcp", async (req: Request, res: Response) => {
  if (!checkAiAuth(req, res)) return;
  const server = buildMcpServer();
  const transport = new StreamableHTTPServerTransport({ sessionIdGenerator: undefined });
  res.on("close", () => {
    transport.close();
    server.close();
  });
  try {
    await server.connect(transport);
    await transport.handleRequest(req, res, req.body);
  } catch (e) {
    console.error("MCP request error:", e);
    if (!res.headersSent) {
      res.status(500).json({
        jsonrpc: "2.0",
        error: { code: -32603, message: "internal error" },
        id: null,
      });
    }
  }
});

// Stateless mode does not support server-initiated SSE / sessions.
const methodNotAllowed = (_req: Request, res: Response) =>
  res.status(405).json({ jsonrpc: "2.0", error: { code: -32000, message: "method not allowed" }, id: null });
app.get("/mcp", methodNotAllowed);
app.delete("/mcp", methodNotAllowed);

// ---------------------------------------------------------------------------
// WS relay: the phone connects here (outbound) and stays connected.
// ---------------------------------------------------------------------------
const httpServer = createServer(app);
const wss = new WebSocketServer({ noServer: true });

httpServer.on("upgrade", (req, socket, head) => {
  const url = new URL(req.url ?? "", "http://localhost");
  if (url.pathname !== "/agent") {
    socket.destroy();
    return;
  }
  const token = url.searchParams.get("token") ?? "";
  if (token !== DEVICE_TOKEN) {
    socket.write("HTTP/1.1 401 Unauthorized\r\n\r\n");
    socket.destroy();
    return;
  }
  wss.handleUpgrade(req, socket, head, (ws) => {
    const deviceId = url.searchParams.get("deviceId") || randomUUID();
    const name = url.searchParams.get("name") || "phone";
    const sdk = url.searchParams.get("sdk") || "?";
    registerAgent(ws, deviceId, name, sdk);
  });
});

function registerAgent(ws: WebSocket, deviceId: string, name: string, sdk: string) {
  const device: Device = {
    id: deviceId,
    name,
    sdk,
    ws,
    connectedAt: Date.now(),
    lastSeen: Date.now(),
    pending: new Map(),
  };
  registry.add(device);
  console.log(`[agent] connected: ${deviceId} (${name}, sdk ${sdk})`);

  // Keepalive: the relay pings; the phone keeps the outbound socket warm.
  const ping = setInterval(() => {
    if (ws.readyState === ws.OPEN) ws.ping();
  }, 25_000);

  ws.on("message", (raw) => {
    device.lastSeen = Date.now();
    let msg: { type?: string; reqId?: string; [k: string]: unknown };
    try {
      msg = JSON.parse(raw.toString());
    } catch {
      return;
    }
    if (msg.type === "result" && typeof msg.reqId === "string") {
      registry.resolveResult(deviceId, msg.reqId, {
        code: typeof msg.code === "number" ? msg.code : -1,
        stdout: typeof msg.stdout === "string" ? msg.stdout : "",
        stderr: typeof msg.stderr === "string" ? msg.stderr : "",
        truncated: msg.truncated === true,
        durationMs: typeof msg.durationMs === "number" ? msg.durationMs : 0,
      });
    }
  });

  ws.on("close", () => {
    clearInterval(ping);
    registry.remove(deviceId, ws);
    console.log(`[agent] disconnected: ${deviceId}`);
  });
  ws.on("error", (e) => console.error(`[agent] ws error ${deviceId}:`, e.message));
}

httpServer.listen(PORT, () => {
  console.log(`rish-mcp server listening on :${PORT}`);
  console.log(`  MCP (AI):   POST /mcp        (Bearer AI_TOKEN)`);
  console.log(`  Relay (phone): WS  /agent?token=DEVICE_TOKEN&deviceId=...`);
});
