// Read versionCode/versionName straight out of the APK the relay serves, so the
// "latest release" it reports is whatever is actually on disk rather than a
// constant somebody has to remember to bump.
//
// An APK is a zip whose AndroidManifest.xml is Android binary XML (AXML), not
// text — hence the two small parsers below. Both are read-only and dependency
// free; anything unexpected throws and the caller falls back to its constant.
import { createHash } from "node:crypto";
import { readFileSync, statSync } from "node:fs";
import { inflateRawSync } from "node:zlib";
// android:versionCode / android:versionName, from android.R.attr.
const ATTR_VERSION_CODE = 0x0101021b;
const ATTR_VERSION_NAME = 0x0101021c;
// --- zip --------------------------------------------------------------------
/** Extract one entry by name. Only the subset of zip that APKs actually use. */
function readZipEntry(buf, wanted) {
    // End of central directory: scan back from the end (comment is usually empty).
    let eocd = -1;
    for (let i = buf.length - 22; i >= 0 && i > buf.length - 22 - 0xffff; i--) {
        if (buf.readUInt32LE(i) === 0x06054b50) {
            eocd = i;
            break;
        }
    }
    if (eocd < 0)
        throw new Error("not a zip (no end-of-central-directory)");
    const entries = buf.readUInt16LE(eocd + 10);
    let p = buf.readUInt32LE(eocd + 16);
    if (p === 0xffffffff)
        throw new Error("zip64 central directory is not supported");
    for (let i = 0; i < entries; i++) {
        if (buf.readUInt32LE(p) !== 0x02014b50)
            throw new Error("corrupt central directory");
        const method = buf.readUInt16LE(p + 10);
        const compressedSize = buf.readUInt32LE(p + 20);
        const nameLen = buf.readUInt16LE(p + 28);
        const extraLen = buf.readUInt16LE(p + 30);
        const commentLen = buf.readUInt16LE(p + 32);
        const localOffset = buf.readUInt32LE(p + 42);
        const name = buf.toString("utf8", p + 46, p + 46 + nameLen);
        if (name === wanted) {
            if (buf.readUInt32LE(localOffset) !== 0x04034b50)
                throw new Error("corrupt local header");
            // The local header repeats the name and may carry a different extra field.
            const dataStart = localOffset + 30 + buf.readUInt16LE(localOffset + 26) + buf.readUInt16LE(localOffset + 28);
            const raw = buf.subarray(dataStart, dataStart + compressedSize);
            if (method === 0)
                return raw; // stored
            if (method === 8)
                return inflateRawSync(raw); // deflate
            throw new Error(`unsupported zip compression method ${method}`);
        }
        p += 46 + nameLen + extraLen + commentLen;
    }
    throw new Error(`${wanted} not found in apk`);
}
// --- android binary xml ------------------------------------------------------
const CHUNK_STRING_POOL = 0x001c0001;
const CHUNK_RESOURCE_MAP = 0x00080180;
const CHUNK_START_TAG = 0x00100102;
function parseStringPool(buf, start) {
    const count = buf.readUInt32LE(start + 8);
    const flags = buf.readUInt32LE(start + 16);
    const stringsStart = buf.readUInt32LE(start + 20);
    const utf8 = (flags & (1 << 8)) !== 0;
    const out = [];
    for (let i = 0; i < count; i++) {
        const off = start + stringsStart + buf.readUInt32LE(start + 28 + i * 4);
        if (utf8) {
            // Two varints (utf16 length, then byte length); each is 1 or 2 bytes.
            let p = off;
            if (buf[p] & 0x80)
                p += 2;
            else
                p += 1;
            let byteLen = buf[p];
            if (byteLen & 0x80) {
                byteLen = ((byteLen & 0x7f) << 8) | buf[p + 1];
                p += 2;
            }
            else {
                p += 1;
            }
            out.push(buf.toString("utf8", p, p + byteLen));
        }
        else {
            let len = buf.readUInt16LE(off);
            let p = off + 2;
            if (len & 0x8000) {
                // Surrogate-style length extension for very long strings.
                len = ((len & 0x7fff) << 16) | buf.readUInt16LE(p);
                p += 2;
            }
            out.push(buf.toString("utf16le", p, p + len * 2));
        }
    }
    return out;
}
/** Pull versionCode/versionName off the root <manifest> element. */
function parseManifest(axml) {
    if (axml.length < 8 || axml.readUInt32LE(0) !== 0x00080003) {
        throw new Error("not android binary xml");
    }
    let strings = [];
    let resourceMap = [];
    let p = 8; // skip file magic + size
    while (p + 8 <= axml.length) {
        const type = axml.readUInt32LE(p);
        const size = axml.readUInt32LE(p + 4);
        if (size <= 0 || p + size > axml.length)
            break;
        if (type === CHUNK_STRING_POOL) {
            strings = parseStringPool(axml, p);
        }
        else if (type === CHUNK_RESOURCE_MAP) {
            // Maps string-pool index -> android attribute resource id, which is how we
            // recognise versionCode/versionName without trusting attribute names.
            // ResChunk_header is {u16 type, u16 headerSize, u32 size}.
            const headerSize = axml.readUInt16LE(p + 2);
            for (let q = p + headerSize; q + 4 <= p + size; q += 4) {
                resourceMap.push(axml.readUInt32LE(q));
            }
        }
        else if (type === CHUNK_START_TAG) {
            const attrStart = p + 8 + 8 + 8; // chunk header, node header, ns+name
            const attrSize = axml.readUInt16LE(attrStart + 2);
            const attrCount = axml.readUInt16LE(attrStart + 4);
            let versionName;
            let versionCode;
            for (let a = 0; a < attrCount; a++) {
                const at = attrStart + 12 + a * attrSize;
                const nameIdx = axml.readInt32LE(at + 4);
                const rawValueIdx = axml.readInt32LE(at + 8);
                const dataType = axml.readUInt8(at + 15);
                const data = axml.readUInt32LE(at + 16);
                const resId = resourceMap[nameIdx];
                if (resId === ATTR_VERSION_CODE) {
                    versionCode = data;
                }
                else if (resId === ATTR_VERSION_NAME) {
                    // 0x03 = TYPE_STRING, where data indexes the pool.
                    if (rawValueIdx >= 0)
                        versionName = strings[rawValueIdx];
                    else if (dataType === 0x03)
                        versionName = strings[data];
                }
            }
            // The root element is <manifest>; both attributes live there.
            if (versionName !== undefined && versionCode !== undefined) {
                return { versionName, versionCode };
            }
            throw new Error("root element carries no versionCode/versionName");
        }
        p += size;
    }
    throw new Error("no start tag in manifest");
}
// --- public ------------------------------------------------------------------
let cache = null;
/**
 * Parse the APK at [path]. Cached on size+mtime, so replacing the file (the
 * compose bind mount is how a new build lands) is picked up without a restart
 * while repeated requests stay cheap.
 */
export function readApkInfo(path) {
    const st = statSync(path);
    const key = `${st.size}:${st.mtimeMs}`;
    if (cache && cache.key === key)
        return cache.info;
    const buf = readFileSync(path);
    const { versionName, versionCode } = parseManifest(readZipEntry(buf, "AndroidManifest.xml"));
    const info = {
        versionName,
        versionCode,
        sizeBytes: st.size,
        sha256: createHash("sha256").update(buf).digest("hex"),
        modifiedAt: new Date(st.mtimeMs).toISOString(),
    };
    cache = { key, info };
    return info;
}
