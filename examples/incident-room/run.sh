#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
if [ -d "${REPO_ROOT}/bin" ]; then
    export PATH="${REPO_ROOT}/bin:${PATH}"
fi

COORD_ADDR="127.0.0.1:18643"
COORD_HEALTH=":18681"
COORD_DATA="/tmp/incidentroom-coord"
LOG_DIR="/tmp/incidentroom-logs"
GO_TMPDIR="${TMPDIR:-/tmp}"
VARIANT="${INCIDENT_ROOM_VARIANT:-deterministic}"
USE_LMSTUDIO_ANALYST=0
USE_CODEX_STATUS=0

# name:listen_port:metrics_port
NODES="
support-node:19643:19291
ops-node:19644:19292
finance-node:19645:19293
comms-node:19646:19294
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
        checkout)
            printf '%s' "EU customers cannot complete checkout"
            ;;
        billing)
            printf '%s' "Subscriptions are paid but not activating"
            ;;
        gateway)
            printf '%s' "API requests are timing out in one region"
            ;;
        *)
            return 1
            ;;
    esac
}

print_scenarios() {
    echo ""
    echo -e "  ${BOLD}Incident scenarios${RESET}"
    echo -e "  ${DIM}────────────────────────────────────────${RESET}"
    echo ""
    echo "  checkout  EU checkout failures after a bad release"
    echo "  billing   successful payments but delayed subscription activation"
    echo "  gateway   regional latency and timeouts across several APIs"
    echo ""
    echo "  Examples:"
    echo "    ./run.sh fire checkout"
    echo "    ./run.sh fire billing"
    echo "    ./run.sh fire \"EU customers cannot complete checkout\""
    echo ""
}

llm_base_url() {
    printf '%s' "${LLM_BASE_URL:-http://localhost:1234/v1}"
}

resolve_variant_agents() {
    USE_LMSTUDIO_ANALYST=0
    USE_CODEX_STATUS=0
    if [ "${VARIANT}" != "llm" ]; then
        return 0
    fi
    if command -v codex >/dev/null 2>&1; then
        USE_CODEX_STATUS=1
    fi
    if curl -sf "$(llm_base_url)/models" >/dev/null 2>&1; then
        USE_LMSTUDIO_ANALYST=1
    fi
}

agent_entries() {
    printf '%s\n' \
        "support-node:support_triage.py" \
        "support-node:orchestrator.py" \
        "ops-node:logs_agent.py" \
        "ops-node:metrics_agent.py" \
        "ops-node:release_agent.py" \
        "finance-node:billing_agent.py"
    if [ "${USE_LMSTUDIO_ANALYST}" = "1" ]; then
        printf '%s\n' "comms-node:lmstudio_analyst.py"
    fi
    if [ "${USE_CODEX_STATUS}" = "1" ]; then
        printf '%s\n' "comms-node:codex_status_agent.py"
    else
        printf '%s\n' "comms-node:status_agent.py"
    fi
}

all_agent_entries() {
    printf '%s\n' \
        "support-node:support_triage.py" \
        "support-node:orchestrator.py" \
        "ops-node:logs_agent.py" \
        "ops-node:metrics_agent.py" \
        "ops-node:release_agent.py" \
        "finance-node:billing_agent.py" \
        "comms-node:status_agent.py" \
        "comms-node:lmstudio_analyst.py" \
        "comms-node:codex_status_agent.py"
}

