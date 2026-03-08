# Agent Swarm

Five pure Go agents analyze the tailbus codebase in parallel — no external
dependencies, completes in seconds, shows the mesh at full speed.

## What it demonstrates

- **Go agents via gRPC**: direct connection to daemon Unix sockets (no Python SDK)
- **5-node mesh**: each agent on its own daemon for maximum dashboard visibility
- **Parallel fan-out**: orchestrator dispatches 4 analysis tasks simultaneously
- **Zero external deps**: pure Go stdlib analysis (go/parser, filepath.Walk)

## Requirements

- `tailbus-coord`, `tailbusd`, `tailbus` binaries (`make build`)
- Go 1.25+ (for building the swarm binary)

No Python, no LLM, no network access required.

## Quick start

```bash
make build
cd examples/agent-swarm
./run.sh demo          # build + start → fire → stop (fully unattended, ~10s)
```

## Manual run

```bash
./run.sh start         # start coord + 5 daemons + 5 agents
./run.sh dashboard     # (in another terminal) watch the TUI
./run.sh fire          # analyze the tailbus repo
./run.sh stop
```

## Agents

| Handle | Capability | Analysis |
|--------|-----------|----------|
| `swarm-orchestrator` | `swarm.orchestrate` | Fans out, collects, summarizes |
| `loc-counter` | `swarm.analyze.loc` | Lines of code per directory |
| `import-scanner` | `swarm.analyze.imports` | External dependency usage |
| `todo-finder` | `swarm.analyze.todos` | TODO/FIXME/HACK comments |
| `complexity-gauge` | `swarm.analyze.complexity` | Functions over 40 lines |

## Architecture

All 5 agents run as goroutines in a single Go binary (`swarm-demo`), each
connecting to a separate daemon via gRPC over Unix socket.

```
control-node ──── swarm-orchestrator
loc-node ──────── loc-counter
imports-node ──── import-scanner
todos-node ────── todo-finder
complexity-node ─ complexity-gauge
```

The orchestrator creates a room, fans out `analyze_request` messages, collects
all `analyze_reply` messages, and resolves the calling session with a summary.
