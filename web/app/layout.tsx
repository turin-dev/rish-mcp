import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "rish-mcp — put your phone in the loop",
  description:
    "Give your AI a direct, inspectable path to the Android device you actually use.",
};

export default function RootLayout({ children }: LayoutProps<"/">) {
  return (
    <html lang="en">
      <body suppressHydrationWarning>{children}</body>
    </html>
  );
}
