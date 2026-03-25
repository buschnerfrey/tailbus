# CLI Tools

Turn any CLI program into a tool Claude or Cursor can call — in one line.

Best for: the fastest way to see why tailbus exists.

## Why this example matters

No agents. No Python. No LLM. Just this:

```bash
tailbus attach --exec --handle search --description "Search code" -- grep -rn --include="*.go"
```

That one line turns `grep` into an MCP tool. Claude can now search your codebase
by calling `search` — including from another machine.

This demo attaches 5 CLI tools to the mesh and shows them working through the
MCP gateway. Each tool is a single `tailbus attach --exec` invocation.

## What it demonstrates

- **`--exec` mode**: any command that reads arguments and writes stdout is an agent
- **MCP gateway**: every attached tool appears as a Claude/Cursor tool automatically
- **Zero dependencies**: no Python, no LLM, no Docker — just the tailbus binaries
- **One line per tool**: each attach command is self-contained

## Tools attached

| Handle | Command | What it does |
|--------|---------|-------------|
| `search` | `grep -rn --include="*.go"` | Search Go source code by pattern |
| `readfile` | `cat` | Read any file's contents |
| `ls` | `ls -1` | List files in a directory |
| `git-log` | `git log --oneline -20` | Show recent commits |
| `git-diff` | `git diff --stat` | Show uncommitted changes |

## Quick start

```bash
make build
cd examples/cli-tools
./run.sh demo
```

Or step by step:

```bash
./run.sh doctor      # check prerequisites
./run.sh start       # start coord + daemon + attach tools
./run.sh fire        # call each tool via MCP
./run.sh dashboard   # (optional) watch the TUI
./run.sh stop        # shut down
```

## Connect Claude or Cursor

After `./run.sh start`, add to your MCP config:

```json
{
  "mcpServers": {
    "tailbus": { "url": "http://localhost:18852/mcp" }
  }
}
```

Now Claude can call `search`, `readfile`, `ls`, `git-log`, and `git-diff` as
tools — all backed by real CLI programs running on your machine.

## Add your own tools

```bash
# SQLite queries
tailbus attach --exec --handle sql -- sqlite3 -json mydb.db

# Docker container list
tailbus attach --exec --handle containers -- docker ps --format json

# Run tests
tailbus attach --exec --handle test -- go test -short

# Check disk usage
tailbus attach --exec --handle disk -- df -h

# Anything that takes an argument and writes to stdout works.
```
