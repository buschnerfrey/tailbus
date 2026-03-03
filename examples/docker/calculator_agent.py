#!/usr/bin/env python3
"""Calculator agent — demonstrates tool-style commands with JSON schema.

Registers as 'calculator' with add/multiply commands. Responds to incoming
messages by parsing the command and returning the result.
"""

import asyncio
import json
import os
import sys

# Add SDK to path (when running inside container with mounted SDK)
sys.path.insert(0, "/sdk/python/src")

from tailbus import AsyncAgent, Manifest, CommandSpec, Message


agent = AsyncAgent(
    "calculator",
    manifest=Manifest(
        description="A calculator that can add and multiply numbers",
        commands=[
            CommandSpec(
                name="add",
                description="Add two numbers together",
                parameters_schema=json.dumps({
                    "type": "object",
                    "properties": {
                        "a": {"type": "number", "description": "First number"},
                        "b": {"type": "number", "description": "Second number"},
                    },
                    "required": ["a", "b"],
                }),
            ),
            CommandSpec(
                name="multiply",
                description="Multiply two numbers together",
                parameters_schema=json.dumps({
                    "type": "object",
                    "properties": {
                        "a": {"type": "number", "description": "First number"},
                        "b": {"type": "number", "description": "Second number"},
                    },
                    "required": ["a", "b"],
                }),
            ),
        ],
        tags=["math", "tools"],
        version="1.0.0",
    ),
    socket=os.environ.get("TAILBUS_SOCKET", "/tmp/tailbusd.sock"),
)


@agent.on_message
async def handle(msg: Message):
    """Handle incoming messages by parsing commands."""
    try:
        data = json.loads(msg.payload)
    except json.JSONDecodeError:
        # Plain text — try to parse as "add 1 2" style
        parts = msg.payload.split()
        if len(parts) == 3 and parts[0] in ("add", "multiply"):
            data = {
                "command": parts[0],
                "arguments": {"a": float(parts[1]), "b": float(parts[2])},
            }
        else:
            await agent.resolve(msg.session, json.dumps({
                "error": "Expected JSON with 'command' and 'arguments' fields",
            }), content_type="application/json")
            return

    command = data.get("command", "")
    args = data.get("arguments", data.get("message", data))

    if isinstance(args, str):
        # MCP gateway sends {"message": "..."} — try to parse as command
        try:
            args = json.loads(args)
            command = args.get("command", command)
            args = args.get("arguments", args)
        except (json.JSONDecodeError, AttributeError):
            pass

    if command == "add":
        a = float(args.get("a", 0))
        b = float(args.get("b", 0))
        result = a + b
        await agent.resolve(msg.session, json.dumps({"result": result}),
                          content_type="application/json")
    elif command == "multiply":
        a = float(args.get("a", 0))
        b = float(args.get("b", 0))
        result = a * b
        await agent.resolve(msg.session, json.dumps({"result": result}),
                          content_type="application/json")
    else:
        await agent.resolve(msg.session, json.dumps({
            "error": f"Unknown command: {command}. Available: add, multiply",
        }), content_type="application/json")


async def main():
    print(f"[calculator] starting on socket {agent._socket}", flush=True)
    async with agent:
        await agent.register()
        print("[calculator] registered, listening for messages...", flush=True)
        await agent.run_forever()


if __name__ == "__main__":
    asyncio.run(main())
