#!/usr/bin/env bash
# Build (and sign) the rish-mcp agent APK inside Docker. Host stays clean.
set -euo pipefail
cd "$(dirname "$0")"

IMAGE=rishmcp-android-build
DOCKER="${DOCKER:-sudo docker}"
KEYSTORE="$PWD/release.keystore"
KS_PASS="${KEYSTORE_PASSWORD:-rishmcp-local}"
KEY_ALIAS="${KEY_ALIAS:-rishmcp}"
KEY_PASS="${KEY_PASSWORD:-rishmcp-local}"

# 1. One-time signing keystore (debug-grade; fine for a personal sideloaded app).
if [ ! -f "$KEYSTORE" ]; then
  echo ">> generating release keystore"
  keytool -genkeypair -v \
    -keystore "$KEYSTORE" -storepass "$KS_PASS" \
    -alias "$KEY_ALIAS" -keypass "$KEY_PASS" \
    -keyalg RSA -keysize 2048 -validity 10000 \
    -dname "CN=rish-mcp, O=scin.kr, C=KR"
fi

# 2. Build the toolchain image (cached after first run).
echo ">> building Android toolchain image (first run downloads the SDK)…"
$DOCKER build -t "$IMAGE" -f Dockerfile.build .

# 3. Assemble the signed release APK via a bind mount.
echo ">> assembling release APK…"
$DOCKER run --rm \
  -u "$(id -u):$(id -g)" \
  -v "$PWD":/work \
  -e HOME=/work/.dockerhome \
  -e GRADLE_USER_HOME=/work/.gradle \
  -e ANDROID_USER_HOME=/work/.android \
  -e ANDROID_SDK_HOME=/work \
  -e KEYSTORE_FILE=/work/release.keystore \
  -e KEYSTORE_PASSWORD="$KS_PASS" \
  -e KEY_ALIAS="$KEY_ALIAS" \
  -e KEY_PASSWORD="$KEY_PASS" \
  "$IMAGE" \
  bash -lc 'mkdir -p "$HOME" "$GRADLE_USER_HOME" "$ANDROID_USER_HOME" && gradle --no-daemon -p /work assembleRelease'

OUT="app/build/outputs/apk/release/app-release.apk"
if [ -f "$OUT" ]; then
  cp "$OUT" ./rish-mcp-agent.apk
  echo ">> done: $PWD/rish-mcp-agent.apk"
  ls -la ./rish-mcp-agent.apk
else
  echo "!! build did not produce $OUT" >&2
  exit 1
fi
