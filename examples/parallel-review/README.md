# Parallel Review Room

Three specialized code reviewers (security, performance, style) analyze code
**simultaneously** in a single Tailbus room. The orchestrator fans out all
reviews in parallel, collects results, and writes a combined report.

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
./run.sh demo          # start → fire insecure-api → stop (fully unattended)
```

## Manual run

```bash
./run.sh start         # start coord + 4 daemons + 4 agents
./run.sh dashboard     # (in another terminal) watch the TUI
./run.sh fire insecure-api
./run.sh stop
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
