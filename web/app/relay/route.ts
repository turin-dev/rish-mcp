export const dynamic = "force-static";

const INSTALLER = `#!/usr/bin/env sh
# rish-mcp installer: curl -fsSL https://rish-mcp.turin.my | sh
#
# Interactive when a terminal is available (answers are read from /dev/tty,
# since stdin is the downloaded script). Pass --yes, set RISH_MCP_YES=1, or
# run without a terminal to take every default:
#   curl -fsSL https://rish-mcp.turin.my | sh -s -- --yes
#   curl -fsSL https://rish-mcp.turin.my | sh -s -- client --url https://mcp.example.com/mcp
set -eu

# Everything runs from main() at the end, so a download cut short by the
# network never executes half a script.
main() {
  IMAGE="\${RISH_MCP_RELAY_IMAGE:-ghcr.io/turin-dev/rish-mcp-relay:latest}"
  CONTAINER_NAME="\${RISH_MCP_RELAY_NAME:-rish-mcp-relay}"
  CONFIG_DIR="\${RISH_MCP_RELAY_CONFIG_DIR:-\${XDG_CONFIG_HOME:-$HOME/.config}/rish-mcp}"
  ENV_FILE="$CONFIG_DIR/relay.env"
  CLIENT_FILE="$CONFIG_DIR/client.json"

  ACTION=""
  YES="\${RISH_MCP_YES:-0}"
  ARG_PORT=""
  ARG_PUBLIC_URL=""
  ARG_URL="\${RISH_MCP_URL:-}"
  ARG_TOKEN=""
  while [ $# -gt 0 ]; do
    case "$1" in
      server|client) ACTION="$1" ;;
      --action) need_value "$@"; ACTION="$2"; shift ;;
      --action=*) ACTION="\${1#*=}" ;;
      --port) need_value "$@"; ARG_PORT="$2"; shift ;;
      --port=*) ARG_PORT="\${1#*=}" ;;
      --public-url) need_value "$@"; ARG_PUBLIC_URL="$2"; shift ;;
      --public-url=*) ARG_PUBLIC_URL="\${1#*=}" ;;
      --url) need_value "$@"; ARG_URL="$2"; shift ;;
      --url=*) ARG_URL="\${1#*=}" ;;
      --token) need_value "$@"; ARG_TOKEN="$2"; shift ;;
      --token=*) ARG_TOKEN="\${1#*=}" ;;
      -y|--yes) YES=1 ;;
      -h|--help) usage; return 0 ;;
      *) die "unknown option: $1 (see --help)" ;;
    esac
    shift
  done
  case "$ACTION" in
    ''|server|client) ;;
    *) die "action must be server or client (got $ACTION)" ;;
  esac

  INTERACTIVE=0
  if [ "$YES" != 1 ] && (: </dev/tty) 2>/dev/null; then
    INTERACTIVE=1
    trap 'stty echo </dev/tty 2>/dev/null || true' EXIT
    trap 'stty echo </dev/tty 2>/dev/null || true; exit 130' INT TERM
  fi

  if [ -t 1 ] && [ -z "\${NO_COLOR:-}" ]; then
    C_RESET="$(printf '\\033[0m')"
    C_BOLD="$(printf '\\033[1m')"
    C_DIM="$(printf '\\033[2m')"
    C_ACCENT="$(printf '\\033[38;5;209m')"
    C_GOOD="$(printf '\\033[32m')"
    C_BAD="$(printf '\\033[31m')"
  else
    C_RESET="" C_BOLD="" C_DIM="" C_ACCENT="" C_GOOD="" C_BAD=""
  fi

  if [ -z "$ACTION" ]; then
    if [ "$INTERACTIVE" = 1 ]; then
      heading "rish-mcp setup"
      say "$(dim "Relay server + MCP client configuration. Ctrl-C to quit at any time.")"
      say ""
      say "  1) Install or update the relay server"
      say "  2) Configure an MCP client"
      say "  3) Exit"
      while :; do
        ask "Choice" "1"
        case "$REPLY" in
          1) ACTION=server; break ;;
          2) ACTION=client; break ;;
          3|q) return 0 ;;
          *) warn "choose 1, 2 or 3" ;;
        esac
      done
    else
      ACTION=server
    fi
  fi

  if [ "$ACTION" = server ]; then
    install_server
    if [ "$INTERACTIVE" = 1 ]; then
      say ""
      if confirm "Configure an MCP client now?" y; then
        configure_client
      fi
    fi
  else
    configure_client
  fi
}

usage() {
  cat <<'EOF'
rish-mcp installer

  curl -fsSL https://rish-mcp.turin.my | sh                 interactive menu
  curl -fsSL https://rish-mcp.turin.my | sh -s -- [action] [options]

Actions:
  server                 install or update the relay container (default with --yes)
  client                 create an MCP client configuration

Options:
  --port <n>             relay host port (default 8080, env RISH_MCP_RELAY_PORT)
  --public-url <url>     public https URL of the relay, e.g. https://mcp.example.com
  --url <url>            MCP endpoint for the client config (env RISH_MCP_URL)
  --token <token>        AI_TOKEN for the client config
  -y, --yes              take every default, ask nothing (env RISH_MCP_YES=1)
  -h, --help             show this help

Environment: AI_TOKEN, DEVICE_TOKEN, RISH_MCP_RELAY_IMAGE, RISH_MCP_RELAY_NAME,
RISH_MCP_RELAY_CONFIG_DIR, NO_COLOR.
EOF
}

# --- output ---

say() { printf '%s\\n' "$*"; }
dim() { printf '%s%s%s' "$C_DIM" "$*" "$C_RESET"; }
heading() { printf '\\n%s%s%s\\n' "$C_BOLD$C_ACCENT" "$*" "$C_RESET"; }
good() { printf '%s✓ %s%s\\n' "$C_GOOD" "$*" "$C_RESET"; }
warn() { printf '%s! %s%s\\n' "$C_BAD" "$*" "$C_RESET" >&2; }
die() { printf '%s✗ %s%s\\n' "\${C_BAD:-}" "$*" "\${C_RESET:-}" >&2; exit 1; }
need_value() { [ $# -ge 2 ] && [ -n "$2" ] || die "$1 needs a value"; }

# --- prompts (no-ops returning the default when not interactive) ---

# ask LABEL [DEFAULT] -> REPLY
ask() {
  REPLY="\${2:-}"
  [ "$INTERACTIVE" = 1 ] || return 0
  if [ -n "\${2:-}" ]; then
    printf '%s %s ' "$1" "$(dim "[$2]")"
  else
    printf '%s ' "$1"
  fi
  IFS= read -r _answer </dev/tty || _answer=""
  [ -z "$_answer" ] || REPLY="$_answer"
}

# ask_secret LABEL -> REPLY (input not echoed)
ask_secret() {
  REPLY=""
  [ "$INTERACTIVE" = 1 ] || return 0
  printf '%s ' "$1"
  stty -echo </dev/tty 2>/dev/null || true
  IFS= read -r REPLY </dev/tty || REPLY=""
  stty echo </dev/tty 2>/dev/null || true
  printf '\\n'
}

# confirm LABEL y|n -> exit status
confirm() {
  if [ "$INTERACTIVE" != 1 ]; then
    [ "$2" = y ]
    return
  fi
  if [ "$2" = y ]; then _hint="[Y/n]"; else _hint="[y/N]"; fi
  printf '%s %s ' "$1" "$(dim "$_hint")"
  IFS= read -r _answer </dev/tty || _answer=""
  case "$_answer" in
    [Yy]*) return 0 ;;
    [Nn]*) return 1 ;;
    *) [ "$2" = y ] ;;
  esac
}

# --- helpers ---

valid_port() {
  case "$1" in
    ''|*[!0-9]*) return 1 ;;
  esac
  [ "$1" -ge 1 ] && [ "$1" -le 65535 ]
}

valid_url() {
  case "$1" in
    http://?*|https://?*) ;;
    *) return 1 ;;
  esac
  case "$1" in
    *[[:space:]]*) return 1 ;;
  esac
}

no_newline() {
  case "$2" in
    *"
"*) die "$1 must not contain newlines" ;;
  esac
}

new_token() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex 24
  elif [ -r /dev/urandom ] && command -v od >/dev/null 2>&1; then
    od -An -N24 -tx1 /dev/urandom | tr -d ' \\n'
  else
    die "openssl or /dev/urandom is required to create relay credentials"
  fi
}

# env_get KEY FILE -> value of the first KEY=... line
env_get() {
  [ -r "$2" ] || return 0
  sed -n "s/^$1=//p" "$2" | head -n 1
}

http_ok() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsS -o /dev/null --max-time 2 "$1" 2>/dev/null
  elif command -v wget >/dev/null 2>&1; then
    wget -q -O /dev/null -T 2 "$1" 2>/dev/null
  else
    return 2
  fi
}

json_escape() {
  printf '%s' "$1" | sed 's/\\\\/\\\\\\\\/g; s/"/\\\\"/g'
}

mask() {
  printf '%s…' "$(printf '%s' "$1" | cut -c1-6)"
}

# --- relay server ---

install_server() {
  heading "Relay server"

  command -v docker >/dev/null 2>&1 ||
    die "Docker is required. Install Docker Engine or Docker Desktop and run this again."
  docker info >/dev/null 2>&1 ||
    die "Cannot reach the Docker daemon. Start it, or add your user to the docker group (or rerun with sudo)."

  umask 077
  mkdir -p "$CONFIG_DIR"

  old_ai="$(env_get AI_TOKEN "$ENV_FILE")"
  old_device="$(env_get DEVICE_TOKEN "$ENV_FILE")"
  old_public="$(env_get PUBLIC_URL "$ENV_FILE")"
  old_port="$(env_get RELAY_PORT "$ENV_FILE")"

  port="\${ARG_PORT:-\${RISH_MCP_RELAY_PORT:-\${old_port:-8080}}}"
  while :; do
    ask "Host port for the relay" "$port"
    if valid_port "$REPLY"; then port="$REPLY"; break; fi
    [ "$INTERACTIVE" = 1 ] || die "invalid port: $REPLY"
    warn "enter a port between 1 and 65535"
  done

  public_url="\${ARG_PUBLIC_URL:-$old_public}"
  if [ "$INTERACTIVE" = 1 ]; then
    say "$(dim "The public https URL your reverse proxy serves the relay on (needed for OAuth clients like claude.ai).")"
    say "$(dim "Leave empty for local use; you can rerun this later.")"
  fi
  while :; do
    ask "Public URL" "$public_url"
    public_url="\${REPLY%/}"
    if [ -z "$public_url" ] || valid_url "$public_url"; then break; fi
    [ "$INTERACTIVE" = 1 ] || die "invalid public URL: $public_url"
    warn "enter an absolute http(s) URL such as https://mcp.example.com"
  done

  # AI_TOKEN / DEVICE_TOKEN from the environment win; otherwise keep what the
  # last run saved, so re-running never silently breaks paired phones.
  if [ -z "\${AI_TOKEN:-}\${DEVICE_TOKEN:-}" ] && [ -n "$old_ai$old_device" ] &&
    ! confirm "Keep the existing tokens from $ENV_FILE?" y; then
    warn "new tokens: paired phones and MCP clients must be updated with them"
    old_ai=""
    old_device=""
  fi
  ai_token="\${AI_TOKEN:-\${old_ai:-$(new_token)}}"
  device_token="\${DEVICE_TOKEN:-\${old_device:-$(new_token)}}"
  no_newline AI_TOKEN "$ai_token"
  no_newline DEVICE_TOKEN "$device_token"

  say ""
  say "  image        $IMAGE"
  say "  container    $CONTAINER_NAME"
  say "  port         $port"
  say "  public URL   \${public_url:-(not set, local only)}"
  say "  credentials  $ENV_FILE"
  if docker container inspect "$CONTAINER_NAME" >/dev/null 2>&1; then
    say "  $(dim "an existing $CONTAINER_NAME container will be replaced")"
  fi
  confirm "Install the relay now?" y || die "cancelled"

  {
    printf 'AI_TOKEN=%s\\n' "$ai_token"
    printf 'DEVICE_TOKEN=%s\\n' "$device_token"
    [ -z "$public_url" ] || printf 'PUBLIC_URL=%s\\n' "$public_url"
    printf 'RELAY_PORT=%s\\n' "$port"
  } >"$ENV_FILE"
  chmod 600 "$ENV_FILE"

  say ""
  say "$(dim "Pulling $IMAGE...")"
  docker pull "$IMAGE"

  # Replace only the named container created by this installer so re-running
  # it upgrades the relay without touching unrelated Docker workloads.
  if docker container inspect "$CONTAINER_NAME" >/dev/null 2>&1; then
    docker rm -f "$CONTAINER_NAME" >/dev/null
  fi

  docker run --detach \\
    --name "$CONTAINER_NAME" \\
    --restart unless-stopped \\
    --publish "$port:8080" \\
    --env-file "$ENV_FILE" \\
    --env PORT=8080 \\
    --read-only \\
    --tmpfs /tmp:size=64m,noexec,nosuid,nodev \\
    --cap-drop ALL \\
    --security-opt no-new-privileges:true \\
    --log-opt max-size=10m \\
    --log-opt max-file=3 \\
    "$IMAGE" >/dev/null

  health="http://127.0.0.1:$port/healthz"
  healthy=0
  i=0
  while [ $i -lt 15 ]; do
    status=0
    http_ok "$health" || status=$?
    [ "$status" -ne 0 ] || { healthy=1; break; }
    [ "$status" -ne 2 ] || { healthy=2; break; }
    i=$((i + 1))
    sleep 1
  done

  say ""
  case "$healthy" in
    1) good "relay is running ($health)" ;;
    2) good "relay container started (no curl/wget to check $health)" ;;
    *) warn "relay did not answer $health yet; check: docker logs $CONTAINER_NAME" ;;
  esac
  base="\${public_url:-http://127.0.0.1:$port}"
  say "  MCP endpoint      $base/mcp"
  say "  Android endpoint  $base/agent"
  say "  credentials       $ENV_FILE $(dim "(mode 600)")"
  [ -n "$public_url" ] || say "  $(dim "Put a TLS reverse proxy in front of port $port before using it from a phone or remote AI client.")"

  if [ "$INTERACTIVE" = 1 ] && confirm "Show the DEVICE_TOKEN for the Android app?" n; then
    say "  DEVICE_TOKEN  $device_token"
  fi

  RELAY_BASE="$base"
}

# --- MCP client ---

configure_client() {
  heading "MCP client"

  default_url="$ARG_URL"
  if [ -z "$default_url" ]; then
    if [ -n "\${RELAY_BASE:-}" ]; then
      default_url="$RELAY_BASE/mcp"
    elif [ -n "$(env_get PUBLIC_URL "$ENV_FILE")" ]; then
      default_url="$(env_get PUBLIC_URL "$ENV_FILE")/mcp"
    else
      default_url="http://127.0.0.1:\${RISH_MCP_RELAY_PORT:-$(env_get RELAY_PORT "$ENV_FILE")}/mcp"
      [ "$default_url" != "http://127.0.0.1:/mcp" ] || default_url="http://127.0.0.1:8080/mcp"
    fi
  fi
  while :; do
    ask "MCP URL" "$default_url"
    url="$REPLY"
    if valid_url "$url"; then break; fi
    [ "$INTERACTIVE" = 1 ] || die "invalid MCP URL: $url"
    warn "enter an absolute http(s) URL ending in /mcp"
  done

  token="\${ARG_TOKEN:-\${AI_TOKEN:-}}"
  if [ -z "$token" ]; then
    local_token="$(env_get AI_TOKEN "$ENV_FILE")"
    if [ -n "$local_token" ] && confirm "Use the AI_TOKEN from $ENV_FILE ($(mask "$local_token"))?" y; then
      token="$local_token"
    fi
  fi
  while [ -z "$token" ]; do
    [ "$INTERACTIVE" = 1 ] || die "AI_TOKEN is required: pass --token or set AI_TOKEN"
    ask_secret "AI_TOKEN (input hidden):"
    token="$REPLY"
  done
  no_newline AI_TOKEN "$token"

  ask "Server name in the client" "phone"
  name="$REPLY"

  umask 077
  mkdir -p "$CONFIG_DIR"
  json_name="$(json_escape "$name")"
  json_url="$(json_escape "$url")"
  json_token="$(json_escape "$token")"
  cat >"$CLIENT_FILE" <<EOF
{
  "mcpServers": {
    "$json_name": {
      "type": "http",
      "url": "$json_url",
      "headers": {
        "Authorization": "Bearer $json_token"
      }
    }
  }
}
EOF
  chmod 600 "$CLIENT_FILE"
  say ""
  good "client configuration saved to $CLIENT_FILE"

  # Only offered interactively: --yes never edits another tool's config.
  if [ "$INTERACTIVE" = 1 ] && command -v claude >/dev/null 2>&1 &&
    confirm "Add it to Claude Code now (claude mcp add)?" y; then
    if claude mcp add --transport http "$name" "$url" --header "Authorization: Bearer $token"; then
      good "added \\"$name\\" to Claude Code"
    else
      warn "claude mcp add failed; use $CLIENT_FILE instead"
    fi
  else
    say "$(dim "Copy the mcpServers entry from $CLIENT_FILE into your MCP client, or for Claude Code:")"
    say "  claude mcp add --transport http $name $url --header \\"Authorization: Bearer \\$AI_TOKEN\\""
  fi
}

main "$@"
`;

export function GET() {
  return new Response(INSTALLER, {
    headers: {
      "Cache-Control": "no-store",
      "Content-Disposition": 'inline; filename="install.sh"',
      "Content-Type": "text/plain; charset=utf-8",
      "X-Content-Type-Options": "nosniff",
    },
  });
}
