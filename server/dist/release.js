// Where the agent APK comes from.
//
// Previously a bind mount of ./app/rish-mcp-agent.apk — a gitignored file that
// does not exist on a fresh checkout, which Docker then helpfully creates as a
// directory. The APK that matters is the signed one CI publishes to a GitHub
// release, so fetch that instead and keep a local copy.
//
// The cached file is the served artifact: bytes are verified by parsing them as
// an APK before they replace the previous copy, and a restart with no network
// still serves whatever was cached.
import { createHash } from "node:crypto";
import { mkdirSync, readFileSync, renameSync, statSync, writeFileSync, existsSync } from "node:fs";
import { join, resolve } from "node:path";
import { readApkInfo } from "./apkinfo.js";
export function releaseOptionsFromEnv() {
    return {
        repo: process.env.GITHUB_REPO ?? "turin-dev/rish-mcp",
        cacheDir: resolve(process.env.RELEASE_CACHE_DIR ?? "/var/cache/rish-mcp"),
        apiBase: process.env.GITHUB_API_BASE ?? "https://api.github.com",
        pollMs: Number(process.env.RELEASE_POLL_MS ?? 900_000), // 15 min; API allows far more
        localApk: process.env.APK_PATH ? resolve(process.env.APK_PATH) : undefined,
    };
}
export class ReleaseSource {
    opts;
    current = null;
    timer = null;
    constructor(opts) {
        this.opts = opts;
    }
    get() {
        // A local override is re-read each call so a rebuilt APK is picked up.
        if (this.opts.localApk) {
            try {
                const info = readApkInfo(this.opts.localApk);
                return { ...info, tag: "local", path: this.opts.localApk };
            }
            catch {
                return null;
            }
        }
        return this.current;
    }
    /** Load any cached copy, then poll for new releases. Never throws. */
    start() {
        if (this.opts.localApk) {
            console.log(`[release] serving local APK_PATH=${this.opts.localApk} (GitHub polling disabled)`);
            return;
        }
        this.loadCache();
        void this.refresh();
        this.timer = setInterval(() => void this.refresh(), this.opts.pollMs);
        this.timer.unref?.();
    }
    stop() {
        if (this.timer)
            clearInterval(this.timer);
    }
    get apkFile() {
        return join(this.opts.cacheDir, "agent.apk");
    }
    get metaFile() {
        return join(this.opts.cacheDir, "release.json");
    }
    loadCache() {
        try {
            if (!existsSync(this.apkFile) || !existsSync(this.metaFile))
                return;
            const meta = JSON.parse(readFileSync(this.metaFile, "utf8"));
            const info = readApkInfo(this.apkFile);
            this.current = { ...info, tag: meta.tag ?? "unknown", path: this.apkFile };
            console.log(`[release] cached ${this.current.versionName} (${this.current.tag}) restored`);
        }
        catch (e) {
            console.warn(`[release] ignoring unusable cache: ${msg(e)}`);
        }
    }
    /** Check GitHub for a newer release and download it. Swallows all errors. */
    async refresh() {
        try {
            const url = `${this.opts.apiBase}/repos/${this.opts.repo}/releases/latest`;
            const res = await fetch(url, {
                headers: { accept: "application/vnd.github+json", "user-agent": "rish-mcp" },
                signal: AbortSignal.timeout(20_000),
            });
            if (!res.ok)
                throw new Error(`GET ${url} -> ${res.status}`);
            const body = (await res.json());
            const tag = body.tag_name;
            if (!tag)
                throw new Error("release has no tag_name");
            if (this.current?.tag === tag)
                return; // already have it
            const asset = (body.assets ?? []).find((a) => a.name.endsWith(".apk"));
            if (!asset)
                throw new Error(`release ${tag} has no .apk asset`);
            console.log(`[release] fetching ${asset.name} from ${tag}`);
            const dl = await fetch(asset.browser_download_url, {
                headers: { "user-agent": "rish-mcp" },
                signal: AbortSignal.timeout(120_000),
            });
            if (!dl.ok)
                throw new Error(`download -> ${dl.status}`);
            const bytes = Buffer.from(await dl.arrayBuffer());
            mkdirSync(this.opts.cacheDir, { recursive: true });
            const tmp = `${this.apkFile}.tmp`;
            writeFileSync(tmp, bytes);
            // Parse before publishing: a truncated or wrong-content-type download must
            // not replace a working APK.
            const info = readApkInfo(tmp);
            if (info.versionName !== tag.replace(/^v/, "")) {
                console.warn(`[release] tag ${tag} does not match APK versionName ${info.versionName}; trusting the APK`);
            }
            renameSync(tmp, this.apkFile);
            writeFileSync(this.metaFile, JSON.stringify({ tag, fetchedAt: new Date().toISOString() }));
            const fresh = readApkInfo(this.apkFile);
            this.current = { ...fresh, tag, path: this.apkFile };
            console.log(`[release] now serving ${fresh.versionName} (${tag}, sha256 ${fresh.sha256.slice(0, 12)}…)`);
        }
        catch (e) {
            // Keep serving whatever we already have.
            console.warn(`[release] refresh failed: ${msg(e)}`);
        }
    }
}
function msg(e) {
    return e instanceof Error ? e.message : String(e);
}
export function sha256(buf) {
    return createHash("sha256").update(buf).digest("hex");
}
export function fileSize(path) {
    return statSync(path).size;
}
