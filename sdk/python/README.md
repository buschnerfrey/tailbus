# tailbus Python SDK

Python SDK for [tailbus](https://github.com/alexanderfrey/tailbus) — Slack for AI agents.

## Install

```bash
pip install tailbus
```

Requires Python 3.10+ and the `tailbus` CLI on your PATH (`make build` or the install script).

> **Compatibility:** SDK 0.4.0 requires tailbusd 0.4.0+.

## Quick start

### Echo agent (async)

```python
import asyncio
from tailbus import AsyncAgent, Manifest, Message

agent = AsyncAgent("echo", manifest=Manifest(description="Echoes messages back"))

@agent.on_message
async def handle(msg):
    if isinstance(msg, Message):
        await agent.send(msg.session, f"echo: {msg.payload}")
        await agent.resolve(msg.session)

async def main():
    async with agent:
        await agent.register()
        await agent.run_forever()

asyncio.run(main())
```

### Request-response (sync)

```python
from tailbus import SyncAgent

with SyncAgent("client") as agent:
    agent.register()
    opened = agent.open_session("echo", "hello")
    agent.resolve(opened.session, "done")
```

## Concepts

**Sessions** are point-to-point conversations between two agents. One agent opens a session, both sides exchange messages, and either side resolves it to close.

**Rooms** are shared spaces where multiple agents communicate via broadcast events. An agent creates a room, invites members, and all members receive every posted message.

**Discovery** lets agents find each other by capabilities, domains, or tags without knowing handle names in advance.

**Manifests** describe what an agent does — its capabilities, accepted input types, commands, and metadata. Other agents can introspect a handle to read its manifest.

## API reference

### AsyncAgent

```python
AsyncAgent(
    handle: str,
    *,
    manifest: Manifest | None = None,
    binary: str = "tailbus",
    socket: str = "/tmp/tailbusd.sock",
)
```

Use as an async context manager:

```python
async with AsyncAgent("my-agent") as agent:
    await agent.register()
    # ...
```

Or manage the lifecycle manually with `await agent.start()` / `await agent.close()`.

#### Registration

| Method | Returns | Description |
|--------|---------|-------------|
| `register(*, manifest=None)` | `Registered` | Register with the local daemon. Must be called before any other operation. |

#### Sessions

| Method | Returns | Description |
|--------|---------|-------------|
| `open_session(to, payload, *, content_type="text/plain", trace_id="")` | `Opened` | Open a session with another agent. Returns the session ID. |
| `send(session, payload, *, content_type="text/plain")` | `Sent` | Send a message within an open session. |
| `resolve(session, payload="", *, content_type="text/plain")` | `Resolved` | Close a session with an optional final message. |
| `list_sessions()` | `list[SessionInfo]` | List this agent's active sessions. |

#### Discovery

| Method | Returns | Description |
|--------|---------|-------------|
| `introspect(handle)` | `Introspected` | Read another handle's manifest. |
| `list_handles(*, tags=None)` | `list[HandleEntry]` | List all registered handles, optionally filtered by tags. |
| `find_handles(*, capabilities, domains, tags, command_name, version, limit)` | `list[HandleMatch]` | Ranked search for agents matching constraints. All parameters optional. |

#### Rooms

| Method | Returns | Description |
|--------|---------|-------------|
| `create_room(title, members=None)` | `str` (room_id) | Create a room and invite members. |
| `join_room(room_id)` | `bool` | Join an existing room. |
| `leave_room(room_id)` | `bool` | Leave a room. |
| `post_room_message(room_id, payload, *, content_type="text/plain", trace_id="")` | `RoomPosted` | Post a message to a room. |
| `list_rooms()` | `list[RoomInfo]` | List rooms this agent belongs to. |
| `list_room_members(room_id)` | `list[str]` | List members of a room. |
| `replay_room(room_id, *, since_seq=0)` | `list[RoomEvent]` | Replay room events from a given sequence number. |
| `close_room(room_id)` | `bool` | Close a room to new messages. |

#### Message handling

| Method | Description |
|--------|-------------|
| `on_message(fn)` | Decorator to register a handler for incoming `Message` and `RoomEvent` objects. Supports both sync and async handlers. |
| `run_forever()` | Block until `close()` is called or the bridge process exits. |

### SyncAgent

Same API as `AsyncAgent` but all methods are synchronous. Runs an internal event loop on a daemon thread.

```python
with SyncAgent("my-agent", socket="/tmp/tailbusd.sock") as agent:
    agent.register()
    # all methods block until complete
```

### Manifest

```python
Manifest(
    description: str = "",
    commands: tuple[CommandSpec, ...] = (),
    tags: tuple[str, ...] = (),
    version: str = "",
    capabilities: tuple[str, ...] = (),
    domains: tuple[str, ...] = (),
    input_types: tuple[str, ...] = (),
    output_types: tuple[str, ...] = (),
)
```

### CommandSpec

```python
CommandSpec(
    name: str,
    description: str,
    parameters_schema: str = "",   # JSON Schema string
)
```

### Data types

All response types are frozen dataclasses.

| Type | Key fields |
|------|------------|
| `Registered` | `handle` |
| `Opened` | `session`, `message_id`, `trace_id` |
| `Sent` | `message_id` |
| `Resolved` | `message_id` |
| `Message` | `session`, `from_handle`, `to_handle`, `payload`, `content_type`, `message_type`, `trace_id` |
| `RoomEvent` | `room_id`, `room_seq`, `sender_handle`, `subject_handle`, `payload`, `content_type`, `event_type` |
| `RoomInfo` | `room_id`, `title`, `created_by`, `members`, `status` |
| `RoomPosted` | `event_id`, `room_seq` |
| `HandleEntry` | `handle`, `manifest` |
| `HandleMatch` | `handle`, `manifest`, `score`, `match_reasons` |
| `Introspected` | `handle`, `found`, `manifest` |
| `SessionInfo` | `session`, `from_handle`, `to_handle`, `state` |

### Errors

All exceptions inherit from `TailbusError`.

| Exception | When |
|-----------|------|
| `BridgeError` | The daemon returned an error (e.g. handle not found, room closed). Has `.error` and `.request_type` attributes. |
| `BridgeDiedError` | The bridge subprocess exited. Has `.returncode` attribute. |
| `BinaryNotFoundError` | The `tailbus` binary is not on PATH. |
| `NotRegisteredError` | An operation was attempted before `register()`. |
| `AlreadyRegisteredError` | `register()` was called twice. |

## Examples

### Discover agents by capability

```python
async with AsyncAgent("orchestrator") as agent:
    await agent.register()

    # Find all agents that can review code
    reviewers = await agent.find_handles(capabilities=["code.review"])
    for match in reviewers:
        print(f"{match.handle} (score={match.score}): {match.match_reasons}")
```

### Multi-agent room coordination

```python
async with AsyncAgent("coordinator", manifest=Manifest(
    description="Task coordinator",
    capabilities=("task.coordinate",),
)) as agent:
    await agent.register()

    # Create a room with workers
    workers = await agent.find_handles(capabilities=["task.execute"])
    room_id = await agent.create_room(
        "task-42",
        members=[w.handle for w in workers],
    )

    # Post a task
    await agent.post_room_message(
        room_id,
        '{"kind": "task", "description": "implement feature X"}',
        content_type="application/json",
    )

    # Later: replay to see what happened
    events = await agent.replay_room(room_id)
    for event in events:
        if event.event_type == "message_posted":
            print(f"{event.sender_handle}: {event.payload}")

    await agent.close_room(room_id)
```

### Handle incoming sessions and room events

```python
agent = AsyncAgent("worker", manifest=Manifest(
    description="Task worker",
    capabilities=("task.execute",),
    input_types=("application/json",),
))

@agent.on_message
async def handle(msg):
    if isinstance(msg, Message):
        # Point-to-point session message
        print(f"Session from {msg.from_handle}: {msg.payload}")
        await agent.resolve(msg.session, "done")

    elif isinstance(msg, RoomEvent):
        # Broadcast room event
        if msg.event_type == "message_posted":
            print(f"Room {msg.room_id}: {msg.sender_handle} says {msg.payload}")

async def main():
    async with agent:
        await agent.register()
        await agent.run_forever()
```

### Error handling

```python
from tailbus import AsyncAgent, BridgeError, BridgeDiedError

async with AsyncAgent("client") as agent:
    await agent.register()
    try:
        opened = await agent.open_session("missing-agent", "hello")
    except BridgeError as exc:
        print(f"Failed ({exc.request_type}): {exc.error}")
    except BridgeDiedError as exc:
        print(f"Daemon connection lost (exit code {exc.returncode})")
```

### Manifest with typed commands

```python
import json

agent = AsyncAgent("calculator", manifest=Manifest(
    description="Basic calculator",
    commands=(
        CommandSpec(
            name="add",
            description="Add two numbers",
            parameters_schema=json.dumps({
                "type": "object",
                "properties": {
                    "a": {"type": "number"},
                    "b": {"type": "number"},
                },
                "required": ["a", "b"],
            }),
        ),
    ),
    tags=("math", "utility"),
    version="1.0.0",
    capabilities=("math.arithmetic",),
    domains=("engineering",),
    input_types=("application/json",),
    output_types=("application/json",),
))
```

### Custom socket path

```python
# Connect to a specific daemon instance
agent = AsyncAgent("my-agent", socket="/tmp/my-project.sock")
```

The socket path defaults to `/tmp/tailbusd.sock`. Override it when running multiple daemons or using example scripts that create project-specific sockets.

## Architecture

The SDK communicates with the local `tailbusd` daemon through a JSON-lines stdio bridge:

```
Your Python code  ←→  AsyncAgent  ←→  tailbus bridge (subprocess)  ←→  tailbusd (Unix socket)
```

The bridge is the `tailbus` CLI binary running in agent mode. It translates between the JSON-lines protocol and the daemon's gRPC API over a Unix socket. This means:

- No gRPC dependency in your Python code
- No protobuf compilation needed
- The `tailbus` binary must be on PATH (or pass `binary="/path/to/tailbus"`)
- The daemon must be running and accessible at the configured socket path

## License

MIT
