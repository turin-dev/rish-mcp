import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "rish-mcp — give your AI a phone",
  description:
    "Give your AI a real Android device to work with — MCP shell access without VPN, emulators, or Shizuku.",
};

export default function RootLayout({ children }: LayoutProps<"/">) {
  return (
    <html lang="en">
      <body suppressHydrationWarning>{children}</body>
    </html>
  );
}
