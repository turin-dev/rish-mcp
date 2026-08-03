// Public release endpoint — a separate process from the relay.
//
// The relay on the private host holds shell access to the owner's devices and
// is gated behind AI_TOKEN/DEVICE_TOKEN. This one holds no secrets and answers
// no questions about devices: it only says which agent build is current and
// hands out that APK. Running it as its own container means the public
// hostname has no route to /mcp or /agent even if a proxy rule is wrong.
import express, { type Request, type Response } from "express";
import { existsSync } from "node:fs";
import { resolve } from "node:path";
import { readApkInfo } from "./apkinfo.js";

const PORT = Number(process.env.PORT ?? 8080);
// Resolved: res.sendFile() rejects relative paths.
const APK_PATH = resolve(process.env.APK_PATH ?? "/srv/agent.apk");
// A 5 MB download on an unauthenticated route; keep one client from looping on it.
const DOWNLOADS_PER_HOUR = Number(process.env.DOWNLOADS_PER_HOUR ?? 30);

const app = express();
app.set("trust proxy", true);
app.disable("x-powered-by");

function release() {
  try {
    const apk = readApkInfo(APK_PATH);
    return {
      versionName: apk.versionName,
      versionCode: apk.versionCode,
      sizeBytes: apk.sizeBytes,
      sha256: apk.sha256,
      modifiedAt: apk.modifiedAt,
      download: "/agent.apk",
    };
  } catch (e) {
    console.warn(`[public] cannot read ${APK_PATH}: ${e instanceof Error ? e.message : String(e)}`);
    return null;
  }
}

app.get("/healthz", (_req, res) => {
  const r = release();
  // Deliberately says nothing about connected devices — that is the relay's business.
  res.status(r ? 200 : 503).json({ ok: !!r, release: r?.versionName ?? null });
});

app.get("/api/version/release", (_req, res) => {
  const r = release();
  if (!r) {
    res.status(503).json({ error: "release metadata unavailable" });
    return;
  }
  res.set("Cache-Control", "public, max-age=60");
  res.json(r);
});

const hits = new Map<string, { count: number; resetAt: number }>();
function rateLimited(ip: string): boolean {
  const now = Date.now();
  const bucket = hits.get(ip);
  if (!bucket || now > bucket.resetAt) {
    hits.set(ip, { count: 1, resetAt: now + 3_600_000 });
    if (hits.size > 10_000) for (const [k, v] of hits) if (now > v.resetAt) hits.delete(k);
    return false;
  }
  bucket.count += 1;
  return bucket.count > DOWNLOADS_PER_HOUR;
}

app.get("/agent.apk", (req: Request, res: Response) => {
  if (!existsSync(APK_PATH)) {
    res.status(503).type("text/plain").send("apk not available");
    return;
  }
  if (rateLimited(req.ip ?? "unknown")) {
    res.status(429).type("text/plain").send("too many downloads; try again later");
    return;
  }
  const r = release();
  if (r) {
    // Lets a client verify the download and name the file sensibly.
    res.setHeader("X-Apk-Version", r.versionName);
    res.setHeader("X-Apk-Version-Code", String(r.versionCode));
    res.setHeader("X-Apk-Sha256", r.sha256);
    res.setHeader("Content-Disposition", `attachment; filename="rish-mcp-agent-${r.versionName}.apk"`);
  }
  res.setHeader("Content-Type", "application/vnd.android.package-archive");
  res.sendFile(APK_PATH);
});

app.use((_req, res) => res.status(404).type("text/plain").send("not found"));

app.listen(PORT, () => {
  console.log(`rish-mcp public release endpoint on :${PORT}`);
  console.log(`  GET /api/version/release   current agent build (from ${APK_PATH})`);
  console.log(`  GET /agent.apk             that build, unauthenticated`);
});
