#!/usr/bin/env python3
"""LLM-backed chat agent for the chat-room example.

Configure via environment variables:
  AGENT_HANDLE   = atlas | nova | ...
  AGENT_STYLE    = analytical | creative
  TAILBUS_SOCKET = daemon socket path
"""

from __future__ import annotations

import asyncio
import json
import os
import re
import sys
import time
import urllib.error
import urllib.request
from typing import Any, Callable, Iterable

sys_path = os.path.join(os.path.dirname(__file__), "../../sdk/python/src")
if sys_path not in sys.path:
    sys.path.insert(0, sys_path)

from tailbus import AsyncAgent, BridgeError, Manifest, RoomEvent

HANDLE = os.environ.get("AGENT_HANDLE", "atlas")
STYLE = os.environ.get("AGENT_STYLE", "analytical")
LLM_BASE_URL = os.environ.get("LLM_BASE_URL", "http://127.0.0.1:1234/v1")
LLM_MODEL = os.environ.get("LLM_MODEL", "")
LLM_REQUEST_TIMEOUT = float(os.environ.get("LLM_REQUEST_TIMEOUT", "120"))
REPLY_TIMEOUT = float(os.environ.get("REPLY_TIMEOUT", "90"))

DIM = "\033[2m"
BOLD = "\033[1m"
GREEN = "\033[32m"
YELLOW = "\033[33m"
RESET = "\033[0m"

STYLE_PROMPTS: dict[str, str] = {
    "analytical": """\
You are Atlas, a thoughtful and precise conversationalist in a shared chat room.
You give well-reasoned, structured answers. You cite best practices and consider
trade-offs. You're the senior engineer in the room — thorough but not verbose.
Keep responses to 2-4 sentences unless the topic demands more.""",
    "creative": """\
You are Nova, a bold and creative conversationalist in a shared chat room.
You give opinionated, concise answers. You challenge assumptions and suggest
unconventional approaches. You're the startup CTO — direct, practical, sometimes
contrarian. Keep responses to 1-3 sentences. Be punchy.""",
}


def say(msg: str) -> None:
    print(f"  {DIM}{HANDLE}{RESET}  {msg}", flush=True)


def is_room_closed_error(exc: Exception) -> bool:
    return isinstance(exc, BridgeError) and "room" in str(exc).lower() and "is closed" in str(exc).lower()


agent = AsyncAgent(
    HANDLE,
    manifest=Manifest(
        description=f"Chat agent with {STYLE} personality",
        commands=[],
        tags=["llm", "chat", STYLE],
        version="1.0.0",
        capabilities=["chat.converse"],
        domains=["general"],
        input_types=["application/json"],
        output_types=["application/json"],
    ),
    socket=os.environ.get("TAILBUS_SOCKET", "/tmp/tailbusd.sock"),
)


def extract_mentions(text: str) -> list[str]:
    return re.findall(r"@(\w[\w-]*)", text)


def should_respond(payload: dict[str, Any]) -> bool:
    author = payload.get("author", "")
    if author == HANDLE:
        return False
    # Don't respond to other agents (prevent loops).
    if payload.get("is_agent"):
        return False
    mentions = extract_mentions(str(payload.get("text", "")))
    if mentions and HANDLE not in mentions:
        return False
    return True


def build_conversation(events: list[RoomEvent], limit: int = 20) -> list[dict[str, str]]:
    """Build an LLM messages list from room history."""
    messages: list[dict[str, str]] = []
    for event in events:
        if event.event_type != "message_posted" or event.content_type != "application/json":
            continue
        try:
            payload = json.loads(event.payload)
        except json.JSONDecodeError:
            continue
        if payload.get("kind") != "chat":
            continue
        author = payload.get("author", "unknown")
        text = str(payload.get("text", ""))
        if not text:
            continue
        if author == HANDLE:
            messages.append({"role": "assistant", "content": text})
        else:
            messages.append({"role": "user", "content": f"[{author}] {text}"})
    return messages[-limit:]


def llm_call(system: str, conversation: list[dict[str, str]]) -> str:
    body: dict[str, Any] = {
        "messages": [{"role": "system", "content": system}] + conversation,
        "temperature": 0.7,
        "max_tokens": 1024,
        "stream": True,
    }
    if LLM_MODEL:
        body["model"] = LLM_MODEL
    data = json.dumps(body).encode("utf-8")
    req = urllib.request.Request(
        f"{LLM_BASE_URL}/chat/completions",
        data=data,
        headers={"Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(req, timeout=LLM_REQUEST_TIMEOUT) as resp:
            return _collect_stream(resp)
    except urllib.error.URLError as exc:
        return f"[error: could not reach LM Studio — {exc.reason}]"
    except Exception as exc:
        return f"[error: {exc}]"


def _collect_stream(lines: Iterable[bytes]) -> str:
    chunks: list[str] = []
    for raw_line in lines:
        line = raw_line.decode("utf-8", errors="ignore").strip()
        if not line or not line.startswith("data:"):
            continue
        payload = line[5:].strip()
        if payload == "[DONE]":
            break
        try:
            event = json.loads(payload)
        except json.JSONDecodeError:
            continue
        choices = event.get("choices", [])
        if not choices:
            continue
        delta = choices[0].get("delta", {})
        text = str(delta.get("content", "") or "") if isinstance(delta, dict) else ""
        if text:
            chunks.append(text)
    result = "".join(chunks).strip()
    if "<think>" in result:
        parts = result.split("</think>")
        result = parts[-1].strip() if len(parts) > 1 else result
    return result


@agent.on_message
async def handle(msg: RoomEvent) -> None:
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
    if not should_respond(payload):
        return

    author = payload.get("author", "someone")
    text = str(payload.get("text", ""))
    say(f"replying to {BOLD}{author}{RESET}: {text[:80]}")

    # Replay room for conversation context.
    try:
        events = await agent.replay_room(msg.room_id)
    except Exception:
        events = []

    system = STYLE_PROMPTS.get(STYLE, STYLE_PROMPTS["analytical"])
    system += (
        "\n\nYou are in a chat room. Other participants may include humans and other AI agents. "
        "Messages from others are prefixed with [their_name]. Respond naturally to the conversation. "
        "If someone @mentions you specifically, make sure to address their question directly."
    )
    conversation = build_conversation(events)

    started = time.monotonic()
    try:
        response = await asyncio.wait_for(
            asyncio.shield(asyncio.to_thread(llm_call, system, conversation)),
            timeout=REPLY_TIMEOUT,
        )
    except asyncio.TimeoutError:
        response = "[timed out generating response]"

    if not response:
        return

    elapsed = round(time.monotonic() - started, 1)
    say(f"{GREEN}replied{RESET} in {elapsed:.1f}s")

    reply = {
        "kind": "chat",
        "author": HANDLE,
        "text": response,
        "is_agent": True,
        "elapsed_sec": elapsed,
    }
    try:
        await agent.post_room_message(
            msg.room_id,
            json.dumps(reply),
            content_type="application/json",
        )
    except Exception as exc:
        if not is_room_closed_error(exc):
            raise


async def main() -> None:
    say("connecting...")
    async with agent:
        await agent.register()
        say(f"{GREEN}ready{RESET} — {STYLE} chat agent")
        await agent.run_forever()


if __name__ == "__main__":
    asyncio.run(main())
