#!/usr/bin/env python3
"""Interactive chat terminal for the chat-room example.

Registers as a human agent, creates a room with LLM agents,
and bridges stdin/stdout to the room.
"""

from __future__ import annotations

import asyncio
import json
import os
import sys
import time

sys_path = os.path.join(os.path.dirname(__file__), "../../sdk/python/src")
if sys_path not in sys.path:
    sys.path.insert(0, sys_path)

from tailbus import AsyncAgent, Manifest, Message, RoomEvent

HANDLE = os.environ.get("CHAT_HANDLE", "you")
SOCKET = os.environ.get("TAILBUS_SOCKET", "/tmp/chatroom-control-node.sock")

DIM = "\033[2m"
BOLD = "\033[1m"
GREEN = "\033[32m"
YELLOW = "\033[33m"
CYAN = "\033[36m"
RED = "\033[31m"
RESET = "\033[0m"

# Color palette for agent names.
AGENT_COLORS = {
    "atlas": "\033[34m",   # blue
    "nova": "\033[35m",    # magenta
}

agent = AsyncAgent(
    HANDLE,
    manifest=Manifest(
        description="Human chat participant",
        commands=[],
        tags=["human", "chat"],
        version="1.0.0",
        capabilities=["chat.human"],
        domains=["general"],
    ),
    socket=SOCKET,
)

room_id: str = ""


def color_for(name: str) -> str:
    if name in AGENT_COLORS:
        return AGENT_COLORS[name]
    return CYAN


def wrap_text(text: str, indent: int, width: int = 78) -> str:
    """Word-wrap text with indent for continuation lines."""
    if len(text) + indent <= width:
        return text
    lines: list[str] = []
    for paragraph in text.split("\n"):
        current = ""
        for word in paragraph.split():
            test = f"{current} {word}".strip()
            if len(test) + indent > width and current:
                lines.append(current)
                current = word
            else:
                current = test
        if current:
            lines.append(current)
        if not paragraph:
            lines.append("")
    prefix = " " * indent
    return ("\n" + prefix).join(lines)


@agent.on_message
async def handle(msg: Message | RoomEvent) -> None:
    if not isinstance(msg, RoomEvent):
        return
    if msg.event_type != "message_posted" or msg.content_type != "application/json":
        return
    try:
        payload = json.loads(msg.payload)
    except json.JSONDecodeError:
        return
    if payload.get("kind") != "chat":
        return
    author = payload.get("author", "?")
    if author == HANDLE:
        return
    text = str(payload.get("text", ""))
    if not text:
        return

    color = color_for(author)
    name_len = len(author)
    indent = name_len + 5  # "  name  " padding
    wrapped = wrap_text(text, indent)
    # Print on a new line, then re-show prompt.
    sys.stdout.write(f"\r\033[K  {color}{BOLD}{author}{RESET}  {wrapped}\n")
    sys.stdout.write(f"  {GREEN}{HANDLE}{RESET} > ")
    sys.stdout.flush()


async def read_stdin() -> str | None:
    """Read a line from stdin without blocking the event loop."""
    loop = asyncio.get_running_loop()
    try:
        return await loop.run_in_executor(None, sys.stdin.readline)
    except EOFError:
        return None


async def chat_loop() -> None:
    global room_id

    # Discover chat agents.
    matches = await agent.find_handles(capabilities=["chat.converse"], limit=10)
    if not matches:
        print(f"  {RED}No chat agents found.{RESET} Start agents first with ./run.sh start")
        return
    members = [m.handle for m in matches]
    member_list = ", ".join(f"{color_for(h)}{h}{RESET}" for h in members)

    # Create room.
    room_id = await agent.create_room("chat", members=members)

    # Post a greeting so agents know the room is live.
    await agent.post_room_message(
        room_id,
        json.dumps({
            "kind": "room_started",
            "author": HANDLE,
            "members": [HANDLE] + members,
        }),
        content_type="application/json",
    )

    # Print header.
    all_names = f"{GREEN}{HANDLE}{RESET}, {member_list}"
    print()
    print(f"  {BOLD}Chat Room{RESET}  {DIM}({all_names}{DIM}){RESET}")
    print(f"  {DIM}{'─' * 50}{RESET}")
    print(f"  {DIM}type a message and press enter. @name to direct.{RESET}")
    print(f"  {DIM}ctrl-c to quit.{RESET}")
    print()

    # Main input loop.
    while True:
        sys.stdout.write(f"  {GREEN}{HANDLE}{RESET} > ")
        sys.stdout.flush()
        line = await read_stdin()
        if line is None:
            break
        text = line.strip()
        if not text:
            continue
        if text.lower() in ("quit", "exit", "/quit", "/exit"):
            break

        msg_payload = {
            "kind": "chat",
            "author": HANDLE,
            "text": text,
            "sent_at": int(time.time()),
        }
        try:
            await agent.post_room_message(
                room_id,
                json.dumps(msg_payload),
                content_type="application/json",
            )
        except Exception as exc:
            print(f"  {RED}send failed{RESET}: {exc}")

    # Clean up.
    print(f"\n  {DIM}closing room...{RESET}")
    try:
        await agent.close_room(room_id)
    except Exception:
        pass


async def main() -> None:
    async with agent:
        await agent.register()
        await chat_loop()


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except KeyboardInterrupt:
        print(f"\n  {DIM}bye{RESET}")
        if room_id:
            # Best-effort close.
            try:
                asyncio.run(agent.close_room(room_id))
            except Exception:
                pass
