# Chat Room

Interactive multi-agent chat: you type, two LLM agents respond. Each agent
has a distinct personality and runs on its own daemon node.

Best for: the fastest way to feel what a Tailbus room is.

## Why this example matters

This is the easiest example to understand viscerally. You join the room as a
human participant, the agents see the shared transcript, and the difference
between direct sessions and room-based collaboration becomes obvious quickly.

## What it demonstrates

- **Interactive room participation**: human agent alongside AI agents in real-time
- **@mention routing**: direct messages to specific agents with `@atlas` or `@nova`
- **Multi-node topology**: 3 daemons visible in the dashboard
- **Conversation context**: agents see the full room history and respond contextually

## Agents

| Handle | Style | Personality |
|--------|-------|-------------|
| `atlas` | analytical | Thorough, structured, cites best practices |
| `nova` | creative | Opinionated, concise, sometimes contrarian |

## Requirements

- `tailbus-coord`, `tailbusd`, `tailbus` binaries (`make build`)
- Python 3.11+
- [LM Studio](https://lmstudio.ai/) running locally with a model loaded

## Quick start

```bash
make build
cd examples/chat-room
./run.sh doctor

# Terminal 1: start the infrastructure and agents
./run.sh start

# Terminal 2 (optional): watch the dashboard
./run.sh dashboard

# Terminal 3: open the chat
./run.sh chat
```

If you want a full reset after a run:

```bash
./run.sh clean
```

## What to watch for

- the room forming around `you`, `atlas`, and `nova`
- both agents reacting to the same shared context
- `@atlas` and `@nova` mentions steering participation without changing topology
- the dashboard showing human and AI room participation together

## Usage

```
  Chat Room  (you, atlas, nova)
  ──────────────────────────────────────────────────
  type a message and press enter. @name to direct.
  ctrl-c to quit.

  you > What's the best way to handle errors in Go?

  atlas  Use sentinel errors for expected conditions and wrap errors
         with fmt.Errorf for context. The errors.Is and errors.As
         functions let callers inspect wrapped chains.

  nova   Return errors. Handle them at the boundary. That's it.
         Most Go code over-wraps. Keep it simple.

  you > @atlas what about custom error types?

  atlas  Define custom types when callers need to branch on error
         properties beyond identity — e.g., an HTTPError with a
         status code field.
```

Both agents respond to general messages. Use `@atlas` or `@nova` to direct
a message to one specific agent.

## Architecture

```
control-node ── you     (human, chat.py)
atlas-node ──── atlas   (LLM, analytical)
nova-node ───── nova    (LLM, creative)
```

## Output

This example is live and transient:

- room traffic is visible in the dashboard
- logs are written to `/tmp/chatroom-logs/`
- `./run.sh clean` removes persisted local state and logs
