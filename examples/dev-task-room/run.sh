#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
if [ -d "${REPO_ROOT}/bin" ]; then
    export PATH="${REPO_ROOT}/bin:${PATH}"
fi

COORD_ADDR="127.0.0.1:18743"
COORD_HEALTH=":18781"
COORD_DATA="/tmp/devtaskroom-coord"
LOG_DIR="/tmp/devtaskroom-logs"
WORKSPACE_ROOT="${WORKSPACE_ROOT:-/tmp/devtaskroom-workspace}"
OUTPUT_DIR="${SCRIPT_DIR}/output"
GO_TMPDIR="${TMPDIR:-/tmp}"

NODES="
control-node:19743:19311
implement-node:19744:19312
review-node:19745:19313
test-node:19746:19314
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

scenario_text() {
    case "$1" in
        focus-timer)
            printf '%s' "Build a small self-contained Python focus timer app in about 100 lines using only the standard library. Make it runnable from the terminal, support configurable work and break durations, show a live countdown, and include a couple of unit tests for the core timer-state logic."
            ;;
        snake-clone)
            printf '%s' "Build a simple Snake clone in Python using only the standard library. Include a playable interface, food spawning, score tracking, wall and self collision, restart handling, and unit tests for the core game logic."
            ;;
        parser-edge-case)
            printf '%s' "Extend the CSV parser so quoted commas are handled correctly and add coverage for the edge case."
            ;;
        todo-filter)
            printf '%s' "Make the todo status filter case-insensitive and ensure the tests cover mixed-case input."
            ;;
        client-timeout)
            printf '%s' "Add a timeout parameter to the HTTP client, thread it through the client API, and update the tests to cover both the default behavior and an explicit timeout override."
            ;;
        *)
            return 1
            ;;
    esac
}

print_scenarios() {
    echo ""
    echo -e "  ${BOLD}Dev Task scenarios${RESET}"
    echo -e "  ${DIM}────────────────────────────────────────${RESET}"
    echo ""
    echo "  focus-timer     build a small terminal Pomodoro timer"
    echo "  snake-clone      build a Snake clone in Python"
    echo "  parser-edge-case fix quoted-comma parsing"
    echo "  todo-filter      make the todo filter case-insensitive"
    echo "  client-timeout   add explicit timeout support to the HTTP client"
    echo ""
    echo "  Examples:"
    echo "    ./run.sh fire focus-timer"
    echo "    ./run.sh fire snake-clone"
    echo "    ./run.sh fire-task \"Add a timeout parameter to the HTTP client and update tests\""
    echo ""
}

llm_base_url() {
    printf '%s' "${LLM_BASE_URL:-http://localhost:1234/v1}"
}

doctor() {
    echo ""
    echo -e "  ${BOLD}Dev Task Room Doctor${RESET}"
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
    good "workspace root will be ${WORKSPACE_ROOT}"
    echo ""
    echo -e "  ${DIM}This demo requires Codex + LM Studio.${RESET}"
    echo -e "  ${DIM}Next:${RESET} ./run.sh && ./run.sh fire client-timeout"
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
    say "stopping all dev-task-room processes..."
    for script in orchestrator.py workspace_agent.py implementer.py critic.py test_strategist.py; do
        kill_processes_matching "${SCRIPT_DIR}/${script}"
    done
    for entry in $NODES; do
        local name listen_port metrics_port
        IFS=: read -r name listen_port metrics_port <<< "$entry"
        kill_listener_on_port "${listen_port}"
        kill_listener_on_port "${metrics_port}"
    done
    kill_processes_matching "tailbus-coord.*${COORD_ADDR}"
    kill_listener_on_port 18743
    kill_listener_on_port 18781
    sleep 1
    rm -f /tmp/devtaskroom-*.sock
    rm -f \
        "${GO_TMPDIR}/tailbusd-control-node.coord-fp" \
        "${GO_TMPDIR}/tailbusd-implement-node.coord-fp" \
        "${GO_TMPDIR}/tailbusd-review-node.coord-fp" \
        "${GO_TMPDIR}/tailbusd-test-node.coord-fp"
    rm -rf "${COORD_DATA}"
    rm -rf \
        "${GO_TMPDIR}/tailbusd-control-node" \
        "${GO_TMPDIR}/tailbusd-implement-node" \
        "${GO_TMPDIR}/tailbusd-review-node" \
        "${GO_TMPDIR}/tailbusd-test-node"
    rm -rf "${WORKSPACE_ROOT}"
    good "stopped and cleared persisted room state"
}

clean_all() {
    stop_all 2>/dev/null || true
    rm -rf "${LOG_DIR}" "${OUTPUT_DIR}"
    good "cleaned logs, transcripts, artifacts, and persisted state"
}

watch_logs() {
    if [ ! -d "$LOG_DIR" ]; then
        fail "no logs found — is the demo running?"
    fi
    echo ""
    echo -e "  ${BOLD}Watching dev-task-room logs${RESET}  ${DIM}(ctrl-c to stop)${RESET}"
    echo -e "  ${DIM}────────────────────────────────────────${RESET}"
    echo ""
    tail -f "$LOG_DIR"/agent-*.log 2>/dev/null
}

