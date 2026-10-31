import { NextResponse } from "next/server";
import type { NextRequest } from "next/server";

// `curl -fsSL https://rish-mcp.turin.my | sh` installs the relay: command-line
// HTTP clients get the /relay installer, browsers keep the landing page.
const CLI_CLIENT = /^(curl|wget|fetch|httpie|xh)\//i;

export function proxy(request: NextRequest) {
  const userAgent = request.headers.get("user-agent") ?? "";
  const response = CLI_CLIENT.test(userAgent)
    ? NextResponse.rewrite(new URL("/relay", request.url))
    : NextResponse.next();
  // Same URL, two bodies: keep shared caches from mixing them up.
  response.headers.set("Vary", "User-Agent");
  return response;
}

export const config = {
  matcher: "/",
};
