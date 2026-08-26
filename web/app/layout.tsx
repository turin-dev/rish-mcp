import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "rish-mcp — your AI, with a real device",
  description:
    "Connect any MCP client to the Android phone you actually use. Real hardware, shell-level access, and an outbound-only relay.",
};

export default function RootLayout({ children }: LayoutProps<"/">) {
  return (
    <html lang="en">
      <body suppressHydrationWarning>{children}</body>
    </html>
  );
}