launch_dashboard() {
    local sock="/tmp/devtaskroom-control-node.sock"
    if [ ! -S "$sock" ]; then
        fail "control node not running (no socket at $sock)"
    fi
    exec tailbus -socket "$sock" dashboard
}

start_all() {
    doctor >/dev/null
    stop_all 2>/dev/null || true
    mkdir -p "$LOG_DIR" "$COORD_DATA" "$OUTPUT_DIR"

    echo ""
    echo -e "  ${BOLD}Dev Task Room${RESET}"
    echo -e "  ${DIM}────────────────────────────────────────${RESET}"
    echo -e "  ${DIM}workspace: ${WORKSPACE_ROOT}${RESET}"
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
        local sock="/tmp/devtaskroom-${name}.sock"
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

    say "starting agents..."
    TAILBUS_SOCKET="/tmp/devtaskroom-control-node.sock" OUTPUT_DIR="${OUTPUT_DIR}" python3 "${SCRIPT_DIR}/orchestrator.py" \
        > "${LOG_DIR}/agent-orchestrator.log" 2>&1 &
    TAILBUS_SOCKET="/tmp/devtaskroom-control-node.sock" WORKSPACE_ROOT="${WORKSPACE_ROOT}" OUTPUT_DIR="${OUTPUT_DIR}" python3 "${SCRIPT_DIR}/workspace_agent.py" \
        > "${LOG_DIR}/agent-workspace-agent.log" 2>&1 &
    TAILBUS_SOCKET="/tmp/devtaskroom-implement-node.sock" CODEX_MODEL="${CODEX_MODEL:-gpt-5.1-codex-mini}" OUTPUT_DIR="${OUTPUT_DIR}" python3 "${SCRIPT_DIR}/implementer.py" \
        > "${LOG_DIR}/agent-implementer.log" 2>&1 &
    TAILBUS_SOCKET="/tmp/devtaskroom-review-node.sock" LLM_BASE_URL="$(llm_base_url)" LLM_MODEL="${LLM_MODEL:-}" OUTPUT_DIR="${OUTPUT_DIR}" python3 "${SCRIPT_DIR}/critic.py" \
        > "${LOG_DIR}/agent-critic.log" 2>&1 &
    TAILBUS_SOCKET="/tmp/devtaskroom-test-node.sock" LLM_BASE_URL="$(llm_base_url)" LLM_MODEL="${LLM_MODEL:-}" OUTPUT_DIR="${OUTPUT_DIR}" python3 "${SCRIPT_DIR}/test_strategist.py" \
        > "${LOG_DIR}/agent-test-strategist.log" 2>&1 &

    sleep 2
    for log in "${LOG_DIR}/agent-orchestrator.log" "${LOG_DIR}/agent-workspace-agent.log" "${LOG_DIR}/agent-implementer.log" "${LOG_DIR}/agent-critic.log" "${LOG_DIR}/agent-test-strategist.log"; do
        grep -q "ready" "$log" || fail "agent failed to start — check $log"
    done

    echo ""
    echo -e "  ${GREEN}All running.${RESET} 1 coord + 4 daemons + 5 agents"
    echo -e "  ${DIM}Open another terminal for:${RESET} ./run.sh dashboard"
    echo -e "  ${DIM}Then run:${RESET} ./run.sh fire todo-filter"
    echo ""
}

fire_task() {
    local task="$1"
    local sock="/tmp/devtaskroom-control-node.sock"
    local fire_timeout="${FIRE_TIMEOUT:-600s}"
    [ -S "$sock" ] || fail "control node not running (no socket at $sock)"
    say "firing task..."
    say "watch ${CYAN}./run.sh dashboard${RESET} or ${CYAN}./run.sh logs${RESET} while the room is active"
    echo ""
    local payload
    payload="$(python3 -c 'import json,sys; print(json.dumps({"command":"run_task","arguments":{"task":sys.argv[1]}}))' "$task")"
    tailbus -socket "$sock" fire -timeout "$fire_timeout" task-orchestrator "$payload"
}

fire_scenario() {
    local task="$1"
    if scenario_text "$task" >/dev/null 2>&1; then
        fire_task "$(scenario_text "$task")"
    else
        fire_task "$task"
    fi
}

case "${1:-start}" in
    start) start_all ;;
    stop) stop_all ;;
    clean) clean_all ;;
    logs) watch_logs ;;
    doctor) doctor ;;
    dashboard) launch_dashboard ;;
    scenarios) print_scenarios ;;
    fire)
        shift
        [ "$#" -gt 0 ] || fail "usage: ./run.sh fire <scenario|task>"
        fire_scenario "$*"
        ;;
    fire-task)
        shift
        [ "$#" -gt 0 ] || fail "usage: ./run.sh fire-task \"task text\""
        fire_task "$*"
        ;;
    demo)
        start_all
        say "running demo task ${BOLD}(todo-filter)${RESET}..."
        echo ""
        fire_scenario "todo-filter"
        result=$?
        echo ""
        say "demo complete (exit ${result})"
        stop_all
        exit $result
        ;;
    *)
        cat <<EOF
Usage: ./run.sh [start|stop|clean|logs|doctor|dashboard|scenarios|fire|fire-task|demo]
EOF
        exit 1
        ;;
esac
