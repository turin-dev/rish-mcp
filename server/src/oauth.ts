// Minimal OAuth 2.0 authorization server in front of /mcp, so clients that
// only speak OAuth (claude.ai custom connectors, hence the Claude mobile app)
// can connect. Single-user by design: "logging in" at /authorize means typing
// the relay's AI_TOKEN once; everything issued afterwards is a stateless
// HMAC-signed token derived from that same secret, so nothing is persisted
// and rotating AI_TOKEN revokes every issued token at once.
//
// Implements the parts MCP clients need (RFC 8414 + 9728 metadata, RFC 7591
// dynamic client registration, authorization-code grant with mandatory PKCE
// S256, refresh_token grant) and nothing else.
import { createHash, createHmac, timingSafeEqual } from "node:crypto";
import { Router, type Request, type Response } from "express";

export interface OAuthConfig {
  publicUrl: string; // external base URL, no trailing slash, e.g. https://mcp.example.com
  aiToken: string; // the "access key" the owner types on the consent page
  accessTtlSec?: number; // default 1h
  refreshTtlSec?: number; // default 90d
}

const CODE_TTL_SEC = 300;

type Payload =
  | { t: "client"; ru: string[]; iat: number }
  | { t: "code"; cid: string; ru: string; cc: string; exp: number }
  | { t: "at"; exp: number }
  | { t: "rt"; cid: string; exp: number };

function b64url(buf: Buffer): string {
  return buf.toString("base64url");
}

function now(): number {
  return Math.floor(Date.now() / 1000);
}

function safeEqual(a: string, b: string): boolean {
  const ab = Buffer.from(a);
  const bb = Buffer.from(b);
  return ab.length === bb.length && timingSafeEqual(ab, bb);
}

export class OAuthProvider {
  private key: Buffer;
  private cfg: Required<OAuthConfig>;
  // Best-effort single-use enforcement for auth codes (in-memory; a restart
  // only re-opens the <=5min replay window, which the PKCE binding still guards).
  private usedCodes = new Map<string, number>();
  // Per-IP throttle on consent-page submissions (the only place the real
  // secret is compared, i.e. the only brute-forceable surface).
  private attempts = new Map<string, { count: number; resetAt: number }>();

  constructor(cfg: OAuthConfig) {
    this.cfg = {
      accessTtlSec: 3600,
      refreshTtlSec: 90 * 24 * 3600,
      ...cfg,
      publicUrl: cfg.publicUrl.replace(/\/+$/, ""),
    };
    this.key = createHmac("sha256", "rish-mcp-oauth-v1").update(cfg.aiToken).digest();
  }

  private sign(p: Payload): string {
    const body = b64url(Buffer.from(JSON.stringify(p)));
    const sig = b64url(createHmac("sha256", this.key).update(body).digest());
    return `${body}.${sig}`;
  }

  private verify(token: string): Payload | null {
    const dot = token.lastIndexOf(".");
    if (dot < 1) return null;
    const body = token.slice(0, dot);
    const sig = token.slice(dot + 1);
    const expect = b64url(createHmac("sha256", this.key).update(body).digest());
    if (!safeEqual(sig, expect)) return null;
    try {
      const p = JSON.parse(Buffer.from(body, "base64url").toString()) as Payload;
      if ("exp" in p && p.exp < now()) return null;
      return p;
    } catch {
      return null;
    }
  }

  /** Used by the /mcp bearer check: accepts tokens issued via the OAuth flow. */
  verifyAccessToken(token: string): boolean {
    const p = this.verify(token);
    return p?.t === "at";
  }

  private rateLimited(ip: string): boolean {
    const t = now();
    const e = this.attempts.get(ip);
    if (!e || e.resetAt < t) {
      this.attempts.set(ip, { count: 1, resetAt: t + 300 });
      return false;
    }
    e.count += 1;
    return e.count > 10;
  }

  wwwAuthenticate(): string {
    return `Bearer resource_metadata="${this.cfg.publicUrl}/.well-known/oauth-protected-resource"`;
  }

