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
  fi
  setup_ui

  if [ -z "$ACTION" ]; then
    if [ "$INTERACTIVE" = 1 ]; then
      banner
      choose "What do you want to do?" 1 \\
        "Install or update the relay server" \\
        "Configure an MCP client" \\
        "Exit"
      case "$REPLY" in
        1) ACTION=server ;;
        2) ACTION=client ;;
        *) return 0 ;;
      esac
    else
      ACTION=server
    fi
  fi

  if [ "$ACTION" = server ]; then
    install_server
    if [ "$INTERACTIVE" = 1 ] && confirm "Configure an MCP client now?" y; then
      configure_client
    fi
  else
    configure_client
  fi

  [ "$INTERACTIVE" != 1 ] || printf '\\n%s✻%s %sAll set.%s\\n\\n' "$A" "$R" "$B" "$R"
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

# --- terminal UI ---
#
# FANCY=1 (a real terminal on both ends) gets the boxed prompts, arrow-key
# menus and spinners; anything else gets plain lines and numbered prompts.

setup_ui() {
  ESC="$(printf '\\033')"
  CR="$(printf '\\r')"
  NL='
'
  FANCY=0
  if [ "$INTERACTIVE" = 1 ] && [ -t 1 ] && [ "\${TERM:-dumb}" != dumb ]; then
    FANCY=1
  fi
  if [ -t 1 ] && [ -z "\${NO_COLOR:-}" ] && [ "\${TERM:-dumb}" != dumb ]; then
    R="$ESC[0m" B="$ESC[1m" D="$ESC[2m" G="$ESC[32m" RED="$ESC[31m"
    case "\${COLORTERM:-}" in
      truecolor|24bit) A="$ESC[38;2;215;119;87m" ;;
      *) A="$ESC[38;5;173m" ;;
    esac
  else
    R="" B="" D="" G="" RED="" A=""
  fi

  COLS=80
  if [ "$FANCY" = 1 ]; then
    _size="$(stty size </dev/tty 2>/dev/null || true)"
    COLS="\${_size#* }"
    case "$COLS" in ''|*[!0-9]*) COLS=80 ;; esac
  fi
  BOX=$((COLS - 2))
  [ "$BOX" -le 68 ] || BOX=68
  [ "$BOX" -ge 32 ] || BOX=32

  SPIN_PID=""
  STTY_SAVED=""
  if [ "$INTERACTIVE" = 1 ]; then
    STTY_SAVED="$(stty -g </dev/tty 2>/dev/null || true)"
  fi
  trap 'restore_tty' EXIT
  trap 'restore_tty; printf "\\n"; exit 130' INT TERM
}

restore_tty() {
  [ -z "\${SPIN_PID:-}" ] || kill "$SPIN_PID" 2>/dev/null || true
  [ -z "\${STTY_SAVED:-}" ] || stty "$STTY_SAVED" </dev/tty 2>/dev/null || true
  [ "\${FANCY:-0}" != 1 ] || printf '%s' "$ESC[?25h"
}

rep() {
  _out="" _count="$2"
  while [ "$_count" -gt 0 ]; do _out="$_out$1"; _count=$((_count - 1)); done
  printf '%s' "$_out"
}

