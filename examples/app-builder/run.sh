#!/usr/bin/env bash
set -euo pipefail

# ── Multi-node app builder demo ──────────────────────────────────────────────
#
# Starts a local coord server + 4 daemons, each running one agent.
# The tailbus dashboard shows the full mesh topology with animated
# message flow between nodes.
#
# Usage:
#   ./run.sh                    # start everything
#   ./run.sh stop               # tear it all down
#   ./run.sh fire "a todo app"  # send a build request
#   ./run.sh logs               # watch the agent conversation
#
# Then open a dashboard against any node:
#   tailbus -socket /tmp/appbuilder-orchestrator.sock dashboard
#
# ─────────────────────────────────────────────────────────────────────────────

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
if [ -d "${REPO_ROOT}/bin" ]; then
    export PATH="${REPO_ROOT}/bin:${PATH}"
fi
COORD_ADDR="127.0.0.1:18443"
COORD_HEALTH=":18081"
COORD_DATA="/tmp/appbuilder-coord"
LOG_DIR="/tmp/appbuilder-logs"
OUTPUT_DIR="${OUTPUT_DIR:-${SCRIPT_DIR}/output}"

# Node definitions: name:listen_port:metrics_port:script
NODES="
orchestrator:19443:19091:orchestrator.py
claude-coder:19444:19092:claude_coder.py
codex-coder:19445:19093:codex_coder.py
lmstudio-coder:19446:19094:lmstudio_coder.py
"

DIM="\033[2m"
BOLD="\033[1m"
GREEN="\033[32m"
RED="\033[31m"
CYAN="\033[36m"
YELLOW="\033[33m"
RESET="\033[0m"

say()  { echo -e "  ${DIM}run.sh${RESET}  $*"; }
good() { echo -e "  ${DIM}run.sh${RESET}  ${GREEN}✓${RESET} $*"; }
warn() { echo -e "  ${DIM}run.sh${RESET}  ${YELLOW}!${RESET} $*"; }
fail() { echo -e "  ${DIM}run.sh${RESET}  ${RED}✗${RESET} $*"; exit 1; }

llm_base_url() {
    printf '%s' "${LLM_BASE_URL:-http://localhost:1234/v1}"
}

doctor() {
    echo ""
    echo -e "  ${BOLD}App Builder Doctor${RESET}"
    echo -e "  ${DIM}────────────────────────────────────────${RESET}"
    echo ""
    for tool in tailbus-coord tailbusd tailbus python3 curl; do
        command -v "$tool" >/dev/null 2>&1 || fail "$tool not found in PATH"
        good "$tool found"
    done
    curl -sf "$(llm_base_url)/models" >/dev/null 2>&1 || fail "LM Studio not reachable at $(llm_base_url)"
    good "LM Studio reachable at $(llm_base_url)"
    local model_count
    model_count=$(curl -sf "$(llm_base_url)/models" 2>/dev/null \
        | python3 -c "import json,sys; d=json.load(sys.stdin); print(len(d.get('data',[])))" 2>/dev/null || echo 0)
    [ "$model_count" -gt 0 ] || fail "LM Studio has no models loaded — open LM Studio and load a model"
    good "LM Studio has ${model_count} model(s) loaded"
    if command -v claude >/dev/null 2>&1; then
        good "claude found"
    else
        warn "claude not found — claude-coder will error if selected"
    fi
    if command -v codex >/dev/null 2>&1; then
        good "codex found"
    else
        warn "codex not found — codex-coder will error if selected"
    fi
    echo ""
    echo -e "  ${DIM}Next:${RESET} ./run.sh && ./run.sh fire \"A todo app with HTML, CSS, and vanilla JS\""
    echo ""
}

# ── Stop ─────────────────────────────────────────────────────────────────────

stop_all() {
    say "stopping all app-builder processes..."

    for entry in $NODES; do
        local name="${entry%%:*}"
        local sock="/tmp/appbuilder-${name}.sock"

        # Kill agent (python script using this socket)
        local agent_pids
        agent_pids=$(pgrep -f "TAILBUS_SOCKET=${sock}" 2>/dev/null || true)
        if [ -n "$agent_pids" ]; then
            kill $agent_pids 2>/dev/null || true
        fi

        # Kill daemon
        local daemon_pids
        daemon_pids=$(pgrep -f "tailbusd.*-node-id ${name}" 2>/dev/null || true)
        if [ -n "$daemon_pids" ]; then
            kill $daemon_pids 2>/dev/null || true
        fi
    done

    # Kill coord
    local coord_pids
    coord_pids=$(pgrep -f "tailbus-coord.*${COORD_ADDR}" 2>/dev/null || true)
    if [ -n "$coord_pids" ]; then
        kill $coord_pids 2>/dev/null || true
    fi

    sleep 1

    # Clean up sockets
    rm -f /tmp/appbuilder-*.sock
    rm -f \
        /tmp/tailbusd-orchestrator.coord-fp \
        /tmp/tailbusd-claude-coder.coord-fp \
        /tmp/tailbusd-codex-coder.coord-fp \
        /tmp/tailbusd-lmstudio-coder.coord-fp

    good "stopped"
}

clean_all() {
    stop_all 2>/dev/null || true
    rm -rf "$COORD_DATA" "$LOG_DIR" "$OUTPUT_DIR"
    rm -rf \
        /tmp/tailbusd-orchestrator \
        /tmp/tailbusd-claude-coder \
        /tmp/tailbusd-codex-coder \
        /tmp/tailbusd-lmstudio-coder
    good "cleaned logs, outputs, and persisted state"
}

# ── Fire ─────────────────────────────────────────────────────────────────────

