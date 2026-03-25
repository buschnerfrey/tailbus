#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
if [ -d "${REPO_ROOT}/bin" ]; then
    export PATH="${REPO_ROOT}/bin:${PATH}"
fi

COORD_ADDR="127.0.0.1:18850"
COORD_HEALTH=":18851"
COORD_DATA="/tmp/clitools-coord"
LOG_DIR="/tmp/clitools-logs"
SOCK="/tmp/clitools.sock"
MCP_PORT=18852
METRICS_PORT=18853
TARGET_DIR="${TARGET_DIR:-${REPO_ROOT}}"
GO_TMPDIR="${TMPDIR:-/tmp}"

DIM="\033[2m"
BOLD="\033[1m"
GREEN="\033[32m"
YELLOW="\033[33m"
RED="\033[31m"
CYAN="\033[36m"
RESET="\033[0m"

say()  { echo -e "  ${DIM}run.sh${RESET}  $*"; }
good() { echo -e "  ${DIM}run.sh${RESET}  ${GREEN}✓${RESET} $*"; }
warn() { echo -e "  ${DIM}run.sh${RESET}  ${YELLOW}!${RESET} $*"; }
fail() { echo -e "  ${DIM}run.sh${RESET}  ${RED}✗${RESET} $*"; exit 1; }

doctor() {
    echo ""
    echo -e "  ${BOLD}CLI Tools Demo — Doctor${RESET}"
    echo -e "  ${DIM}────────────────────────────────────────${RESET}"
    echo ""
    for tool in tailbus-coord tailbusd tailbus; do
        command -v "$tool" >/dev/null 2>&1 || fail "$tool not found — run 'make build' from repo root"
        good "$tool found"
    done
    command -v curl >/dev/null 2>&1 || fail "curl not found"
    good "curl found"
    command -v python3 >/dev/null 2>&1 || fail "python3 not found"
    good "python3 found"
    good "no external LLM or network dependency required"
    echo ""
    echo -e "  ${DIM}Next:${RESET} ./run.sh start"
    echo ""
}

kill_listener_on_port() {
    local port="$1"
    local pids=()
    while IFS= read -r pid; do
        [ -n "$pid" ] && pids+=("$pid")
    done < <(lsof -tiTCP:"${port}" -sTCP:LISTEN 2>/dev/null || true)
    [ "${#pids[@]}" -eq 0 ] && return 0
    kill "${pids[@]}" 2>/dev/null || true
    sleep 0.5
}

stop_all() {
    say "stopping..."
    kill_listener_on_port "${COORD_ADDR##*:}"
    kill_listener_on_port "$COORD_HEALTH"
    kill_listener_on_port "$MCP_PORT"
    kill_listener_on_port "$METRICS_PORT"
    # Kill attach processes (suppress "Terminated" messages)
    pkill -f "tailbus.*clitools.sock.*attach" 2>/dev/null || true
    wait 2>/dev/null || true
    sleep 0.3
    rm -f "$SOCK"
    rm -rf "${GO_TMPDIR}/tailbusd-clitools"*
    good "stopped"
}

clean_all() {
    stop_all 2>/dev/null || true
    rm -rf "$COORD_DATA" "$LOG_DIR"
    good "cleaned"
}

