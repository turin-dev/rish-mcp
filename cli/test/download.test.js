import test from "node:test";
import assert from "node:assert/strict";
import { existsSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { downloadFile } from "../download.js";

function tempPath() {
  return mkdtempSync(path.join(tmpdir(), "rish-mcp-download-"));
}

async function startServer(handler) {
  const { createServer } = await import("node:http");
  const server = createServer(handler);
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });
  return server;
}

test("downloadFile replaces the destination only after a complete response", async () => {
  const dir = tempPath();
  const dest = path.join(dir, "agent.apk");
  writeFileSync(dest, "old-cache");
  const server = await startServer((_req, res) => {
    res.writeHead(200, { "content-type": "application/octet-stream" });
    res.end("new-cache");
  });
  try {
    await downloadFile(`http://127.0.0.1:${server.address().port}/agent.apk`, dest, { timeoutMs: 5_000 });
    assert.equal(readFileSync(dest, "utf8"), "new-cache");
  } finally {
    server.close();
  }
});

test("downloadFile removes partial output and preserves an existing cache on failure", async () => {
  const dir = tempPath();
  const dest = path.join(dir, "agent.apk");
  writeFileSync(dest, "old-cache");
  const server = await startServer((_req, res) => {
    res.writeHead(200, { "content-type": "application/octet-stream" });
    res.write("partial");
    setTimeout(() => res.destroy(), 10);
  });
  try {
    await assert.rejects(
      downloadFile(`http://127.0.0.1:${server.address().port}/agent.apk`, dest, { timeoutMs: 5_000 }),
      /download failed/
    );
    assert.equal(readFileSync(dest, "utf8"), "old-cache");
    assert.equal(existsSync(`${dest}.part-${process.pid}`), false);
  } finally {
    server.close();
  }
});
