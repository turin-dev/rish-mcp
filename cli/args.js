// Pure command-line parsing shared by the setup CLI and its tests.
export function parseArgs(argv = [], env = {}) {
  const cliArgs = [...argv];
  const help = cliArgs.includes("--help") || cliArgs.includes("-h");
  const version = cliArgs.includes("--version") || cliArgs.includes("-v");
  const nonInteractive =
    cliArgs.includes("--yes") ||
    cliArgs.includes("-y") ||
    env.RISH_MCP_YES === "1";

  function argValue(flag) {
    const eqPrefix = flag + "=";
    const eqForm = cliArgs.find((arg) => arg.startsWith(eqPrefix));
    if (eqForm) return eqForm.slice(eqPrefix.length);

    const index = cliArgs.indexOf(flag);
    if (index === -1 || index + 1 >= cliArgs.length) return undefined;
    const value = cliArgs[index + 1];
    return value.startsWith("-") ? undefined : value;
  }

  return { cliArgs, help, version, nonInteractive, argValue };
}
