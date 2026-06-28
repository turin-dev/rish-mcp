// Pull a binary file off the phone in base64 chunks over run_shell (no scp needed).
// Usage: MCP_URL=.. node test/fetch-file.mjs <AI_TOKEN> <remotePath> <localOut>
import { writeFileSync } from "node:fs";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StreamableHTTPClientTransport } from "@modelcontextprotocol/sdk/client/streamableHttp.js";

const [AI_TOKEN, remote, out] = process.argv.slice(2);
const MCP_URL = process.env.MCP_URL ?? "https://mcp.example.com/mcp";
const CHUNK = 150000;

const client = new Client({ name: "fetch", version: "1.0.0" });
await client.connect(new StreamableHTTPClientTransport(new URL(MCP_URL), {
  requestInit: { headers: { Authorization: `Bearer ${AI_TOKEN}` } },
}));

const sh = async (cmd) => {
  const r = await client.callTool({ name: "run_shell", arguments: { cmd, timeoutMs: 30000 } });
  return r.content[0].text;
};
const grab = (text, tag) => (text.match(new RegExp(`${tag}<([A-Za-z0-9+/=]*)>`)) ?? [])[1] ?? "";

const sizeText = await sh(`printf 'SZ<'; wc -c < '${remote}' | tr -d ' \\n'; printf '>'`);
const size = parseInt(grab(sizeText, "SZ") || "0", 10);
if (!size) { console.error("could not stat", remote, "\n", sizeText); process.exit(1); }
console.error(`size=${size} bytes, ${Math.ceil(size / CHUNK)} chunks`);

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
let b64 = "";
const nChunks = Math.ceil(size / CHUNK);
for (let i = 0; i < nChunks; i++) {
  const expect = Math.min(CHUNK, size - i * CHUNK);
  let part = "";
  for (let attempt = 0; attempt < 6 && part.length === 0; attempt++) {
    if (attempt) await sleep(1500); // wait out a watchdog reconnect
    try {
      const t = await sh(`printf 'B<'; dd if='${remote}' bs=${CHUNK} skip=${i} count=1 2>/dev/null | base64 -w0; printf '>'`);
      part = grab(t, "B");
    } catch { part = ""; }
  }
  if (part.length === 0) { console.error(`\nchunk ${i + 1}/${nChunks} failed`); process.exit(1); }
  process.stderr.write(`  chunk ${i + 1}/${nChunks} (+${part.length}, ~${expect * 4 / 3 | 0})   \r`);
  b64 += part;
}
writeFileSync(out, Buffer.from(b64, "base64"));
console.error(`\nwrote ${out}`);
await client.close();
