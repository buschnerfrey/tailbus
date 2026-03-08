#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
if [ -d "${REPO_ROOT}/bin" ]; then
    export PATH="${REPO_ROOT}/bin:${PATH}"
fi

COORD_ADDR="127.0.0.1:18643"
COORD_HEALTH=":18681"
COORD_DATA="/tmp/chatroom-coord"
LOG_DIR="/tmp/chatroom-logs"
GO_TMPDIR="${TMPDIR:-/tmp}"

NODES="
control-node:19643:19211
atlas-node:19644:19212
nova-node:19645:19213
"

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

llm_base_url() {
    printf '%s' "${LLM_BASE_URL:-http://127.0.0.1:1234/v1}"
}

doctor() {
    echo ""
    echo -e "  ${BOLD}Chat Room Doctor${RESET}"
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
    echo ""
}

kill_pid_list() {
    local sig="$1"
    shift || true
    if [ "$#" -gt 0 ]; then
        kill "-${sig}" "$@" 2>/dev/null || true
    fi
}

wait_for_pids_to_exit() {
    local deadline_secs="${1:-3}"
    shift || true
    local end=$((SECONDS + deadline_secs))
    while [ "$#" -gt 0 ] && [ "$SECONDS" -lt "$end" ]; do
        local remaining=()
        local pid
        for pid in "$@"; do
            if kill -0 "$pid" 2>/dev/null; then
                remaining+=("$pid")
            fi
        done
        if [ "${#remaining[@]}" -eq 0 ]; then
            return 0
        fi
        sleep 0.2
        set -- "${remaining[@]}"
    done
    return 1
}

kill_listener_on_port() {
    local port="$1"
    local pids=()
    while IFS= read -r pid; do
        [ -n "$pid" ] && pids+=("$pid")
    done < <(lsof -tiTCP:"${port}" -sTCP:LISTEN 2>/dev/null || true)
    if [ "${#pids[@]}" -eq 0 ]; then
        return 0
    fi
    kill_pid_list TERM "${pids[@]}"
    if ! wait_for_pids_to_exit 3 "${pids[@]}"; then
        kill_pid_list KILL "${pids[@]}"
    fi
}

kill_processes_matching() {
    local pattern="$1"
    local pids=()
    while IFS= read -r pid; do
        [ -n "$pid" ] && pids+=("$pid")
    done < <(pgrep -f "$pattern" 2>/dev/null || true)
    if [ "${#pids[@]}" -eq 0 ]; then
        return 0
    fi
    kill_pid_list TERM "${pids[@]}"
    if ! wait_for_pids_to_exit 3 "${pids[@]}"; then
        kill_pid_list KILL "${pids[@]}"
    fi
}

stop_all() {
    say "stopping all chat-room processes..."
    kill_processes_matching "${SCRIPT_DIR}/chat_agent.py"
    kill_processes_matching "${SCRIPT_DIR}/chat.py"
    for entry in $NODES; do
        local name listen_port metrics_port
        IFS=: read -r name listen_port metrics_port <<< "$entry"
        kill_listener_on_port "${listen_port}"
        kill_listener_on_port "${metrics_port}"
    done
    kill_processes_matching "tailbus-coord.*${COORD_ADDR}"
    kill_listener_on_port 18643
    kill_listener_on_port 18681
    sleep 1
    rm -f /tmp/chatroom-*.sock
    for entry in $NODES; do
        local name listen_port metrics_port
        IFS=: read -r name listen_port metrics_port <<< "$entry"
        rm -f "${GO_TMPDIR}/tailbusd-${name}.coord-fp"
    done
    good "stopped"
}