doctor() {
    echo ""
    echo -e "  ${BOLD}Incident Room Doctor${RESET}"
    echo -e "  ${DIM}────────────────────────────────────────${RESET}"
    echo ""

    for tool in tailbus-coord tailbusd tailbus python3 curl; do
        command -v "$tool" >/dev/null 2>&1 || fail "$tool not found in PATH"
        good "$tool found"
    done

    if [ "${VARIANT}" = "deterministic" ]; then
        good "deterministic mode selected — no external LLM dependency required"
    fi

    if [ "${VARIANT}" = "llm" ]; then
        if command -v codex >/dev/null 2>&1; then
            good "codex found"
        else
            warn "codex not found — using deterministic status-agent"
        fi
        if curl -sf "$(llm_base_url)/models" >/dev/null 2>&1; then
            good "LM Studio reachable at $(llm_base_url)"
        else
            warn "LM Studio not reachable at $(llm_base_url) — skipping lmstudio-analyst"
        fi
        resolve_variant_agents
        if [ "${USE_LMSTUDIO_ANALYST}" = "1" ]; then
            good "lmstudio-analyst enabled"
        fi
        if [ "${USE_CODEX_STATUS}" = "1" ]; then
            good "codex-status-agent enabled"
        else
            good "status-agent enabled"
        fi
    fi

    echo ""
    if [ "${VARIANT}" = "deterministic" ]; then
        echo -e "  ${DIM}Next:${RESET} ./run.sh && ./run.sh fire checkout"
    else
        echo -e "  ${DIM}Next:${RESET} ./run-llm.sh && ./run.sh fire checkout"
    fi
    echo ""
    return 0
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
    say "stopping all incident-room processes..."

    local entry
    while IFS= read -r entry; do
        [ -n "$entry" ] || continue
        local node script
        IFS=: read -r node script <<< "$entry"
        local sock="/tmp/incidentroom-${node}.sock"
        kill_processes_matching "TAILBUS_SOCKET=${sock}"
        kill_processes_matching "${SCRIPT_DIR}/${script}"
    done < <(all_agent_entries)

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
    rm -f /tmp/incidentroom-*.sock
    rm -f \
        "${GO_TMPDIR}/tailbusd-support-node.coord-fp" \
        "${GO_TMPDIR}/tailbusd-ops-node.coord-fp" \
        "${GO_TMPDIR}/tailbusd-finance-node.coord-fp" \
        "${GO_TMPDIR}/tailbusd-comms-node.coord-fp"
    good "stopped"
}

clean_all() {
    stop_all 2>/dev/null || true
    rm -rf "${COORD_DATA}" "${LOG_DIR}" "${SCRIPT_DIR}/output"
    rm -rf \
        "${GO_TMPDIR}/tailbusd-support-node" \
        "${GO_TMPDIR}/tailbusd-ops-node" \
        "${GO_TMPDIR}/tailbusd-finance-node" \
        "${GO_TMPDIR}/tailbusd-comms-node"
    good "cleaned logs, transcripts, and persisted state"
}

fire_incident() {
    local incident="$1"
    local sock="/tmp/incidentroom-support-node.sock"
    local payload
    local scenario="$incident"

    if [ ! -S "$sock" ]; then
        fail "support node not running (no socket at $sock)"
    fi

    if scenario_text "$scenario" >/dev/null 2>&1; then
        incident="$(scenario_text "$scenario")"
        say "firing scenario ${CYAN}${scenario}${RESET}..."
    else
        say "firing incident..."
    fi

    payload="$(python3 -c 'import json,sys; print(json.dumps({"command":"report_incident","arguments":{"incident":sys.argv[1]}}))' "$incident")"
    say "watch ${CYAN}./run.sh dashboard${RESET} or ${CYAN}./run.sh logs${RESET} while the room is active"
    echo ""
    tailbus -socket "$sock" fire support-triage "$payload"
}

watch_logs() {
    if [ ! -d "$LOG_DIR" ]; then
        fail "no logs found — is the demo running?"
    fi
    echo ""
    echo -e "  ${BOLD}Watching incident-room logs${RESET}  ${DIM}(ctrl-c to stop)${RESET}"
    echo -e "  ${DIM}────────────────────────────────────────${RESET}"
    echo ""
    tail -f "$LOG_DIR"/agent-*.log 2>/dev/null
}

launch_dashboard() {
    local sock="/tmp/incidentroom-support-node.sock"
    if [ ! -S "$sock" ]; then
        fail "support node not running (no socket at $sock)"
    fi
    exec tailbus -socket "$sock" dashboard
}

