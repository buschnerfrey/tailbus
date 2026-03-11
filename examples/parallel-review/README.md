# Parallel Review Room

Three specialized code reviewers (security, performance, style) analyze code
**simultaneously** in a single Tailbus room. The orchestrator fans out all
reviews in parallel, collects results, and writes a combined report.

Best for: seeing parallel specialist work in one shared room.

## Why this example matters

This is the clearest demo of parallel specialist work in Tailbus. It shows
that one orchestrator can open a room, fan out work to multiple reviewers at
once, and collect distinct judgments without collapsing everything into a
single serial chain.

## What it demonstrates

- **Parallel fan-out**: 3 review requests dispatched simultaneously via `asyncio.gather`
- **Capability discovery**: orchestrator finds reviewers by capability (`review.security`, etc.)
- **Multi-node topology**: 4 daemons, each hosting one agent
- **Dashboard visualization**: 3 busy turns running at the same time

## Requirements

- `tailbus-coord`, `tailbusd`, `tailbus` binaries (`make build`)
- Python 3.11+
- [LM Studio](https://lmstudio.ai/) running locally with a model loaded

## Quick start

```bash
make build
cd examples/parallel-review
./run.sh doctor
./run.sh demo          # start → fire insecure-api → stop (fully unattended)
```

## What to watch for

- all three reviewers becoming active at once
- capability discovery selecting reviewers by role
- parallel room traffic rather than one-by-one orchestration
- the combined report landing after three independent review passes

## Manual run

```bash
./run.sh start         # start coord + 4 daemons + 4 agents
./run.sh dashboard     # (in another terminal) watch the TUI
./run.sh fire insecure-api
./run.sh stop
./run.sh clean
```

## Scenarios

| Name | Description |
|------|-------------|
| `insecure-api` | Flask API with SQL injection, auth bypass, secret exposure |
| `slow-search` | O(n²) loops, bubble sort, redundant iterations |
| `messy-code` | Single-letter variables, deep nesting, no docs |

## Architecture

```
control-node ─── review-orchestrator (workflow.orchestrate)
security-node ── security-reviewer   (review.security)
perf-node ────── perf-reviewer       (review.performance)
style-node ───── style-reviewer      (review.style)
```

All reviewers use a single `reviewer.py` configured via environment variables.

## Output

Combined reports and generated example output are written under:

- `examples/parallel-review/output/`
