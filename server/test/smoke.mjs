// End-to-end smoke test: fake phone agent + MCP client through the real server.
import { spawn, execSync } from "node:child_process";
import { WebSocket } from "ws";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StreamableHTTPClientTransport } from "@modelcontextprotocol/sdk/client/streamableHttp.js";

const PORT = 8099;
const AI_TOKEN = "ai-test-token";
const DEVICE_TOKEN = "device-test-token";
const base = `http://127.0.0.1:${PORT}`;

const env = { ...process.env, PORT: String(PORT), AI_TOKEN, DEVICE_TOKEN };
const srv = spawn("node", ["dist/index.js"], { env, stdio: "inherit" });

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
let failed = false;
const assert = (c, m) => { if (!c) { console.error("ASSERT FAIL:", m); failed = true; } else console.log("ok -", m); };

try {
  await sleep(800);

  // Fake phone: connect to relay, actually run commands in the local shell as a stand-in for rish.
  const agent = new WebSocket(`ws://127.0.0.1:${PORT}/agent?token=${DEVICE_TOKEN}&deviceId=test-phone&name=FakeS23&sdk=36`);
  await new Promise((res, rej) => { agent.once("open", res); agent.once("error", rej); });
  agent.on("message", (raw) => {
    const m = JSON.parse(raw.toString());
    if (m.type !== "exec") return;
    const t0 = Date.now();
    let code = 0, stdout = "", stderr = "";
    try { stdout = execSync(m.cmd, { timeout: m.timeoutMs, encoding: "utf8" }); }
    catch (e) { code = e.status ?? 1; stdout = e.stdout?.toString() ?? ""; stderr = e.stderr?.toString() ?? String(e.message); }
    agent.send(JSON.stringify({ type: "result", reqId: m.reqId, code, stdout, stderr, durationMs: Date.now() - t0 }));
  });
  await sleep(200);

  // MCP client (the "AI") connects with bearer auth.
  const transport = new StreamableHTTPClientTransport(new URL(`${base}/mcp`), {
    requestInit: { headers: { Authorization: `Bearer ${AI_TOKEN}` } },
  });
  const client = new Client({ name: "smoke", version: "1.0.0" });
  await client.connect(transport);

  const tools = await client.listTools();
  assert(tools.tools.some((t) => t.name === "run_shell"), "run_shell tool advertised");
  assert(tools.tools.some((t) => t.name === "list_devices"), "list_devices tool advertised");

  const dev = await client.callTool({ name: "list_devices", arguments: {} });
  assert(dev.content[0].text.includes("test-phone"), "list_devices shows the fake phone");

  const ok = await client.callTool({ name: "run_shell", arguments: { cmd: "echo hello-from-phone" } });
  assert(ok.content[0].text.includes("hello-from-phone"), "run_shell returns stdout");
  assert(ok.content[0].text.includes("exit=0"), "run_shell reports exit=0");

  const bad = await client.callTool({ name: "run_shell", arguments: { cmd: "sh -c 'exit 7'" } });
  assert(bad.isError === true, "non-zero exit flagged isError");

  await client.close();

  // Unauthorized AI is rejected.
  const r = await fetch(`${base}/mcp`, {
    method: "POST",
    headers: { "content-type": "application/json", accept: "application/json, text/event-stream" },
    body: JSON.stringify({ jsonrpc: "2.0", id: 1, method: "initialize", params: {} }),
  });
  assert(r.status === 401, "missing bearer rejected with 401");

  agent.close();
} catch (e) {
  console.error("THREW:", e);
  failed = true;
} finally {
  srv.kill("SIGTERM");
  await sleep(200);
  console.log(failed ? "\nSMOKE: FAIL" : "\nSMOKE: PASS");
  process.exit(failed ? 1 : 0);
}
