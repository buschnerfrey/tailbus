#!/usr/bin/env python3
"""Echo agent for the multi-machine example."""

import asyncio
import os
import sys

sys.path.insert(0, "/sdk/python/src")

from tailbus import AsyncAgent, Manifest, Message

agent = AsyncAgent(
    "echo",
    manifest=Manifest(
        description="Echoes messages back to the sender",
        tags=["utility", "demo"],
        version="1.0.0",
    ),
    socket=os.environ.get("TAILBUS_SOCKET", "/tmp/tailbusd.sock"),
)


@agent.on_message
async def handle(msg: Message):
    await agent.resolve(msg.session, f"Echo from {agent.handle}: {msg.payload}")


async def main():
    print(f"[echo] starting on socket {agent._socket}", flush=True)
    async with agent:
        await agent.register()
        print("[echo] registered, listening for messages...", flush=True)
        await agent.run_forever()


if __name__ == "__main__":
    asyncio.run(main())
