# tailbus Python SDK

Python SDK for [tailbus](https://github.com/alexanderfrey/tailbus) — an agent communication mesh.

## Install

```bash
pip install tailbus
```

Requires Python 3.10+ and the `tailbus` CLI on your PATH.

## Quick start

### Async

```python
from tailbus import AsyncAgent, Manifest

async with AsyncAgent("my-agent") as agent:
    await agent.register(manifest=Manifest(description="My agent"))

    @agent.on_message
    async def handle(msg):
        await agent.send(msg.session, f"echo: {msg.body}")

    await agent.run_forever()
```

### Sync

```python
from tailbus import SyncAgent

with SyncAgent("my-agent") as agent:
    agent.register()
    opened = agent.open_session("other-agent", "hello")
    agent.send(opened.session, "follow-up")
    agent.resolve(opened.session, "done")
```

## API

- **`AsyncAgent(handle, binary="tailbus")`** — async/await agent with `@on_message` decorator and `run_forever()`
- **`SyncAgent(handle, binary="tailbus")`** — threading-based synchronous wrapper
- **`Manifest(description, commands)`** — service manifest with optional command specs
- **`CommandSpec(name, description, schema)`** — typed command with JSON Schema

Both agents support: `register()`, `open_session()`, `send()`, `resolve()`, `list_handles()`, `list_sessions()`, `introspect()`.

## License

MIT
