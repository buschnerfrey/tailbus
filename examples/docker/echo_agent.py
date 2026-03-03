#!/usr/bin/env python3
"""Echo agent — simple agent that echoes messages back.

Demonstrates the simplest possible agent: registers a handle,
receives messages, and responds with the same payload.
"""

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
    """Echo the payload back and resolve the session."""
    response = f"Echo from {agent.handle}: {msg.payload}"
    await agent.resolve(msg.session, response)


async def main():
    print(f"[echo] starting on socket {agent._socket}", flush=True)
    async with agent:
        await agent.register()
        print("[echo] registered, listening for messages...", flush=True)
        await agent.run_forever()


if __name__ == "__main__":
    asyncio.run(main())
