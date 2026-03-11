#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
if [ -d "${REPO_ROOT}/bin" ]; then
    export PATH="${REPO_ROOT}/bin:${PATH}"
fi

COORD_ADDR="127.0.0.1:18443"
COORD_HEALTH=":18081"
COORD_DATA="/tmp/pairsolver-coord"
LOG_DIR="/tmp/pairsolver-logs"
GO_TMPDIR="${TMPDIR:-/tmp}"

# name:listen_port:metrics_port:script
NODES="
orchestrator:19443:19091:orchestrator.py
codex-solver:19444:19092:codex_solver.py
lmstudio-solver:19445:19093:lmstudio_solver.py
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
fail() { echo -e "  ${DIM}run.sh${RESET}  ${RED}✗${RESET} $*"; exit 1; }

llm_base_url() {
    printf '%s' "${LLM_BASE_URL:-http://localhost:1234/v1}"
}

doctor() {
    echo ""
    echo -e "  ${BOLD}Pair Solver Doctor${RESET}"
    echo -e "  ${DIM}────────────────────────────────────────${RESET}"
    echo ""
    for tool in tailbus-coord tailbusd tailbus python3 curl codex; do
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
    echo -e "  ${DIM}Next:${RESET} ./run.sh && ./run.sh fire \"Write a Python function that finds the longest palindromic substring\""
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
    local pid
    local end=$((SECONDS + deadline_secs))
    while [ "$#" -gt 0 ] && [ "$SECONDS" -lt "$end" ]; do
        local remaining=()
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
    say "stopping all pair-solver processes..."

    for entry in $NODES; do
        local name listen_port metrics_port script
        IFS=: read -r name listen_port metrics_port script <<< "$entry"
        local sock="/tmp/pairsolver-${name}.sock"

        kill_processes_matching "TAILBUS_SOCKET=${sock}"
        kill_processes_matching "${SCRIPT_DIR}/${script}"
        kill_listener_on_port "${listen_port}"
        kill_listener_on_port "${metrics_port}"

        if [ -S "$sock" ]; then
            local socket_pids=()
            while IFS= read -r pid; do
                [ -n "$pid" ] && socket_pids+=("$pid")
            done < <(lsof -t "$sock" 2>/dev/null || true)
            if [ "${#socket_pids[@]}" -gt 0 ]; then
                kill_pid_list TERM "${socket_pids[@]}"
                if ! wait_for_pids_to_exit 3 "${socket_pids[@]}"; then
                    kill_pid_list KILL "${socket_pids[@]}"
                fi
            fi
        fi
    done

    kill_processes_matching "tailbus-coord.*${COORD_ADDR}"
    kill_listener_on_port 18443
    kill_listener_on_port 18081

    sleep 1
    rm -f /tmp/pairsolver-*.sock
    rm -f \
        "${GO_TMPDIR}/tailbusd-orchestrator.coord-fp" \
        "${GO_TMPDIR}/tailbusd-codex-solver.coord-fp" \
        "${GO_TMPDIR}/tailbusd-lmstudio-solver.coord-fp"
    good "stopped"
}

clean_all() {
    stop_all 2>/dev/null || true
    rm -rf "${COORD_DATA}" "${LOG_DIR}" "${SCRIPT_DIR}/output"
    rm -rf \
        "${GO_TMPDIR}/tailbusd-orchestrator" \
        "${GO_TMPDIR}/tailbusd-codex-solver" \
        "${GO_TMPDIR}/tailbusd-lmstudio-solver"
    good "cleaned logs, outputs, and persisted state"
}

fire_problem() {
    local problem="$1"
    local sock="/tmp/pairsolver-orchestrator.sock"
    local payload

    if [ ! -S "$sock" ]; then
        fail "orchestrator not running (no socket at $sock)"
    fi

    payload="$(python3 -c 'import json,sys; print(json.dumps({"command":"solve","arguments":{"problem":sys.argv[1]}}))' "$problem")"
    say "firing solve request..."
    echo ""
    tailbus -socket "$sock" fire orchestrator "$payload"
}

watch_logs() {
    if [ ! -d "$LOG_DIR" ]; then
        fail "no logs found — is the demo running?"
    fi

    echo ""
    echo -e "  ${BOLD}Watching pair-solver logs${RESET}  ${DIM}(ctrl-c to stop)${RESET}"
    echo -e "  ${DIM}────────────────────────────────────────${RESET}"
    echo ""

    tail -f \
        "$LOG_DIR/agent-orchestrator.log" \
        "$LOG_DIR/agent-codex-solver.log" \
        "$LOG_DIR/agent-lmstudio-solver.log" \
        2>/dev/null
}

launch_dashboard() {
    local sock="/tmp/pairsolver-orchestrator.sock"
    if [ ! -S "$sock" ]; then
        fail "orchestrator not running (no socket at $sock)"
    fi
    exec tailbus -socket "$sock" dashboard
}

start_all() {
    doctor >/dev/null

    stop_all 2>/dev/null || true

    mkdir -p "$LOG_DIR" "$COORD_DATA" "${SCRIPT_DIR}/output"

    echo ""
    echo -e "  ${BOLD}Pair Solver — Room Demo${RESET}"
    echo -e "  ${DIM}────────────────────────────────────────${RESET}"
    echo ""

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

    for entry in $NODES; do
        local name listen_port metrics_port script
        IFS=: read -r name listen_port metrics_port script <<< "$entry"
        local sock="/tmp/pairsolver-${name}.sock"

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

    echo ""
    say "starting agents..."
    for entry in $NODES; do
        local name listen_port metrics_port script
        IFS=: read -r name listen_port metrics_port script <<< "$entry"
        local sock="/tmp/pairsolver-${name}.sock"

        TAILBUS_SOCKET="$sock" OUTPUT_DIR="${SCRIPT_DIR}/output" python3 "${SCRIPT_DIR}/${script}" \
            > "$LOG_DIR/agent-${name}.log" 2>&1 &

        good "agent ${BOLD}@${name}${RESET}"
    done

    sleep 1

    echo ""
    echo -e "  ${DIM}────────────────────────────────────────${RESET}"
    echo -e "  ${GREEN}All running.${RESET} 1 coord + 3 daemons + 3 agents"
    echo ""
    echo -e "  ${BOLD}Dashboard:${RESET}"
    echo -e "    tailbus -socket /tmp/pairsolver-orchestrator.sock dashboard"
    echo ""
    echo -e "  ${BOLD}Solve a problem:${RESET}"
    echo -e "    ./run.sh fire \"Write a Python function that finds the longest palindromic substring\""
    echo ""
    echo -e "  ${BOLD}Watch the room conversation:${RESET}"
    echo -e "    ./run.sh logs"
    echo ""
    echo -e "  ${BOLD}Stop everything:${RESET}"
    echo -e "    ./run.sh stop"
    echo ""
}

case "${1:-start}" in
    start|"")
        start_all
        ;;
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
            fail "usage: ./run.sh fire \"problem statement\""
        fi
        fire_problem "$2"
        ;;
    logs|watch)
        watch_logs
        ;;
    demo)
        start_all
        echo ""
        fire_problem "Write a Python function that finds the longest palindromic substring"
        ;;
    *)
        echo "usage: ./run.sh [start|stop|clean|doctor|dashboard|fire \"problem statement\"|logs|demo]"
        exit 1
        ;;
esac