  router(): Router {
    const r = Router();
    const base = this.cfg.publicUrl;

    const asMetadata = {
      issuer: base,
      authorization_endpoint: `${base}/oauth/authorize`,
      token_endpoint: `${base}/oauth/token`,
      registration_endpoint: `${base}/oauth/register`,
      response_types_supported: ["code"],
      grant_types_supported: ["authorization_code", "refresh_token"],
      code_challenge_methods_supported: ["S256"],
      token_endpoint_auth_methods_supported: ["none"],
      scopes_supported: [],
    };
    const prMetadata = {
      resource: `${base}/mcp`,
      authorization_servers: [base],
      bearer_methods_supported: ["header"],
    };
    // Clients probe both the bare well-known path and the path-suffixed
    // variant (RFC 8414 §3 / RFC 9728 §3), hence the wildcards.
    r.get(["/.well-known/oauth-authorization-server", "/.well-known/oauth-authorization-server/*splat"], (_req, res) => {
      res.json(asMetadata);
    });
    r.get(["/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/*splat"], (_req, res) => {
      res.json(prMetadata);
    });

    // RFC 7591 dynamic client registration, open by design (public clients
    // only). The client_id is itself a signed record of the redirect URIs, so
    // no registry is kept; possessing a client_id grants nothing without the
    // consent step.
    r.post("/oauth/register", (req: Request, res: Response) => {
      const ru = (req.body?.redirect_uris ?? []) as unknown;
      if (!Array.isArray(ru) || ru.length === 0 || !ru.every((u) => typeof u === "string" && /^https?:\/\//.test(u))) {
        res.status(400).json({ error: "invalid_client_metadata", error_description: "redirect_uris (http/https) required" });
        return;
      }
      const client_id = this.sign({ t: "client", ru: ru as string[], iat: now() });
      res.status(201).json({
        client_id,
        redirect_uris: ru,
        token_endpoint_auth_method: "none",
        grant_types: ["authorization_code", "refresh_token"],
        response_types: ["code"],
      });
    });

    const parseAuthReq = (q: Record<string, unknown>) => {
      const client_id = String(q.client_id ?? "");
      const redirect_uri = String(q.redirect_uri ?? "");
      const client = this.verify(client_id);
      if (client?.t !== "client") return { error: "unknown client_id" };
      if (!client.ru.includes(redirect_uri)) return { error: "redirect_uri not registered for this client" };
      if (String(q.response_type ?? "") !== "code") return { error: "response_type must be 'code'" };
      const cc = String(q.code_challenge ?? "");
      if (!cc || String(q.code_challenge_method ?? "") !== "S256") return { error: "PKCE S256 code_challenge required" };
      return { client_id, redirect_uri, cc, state: q.state ? String(q.state) : undefined };
    };

    const hiddenInputs = (p: { client_id: string; redirect_uri: string; cc: string; state?: string }) =>
      Object.entries({
        response_type: "code",
        client_id: p.client_id,
        redirect_uri: p.redirect_uri,
        code_challenge: p.cc,
        code_challenge_method: "S256",
        ...(p.state ? { state: p.state } : {}),
      })
        .map(([k, v]) => `<input type="hidden" name="${k}" value="${escapeHtml(v)}">`)
        .join("\n      ");

    r.get("/oauth/authorize", (req: Request, res: Response) => {
      const p = parseAuthReq(req.query as Record<string, unknown>);
      if ("error" in p) {
        res.status(400).type("text/plain").send(`invalid authorization request: ${p.error}`);
        return;
      }
      res.type("html").send(consentPage(hiddenInputs(p)));
    });

    r.post("/oauth/authorize", (req: Request, res: Response) => {
      const ip = req.ip ?? "?";
      if (this.rateLimited(ip)) {
        res.status(429).type("text/plain").send("too many attempts, try again in a few minutes");
        return;
      }
      const p = parseAuthReq(req.body as Record<string, unknown>);
      if ("error" in p) {
        res.status(400).type("text/plain").send(`invalid authorization request: ${p.error}`);
        return;
      }
      if (!safeEqual(String(req.body?.key ?? ""), this.cfg.aiToken)) {
        res.status(401).type("html").send(consentPage(hiddenInputs(p), "Wrong access key."));
        return;
      }
      const code = this.sign({ t: "code", cid: sha256(p.client_id), ru: p.redirect_uri, cc: p.cc, exp: now() + CODE_TTL_SEC });
      const loc = new URL(p.redirect_uri);
      loc.searchParams.set("code", code);
      if (p.state) loc.searchParams.set("state", p.state);
      res.redirect(302, loc.toString());
    });

    r.post("/oauth/token", (req: Request, res: Response) => {
      const grant = String(req.body?.grant_type ?? "");
      const fail = (error: string, desc?: string) => res.status(400).json({ error, ...(desc ? { error_description: desc } : {}) });

      if (grant === "authorization_code") {
        const code = String(req.body?.code ?? "");
        const c = this.verify(code);
        if (c?.t !== "code") return fail("invalid_grant", "bad or expired code");
        if (this.usedCodes.has(code)) return fail("invalid_grant", "code already used");
        if (c.ru !== String(req.body?.redirect_uri ?? "")) return fail("invalid_grant", "redirect_uri mismatch");
        const verifier = String(req.body?.code_verifier ?? "");
        if (b64url(createHash("sha256").update(verifier).digest()) !== c.cc) return fail("invalid_grant", "PKCE verification failed");
        this.usedCodes.set(code, c.exp);
        for (const [k, exp] of this.usedCodes) if (exp < now()) this.usedCodes.delete(k);
        res.json(this.issueTokens(c.cid));
        return;
      }

      if (grant === "refresh_token") {
        const rt = this.verify(String(req.body?.refresh_token ?? ""));
        if (rt?.t !== "rt") return fail("invalid_grant", "bad or expired refresh_token");
        res.json(this.issueTokens(rt.cid));
        return;
      }

      fail("unsupported_grant_type");
    });

    return r;
  }

  private issueTokens(cid: string) {
    return {
      access_token: this.sign({ t: "at", exp: now() + this.cfg.accessTtlSec }),
      token_type: "Bearer",
      expires_in: this.cfg.accessTtlSec,
      refresh_token: this.sign({ t: "rt", cid, exp: now() + this.cfg.refreshTtlSec }),
    };
  }
}

function sha256(s: string): string {
  return createHash("sha256").update(s).digest("base64url");
}

function escapeHtml(s: string): string {
  return s.replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);
}