start_all() {
    doctor
    stop_all 2>/dev/null || true
    mkdir -p "$LOG_DIR" "$COORD_DATA"

    echo ""
    echo -e "  ${BOLD}CLI Tools Demo${RESET}"
    echo -e "  ${DIM}────────────────────────────────────────${RESET}"
    echo -e "  ${DIM}Turn any CLI program into a Claude tool${RESET}"
    echo ""

    # Coord
    say "starting coord..."
    tailbus-coord \
        -listen "$COORD_ADDR" \
        -health-addr "$COORD_HEALTH" \
        -data-dir "$COORD_DATA" \
        > "$LOG_DIR/coord.log" 2>&1 &
    sleep 1
    curl -sf "http://127.0.0.1${COORD_HEALTH}/healthz" >/dev/null || fail "coord didn't start"
    good "coord ready"

    # Daemon with MCP gateway
    say "starting daemon with MCP gateway on :${MCP_PORT}..."
    tailbusd \
        -node-id clitools \
        -coord "$COORD_ADDR" \
        -advertise "127.0.0.1:18854" \
        -listen ":18854" \
        -socket "$SOCK" \
        -metrics ":$METRICS_PORT" \
        -mcp ":$MCP_PORT" \
        > "$LOG_DIR/daemon.log" 2>&1 &
    local retries=0
    while [ ! -S "$SOCK" ]; do
        retries=$((retries + 1))
        [ $retries -gt 30 ] && fail "daemon didn't start — check $LOG_DIR/daemon.log"
        sleep 0.2
    done
    good "daemon ready"

    # Attach CLI tools as agents
    say "attaching CLI tools as agents..."

    tailbus -socket "$SOCK" attach --exec \
        --handle search \
        --description "Search code by pattern (grep)" \
        --tag tools --capability code.search \
        --cwd "$TARGET_DIR" \
        -- grep -rn --include="*.go" \
        > "$LOG_DIR/search.log" 2>&1 &
    sleep 0.3

    tailbus -socket "$SOCK" attach --exec \
        --handle readfile \
        --description "Read a file's contents" \
        --tag tools --capability file.read \
        --cwd "$TARGET_DIR" \
        -- cat \
        > "$LOG_DIR/readfile.log" 2>&1 &
    sleep 0.3

    tailbus -socket "$SOCK" attach --exec \
        --handle ls \
        --description "List files in a directory" \
        --tag tools --capability file.list \
        --cwd "$TARGET_DIR" \
        -- ls -1 \
        > "$LOG_DIR/ls.log" 2>&1 &
    sleep 0.3

    tailbus -socket "$SOCK" attach --exec \
        --handle git-log \
        --description "Show recent git commits" \
        --tag tools --capability git.log \
        --cwd "$TARGET_DIR" \
        -- git log --oneline -20 \
        > "$LOG_DIR/git-log.log" 2>&1 &
    sleep 0.3

    tailbus -socket "$SOCK" attach --exec \
        --handle git-diff \
        --description "Show uncommitted changes" \
        --tag tools --capability git.diff \
        --cwd "$TARGET_DIR" \
        -- git diff --stat \
        > "$LOG_DIR/git-diff.log" 2>&1 &
    sleep 0.3

    # Verify all registered
    local ok=0
    for handle in search readfile ls git-log git-diff; do
        if grep -q "registered" "$LOG_DIR/${handle}.log" 2>/dev/null; then
            ok=$((ok + 1))
        fi
    done
    good "${ok} tools registered as MCP agents"

    echo ""
    echo -e "  ${GREEN}Running.${RESET} MCP gateway at ${CYAN}http://localhost:${MCP_PORT}/mcp${RESET}"
    echo ""
    echo -e "  ${BOLD}Add to your Claude/Cursor config:${RESET}"
    echo ""
    echo -e "    ${DIM}{${RESET}"
    echo -e "      ${DIM}\"mcpServers\": {${RESET}"
    echo -e "        ${DIM}\"tailbus\": { \"url\": \"http://localhost:${MCP_PORT}/mcp\" }${RESET}"
    echo -e "      ${DIM}}${RESET}"
    echo -e "    ${DIM}}${RESET}"
    echo ""
    echo -e "  ${DIM}Or try:${RESET} ./run.sh fire"
    echo ""
}

mcp_call() {
    local tool="$1"
    local message="$2"
    local id="${3:-1}"
    curl -s "http://localhost:${MCP_PORT}/mcp" \
        -d "{\"jsonrpc\":\"2.0\",\"id\":${id},\"method\":\"tools/call\",\"params\":{\"name\":\"${tool}\",\"arguments\":{\"message\":\"${message}\"}}}" \
        | python3 -c "import sys,json; r=json.load(sys.stdin); print(r.get('result',{}).get('content',[{}])[0].get('text','(no output)'))" 2>/dev/null
}

