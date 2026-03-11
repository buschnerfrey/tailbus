# Multi-AI Collaborative App Builder

Three AI coding agents collaborate via Tailbus to build an app from a natural language description. Each agent independently generates code for every file, then a consensus step merges the best parts.

Best for: a flashy multi-coder demo once the simpler room examples already make sense.

## Why this example matters

This demo emphasizes mesh topology and multi-agent coding fan-out. It is less
about rooms as the primary abstraction and more about showing several coding
agents collaborating across separate daemons while the dashboard stays busy.

## Requirements

- `tailbus`, `tailbusd`, `tailbus-coord` built (`make build`)
- `python3`
- `curl`
- [LM Studio](https://lmstudio.ai/) running with a code-capable model loaded
- [Claude Code](https://docs.anthropic.com/en/docs/claude-code) CLI optional
- [OpenAI Codex](https://openai.com/index/codex/) CLI optional

## Architecture

```
User → tailbus fire orchestrator '{"app": "a todo app"}'
                    │
           @orchestrator (Python)
           ┌────────────────────────┐
           │ 1. Plan: @lmstudio    │
           │    breaks app into     │
           │    file manifest       │
           │                        │
           │ 2. Code: all 3 coders │
           │    generate in parallel │
           │                        │
           │ 3. Consensus: LM      │
           │    Studio merges best  │
           │                        │
           │ 4. Write files to disk │
           └────────┬───┬───┬───────┘
                    │   │   │
          ┌─────────┘   │   └─────────┐
          ↓             ↓             ↓
    @claude-coder  @codex-coder  @lmstudio-coder
    (Claude CLI)   (Codex CLI)   (LM Studio API)
```

Each agent runs on its own tailbus daemon — the mesh routes messages between them over P2P gRPC. The dashboard shows the full topology with animated message flow.

## Quick start

```bash
make build
cd examples/app-builder
./run.sh doctor

# Start everything: 1 coord + 4 daemons + 4 agents
./run.sh

# Open the dashboard — watch the mesh topology
./run.sh dashboard

# In another terminal, watch the agent conversation
./run.sh logs

# Build an app
./run.sh fire "A todo app with HTML, CSS, and vanilla JS. Local storage, add/delete/toggle."

# When done
./run.sh stop
./run.sh clean
```

## What to watch for

- the orchestrator planning through `lmstudio-coder`
- per-file fan-out to multiple coding agents in parallel
- consensus/merge behavior after competing implementations return
- a visibly busy mesh in the dashboard rather than one linear chain

## Agents

| Agent | Handle | Node | Role |
|---|---|---|---|
| `orchestrator.py` | `@orchestrator` | orchestrator | Coordinates the build |
| `lmstudio_coder.py` | `@lmstudio-coder` | lmstudio-coder | Plans, codes, merges (LM Studio API) |
| `claude_coder.py` | `@claude-coder` | claude-coder | Generates code (Claude Code CLI) |
| `codex_coder.py` | `@codex-coder` | codex-coder | Generates code (OpenAI Codex CLI) |

## How It Works

1. **Plan** — `@lmstudio-coder` breaks the app description into a file manifest
2. **Code** — For each file, all 3 coders generate implementations in parallel via `asyncio.gather`
3. **Consensus** — `@lmstudio-coder` reviews all implementations and picks the best or merges
4. **Write** — Merged files are written to the output directory

If a coder times out or errors, consensus proceeds with the remaining implementations.

## Output

Generated files land in:

- `examples/app-builder/output/<app-slug>/`

Operational logs land in:

- `/tmp/appbuilder-logs/`

## Network Topology

`run.sh` starts a local coord server and 4 separate daemons:

```
                    ┌────────────────┐
                    │  coord :18443  │
                    └───┬──┬──┬──┬──┘
            ┌───────────┘  │  │  └───────────┐
            │         ┌────┘  └────┐         │
  ┌─────────┴──┐  ┌───┴────┐  ┌───┴────┐  ┌─┴──────────┐
  │ :19443     │  │ :19444 │  │ :19445 │  │ :19446     │
  │ orchestr.  │  │ claude │  │ codex  │  │ lmstudio   │
  └────────────┘  └────────┘  └────────┘  └────────────┘
```

Daemons discover each other through the coord and establish direct P2P gRPC connections for message delivery. Each daemon has one agent connected via a dedicated Unix socket.

## Environment Variables

| Variable | Default | Description |
|---|---|---|
| `LLM_BASE_URL` | `http://localhost:1234/v1` | LM Studio API endpoint |
| `LLM_MODEL` | (auto) | Model name for LM Studio |
| `OUTPUT_DIR` | `./output` | Where generated apps are written |
| `CLAUDE_TIMEOUT` | `120` | Seconds before Claude CLI times out |
| `CODEX_TIMEOUT` | `120` | Seconds before Codex CLI times out |

## Logs

All logs are written to `/tmp/appbuilder-logs/`:

| File | Contents |
|---|---|
| `coord.log` | Coord server |
| `daemon-*.log` | Per-daemon logs (gRPC, routing) |
| `agent-*.log` | Per-agent logs (the conversation) |

## Manual Setup

If you prefer to run things individually instead of using `run.sh`:

```bash
# Coord
tailbus-coord -listen :18443 -data-dir /tmp/appbuilder-coord -health-addr :18081

# Daemons (one per terminal)
tailbusd -node-id orchestrator -coord 127.0.0.1:18443 -advertise 127.0.0.1:19443 -listen :19443 -socket /tmp/appbuilder-orchestrator.sock -metrics :19091
tailbusd -node-id lmstudio-coder -coord 127.0.0.1:18443 -advertise 127.0.0.1:19446 -listen :19446 -socket /tmp/appbuilder-lmstudio-coder.sock -metrics :19094

# Agents
TAILBUS_SOCKET=/tmp/appbuilder-orchestrator.sock python3 orchestrator.py
TAILBUS_SOCKET=/tmp/appbuilder-lmstudio-coder.sock python3 lmstudio_coder.py
```
