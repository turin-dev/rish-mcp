export const dynamic = "force-static";

const INSTALLER = `#!/usr/bin/env sh
set -eu

IMAGE="\${RISH_MCP_RELAY_IMAGE:-ghcr.io/turin-dev/rish-mcp-relay:latest}"
CONTAINER_NAME="\${RISH_MCP_RELAY_NAME:-rish-mcp-relay}"
PORT="\${RISH_MCP_RELAY_PORT:-8080}"
CONFIG_DIR="\${RISH_MCP_RELAY_CONFIG_DIR:-\${XDG_CONFIG_HOME:-$HOME/.config}/rish-mcp}"
ENV_FILE="$CONFIG_DIR/relay.env"

if ! command -v docker >/dev/null 2>&1; then
  echo "Docker is required. Install Docker Desktop or Docker Engine and run this again." >&2
  exit 1
fi

case "$PORT" in
  ''|*[!0-9]*)
    echo "RISH_MCP_RELAY_PORT must be a numeric host port." >&2
    exit 1
    ;;
esac

new_token() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex 24
  elif [ -r /dev/urandom ] && command -v od >/dev/null 2>&1; then
    od -An -N24 -tx1 /dev/urandom | tr -d ' \\n'
  else
    echo "openssl or /dev/urandom is required to create relay credentials." >&2
    exit 1
  fi
}

umask 077
mkdir -p "$CONFIG_DIR"

if [ ! -s "$ENV_FILE" ]; then
  ai_token="\${AI_TOKEN:-$(new_token)}"
  device_token="\${DEVICE_TOKEN:-$(new_token)}"
  {
    printf 'AI_TOKEN=%s\\n' "$ai_token"
    printf 'DEVICE_TOKEN=%s\\n' "$device_token"
  } > "$ENV_FILE"
fi
chmod 600 "$ENV_FILE"

if ! sed -n 's/^AI_TOKEN=//p' "$ENV_FILE" | head -n 1 | grep -q '[^[:space:]]'; then
  echo "relay.env has no non-empty AI_TOKEN: $ENV_FILE" >&2
  exit 1
fi
if ! sed -n 's/^DEVICE_TOKEN=//p' "$ENV_FILE" | head -n 1 | grep -q '[^[:space:]]'; then
  echo "relay.env has no non-empty DEVICE_TOKEN: $ENV_FILE" >&2
  exit 1
fi

echo "Pulling $IMAGE..."
docker pull "$IMAGE"

# Replace only the named container created by this installer so re-running the
# command upgrades the relay without touching unrelated Docker workloads.
if docker container inspect "$CONTAINER_NAME" >/dev/null 2>&1; then
  docker rm -f "$CONTAINER_NAME" >/dev/null
fi

docker run --detach \\
  --name "$CONTAINER_NAME" \\
  --restart unless-stopped \\
  --publish "$PORT:8080" \\
  --env-file "$ENV_FILE" \\
  --env PORT=8080 \\
  --read-only \\
  --tmpfs /tmp:size=64m,noexec,nosuid,nodev \\
  --cap-drop ALL \\
  --security-opt no-new-privileges:true \\
  --log-opt max-size=10m \\
  --log-opt max-file=3 \\
  "$IMAGE"

printf '\\nRelay container: %s\\n' "$CONTAINER_NAME"
printf 'Local health check: http://127.0.0.1:%s/healthz\\n' "$PORT"
printf 'MCP endpoint (place behind HTTPS): /mcp\\n'
printf 'Android WebSocket (place behind HTTPS): /agent\\n'
printf 'Credentials saved with mode 600: %s\\n' "$ENV_FILE"
`;

export function GET() {
  return new Response(INSTALLER, {
    headers: {
      "Cache-Control": "no-store",
      "Content-Disposition": 'inline; filename="install-relay.sh"',
      "Content-Type": "text/plain; charset=utf-8",
      "X-Content-Type-Options": "nosniff",
    },
  });
}
