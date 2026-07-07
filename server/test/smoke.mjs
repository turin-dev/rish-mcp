// End-to-end smoke test: fake phone agent + MCP client through the real server.
import { spawn, execSync } from "node:child_process";
import { createHash } from "node:crypto";
import { WebSocket } from "ws";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StreamableHTTPClientTransport } from "@modelcontextprotocol/sdk/client/streamableHttp.js";

const PORT = 8099;
const AI_TOKEN = "ai-test-token";
const DEVICE_TOKEN = "device-test-token";
const base = `http://127.0.0.1:${PORT}`;

const env = { ...process.env, PORT: String(PORT), AI_TOKEN, DEVICE_TOKEN, PUBLIC_URL: base };
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

  // Unauthorized AI is rejected, and told where the OAuth flow lives.
  const r = await fetch(`${base}/mcp`, {
    method: "POST",
    headers: { "content-type": "application/json", accept: "application/json, text/event-stream" },
    body: JSON.stringify({ jsonrpc: "2.0", id: 1, method: "initialize", params: {} }),
  });
  assert(r.status === 401, "missing bearer rejected with 401");
  assert((r.headers.get("www-authenticate") ?? "").includes("oauth-protected-resource"), "401 advertises resource metadata");

  // --- OAuth flow (what claude.ai custom connectors do) ---
  const meta = await (await fetch(`${base}/.well-known/oauth-authorization-server`)).json();
  assert(meta.registration_endpoint && meta.authorization_endpoint && meta.token_endpoint, "AS metadata served");
  const pr = await (await fetch(`${base}/.well-known/oauth-protected-resource/mcp`)).json();
  assert(pr.authorization_servers?.[0] === base, "protected-resource metadata points at issuer");

  const redirectUri = "https://client.example/cb";
  const reg = await (await fetch(meta.registration_endpoint, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ redirect_uris: [redirectUri] }),
  })).json();
  assert(typeof reg.client_id === "string" && reg.client_id.length > 0, "dynamic client registration");

  const verifier = "smoke-test-verifier-0123456789-0123456789-42";
  const challenge = createHash("sha256").update(verifier).digest("base64url");
  const authQs = new URLSearchParams({
    response_type: "code", client_id: reg.client_id, redirect_uri: redirectUri,
    code_challenge: challenge, code_challenge_method: "S256", state: "xyz",
  });
  const page = await fetch(`${meta.authorization_endpoint}?${authQs}`);
  assert(page.status === 200 && (await page.text()).includes("<form"), "authorize page renders consent form");

  const wrongKey = await fetch(meta.authorization_endpoint, {
    method: "POST", redirect: "manual",
    headers: { "content-type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({ ...Object.fromEntries(authQs), key: "not-the-token" }),
  });
  assert(wrongKey.status === 401, "wrong access key rejected");

  const approved = await fetch(meta.authorization_endpoint, {
    method: "POST", redirect: "manual",
    headers: { "content-type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({ ...Object.fromEntries(authQs), key: AI_TOKEN }),
  });
  const loc = new URL(approved.headers.get("location") ?? "http://x/");
  const code = loc.searchParams.get("code");
  assert(approved.status === 302 && !!code && loc.searchParams.get("state") === "xyz", "consent redirects with code+state");

  const tok = await (await fetch(meta.token_endpoint, {
    method: "POST",
    headers: { "content-type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({
      grant_type: "authorization_code", code, redirect_uri: redirectUri,
      client_id: reg.client_id, code_verifier: verifier,
    }),
  })).json();
  assert(typeof tok.access_token === "string" && typeof tok.refresh_token === "string", "code+PKCE exchanged for tokens");

  const oauthTransport = new StreamableHTTPClientTransport(new URL(`${base}/mcp`), {
    requestInit: { headers: { Authorization: `Bearer ${tok.access_token}` } },
  });
  const oauthClient = new Client({ name: "smoke-oauth", version: "1.0.0" });
  await oauthClient.connect(oauthTransport);
  const viaOauth = await oauthClient.callTool({ name: "run_shell", arguments: { cmd: "echo via-oauth" } });
  assert(viaOauth.content[0].text.includes("via-oauth"), "MCP works with OAuth access token");
  await oauthClient.close();

  const refreshed = await (await fetch(meta.token_endpoint, {
    method: "POST",
    headers: { "content-type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({ grant_type: "refresh_token", refresh_token: tok.refresh_token, client_id: reg.client_id }),
  })).json();
  assert(typeof refreshed.access_token === "string", "refresh_token grant issues new access token");

  const replay = await fetch(meta.token_endpoint, {
    method: "POST",
    headers: { "content-type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({
      grant_type: "authorization_code", code, redirect_uri: redirectUri,
      client_id: reg.client_id, code_verifier: verifier,
    }),
  });
  assert(replay.status === 400, "auth code cannot be replayed");

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
