import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import test from "node:test";

const execFileAsync = promisify(execFile);
const entrypoint = fileURLToPath(new URL("../index.js", import.meta.url));

test("--version reports the package's 1.0 release identity", async () => {
  const { stdout, stderr } = await execFileAsync(process.execPath, [entrypoint, "--version"]);

  assert.equal(stdout.trim(), "1.0.0");
  assert.equal(stderr, "");
});
