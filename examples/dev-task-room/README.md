# Dev Task Room

`dev-task-room` is a Tailbus demo for arbitrary development tasks.

It uses:
- a Codex-backed implementer
- an LM Studio-backed critic
- a local workspace agent that is the only component allowed to write files on the user's machine
- one shared Tailbus room for the full engineering loop

The user sends a task. Tailbus discovers the collaborators by capability, the implementer proposes a whole-file change set, the critic reviews it, and the workspace agent applies the approved files into a bounded local fixture workspace and runs tests.

## Why this example matters

This shows the Tailbus boundary clearly:
- different agents on different nodes
- different LLM backends for different roles
- one shared room transcript
- capability discovery instead of hardcoded collaborators
- safe local file creation via a local workspace-owning agent

Remote agents do not write directly to your filesystem. They propose changes. The local `workspace-agent` applies them under `/tmp/devtaskroom-workspace`.

## Quickstart

From the repo root:

```bash
make build
cd examples/dev-task-room
./run.sh doctor
./run.sh
```

Open the dashboard in another terminal:

```bash
./run.sh dashboard
```

Then run a curated task:

```bash
./run.sh scenarios
./run.sh fire focus-timer
./run.sh fire snake-clone
```

Or send an arbitrary task:

```bash
./run.sh fire-task "Add a timeout parameter to the HTTP client and update tests."
```

Useful commands:

```bash
./run.sh logs
./run.sh stop
```

## Requirements

- `tailbus`, `tailbusd`, `tailbus-coord` built into the repo `bin/` directory
- `python3`
- `curl`
- `codex` CLI on `PATH`
- LM Studio running at `http://127.0.0.1:1234/v1` unless `LLM_BASE_URL` is set

Optional env vars:

```bash
LLM_BASE_URL=http://127.0.0.1:1234/v1
LLM_MODEL=<lm-studio-model>
CODEX_MODEL=gpt-5.1-codex-mini
TURN_TIMEOUT=600
CODEX_TIMEOUT=600
FIRE_TIMEOUT=600s
WORKSPACE_ROOT=/tmp/devtaskroom-workspace
```

For larger tasks such as `snake-clone`, the default example now allows longer turns before timing out. Override `TURN_TIMEOUT`, `CODEX_TIMEOUT`, or `FIRE_TIMEOUT` if you want tighter or looser budgets.

`CODEX_MODEL` defaults to `gpt-5.1-codex-mini`. Override it if your local `codex` account supports a different model set.

## What gets written locally

The live workspace is materialized from the tracked template in:

- `examples/dev-task-room/workspace-template/`

The mutable workspace is created in:

- `/tmp/devtaskroom-workspace`

The local workspace agent:
- resets that workspace before each task
- rejects absolute paths and path traversal
- applies whole-file writes only inside that root
- runs the fixture tests after applying changes

## Output

The orchestrator writes a markdown transcript to:

- `examples/dev-task-room/output/`

It also persists live task and turn state snapshots to:

- `examples/dev-task-room/output/state/`

That file includes:
- the task
- discovered collaborators
- implementation summary
- review findings
- changed files
- test result
- the room transcript

The state snapshot includes:
- room id and lifecycle status
- per-turn request/progress/reply/timeout data
- latest workflow phase
- output artifact paths for successful or failed runs
