import { randomUUID } from "node:crypto";
import type { WebSocket } from "ws";

export interface ExecResult {
  code: number;
  stdout: string;
  stderr: string;
  truncated: boolean;
  durationMs: number;
}

interface Pending {
  resolve: (r: ExecResult) => void;
  reject: (e: Error) => void;
  timer: NodeJS.Timeout;
}

export interface Device {
  id: string;
  name: string;
  sdk: string;
  ws: WebSocket;
  connectedAt: number;
  lastSeen: number;
  pending: Map<string, Pending>;
}

/** In-memory registry of phones currently connected over the WS relay. */
class Registry {
  private devices = new Map<string, Device>();

  add(device: Device) {
    // If a device reconnects with the same id, drop the stale socket.
    const existing = this.devices.get(device.id);
    if (existing && existing.ws !== device.ws) {
      try {
        existing.ws.close(4000, "replaced by new connection");
      } catch {
        /* ignore */
      }
      this.failAll(existing, new Error("device reconnected"));
    }
    this.devices.set(device.id, device);
  }

  remove(id: string, ws: WebSocket) {
    const d = this.devices.get(id);
    if (d && d.ws === ws) {
      this.failAll(d, new Error("device disconnected"));
      this.devices.delete(id);
    }
  }

  get(id: string): Device | undefined {
    return this.devices.get(id);
  }

  list(): Device[] {
    return [...this.devices.values()];
  }

  /** Default device when caller omits deviceId and exactly one is connected. */
  only(): Device | undefined {
    return this.devices.size === 1 ? this.list()[0] : undefined;
  }

  private failAll(d: Device, err: Error) {
    for (const p of d.pending.values()) {
      clearTimeout(p.timer);
      p.reject(err);
    }
    d.pending.clear();
  }

  /** Resolve a pending exec when the phone returns a result. */
  resolveResult(deviceId: string, reqId: string, msg: Partial<ExecResult>) {
    const device = this.devices.get(deviceId);
    if (!device) return;
    device.lastSeen = Date.now();
    const p = device.pending.get(reqId);
    if (!p) return;
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
  exec(deviceId: string | undefined, cmd: string, timeoutMs: number): Promise<ExecResult> {
    const device = deviceId ? this.devices.get(deviceId) : this.only();
    if (!device) {
      const hint = deviceId
        ? `device '${deviceId}' is not connected`
        : this.devices.size === 0
          ? "no phone is connected to the relay"
          : "multiple devices connected; pass deviceId";
      return Promise.reject(new Error(hint));
    }
    const reqId = randomUUID();
    return new Promise<ExecResult>((resolve, reject) => {
      const timer = setTimeout(() => {
        device.pending.delete(reqId);
        reject(new Error(`exec timed out after ${timeoutMs}ms`));
      }, timeoutMs + 2000); // grace over the phone-side timeout
      device.pending.set(reqId, { resolve, reject, timer });
      try {
        device.ws.send(JSON.stringify({ type: "exec", reqId, cmd, timeoutMs }));
      } catch (e) {
        device.pending.delete(reqId);
        clearTimeout(timer);
        reject(e instanceof Error ? e : new Error(String(e)));
      }
    });
  }
}

export const registry = new Registry();
