import test from "node:test";
import assert from "node:assert/strict";
import { parseArgs, resolveServerURL } from "../args.js";

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
  const equals = parseArgs(["--action=apk", "--server=https://example.test"], {});
  assert.equal(equals.argValue("--action"), "apk");
  assert.equal(equals.argValue("--server"), "https://example.test");

  const separate = parseArgs(["--action", "relay", "--server", ""], {});
  assert.equal(separate.argValue("--action"), "relay");
  assert.equal(separate.argValue("--server"), "");
});

test("parseArgs does not consume another flag as a value", () => {
  const parsed = parseArgs(["--action", "--yes"], {});
  assert.equal(parsed.argValue("--action"), undefined);
  assert.equal(parsed.nonInteractive, true);
});

test("resolveServerURL fails closed unless a server is explicitly configured", () => {
  assert.equal(resolveServerURL([], {}), "");
  assert.equal(resolveServerURL([], { RISH_MCP_SERVER: "https://env.example" }), "https://env.example");
  assert.equal(
    resolveServerURL(["--server=https://cli.example"], { RISH_MCP_SERVER: "https://env.example" }),
    "https://cli.example",
  );
  assert.equal(resolveServerURL(["--server="], { RISH_MCP_SERVER: "https://env.example" }), "");
  assert.equal(resolveServerURL(["--server"], { RISH_MCP_SERVER: "https://env.example" }), "");
});
