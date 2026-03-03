#!/usr/bin/env python3
"""Orchestrator agent — demonstrates multi-agent collaboration.

Receives a task, delegates subtasks to other agents (calculator, echo),
aggregates results, and returns the final answer. Shows how agents
compose into workflows.
"""

import asyncio
import json
import os
import sys

sys.path.insert(0, "/sdk/python/src")

from tailbus import AsyncAgent, Manifest, CommandSpec, Message


agent = AsyncAgent(
    "orchestrator",
    manifest=Manifest(
        description="Orchestrates tasks across multiple agents",
        commands=[
            CommandSpec(
                name="compute",
                description="Compute an expression using calculator: e.g. 'add 2 3' or 'multiply 4 5'",
                parameters_schema=json.dumps({
                    "type": "object",
                    "properties": {
                        "operation": {"type": "string", "enum": ["add", "multiply"]},
                        "a": {"type": "number"},
                        "b": {"type": "number"},
                    },
                    "required": ["operation", "a", "b"],
                }),
            ),
            CommandSpec(
                name="ping",
                description="Ping the echo agent to verify connectivity",
                parameters_schema=json.dumps({
                    "type": "object",
                    "properties": {
                        "message": {"type": "string", "description": "Message to echo"},
                    },
                    "required": ["message"],
                }),
            ),
        ],
        tags=["orchestration", "demo"],
        version="1.0.0",
    ),
    socket=os.environ.get("TAILBUS_SOCKET", "/tmp/tailbusd.sock"),
)

# Track responses by session
pending_responses: dict[str, asyncio.Future] = {}


@agent.on_message
async def handle(msg: Message):
    """Handle incoming messages and delegate to sub-agents."""
    # Check if this is a response to our delegation
    if msg.session in pending_responses:
        fut = pending_responses.pop(msg.session)
        if not fut.done():
            fut.set_result(msg.payload)
        return

    try:
        data = json.loads(msg.payload)
    except json.JSONDecodeError:
        await agent.resolve(msg.session, json.dumps({
            "error": "Expected JSON with 'command' and 'arguments'",
        }), content_type="application/json")
        return

    command = data.get("command", "")
    args = data.get("arguments", data)

    if isinstance(args, str):
        try:
            args = json.loads(args)
            command = args.get("command", command)
            args = args.get("arguments", args)
        except (json.JSONDecodeError, AttributeError):
            pass

    if command == "compute":
        op = args.get("operation", "add")
        a = args.get("a", 0)
        b = args.get("b", 0)

        # Delegate to calculator agent
        calc_payload = json.dumps({"command": op, "arguments": {"a": a, "b": b}})
        try:
            result = await delegate("calculator", calc_payload, timeout=10)
            response = json.loads(result) if isinstance(result, str) else result
            await agent.resolve(msg.session, json.dumps({
                "orchestrator": "compute",
                "delegated_to": "calculator",
                "result": response,
            }), content_type="application/json")
        except asyncio.TimeoutError:
            await agent.resolve(msg.session, json.dumps({
                "error": "Calculator agent did not respond in time",
            }), content_type="application/json")

    elif command == "ping":
        message = args.get("message", "hello")
        try:
            result = await delegate("echo", message, timeout=10)
            await agent.resolve(msg.session, json.dumps({
                "orchestrator": "ping",
                "delegated_to": "echo",
                "response": result,
            }), content_type="application/json")
        except asyncio.TimeoutError:
            await agent.resolve(msg.session, json.dumps({
                "error": "Echo agent did not respond in time",
            }), content_type="application/json")

    else:
        # List available sub-commands
        handles = await agent.list_handles()
        available = [h.handle for h in handles if h.handle != "orchestrator"]
        await agent.resolve(msg.session, json.dumps({
            "error": f"Unknown command: {command}",
            "available_commands": ["compute", "ping"],
            "available_agents": available,
        }), content_type="application/json")


async def delegate(target: str, payload: str, timeout: float = 30) -> str:
    """Open a session to target agent and wait for the response."""
    opened = await agent.open_session(target, payload, content_type="application/json")
    fut: asyncio.Future = asyncio.get_running_loop().create_future()
    pending_responses[opened.session] = fut
    try:
        return await asyncio.wait_for(fut, timeout=timeout)
    except asyncio.TimeoutError:
        pending_responses.pop(opened.session, None)
        raise


async def main():
    print(f"[orchestrator] starting on socket {agent._socket}", flush=True)
    async with agent:
        await agent.register()
        print("[orchestrator] registered, listening for messages...", flush=True)
        await agent.run_forever()


if __name__ == "__main__":
    asyncio.run(main())
