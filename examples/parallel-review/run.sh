#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
if [ -d "${REPO_ROOT}/bin" ]; then
    export PATH="${REPO_ROOT}/bin:${PATH}"
fi

COORD_ADDR="127.0.0.1:18843"
COORD_HEALTH=":18881"
COORD_DATA="/tmp/parallelreview-coord"
LOG_DIR="/tmp/parallelreview-logs"
OUTPUT_DIR="${SCRIPT_DIR}/output"
GO_TMPDIR="${TMPDIR:-/tmp}"

NODES="
control-node:19843:19411
security-node:19844:19412
perf-node:19845:19413
style-node:19846:19414
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
    echo -e "  ${BOLD}Parallel Review Doctor${RESET}"
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
    say "stopping all parallel-review processes..."
    for script in orchestrator.py reviewer.py; do
        kill_processes_matching "${SCRIPT_DIR}/${script}"
    done
    for entry in $NODES; do
        local name listen_port metrics_port
        IFS=: read -r name listen_port metrics_port <<< "$entry"
        kill_listener_on_port "${listen_port}"
        kill_listener_on_port "${metrics_port}"
    done
    kill_processes_matching "tailbus-coord.*${COORD_ADDR}"
    kill_listener_on_port 18843
    kill_listener_on_port 18881
    sleep 1
    rm -f /tmp/parallelreview-*.sock
    rm -f \
        "${GO_TMPDIR}/tailbusd-control-node.coord-fp" \
        "${GO_TMPDIR}/tailbusd-security-node.coord-fp" \
        "${GO_TMPDIR}/tailbusd-perf-node.coord-fp" \
        "${GO_TMPDIR}/tailbusd-style-node.coord-fp"
    good "stopped"
}

clean_all() {
    stop_all 2>/dev/null || true
    rm -rf "${COORD_DATA}" "${LOG_DIR}" "${OUTPUT_DIR}"
    rm -rf \
        "${GO_TMPDIR}/tailbusd-control-node" \
        "${GO_TMPDIR}/tailbusd-security-node" \
        "${GO_TMPDIR}/tailbusd-perf-node" \
        "${GO_TMPDIR}/tailbusd-style-node"
    good "cleaned logs, outputs, and persisted state"
}

watch_logs() {
    if [ ! -d "$LOG_DIR" ]; then
        fail "no logs found — is the demo running?"
    fi
    echo ""
    echo -e "  ${BOLD}Watching parallel-review logs${RESET}  ${DIM}(ctrl-c to stop)${RESET}"
    echo -e "  ${DIM}────────────────────────────────────────${RESET}"
    echo ""
    tail -f "$LOG_DIR"/agent-*.log 2>/dev/null
}

launch_dashboard() {
    local sock="/tmp/parallelreview-control-node.sock"
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
    echo -e "  ${BOLD}Parallel Review Room${RESET}"
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
        local sock="/tmp/parallelreview-${name}.sock"
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
    TAILBUS_SOCKET="/tmp/parallelreview-control-node.sock" OUTPUT_DIR="${OUTPUT_DIR}" \
        python3 "${SCRIPT_DIR}/orchestrator.py" \
        > "${LOG_DIR}/agent-orchestrator.log" 2>&1 &

    TAILBUS_SOCKET="/tmp/parallelreview-security-node.sock" \
        REVIEWER_ROLE=security REVIEWER_HANDLE=security-reviewer REVIEWER_CAP=review.security \
        LLM_BASE_URL="$(llm_base_url)" LLM_MODEL="${LLM_MODEL:-}" \
        python3 "${SCRIPT_DIR}/reviewer.py" \
        > "${LOG_DIR}/agent-security-reviewer.log" 2>&1 &

    TAILBUS_SOCKET="/tmp/parallelreview-perf-node.sock" \
        REVIEWER_ROLE=performance REVIEWER_HANDLE=perf-reviewer REVIEWER_CAP=review.performance \
        LLM_BASE_URL="$(llm_base_url)" LLM_MODEL="${LLM_MODEL:-}" \
        python3 "${SCRIPT_DIR}/reviewer.py" \
        > "${LOG_DIR}/agent-perf-reviewer.log" 2>&1 &

    TAILBUS_SOCKET="/tmp/parallelreview-style-node.sock" \
        REVIEWER_ROLE=style REVIEWER_HANDLE=style-reviewer REVIEWER_CAP=review.style \
        LLM_BASE_URL="$(llm_base_url)" LLM_MODEL="${LLM_MODEL:-}" \
        python3 "${SCRIPT_DIR}/reviewer.py" \
        > "${LOG_DIR}/agent-style-reviewer.log" 2>&1 &

    sleep 2
    for log in "${LOG_DIR}/agent-orchestrator.log" "${LOG_DIR}/agent-security-reviewer.log" "${LOG_DIR}/agent-perf-reviewer.log" "${LOG_DIR}/agent-style-reviewer.log"; do
        grep -q "ready" "$log" || fail "agent failed to start — check $log"
    done

    echo ""
    echo -e "  ${GREEN}All running.${RESET} 1 coord + 4 daemons + 4 agents"
    echo -e "  ${DIM}Open another terminal for:${RESET} ./run.sh dashboard"
    echo -e "  ${DIM}Then run:${RESET} ./run.sh fire insecure-api"
    echo ""
}

fire_scenario() {
    local scenario="$1"
    local sock="/tmp/parallelreview-control-node.sock"
    local fire_timeout="${FIRE_TIMEOUT:-300s}"
    [ -S "$sock" ] || fail "control node not running (no socket at $sock)"
    say "firing review scenario: ${BOLD}${scenario}${RESET}"
    say "watch ${CYAN}./run.sh dashboard${RESET} or ${CYAN}./run.sh logs${RESET}"
    echo ""
    local payload
    payload="$(python3 -c 'import json,sys; print(json.dumps({"command":"review","arguments":{"scenario":sys.argv[1]}}))' "$scenario")"
    tailbus -socket "$sock" fire -timeout "$fire_timeout" review-orchestrator "$payload"
}

print_scenarios() {
    echo ""
    echo -e "  ${BOLD}Review scenarios${RESET}"
    echo -e "  ${DIM}────────────────────────────────────────${RESET}"
    echo ""
    echo "  insecure-api   Flask API with SQL injection, auth bypass, secret exposure"
    echo "  slow-search    O(n^2) search, bubble sort, redundant iterations"
    echo "  messy-code     Single-letter vars, deep nesting, no docs"
    echo ""
    echo "  Examples:"
    echo "    ./run.sh fire insecure-api"
    echo ""
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
        [ "$#" -gt 0 ] || fail "usage: ./run.sh fire <scenario>"
        fire_scenario "$*"
        ;;
    demo)
        start_all
        say "running demo review ${BOLD}(insecure-api)${RESET}..."
        echo ""
        fire_scenario "insecure-api"
        result=$?
        echo ""
        say "demo complete (exit ${result})"
        stop_all
        exit $result
        ;;
    *)
        cat <<EOF
Usage: ./run.sh [start|stop|clean|logs|doctor|dashboard|scenarios|fire|demo]
EOF
        exit 1
        ;;
esac
