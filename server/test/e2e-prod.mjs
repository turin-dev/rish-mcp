// Exercise the LIVE production chain: AI -> <MCP_URL> -> relay -> phone -> shell.
// Usage: MCP_URL=https://mcp.example.com/mcp node test/e2e-prod.mjs <AI_TOKEN> [cmd]
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StreamableHTTPClientTransport } from "@modelcontextprotocol/sdk/client/streamableHttp.js";

const AI_TOKEN = process.argv[2];
const cmd = process.argv[3] ?? "getprop ro.product.model; id; echo ok";
const MCP_URL = process.env.MCP_URL ?? "https://mcp.example.com/mcp";
if (!AI_TOKEN) { console.error("usage: MCP_URL=... node test/e2e-prod.mjs <AI_TOKEN> [cmd]"); process.exit(2); }

const transport = new StreamableHTTPClientTransport(new URL(MCP_URL), {
  requestInit: { headers: { Authorization: `Bearer ${AI_TOKEN}` } },
});
const client = new Client({ name: "e2e-prod", version: "1.0.0" });
await client.connect(transport);

const devs = await client.callTool({ name: "list_devices", arguments: {} });
console.log("=== list_devices ===\n" + devs.content[0].text);

const r = await client.callTool({ name: "run_shell", arguments: { cmd } });
console.log("=== run_shell ===\n" + r.content[0].text);

await client.close();
