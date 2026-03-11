#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
if [ -d "${REPO_ROOT}/bin" ]; then
    export PATH="${REPO_ROOT}/bin:${PATH}"
fi

COORD_ADDR="127.0.0.1:18943"
COORD_HEALTH=":18981"
COORD_DATA="/tmp/agentswarm-coord"
LOG_DIR="/tmp/agentswarm-logs"
TARGET_DIR="${TARGET_DIR:-${REPO_ROOT}}"
GO_TMPDIR="${TMPDIR:-/tmp}"

NODES="
control-node:19943:19511
loc-node:19944:19512
imports-node:19945:19513
todos-node:19946:19514
complexity-node:19947:19515
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

doctor() {
    echo ""
    echo -e "  ${BOLD}Agent Swarm Doctor${RESET}"
    echo -e "  ${DIM}────────────────────────────────────────${RESET}"
    echo ""
    for tool in tailbus-coord tailbusd tailbus; do
        command -v "$tool" >/dev/null 2>&1 || fail "$tool not found in PATH"
        good "$tool found"
    done
    if [ ! -f "${REPO_ROOT}/bin/swarm-demo" ]; then
        say "building swarm-demo binary..."
        (cd "${REPO_ROOT}" && go build -o bin/swarm-demo ./examples/agent-swarm/) || fail "go build failed"
    fi
    good "swarm-demo binary ready"
    good "no external LLM or network dependency required"
    echo ""
    echo -e "  ${DIM}Next:${RESET} ./run.sh demo"
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
    say "stopping all agent-swarm processes..."
    kill_processes_matching "swarm-demo"
    for entry in $NODES; do
        local name listen_port metrics_port
        IFS=: read -r name listen_port metrics_port <<< "$entry"
        kill_listener_on_port "${listen_port}"
        kill_listener_on_port "${metrics_port}"
    done
    kill_processes_matching "tailbus-coord.*${COORD_ADDR}"
    kill_listener_on_port 18943
    kill_listener_on_port 18981
    sleep 1
    rm -f /tmp/agentswarm-*.sock
    for entry in $NODES; do
        local name listen_port metrics_port
        IFS=: read -r name listen_port metrics_port <<< "$entry"
        rm -f "${GO_TMPDIR}/tailbusd-${name}.coord-fp"
    done
    good "stopped"
}

clean_all() {
    stop_all 2>/dev/null || true
    rm -rf "${COORD_DATA}" "${LOG_DIR}"
    for entry in $NODES; do
        local name listen_port metrics_port
        IFS=: read -r name listen_port metrics_port <<< "$entry"
        rm -rf "${GO_TMPDIR}/tailbusd-${name}"
    done
    good "cleaned logs and persisted state"
}

watch_logs() {
    if [ ! -d "$LOG_DIR" ]; then
        fail "no logs found — is the demo running?"
    fi
    echo ""
    echo -e "  ${BOLD}Watching agent-swarm logs${RESET}  ${DIM}(ctrl-c to stop)${RESET}"
    echo -e "  ${DIM}────────────────────────────────────────${RESET}"
    echo ""
    tail -f "$LOG_DIR"/*.log 2>/dev/null
}

launch_dashboard() {
    local sock="/tmp/agentswarm-control-node.sock"
    if [ ! -S "$sock" ]; then
        fail "control node not running (no socket at $sock)"
    fi
    exec tailbus -socket "$sock" dashboard
}

start_all() {
    doctor
    stop_all 2>/dev/null || true
    mkdir -p "$LOG_DIR" "$COORD_DATA"

    echo ""
    echo -e "  ${BOLD}Agent Swarm${RESET}"
    echo -e "  ${DIM}────────────────────────────────────────${RESET}"
    echo -e "  ${DIM}target: ${TARGET_DIR}${RESET}"
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
        local sock="/tmp/agentswarm-${name}.sock"
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

    say "starting swarm agents..."
    SWARM_ORCH_SOCKET="/tmp/agentswarm-control-node.sock" \
    SWARM_LOC_SOCKET="/tmp/agentswarm-loc-node.sock" \
    SWARM_IMPORTS_SOCKET="/tmp/agentswarm-imports-node.sock" \
    SWARM_TODOS_SOCKET="/tmp/agentswarm-todos-node.sock" \
    SWARM_COMPLEXITY_SOCKET="/tmp/agentswarm-complexity-node.sock" \
    TARGET_DIR="${TARGET_DIR}" \
        "${REPO_ROOT}/bin/swarm-demo" \
        > "${LOG_DIR}/swarm-demo.log" 2>&1 &

    sleep 2
    grep -q "ready" "${LOG_DIR}/swarm-demo.log" || fail "swarm agents failed to start — check ${LOG_DIR}/swarm-demo.log"

    echo ""
    echo -e "  ${GREEN}All running.${RESET} 1 coord + 5 daemons + 5 agents"
    echo -e "  ${DIM}Open another terminal for:${RESET} ./run.sh dashboard"
    echo -e "  ${DIM}Then run:${RESET} ./run.sh fire"
    echo ""
}

fire_analyze() {
    local sock="/tmp/agentswarm-control-node.sock"
    local fire_timeout="${FIRE_TIMEOUT:-60s}"
    [ -S "$sock" ] || fail "control node not running (no socket at $sock)"
    say "firing analysis on ${BOLD}${TARGET_DIR}${RESET}..."
    say "watch ${CYAN}./run.sh dashboard${RESET} or ${CYAN}./run.sh logs${RESET}"
    echo ""
    local payload
    payload="$(printf '{"command":"analyze","arguments":{"target_dir":"%s"}}' "$TARGET_DIR")"
    tailbus -socket "$sock" fire -timeout "$fire_timeout" swarm-orchestrator "$payload"
}

case "${1:-start}" in
    start) start_all ;;
    stop) stop_all ;;
    clean) clean_all ;;
    logs) watch_logs ;;
    doctor) doctor ;;
    dashboard) launch_dashboard ;;
    fire)
        fire_analyze
        ;;
    demo)
        start_all
        say "running swarm analysis..."
        echo ""
        fire_analyze
        result=$?
        echo ""
        say "demo complete (exit ${result})"
        stop_all
        exit $result
        ;;
    *)
        cat <<EOF
Usage: ./run.sh [start|stop|clean|logs|doctor|dashboard|fire|demo]
EOF
        exit 1
        ;;
esac
