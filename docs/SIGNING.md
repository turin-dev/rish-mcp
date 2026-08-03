# Official APK signing

Pushes to `master` or `main` build the official `rish-mcp-agent.apk` with one persistent Android signing key.
Pull requests and development branches use a disposable CI key and produce test-only APKs.

## Required GitHub repository secrets

Add these under **Settings → Secrets and variables → Actions → Repository secrets**:

| Secret | Value |
|---|---|
| `ANDROID_KEYSTORE_BASE64` | Base64-encoded contents of the persistent `release.keystore` |
| `ANDROID_KEYSTORE_PASSWORD` | Keystore password |
| `ANDROID_KEY_ALIAS` | Key alias, normally `rishmcp` |
| `ANDROID_KEY_PASSWORD` | Private key password |

The official workflow intentionally fails if any of these secrets are missing. It never falls back to a generated key for an official branch build.

## Create a signing key once

If there is no existing production key, create one once and keep it backed up somewhere safe:

```bash
keytool -genkeypair -v \
  -keystore release.keystore \
  -storepass '<strong-keystore-password>' \
  -alias rishmcp \
  -keypass '<strong-key-password>' \
  -keyalg RSA -keysize 4096 -validity 10000 \
  -dname 'CN=rish-mcp, O=turin-dev, C=KR'
```

Do **not** commit `release.keystore`. It is ignored by Git.

If an already-distributed rish-mcp APK was signed with a key you still have, use that same key instead of generating a new one. Android only allows an installed app to update to an APK signed by the same signing identity (unless an explicit signing-key rotation mechanism is used).

## Encode the keystore for GitHub Secrets

### PowerShell

```powershell
[Convert]::ToBase64String([IO.File]::ReadAllBytes("release.keystore")) | Set-Clipboard
```

Paste the clipboard contents into `ANDROID_KEYSTORE_BASE64`.

### Linux / macOS

```bash
base64 < release.keystore | tr -d '\n'
```

Copy the single-line output into `ANDROID_KEYSTORE_BASE64`.

## What happens on `master` / `main`

1. Server TypeScript build and end-to-end MCP smoke test run.
2. The keystore is restored from GitHub Secrets.
3. The workflow verifies that the configured alias/password can open the key.
4. `app/build-apk.sh` creates the release APK using that key.
5. Android `apksigner` verifies the generated APK and prints its certificate information.
6. A SHA-256 checksum is generated.
7. GitHub Actions uploads an artifact named `rish-mcp-official-<commit-sha>` containing:
   - `rish-mcp-agent.apk`
   - `rish-mcp-agent.apk.sha256`
8. The restored keystore file is removed from the runner even if the job fails.

Official artifacts are retained for 90 days.

## Development builds

Pull requests and manual workflow runs outside `master` / `main` create a test artifact named `rish-mcp-test-<commit-sha>`. Those builds use a disposable CI signing key and must not be used as an update path for official installations.

Development branches are built through their pull request rather than on push, so a
branch with no open PR does not run CI. Use **Actions → CI → Run workflow** if you need
a build before opening one.