fire_build() {
    local app_spec="$1"
    local sock="/tmp/appbuilder-orchestrator.sock"

    if [ ! -S "$sock" ]; then
        fail "orchestrator not running (no socket at $sock)"
    fi

    say "firing build request..."
    echo ""
    tailbus -socket "$sock" fire orchestrator "{\"command\":\"build\",\"arguments\":{\"app\":\"${app_spec}\"}}"
}

# ── Logs ─────────────────────────────────────────────────────────────────────

watch_logs() {
    if [ ! -d "$LOG_DIR" ]; then
        fail "no logs found — is the demo running?"
    fi

    echo ""
    echo -e "  ${BOLD}Watching agent conversation${RESET}  ${DIM}(ctrl-c to stop)${RESET}"
    echo -e "  ${DIM}────────────────────────────────────────${RESET}"
    echo ""

    tail -f \
        "$LOG_DIR/agent-orchestrator.log" \
        "$LOG_DIR/agent-claude-coder.log" \
        "$LOG_DIR/agent-codex-coder.log" \
        "$LOG_DIR/agent-lmstudio-coder.log" \
        2>/dev/null
}

launch_dashboard() {
    local sock="/tmp/appbuilder-orchestrator.sock"
    if [ ! -S "$sock" ]; then
        fail "orchestrator not running (no socket at $sock)"
    fi
    exec tailbus -socket "$sock" dashboard
}

# ── Start ────────────────────────────────────────────────────────────────────

start_all() {
    doctor >/dev/null

    # Stop anything already running
    stop_all 2>/dev/null || true

    mkdir -p "$LOG_DIR" "$COORD_DATA" "$OUTPUT_DIR"

    echo ""
    echo -e "  ${BOLD}App Builder — Multi-Node Demo${RESET}"
    echo -e "  ${DIM}────────────────────────────────────────${RESET}"
    echo ""

    # ── Start coord ──

    say "starting coord on ${CYAN}${COORD_ADDR}${RESET}..."

    tailbus-coord \
        -listen "$COORD_ADDR" \
        -data-dir "$COORD_DATA" \
        -health-addr "$COORD_HEALTH" \
        > "$LOG_DIR/coord.log" 2>&1 &

    local retries=0
    while ! curl -sf "http://127.0.0.1${COORD_HEALTH}/healthz" >/dev/null 2>&1; do
        retries=$((retries + 1))
        if [ $retries -gt 30 ]; then
            fail "coord didn't start — check $LOG_DIR/coord.log"
        fi
        sleep 0.2
    done
    good "coord ready"

    # ── Start daemons ──

    for entry in $NODES; do
        local name listen_port metrics_port script
        IFS=: read -r name listen_port metrics_port script <<< "$entry"
        local sock="/tmp/appbuilder-${name}.sock"

        say "starting daemon ${CYAN}${name}${RESET} on :${listen_port}..."

        tailbusd \
            -node-id "$name" \
            -coord "$COORD_ADDR" \
            -advertise "127.0.0.1:${listen_port}" \
            -listen ":${listen_port}" \
            -socket "$sock" \
            -metrics ":${metrics_port}" \
            > "$LOG_DIR/daemon-${name}.log" 2>&1 &

        retries=0
        while [ ! -S "$sock" ]; do
            retries=$((retries + 1))
            if [ $retries -gt 30 ]; then
                fail "daemon ${name} didn't start — check $LOG_DIR/daemon-${name}.log"
            fi
            sleep 0.2
        done
        good "daemon ${BOLD}${name}${RESET} ready"
    done

    # ── Start agents ──

    echo ""
    say "starting agents..."

    for entry in $NODES; do
        local name listen_port metrics_port script
        IFS=: read -r name listen_port metrics_port script <<< "$entry"
        local sock="/tmp/appbuilder-${name}.sock"

        TAILBUS_SOCKET="$sock" python3 "${SCRIPT_DIR}/${script}" \
            > "$LOG_DIR/agent-${name}.log" 2>&1 &

        good "agent ${BOLD}@${name}${RESET}"
    done

    # Give agents a moment to register
    sleep 1

    echo ""
    echo -e "  ${DIM}────────────────────────────────────────${RESET}"
    echo -e "  ${GREEN}All running.${RESET} 1 coord + 4 daemons + 4 agents"
    echo ""
    echo -e "  ${BOLD}Dashboard:${RESET}"
    echo -e "    tailbus -socket /tmp/appbuilder-orchestrator.sock dashboard"
    echo ""
    echo -e "  ${BOLD}Build an app:${RESET}"
    echo -e "    ./run.sh fire \"A todo app with HTML, CSS, and vanilla JS\""
    echo ""
    echo -e "  ${BOLD}Watch the conversation:${RESET}"
    echo -e "    ./run.sh logs"
    echo ""
    echo -e "  ${BOLD}Stop everything:${RESET}"
    echo -e "    ./run.sh stop"
    echo ""
}

# ── Main ─────────────────────────────────────────────────────────────────────

case "${1:-start}" in
    stop)
        stop_all
        ;;
    clean)
        clean_all
        ;;
    doctor)
        doctor
        ;;
    dashboard)
        launch_dashboard
        ;;
    fire)
        if [ -z "${2:-}" ]; then
            fail "usage: ./run.sh fire \"description of the app\""
        fi
        fire_build "$2"
        ;;
    logs|watch)
        watch_logs
        ;;
    demo)
        start_all
        echo ""
        fire_build "A todo app with HTML, CSS, and vanilla JS. Local storage, add/delete/toggle."
        ;;
    start|"")
        start_all
        ;;
    *)
        echo "usage: ./run.sh [start|stop|clean|doctor|dashboard|fire \"app description\"|logs|demo]"
        exit 1
        ;;
esac