function consentPage(hiddenInputs: string, error = ""): string {
  return `<!doctype html>
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>rish-mcp — authorize</title>
<style>
  body { font: 16px system-ui, sans-serif; background: #111; color: #eee; display: grid; place-items: center; min-height: 100vh; margin: 0; }
  form { background: #1c1c1e; padding: 2rem; border-radius: 12px; width: min(90vw, 22rem); }
  h1 { font-size: 1.1rem; margin: 0 0 .5rem; }
  p { color: #9a9aa0; font-size: .85rem; margin: 0 0 1rem; }
  input[type=password] { width: 100%; box-sizing: border-box; padding: .6rem; border-radius: 8px; border: 1px solid #333; background: #111; color: #eee; }
  button { margin-top: 1rem; width: 100%; padding: .6rem; border: 0; border-radius: 8px; background: #d97757; color: #fff; font-weight: 600; cursor: pointer; }
  .err { color: #ff6b6b; font-size: .85rem; margin-top: .5rem; }
</style>
<form method="post" action="">
  <h1>rish-mcp</h1>
  <p>An MCP client is asking for shell access to your phone. Paste the relay's <code>AI_TOKEN</code> to allow it.</p>
  ${hiddenInputs}
  <input type="password" name="key" placeholder="access key (AI_TOKEN)" autofocus required>
  ${error ? `<div class="err">${escapeHtml(error)}</div>` : ""}
  <button>Authorize</button>
</form>`;
}
