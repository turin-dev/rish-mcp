// Public release endpoint — a separate process from the relay.
//
// The relay on the private host holds shell access to the owner's devices and
// is gated behind AI_TOKEN/DEVICE_TOKEN. This one holds no secrets and answers
// no questions about devices: it only says which agent build is current and
// hands out that APK. Running it as its own container means the public
// hostname has no route to /mcp or /agent even if a proxy rule is wrong.
//
// The APK comes from the GitHub release CI publishes, cached locally.
import express from "express";
import { ReleaseSource, releaseOptionsFromEnv } from "./release.js";
const PORT = Number(process.env.PORT ?? 8080);
// A 5 MB download on an unauthenticated route; keep one client from looping on it.
const DOWNLOADS_PER_HOUR = Number(process.env.DOWNLOADS_PER_HOUR ?? 30);
const releases = new ReleaseSource(releaseOptionsFromEnv());
const app = express();
app.set("trust proxy", true);
app.disable("x-powered-by");
app.get("/healthz", (_req, res) => {
    const r = releases.get();
    // Deliberately says nothing about connected devices — that is the relay's business.
    res.status(r ? 200 : 503).json({ ok: !!r, release: r?.versionName ?? null });
});
app.get("/api/version/release", (_req, res) => {
    const r = releases.get();
    if (!r) {
        res.status(503).json({ error: "release metadata unavailable" });
        return;
    }
    res.set("Cache-Control", "public, max-age=60");
    res.json({
        versionName: r.versionName,
        versionCode: r.versionCode,
        tag: r.tag,
        sizeBytes: r.sizeBytes,
        sha256: r.sha256,
        modifiedAt: r.modifiedAt,
        download: "/agent.apk",
    });
});
const hits = new Map();
function rateLimited(ip) {
    const now = Date.now();
    const bucket = hits.get(ip);
    if (!bucket || now > bucket.resetAt) {
        hits.set(ip, { count: 1, resetAt: now + 3_600_000 });
        if (hits.size > 10_000)
            for (const [k, v] of hits)
                if (now > v.resetAt)
                    hits.delete(k);
        return false;
    }
    bucket.count += 1;
    return bucket.count > DOWNLOADS_PER_HOUR;
}
app.get("/agent.apk", (req, res) => {
    const r = releases.get();
    if (!r) {
        res.status(503).type("text/plain").send("apk not available");
        return;
    }
    if (rateLimited(req.ip ?? "unknown")) {
        res.status(429).type("text/plain").send("too many downloads; try again later");
        return;
    }
    // Lets a client verify the download and name the file sensibly.
    res.setHeader("X-Apk-Version", r.versionName);
    res.setHeader("X-Apk-Version-Code", String(r.versionCode));
    res.setHeader("X-Apk-Sha256", r.sha256);
    res.setHeader("Content-Disposition", `attachment; filename="rish-mcp-agent-${r.versionName}.apk"`);
    res.setHeader("Content-Type", "application/vnd.android.package-archive");
    res.sendFile(r.path);
});
app.use((_req, res) => res.status(404).type("text/plain").send("not found"));
releases.start();
app.listen(PORT, () => {
    console.log(`rish-mcp public release endpoint on :${PORT}`);
    console.log(`  GET /api/version/release   current agent build`);
    console.log(`  GET /agent.apk             that build, unauthenticated`);
});
