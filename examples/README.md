# Examples

Recommended order:

1. [cli-tools](/Users/alexanderfrey/Projects/tailbus/examples/cli-tools)
Start here. Turn any CLI program into a Claude/Cursor tool in one line. Zero dependencies.

2. [chat-room](/Users/alexanderfrey/Projects/tailbus/examples/chat-room)
Fastest interactive demo. Best for understanding rooms and multi-node participation quickly.

3. [incident-room](/Users/alexanderfrey/Projects/tailbus/examples/incident-room)
Flagship product demo. Best for discovery, rooms, cross-node specialists, and dashboard visibility.

4. [dev-task-room](/Users/alexanderfrey/Projects/tailbus/examples/dev-task-room)
Most impressive agentic room demo. Best for safe local execution with visible agent initiative.

5. [parallel-review](/Users/alexanderfrey/Projects/tailbus/examples/parallel-review)
Best for parallel specialist fan-out in one room.

6. [agent-swarm](/Users/alexanderfrey/Projects/tailbus/examples/agent-swarm)
Best zero-dependency demo. Pure Go, fast, and deterministic.

7. [pair-solver](/Users/alexanderfrey/Projects/tailbus/examples/pair-solver)
Best for understanding the room primitive itself.

8. [app-builder](/Users/alexanderfrey/Projects/tailbus/examples/app-builder)
Most visually flashy build demo, but dependency-heavy.

9. [multi-machine](/Users/alexanderfrey/Projects/tailbus/examples/multi-machine)
Best for real cross-machine mesh setup. Highest setup cost.

## Quick Picks

| Example | Best For | External deps | Start |
|---|---|---|---|
| `cli-tools` | **Start here** — CLI → Claude tool | none | `./run.sh demo` |
| `chat-room` | Fast first demo | LM Studio | `./run.sh doctor && ./run.sh start && ./run.sh chat` |
| `incident-room` | Tailbus flagship | none in deterministic mode | `./run.sh doctor && ./run.sh && ./run.sh fire checkout` |
| `dev-task-room` | Agentic room workflow | Codex + LM Studio | `./run.sh doctor && ./run.sh && ./run.sh fire client-timeout` |
| `parallel-review` | Parallel specialist reviews | LM Studio | `./run.sh doctor && ./run.sh demo` |
| `agent-swarm` | Fast deterministic mesh demo | none | `./run.sh doctor && ./run.sh demo` |

## Conventions

Most local examples now support:

- `./run.sh doctor`
- `./run.sh start` or `./run.sh`
- `./run.sh dashboard`
- `./run.sh fire ...`
- `./run.sh logs`
- `./run.sh stop`
- `./run.sh clean`

`clean` is the destructive reset:
- stops the demo
- clears persisted daemon/coord state
- removes logs
- removes generated outputs for examples that produce them

`multi-machine` is the exception. It uses `docker compose` files instead of a local `run.sh`.
