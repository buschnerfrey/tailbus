# Dev Task Room

`dev-task-room` is the Tailbus engineering war-room demo.

It uses:
- a Codex-backed `implementer` that leads the task
- an LM Studio-backed `critic` that reviews and can block unsafe changes
- an LM Studio-backed `test-strategist` that proposes targeted validation work
- a local `workspace-agent` that is the only component allowed to write files on the machine
- one shared Tailbus room as the source of truth for the whole workflow

The user sends a task. Tailbus discovers the collaborators by capability, the workspace agent materializes the bounded fixture workspace, the implementer proposes a plan and a whole-file change set, the critic recruits the test strategist, and the room converges on either an approved apply or a bounded failure outcome.

## Why this example matters

This is the clearest Tailbus room demo in the repo:
- different agents on different nodes
- different model backends for different roles
- one replayable room transcript instead of stitched point-to-point prompts
- capability discovery instead of hardcoded collaborator names
- visible agent initiative: delegation, review, test planning, repair
- a strict local safety boundary around filesystem writes

Remote agents never write directly to your filesystem. They propose work in the room. The local `workspace-agent` applies approved changes under `/tmp/devtaskroom-workspace`.

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

Then run a curated scenario:

```bash
./run.sh scenarios
./run.sh fire client-timeout
./run.sh fire focus-timer
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

`./run.sh stop` also clears the demo's persisted coord and daemon room state, so the next start comes up with an empty `ROOMS` list.

## What to watch for

In the dashboard and transcript you should see:
- `task-orchestrator` discover the room members and open the task
- `workspace-agent` prepare the workspace automatically
- `implementer` post a `plan_proposed` event before it writes a change set
- `critic` delegate to `test-strategist` before posting a review decision
- `implementer` trigger apply only after the room approves
- a `final_outcome` event that closes the loop

The point is not just that agents can talk. The point is that the room makes their decisions legible.

## Requirements

- `tailbus`, `tailbusd`, `tailbus-coord` built into the repo `bin/` directory
- `python3`
- `curl`
- `codex` CLI on `PATH`
- LM Studio running at `http://localhost:1234/v1` unless `LLM_BASE_URL` is set

Optional env vars:

```bash
LLM_BASE_URL=http://localhost:1234/v1
LLM_MODEL=<lm-studio-model>
CODEX_MODEL=gpt-5.1-codex-mini
TURN_TIMEOUT=600
CODEX_TIMEOUT=600
FIRE_TIMEOUT=600s
WORKSPACE_ROOT=/tmp/devtaskroom-workspace
DEV_TASK_ROOM_MAX_REVISIONS=2
DEV_TASK_ROOM_MAX_REPAIRS=1
DEV_TASK_ROOM_DEBUG=1
```

For larger tasks such as `snake-clone`, the example allows longer turns before timing out. Override `TURN_TIMEOUT`, `CODEX_TIMEOUT`, or `FIRE_TIMEOUT` if you want tighter or looser budgets.

`DEV_TASK_ROOM_DEBUG=1` enables extra room-replay diagnostics in the agent logs. Leave it unset for the quieter default output.

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

It also persists live task state snapshots to:

- `examples/dev-task-room/output/state/`

The markdown output includes:
- discovered collaborators
- the latest implementation summary
- the final review outcome
- apply and test status
- the full room transcript

The state snapshot includes:
- room id and lifecycle status
- counts for plans, delegations, implementations, reviews, and apply cycles
- the latest workflow phase
- output artifact paths for successful or failed runs
