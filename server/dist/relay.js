import { randomUUID } from "node:crypto";
export function normalizeKind(raw) {
    return raw === "watch" ? "watch" : "android";
}
/** In-memory registry of Android devices currently connected over the WS relay. */
class Registry {
    devices = new Map();
    add(device) {
        // If a device reconnects with the same id, drop the stale socket.
        const existing = this.devices.get(device.id);
        if (existing && existing.ws !== device.ws) {
            try {
                existing.ws.close(4000, "replaced by new connection");
            }
            catch {
                /* ignore */
            }
            this.failAll(existing, new Error("device reconnected"));
        }
        this.devices.set(device.id, device);
    }
    remove(id, ws) {
        const d = this.devices.get(id);
        if (d && d.ws === ws) {
            this.failAll(d, new Error("device disconnected"));
            this.devices.delete(id);
        }
    }
    get(id) {
        return this.devices.get(id);
    }
    list() {
        return [...this.devices.values()];
    }
    /** Default device when caller omits deviceId and exactly one is connected. */
    only() {
        return this.devices.size === 1 ? this.list()[0] : undefined;
    }
    failAll(d, err) {
        for (const p of d.pending.values()) {
            clearTimeout(p.timer);
            p.reject(err);
        }
        d.pending.clear();
    }
    /** Resolve a pending exec when an Android device returns a result. */
    resolveResult(deviceId, reqId, msg) {
        const device = this.devices.get(deviceId);
        if (!device)
            return;
        device.lastSeen = Date.now();
        const p = device.pending.get(reqId);
        if (!p)
            return;
        device.pending.delete(reqId);
        clearTimeout(p.timer);
        p.resolve({
            code: msg.code ?? -1,
            stdout: msg.stdout ?? "",
            stderr: msg.stderr ?? "",
            truncated: msg.truncated ?? false,
            durationMs: msg.durationMs ?? 0,
        });
    }
    /** Send a command to a device and await its result. */
    exec(deviceId, cmd, timeoutMs) {
        const device = deviceId ? this.devices.get(deviceId) : this.only();
        if (!device) {
            const hint = deviceId
                ? `device '${deviceId}' is not connected`
                : this.devices.size === 0
                    ? "no Android device is connected to the relay"
                    : "multiple devices connected; pass deviceId";
            return Promise.reject(new Error(hint));
        }
        const reqId = randomUUID();
        return new Promise((resolve, reject) => {
            const timer = setTimeout(() => {
                device.pending.delete(reqId);
                reject(new Error(`exec timed out after ${timeoutMs}ms`));
            }, timeoutMs + 2000); // grace over the device-side timeout
            device.pending.set(reqId, { resolve, reject, timer });
            try {
                device.ws.send(JSON.stringify({ type: "exec", reqId, cmd, timeoutMs }));
            }
            catch (e) {
                device.pending.delete(reqId);
                clearTimeout(timer);
                reject(e instanceof Error ? e : new Error(String(e)));
            }
        });
    }
}
export const registry = new Registry();