fire_demo() {
    [ -S "$SOCK" ] || fail "not running — try ./run.sh start first"

    echo ""
    echo -e "  ${BOLD}CLI Tools Demo${RESET}"
    echo -e "  ${DIM}────────────────────────────────────────${RESET}"
    echo -e "  ${DIM}Each tool is a one-liner: tailbus attach --exec --handle <name> -- <command>${RESET}"
    echo ""

    # List tools
    echo -e "  ${CYAN}→ tools/list${RESET}  (what Claude sees)"
    echo -e "  ${DIM}────────────────────────────────────────${RESET}"
    curl -s "http://localhost:${MCP_PORT}/mcp" \
        -d '{"jsonrpc":"2.0","id":0,"method":"tools/list","params":{}}' \
        | python3 -c "
import sys, json
tools = json.load(sys.stdin)['result']['tools']
for t in tools:
    print(f'    {t[\"name\"]:12s}  {t[\"description\"]}')
" 2>/dev/null
    echo ""

    # Search
    echo -e "  ${CYAN}→ search${RESET}  \"runExecAttach\""
    echo -e "  ${DIM}────────────────────────────────────────${RESET}"
    mcp_call search "runExecAttach" 1 | head -5 | sed 's/^/    /'
    echo ""

    # Read file
    echo -e "  ${CYAN}→ readfile${RESET}  \"Makefile\""
    echo -e "  ${DIM}────────────────────────────────────────${RESET}"
    mcp_call readfile "Makefile" 2 | head -8 | sed 's/^/    /'
    echo ""

    # List directory
    echo -e "  ${CYAN}→ ls${RESET}  \"examples/\""
    echo -e "  ${DIM}────────────────────────────────────────${RESET}"
    mcp_call ls "examples/" 3 | sed 's/^/    /'
    echo ""

    # Git log
    echo -e "  ${CYAN}→ git-log${RESET}  (recent commits)"
    echo -e "  ${DIM}────────────────────────────────────────${RESET}"
    mcp_call git-log "" 4 | head -10 | sed 's/^/    /'
    echo ""

    # Git diff
    echo -e "  ${CYAN}→ git-diff${RESET}  (uncommitted changes)"
    echo -e "  ${DIM}────────────────────────────────────────${RESET}"
    local diff_output
    diff_output=$(mcp_call git-diff "" 5)
    if [ -z "$diff_output" ] || [ "$diff_output" = "(no output)" ]; then
        echo "    (clean working tree)"
    else
        echo "$diff_output" | head -10 | sed 's/^/    /'
    fi
    echo ""

    echo -e "  ${GREEN}Done.${RESET} 5 CLI tools → 5 Claude tools. Each was one line."
    echo ""
}

dashboard() {
    [ -S "$SOCK" ] || fail "not running"
    exec tailbus -socket "$SOCK" dashboard
}

logs() {
    [ -d "$LOG_DIR" ] || fail "no logs — is the demo running?"
    tail -f "$LOG_DIR"/*.log 2>/dev/null
}

case "${1:-}" in
    doctor)    doctor ;;
    start)     start_all ;;
    stop)      stop_all ;;
    clean)     clean_all ;;
    fire)      fire_demo ;;
    dashboard) dashboard ;;
    logs)      logs ;;
    demo)
        start_all
        fire_demo
        stop_all
        ;;
    *)
        cat <<EOF

  CLI Tools Demo — turn any CLI program into a Claude/Cursor tool

  Usage: ./run.sh <command>

    doctor      Check prerequisites
    start       Start coord + daemon + attach CLI tools
    fire        Call each tool via MCP (requires start)
    dashboard   Open TUI dashboard
    logs        Tail all logs
    stop        Stop everything
    clean       Stop and remove all state
    demo        start → fire → stop (unattended)

EOF
        ;;
esac
