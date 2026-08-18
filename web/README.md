# rish-mcp product website

This directory contains the public product site for rish-mcp. It explains the
current no-Shizuku architecture and links visitors to the repository's setup,
security, and implementation documentation. It does not host the relay or APK
version service.

## Local development

Use Node.js 22 and install exactly the locked dependencies:

```bash
npm ci
npm run dev
```

Open <http://localhost:3000>. The primary page and styles live in
`app/page.tsx` and `app/globals.css`.

## Validation

```bash
npm run lint
npm run build
```

The production Docker image can be checked from this directory with:

```bash
docker build -t rish-mcp-web .
docker run --rm -p 3000:3000 rish-mcp-web
```

Product claims on the page must match the contracts and limitations documented
in [`../README.md`](../README.md), [`../docs/DESIGN.md`](../docs/DESIGN.md), and
[`../docs/RELEASES.md`](../docs/RELEASES.md). A successful static build is not
evidence of a real-device pairing or release acceptance.