start_all() {
    command -v tailbus-coord >/dev/null || fail "tailbus-coord not found in PATH"
    command -v tailbusd >/dev/null || fail "tailbusd not found in PATH"
    command -v tailbus >/dev/null || fail "tailbus not found in PATH"
    command -v python3 >/dev/null || fail "python3 not found in PATH"
    command -v curl >/dev/null || fail "curl not found in PATH"

    resolve_variant_agents
    stop_all 2>/dev/null || true
    mkdir -p "$LOG_DIR" "$COORD_DATA" "${SCRIPT_DIR}/output"

    echo ""
    echo -e "  ${BOLD}Incident Room — Flagship Demo${RESET}"
    echo -e "  ${DIM}────────────────────────────────────────${RESET}"
    echo -e "  ${DIM}variant: ${VARIANT}${RESET}"
    if [ "${VARIANT}" = "llm" ]; then
        if [ "${USE_LMSTUDIO_ANALYST}" != "1" ]; then
            warn "LM Studio unavailable — skipping lmstudio-analyst"
        fi
        if [ "${USE_CODEX_STATUS}" != "1" ]; then
            warn "codex unavailable — using deterministic status-agent"
        fi
    fi
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
        local name listen_port metrics_port
        IFS=: read -r name listen_port metrics_port <<< "$entry"
        local sock="/tmp/incidentroom-${name}.sock"

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
    while IFS= read -r entry; do
        [ -n "$entry" ] || continue
        local node script
        IFS=: read -r node script <<< "$entry"
        local sock="/tmp/incidentroom-${node}.sock"
        local name="${script%.py}"

        TAILBUS_SOCKET="$sock" OUTPUT_DIR="${SCRIPT_DIR}/output" python3 "${SCRIPT_DIR}/${script}" \
            > "$LOG_DIR/agent-${name}.log" 2>&1 &
        good "agent ${BOLD}${name}${RESET} on ${node}"
    done < <(agent_entries)

    sleep 1

    echo ""
    echo -e "  ${DIM}────────────────────────────────────────${RESET}"
    local agent_count
    agent_count="$(agent_entries | sed '/^$/d' | wc -l | tr -d ' ')"
    echo -e "  ${GREEN}All running.${RESET} 1 coord + 4 daemons + ${agent_count} agents"
    echo ""
    echo -e "  ${BOLD}Dashboard:${RESET}"
    echo -e "    ./run.sh dashboard"
    echo ""
    echo -e "  ${BOLD}Inspect mesh capabilities:${RESET}"
    echo -e "    tailbus -socket /tmp/incidentroom-support-node.sock list --verbose"
    echo -e "    tailbus -socket /tmp/incidentroom-support-node.sock find --capabilities ops.logs.search"
    echo ""
    echo -e "  ${BOLD}Try a scenario:${RESET}"
    echo -e "    ./run.sh scenarios"
    echo -e "    ./run.sh fire checkout"
    echo -e "    ./run.sh fire billing"
    echo ""
    echo -e "  ${BOLD}Open an incident:${RESET}"
    echo -e "    ./run.sh fire \"EU customers cannot complete checkout\""
    echo ""
    echo -e "  ${BOLD}Watch logs:${RESET}"
    echo -e "    ./run.sh logs"
    echo ""
    echo -e "  ${BOLD}Stop everything:${RESET}"
    echo -e "    ./run.sh stop"
    if [ "${VARIANT}" != "llm" ]; then
        echo ""
        echo -e "  ${BOLD}LLM-backed variant:${RESET}"
        echo -e "    ./run-llm.sh"
    fi
    echo ""
}

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
    scenarios)
        print_scenarios
        ;;
    fire)
        shift
        [ $# -ge 1 ] || fail "usage: ./run.sh fire \"incident text\""
        fire_incident "$*"
        ;;
    logs)
        watch_logs
        ;;
    start)
        start_all
        ;;
    *)
        echo "Usage: ./run.sh [start|stop|clean|doctor|dashboard|scenarios|fire|logs]"
        exit 1
        ;;
esac
