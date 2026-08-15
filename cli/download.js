import { createWriteStream, existsSync, rmSync, renameSync } from "node:fs";
import { pipeline } from "node:stream/promises";
import { Readable } from "node:stream";

export const DOWNLOAD_TIMEOUT_MS = 120_000;

function replaceDownloadedFile(temp, dest) {
  try {
    renameSync(temp, dest);
    return;
  } catch (error) {
    // Windows refuses to rename over an existing file. Move the old cache
    // aside only after the new file is fully downloaded, then restore it if
    // the final rename fails.
    if (process.platform !== "win32" || !existsSync(dest)) throw error;
  }

  const backup = `${dest}.backup-${process.pid}-${Date.now()}`;
  renameSync(dest, backup);
  try {
    renameSync(temp, dest);
  } catch (error) {
    if (!existsSync(dest)) renameSync(backup, dest);
    throw error;
  }
  rmSync(backup, { force: true });
}

export async function downloadFile(url, dest, { timeoutMs = DOWNLOAD_TIMEOUT_MS } = {}) {
  const temp = `${dest}.part-${process.pid}-${Date.now()}`;
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), timeoutMs);
  try {
    const res = await fetch(url, { signal: controller.signal });
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    if (!res.body) throw new Error("response has no body");
    await pipeline(Readable.fromWeb(res.body), createWriteStream(temp));
    replaceDownloadedFile(temp, dest);
  } catch (error) {
    const detail = error?.name === "AbortError"
      ? `timed out after ${timeoutMs / 1000}s`
      : error instanceof Error
        ? error.message
        : String(error);
    throw new Error(`download failed: ${detail}`, { cause: error });
  } finally {
    clearTimeout(timeout);
    rmSync(temp, { force: true });
  }
}
