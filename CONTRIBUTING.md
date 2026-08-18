# Contributing to rish-mcp

Thank you for helping improve rish-mcp. This repository is a security-sensitive
rewrite in progress: automated checks prove builds and unit behavior, but do
not replace testing against a real Android device. Participation is governed by
the [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md).

## Before you start

- Base work on `master` and keep each pull request focused.
- Open an issue before changing authentication, the relay protocol, release
  channels, device provisioning, or other public contracts.
- Never commit tokens, keystores, `google-services.json`, `.env` files,
  `local.properties`, dependency directories, or generated build output.
- Use the legacy code under `before/` only as a reference unless the change is
  explicitly about that implementation.

## Local checks

Run the checks for every component you touch:

```bash
# Go relay, public server, and setup CLI
cd server
gofmt -w .
go vet ./...
go test ./...
# Also run this when the host has CGO and a supported C compiler:
go test -race ./...
go build ./...

# Node setup CLI
cd ../cli
npm ci
npm test
npm pack --dry-run

# Product website
cd ../web
npm ci
npm run lint
npm run build
```

For Android, use the repository's containerized toolchain from the repository
root:

```bash
docker build -t rishmcp-android-build -f app/Dockerfile.build app
docker run --rm -v "$PWD/app:/work" -w /work rishmcp-android-build \
  gradle --no-daemon testDebugUnitTest assembleDebug
```

In the pull request, state exactly what you ran and separate automated evidence
from real-device, deployment, or release verification. By contributing, you
agree that your contribution is licensed under the repository's MIT License.
