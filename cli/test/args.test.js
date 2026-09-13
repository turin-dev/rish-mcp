import test from "node:test";
import assert from "node:assert/strict";
import { parseArgs } from "../args.js";

test("parseArgs recognizes help and version flags", () => {
  assert.equal(parseArgs(["--help"], {}).help, true);
  assert.equal(parseArgs(["-h"], {}).help, true);
  assert.equal(parseArgs(["--version"], {}).version, true);
  assert.equal(parseArgs(["-v"], {}).version, true);
  assert.equal(parseArgs([], {}).help, false);
  assert.equal(parseArgs([], {}).version, false);
});

test("parseArgs recognizes non-interactive flags", () => {
  assert.equal(parseArgs(["--yes"], {}).nonInteractive, true);
  assert.equal(parseArgs(["-y"], {}).nonInteractive, true);
  assert.equal(parseArgs([], { RISH_MCP_YES: "1" }).nonInteractive, true);
  assert.equal(parseArgs([], {}).nonInteractive, false);
});

test("parseArgs supports equals and separate values", () => {
  const equals = parseArgs(["--action=server", "--port=8080"], {});
  assert.equal(equals.argValue("--action"), "server");
  assert.equal(equals.argValue("--port"), "8080");

  const separate = parseArgs(["--action", "client", "--url", "https://mcp.example.test/mcp"], {});
  assert.equal(separate.argValue("--action"), "client");
  assert.equal(separate.argValue("--url"), "https://mcp.example.test/mcp");
});

test("parseArgs does not consume another flag as a value", () => {
  const parsed = parseArgs(["--action", "--yes"], {});
  assert.equal(parsed.argValue("--action"), undefined);
  assert.equal(parsed.nonInteractive, true);
});