# fit TEXT WIDTH: cut or pad (ASCII) text to exactly WIDTH columns
fit() {
  _text="$(printf '%s' "$1" | cut -c "1-$2")"
  printf '%s' "$_text"
  rep ' ' $(($2 - \${#_text}))
}

# clip TEXT WIDTH: cut ASCII text to at most WIDTH columns, marking the cut
clip() {
  if [ "\${#1}" -le "$2" ]; then
    printf '%s' "$1"
  else
    printf '%s...' "$(printf '%s' "$1" | cut -c "1-$(($2 - 3))")"
  fi
}

tilde() {
  case "$1" in
    "$HOME"/*) printf '~%s' "\${1#"$HOME"}" ;;
    *) printf '%s' "$1" ;;
  esac
}

banner() {
  if [ "$FANCY" != 1 ]; then
    printf '\\n%s✻ rish-mcp setup%s\\n' "$B$A" "$R"
    return
  fi
  _w=$((BOX - 4))
  printf '\\n%s╭%s╮%s\\n' "$A" "$(rep ─ $((BOX - 2)))" "$R"
  printf '%s│%s %s✻%s %s%s%s %s│%s\\n' "$A" "$R" "$A" "$R" "$B" "$(fit "Welcome to rish-mcp setup" $((_w - 2)))" "$R" "$A" "$R"
  printf '%s│%s %s %s│%s\\n' "$A" "$R" "$(fit "" "$_w")" "$A" "$R"
  printf '%s│%s %s%s%s %s│%s\\n' "$A" "$R" "$D" "$(fit "  Android shell over MCP, self-hosted relay" "$_w")" "$R" "$A" "$R"
  printf '%s│%s %s%s%s %s│%s\\n' "$A" "$R" "$D" "$(fit "  config: $(tilde "$CONFIG_DIR")" "$_w")" "$R" "$A" "$R"
  printf '%s╰%s╯%s\\n' "$A" "$(rep ─ $((BOX - 2)))" "$R"
}

# ⏺ headline, then "⎿" detail lines, like a tool call and its result
step() { printf '\\n%s⏺%s %s%s%s\\n' "$A" "$R" "$B" "$*" "$R"; }
sub() { printf '  %s⎿%s  %s\\n' "$D" "$R" "$*"; }
cont() { printf '     %s\\n' "$*"; }
note() { printf '     %s%s%s\\n' "$D" "$*" "$R"; }
kv() { printf '     %s%s%s %s\\n' "$D" "$(fit "$1" 17)" "$R" "$2"; }
kv_first() { printf '  %s⎿%s  %s%s%s %s\\n' "$D" "$R" "$D" "$(fit "$1" 17)" "$R" "$2"; }
warn() { printf '  %s⎿  %s%s\\n' "$RED" "$*" "$R" >&2; }
die() { printf '\\n%s⏺ %s%s\\n' "\${RED:-}" "$*" "\${R:-}" >&2; exit 1; }
need_value() { [ $# -ge 2 ] && [ -n "$2" ] || die "$1 needs a value"; }

# read_key -> up | down | enter | esc | <char>
read_key() {
  _c="$(dd bs=1 count=1 2>/dev/null </dev/tty; printf x)"
  _c="\${_c%x}"
  case "$_c" in
    "$NL"|"$CR") echo enter ;;
    "$ESC")
      stty min 0 time 1 </dev/tty
      _seq="$(dd bs=1 count=2 2>/dev/null </dev/tty || true)"
      stty min 1 time 0 </dev/tty
      case "$_seq" in
        '[A'|OA) echo up ;;
        '[B'|OB) echo down ;;
        '') echo esc ;;
        *) echo other ;;
      esac
      ;;
    *) printf '%s\\n' "$_c" ;;
  esac
}

# One terminal line per entry: the redraw moves the cursor up by line count.
_draw_choices() {
  _idx=1
  for _opt in "$@"; do
    _opt="$(clip "$_opt" $((COLS - 8)))"
    if [ "$_idx" -eq "$_sel" ]; then
      printf '%s\\r %s❯ %s. %s%s\\n' "$ESC[2K" "$A" "$_idx" "$_opt" "$R"
    else
      printf '%s\\r   %s. %s\\n' "$ESC[2K" "$_idx" "$_opt"
    fi
    _idx=$((_idx + 1))
  done
  if [ "$COLS" -ge 52 ]; then
    printf '%s\\r %s↑/↓ to select · enter to confirm · esc to cancel%s\\n' "$ESC[2K" "$D" "$R"
  else
    printf '%s\\r %s↑/↓ · enter · esc%s\\n' "$ESC[2K" "$D" "$R"
  fi
}

# choose QUESTION DEFAULT_INDEX OPTION... -> REPLY (1-based); esc picks the last
choose() {
  _question="$1" _sel="$2"
  shift 2
  _n=$#
  if [ "$FANCY" != 1 ]; then
    printf '\\n %s\\n' "$_question"
    _idx=1
    for _opt in "$@"; do printf '   %s. %s\\n' "$_idx" "$_opt"; _idx=$((_idx + 1)); done
    while :; do
      printf ' ❯ [%s] ' "$_sel"
      IFS= read -r _answer </dev/tty || _answer=""
      [ -n "$_answer" ] || _answer="$_sel"
      case "$_answer" in
        *[!0-9]*) ;;
        *) if [ "$_answer" -ge 1 ] && [ "$_answer" -le "$_n" ]; then REPLY="$_answer"; return 0; fi ;;
      esac
      printf '   choose 1-%s\\n' "$_n"
    done
  fi

  printf '\\n %s%s%s\\n' "$B" "$(clip "$_question" $((COLS - 2)))" "$R"
  printf '%s' "$ESC[?25l"
  _draw_choices "$@"
  stty -icanon -echo min 1 time 0 </dev/tty
  while :; do
    _key="$(read_key)"
    case "$_key" in
      up|k) if [ "$_sel" -le 1 ]; then _sel="$_n"; else _sel=$((_sel - 1)); fi ;;
      down|j) if [ "$_sel" -ge "$_n" ]; then _sel=1; else _sel=$((_sel + 1)); fi ;;
      enter) break ;;
      esc|q) _sel="$_n"; break ;;
      [1-9]) if [ "$_key" -le "$_n" ]; then _sel="$_key"; break; fi ;;
    esac
    printf '%s' "$ESC[$((_n + 1))A"
    _draw_choices "$@"
  done
  stty "$STTY_SAVED" </dev/tty
  printf '%s' "$ESC[?25h"

  # Collapse the menu into the answer.
  eval "_picked=\\\${$_sel}"
  printf '%s%s' "$ESC[$((_n + 2))A" "$ESC[J"
  printf ' %s\\n' "$(clip "$_question" $((COLS - 2)))"
  sub "$(clip "$_picked" $((COLS - 6)))"
  REPLY="$_sel"
}

# confirm QUESTION y|n -> exit status (the default when not interactive)
confirm() {
  if [ "$INTERACTIVE" != 1 ]; then
    [ "$2" = y ]
    return
  fi
  if [ "$2" = y ]; then _def=1; else _def=2; fi
  choose "$1" "$_def" "Yes" "No"
  [ "$REPLY" = 1 ]
}

# ask LABEL [DEFAULT] [HINT] [secret] -> REPLY (the default when not interactive)
ask() {
  REPLY="\${2:-}"
  [ "$INTERACTIVE" = 1 ] || return 0
  _label="$1" _default="\${2:-}" _hint="\${3:-}" _secret="\${4:-}"

  if [ "$FANCY" != 1 ]; then
    [ -z "$_hint" ] || printf '%s\\n' "$_hint"
    if [ -n "$_default" ]; then printf '%s [%s]: ' "$_label" "$_default"; else printf '%s: ' "$_label"; fi
    [ -z "$_secret" ] || stty -echo </dev/tty 2>/dev/null || true
    IFS= read -r _answer </dev/tty || _answer=""
    [ -z "$_secret" ] || { stty echo </dev/tty 2>/dev/null || true; printf '\\n'; }
    [ -z "$_answer" ] || REPLY="$_answer"
    return 0
  fi

  # Every line must fit the terminal: a wrapped line would throw off the
  # cursor-up count that collapses the box afterwards.
  _lines=3
  printf '\\n %s%s%s' "$B" "$_label" "$R"
  if [ -n "$_default" ]; then
    _room=$((COLS - \${#_label} - 16))
    [ "$_room" -ge 8 ] || _room=8
    _shown="$(printf '%s' "$_default" | cut -c "1-$_room")"
    [ "$_shown" = "$_default" ] || _shown="$(printf '%s' "$_shown" | cut -c "1-$((_room - 3))")..."
    printf '  %senter keeps %s%s' "$D" "$_shown" "$R"
  fi
  printf '\\n'
  if [ -n "$_hint" ]; then
    printf ' %s%s%s\\n' "$D" "$(printf '%s' "$_hint" | cut -c "1-$((COLS - 2))")" "$R"
    _lines=4
  fi
  printf '%s╭%s╮%s\\n' "$D" "$(rep ─ $((BOX - 2)))" "$R"
  printf '%s│%s %s>%s %s%s│%s\\n' "$D" "$R" "$A" "$R" "$(rep ' ' $((BOX - 5)))" "$D" "$R"
  printf '%s╰%s╯%s' "$D" "$(rep ─ $((BOX - 2)))" "$R"
  printf '%s\\r%s' "$ESC[1A" "$ESC[4C"
  [ -z "$_secret" ] || stty -echo </dev/tty
  IFS= read -r _answer </dev/tty || _answer=""
  [ -z "$_secret" ] || { stty echo </dev/tty; printf '\\n'; }
  [ -z "$_answer" ] || REPLY="$_answer"

  # Collapse the box into the answer.
  printf '%s%s' "$ESC[\${_lines}A" "$ESC[J"
  printf ' %s\\n' "$_label"
  if [ -n "$_secret" ]; then
    sub "$(mask "$REPLY")"
  else
    sub "\${REPLY:-(none)}"
  fi
}

# spin LABEL COMMAND... : run COMMAND with a spinner; output only on failure
spin() {
  _label="$1"
  shift
  _shown="$(clip "$_label" $((COLS - 14)))"
  _log="$(mktemp "\${TMPDIR:-/tmp}/rish-mcp.XXXXXX")"
  if [ "$FANCY" != 1 ]; then
    printf '%s⏺%s %s\\n' "$A" "$R" "$_label"
    _status=0
    "$@" >"$_log" 2>&1 || _status=$?
  else
    "$@" >"$_log" 2>&1 &
    SPIN_PID=$!
    _tick=0
    # Keys pressed while working must not echo over the spinner line.
    stty -echo -icanon min 0 time 0 </dev/tty
    printf '%s' "$ESC[?25l"
    while kill -0 "$SPIN_PID" 2>/dev/null; do
      case $((_tick / 2 % 6)) in
        0) _frame='·' ;; 1) _frame='✢' ;; 2) _frame='✳' ;;
        3) _frame='✶' ;; 4) _frame='✻' ;; *) _frame='✽' ;;
      esac
      printf '\\r%s%s%s %s… %s(%ss)%s%s' "$A" "$_frame" "$R" "$_shown" "$D" $((_tick / 10)) "$R" "$ESC[K"
      _tick=$((_tick + 1))
      sleep 0.1 2>/dev/null || sleep 1
    done
    _status=0
    wait "$SPIN_PID" || _status=$?
    SPIN_PID=""
    # ...and are dropped rather than answering the next prompt.
    dd bs=256 count=1 </dev/tty >/dev/null 2>&1 || true
    stty "$STTY_SAVED" </dev/tty
    printf '\\r%s%s' "$ESC[K" "$ESC[?25h"
    if [ "$_status" -eq 0 ]; then
      printf '%s⏺%s %s %s(%ss)%s\\n' "$G" "$R" "$_label" "$D" $((_tick / 10)) "$R"
    fi
  fi
  if [ "$_status" -ne 0 ]; then
    printf '%s⏺%s %s\\n' "$RED" "$R" "$_label" >&2
    tail -n 20 "$_log" | sed 's/^/     /' >&2
  fi
  rm -f "$_log"
  return "$_status"
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
    *"$NL"*) die "$1 must not contain newlines" ;;
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

have_http_client() {
  command -v curl >/dev/null 2>&1 || command -v wget >/dev/null 2>&1
}

http_ok() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsS -o /dev/null --max-time 2 "$1" 2>/dev/null
  else
    wget -q -O /dev/null -T 2 "$1" 2>/dev/null
  fi
}

wait_healthy() {
  _try=0
  while [ "$_try" -lt 15 ]; do
    if http_ok "$1"; then return 0; fi
    _try=$((_try + 1))
    sleep 1
  done
  return 1
}

json_escape() {
  printf '%s' "$1" | sed 's/\\\\/\\\\\\\\/g; s/"/\\\\"/g'
}

mask() {
  [ -n "$1" ] || { printf '(none)'; return; }
  printf '%s...' "$(printf '%s' "$1" | cut -c1-6)"
}

# --- relay server ---

install_server() {
  step "Relay server"

  command -v docker >/dev/null 2>&1 ||
    die "Docker is required. Install Docker Engine or Docker Desktop and run this again."
  docker info >/dev/null 2>&1 ||
    die "Cannot reach the Docker daemon. Start it, or add your user to the docker group (or rerun with sudo)."
  sub "Docker is ready"

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
  while :; do
    ask "Public URL" "$public_url" "https URL in front of the relay (needed for OAuth). Empty = local only."
    public_url="\${REPLY%/}"
    if [ -z "$public_url" ] || valid_url "$public_url"; then break; fi
    [ "$INTERACTIVE" = 1 ] || die "invalid public URL: $public_url"
    warn "enter an absolute http(s) URL such as https://mcp.example.com"
  done

  # AI_TOKEN / DEVICE_TOKEN from the environment win; otherwise keep what the
  # last run saved, so re-running never silently breaks paired phones.
  if [ -z "\${AI_TOKEN:-}\${DEVICE_TOKEN:-}" ] && [ -n "$old_ai$old_device" ] &&
    ! confirm "Keep the existing tokens from $(tilde "$ENV_FILE")?" y; then
    warn "new tokens: paired phones and MCP clients must be updated with them"
    old_ai=""
    old_device=""
  fi
  ai_token="\${AI_TOKEN:-\${old_ai:-$(new_token)}}"
  device_token="\${DEVICE_TOKEN:-\${old_device:-$(new_token)}}"
  no_newline AI_TOKEN "$ai_token"
  no_newline DEVICE_TOKEN "$device_token"

  step "Install relay"
  kv_first image "$IMAGE"
  kv container "$CONTAINER_NAME"
  kv port "$port"
  kv "public URL" "\${public_url:-(not set, local only)}"
  kv credentials "$(tilde "$ENV_FILE")"
  if docker container inspect "$CONTAINER_NAME" >/dev/null 2>&1; then
    note "the existing $CONTAINER_NAME container will be replaced"
  fi
  confirm "Do you want to proceed?" y || die "cancelled"
  printf '\\n'

  {
    printf 'AI_TOKEN=%s\\n' "$ai_token"
    printf 'DEVICE_TOKEN=%s\\n' "$device_token"
    [ -z "$public_url" ] || printf 'PUBLIC_URL=%s\\n' "$public_url"
    printf 'RELAY_PORT=%s\\n' "$port"
  } >"$ENV_FILE"
  chmod 600 "$ENV_FILE"

  spin "Pulling $IMAGE" docker pull "$IMAGE" || die "docker pull failed"

  # Replace only the named container created by this installer so re-running
  # it upgrades the relay without touching unrelated Docker workloads.
  if docker container inspect "$CONTAINER_NAME" >/dev/null 2>&1; then
    spin "Removing the old $CONTAINER_NAME" docker rm -f "$CONTAINER_NAME" || die "docker rm failed"
  fi

  spin "Starting $CONTAINER_NAME" docker run --detach \\
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
    "$IMAGE" || die "docker run failed"

  health="http://127.0.0.1:$port/healthz"
  if have_http_client; then
    spin "Waiting for $health" wait_healthy "$health" ||
      warn "no answer yet; check: docker logs $CONTAINER_NAME"
  fi

  base="\${public_url:-http://127.0.0.1:$port}"
  step "Relay is running"
  kv_first "MCP endpoint" "$base/mcp"
  kv "Android endpoint" "$base/agent"
  kv credentials "$(tilde "$ENV_FILE") (mode 600)"
  [ -n "$public_url" ] || note "Put a TLS reverse proxy in front of port $port before using it from a phone or remote AI client."

  if [ "$INTERACTIVE" = 1 ] && confirm "Show the DEVICE_TOKEN for the Android app?" n; then
    sub "DEVICE_TOKEN  $device_token"
  fi

  RELAY_BASE="$base"
}

# --- MCP client ---

configure_client() {
  step "MCP client"

  default_url="$ARG_URL"
  if [ -z "$default_url" ]; then
    saved_public="$(env_get PUBLIC_URL "$ENV_FILE")"
    saved_port="\${RISH_MCP_RELAY_PORT:-$(env_get RELAY_PORT "$ENV_FILE")}"
    if [ -n "\${RELAY_BASE:-}" ]; then
      default_url="$RELAY_BASE/mcp"
    elif [ -n "$saved_public" ]; then
      default_url="$saved_public/mcp"
    else
      default_url="http://127.0.0.1:\${saved_port:-8080}/mcp"
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
    if [ -n "$local_token" ] && confirm "Use the AI_TOKEN saved in $(tilde "$ENV_FILE") ($(mask "$local_token"))?" y; then
      token="$local_token"
    fi
  fi
  while [ -z "$token" ]; do
    [ "$INTERACTIVE" = 1 ] || die "AI_TOKEN is required: pass --token or set AI_TOKEN"
    ask "AI_TOKEN" "" "input is hidden" secret
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
  step "Saved client configuration"
  sub "$(tilde "$CLIENT_FILE") (mode 600)"

  # Only offered interactively: --yes never edits another tool's config.
  if [ "$INTERACTIVE" = 1 ] && command -v claude >/dev/null 2>&1 &&
    confirm "Add \\"$name\\" to Claude Code now (claude mcp add)?" y; then
    step "claude mcp add --transport http $name $url"
    if claude mcp add --transport http "$name" "$url" --header "Authorization: Bearer $token" >/dev/null 2>&1; then
      sub "added \\"$name\\" to Claude Code"
    else
      warn "claude mcp add failed (does \\"$name\\" already exist?); use $(tilde "$CLIENT_FILE") instead"
    fi
  else
    note "Copy its mcpServers entry into your MCP client, or for Claude Code:"
    cont "claude mcp add --transport http $name $url --header \\"Authorization: Bearer \\$AI_TOKEN\\""
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