watch_logs() {
    if [ ! -d "$LOG_DIR" ]; then
        fail "no logs found — is the demo running?"
    fi
    echo ""
    echo -e "  ${BOLD}Watching chat-room logs${RESET}  ${DIM}(ctrl-c to stop)${RESET}"
    echo -e "  ${DIM}────────────────────────────────────────${RESET}"
    echo ""
    tail -f "$LOG_DIR"/*.log 2>/dev/null
}

launch_dashboard() {
    local sock="/tmp/chatroom-control-node.sock"
    if [ ! -S "$sock" ]; then
        fail "control node not running (no socket at $sock)"
    fi
    exec tailbus -socket "$sock" dashboard
}

start_all() {
    doctor >/dev/null
    stop_all 2>/dev/null || true
    mkdir -p "$LOG_DIR" "$COORD_DATA"

    echo ""
    echo -e "  ${BOLD}Chat Room${RESET}"
    echo -e "  ${DIM}────────────────────────────────────────${RESET}"
    echo ""

    say "starting coord on ${CYAN}${COORD_ADDR}${RESET}..."
    tailbus-coord \
        -listen "${COORD_ADDR}" \
        -health-addr "${COORD_HEALTH}" \
        -data-dir "${COORD_DATA}" \
        > "${LOG_DIR}/coord.log" 2>&1 &
    sleep 1
    curl -sf "http://127.0.0.1${COORD_HEALTH}/healthz" >/dev/null || fail "coord didn't start — check ${LOG_DIR}/coord.log"
    good "coord ready"

    for entry in $NODES; do
        local name listen_port metrics_port
        IFS=: read -r name listen_port metrics_port <<< "$entry"
        local sock="/tmp/chatroom-${name}.sock"
        say "starting daemon ${BOLD}${name}${RESET} on :${listen_port}..."
        tailbusd \
            -listen ":${listen_port}" \
            -metrics ":${metrics_port}" \
            -socket "${sock}" \
            -coord "${COORD_ADDR}" \
            -node-id "${name}" \
            -advertise "127.0.0.1:${listen_port}" \
            > "${LOG_DIR}/daemon-${name}.log" 2>&1 &
        local retries=0
        while [ ! -S "$sock" ]; do
            retries=$((retries + 1))
            if [ $retries -gt 30 ]; then
                fail "daemon ${name} didn't start — check ${LOG_DIR}/daemon-${name}.log"
            fi
            sleep 0.2
        done
        good "daemon ${name} ready"
    done

    say "starting chat agents..."
    TAILBUS_SOCKET="/tmp/chatroom-atlas-node.sock" \
        AGENT_HANDLE=atlas AGENT_STYLE=analytical \
        LLM_BASE_URL="$(llm_base_url)" LLM_MODEL="${LLM_MODEL:-}" \
        python3 "${SCRIPT_DIR}/chat_agent.py" \
        > "${LOG_DIR}/agent-atlas.log" 2>&1 &

    TAILBUS_SOCKET="/tmp/chatroom-nova-node.sock" \
        AGENT_HANDLE=nova AGENT_STYLE=creative \
        LLM_BASE_URL="$(llm_base_url)" LLM_MODEL="${LLM_MODEL:-}" \
        python3 "${SCRIPT_DIR}/chat_agent.py" \
        > "${LOG_DIR}/agent-nova.log" 2>&1 &

    sleep 2
    for log in "${LOG_DIR}/agent-atlas.log" "${LOG_DIR}/agent-nova.log"; do
        grep -q "ready" "$log" || fail "agent failed to start — check $log"
    done

    echo ""
    echo -e "  ${GREEN}All running.${RESET} 1 coord + 3 daemons + 2 chat agents"
    echo -e "  ${DIM}Open another terminal for:${RESET} ./run.sh dashboard"
    echo -e "  ${DIM}Then run:${RESET} ./run.sh chat"
    echo ""
}

launch_chat() {
    local sock="/tmp/chatroom-control-node.sock"
    [ -S "$sock" ] || fail "control node not running — start first with ./run.sh start"
    TAILBUS_SOCKET="$sock" CHAT_HANDLE="${CHAT_HANDLE:-you}" \
        exec python3 "${SCRIPT_DIR}/chat.py"
}

case "${1:-start}" in
    start) start_all ;;
    stop) stop_all ;;
    logs) watch_logs ;;
    doctor) doctor ;;
    dashboard) launch_dashboard ;;
    chat) launch_chat ;;
    *)
        cat <<EOF
Usage: ./run.sh [start|stop|logs|doctor|dashboard|chat]

  start      start coord, daemons, and chat agents
  chat       open the interactive chat terminal
  dashboard  launch the TUI dashboard
  logs       tail agent logs
  stop       stop everything
EOF
        exit 1
        ;;
esac
