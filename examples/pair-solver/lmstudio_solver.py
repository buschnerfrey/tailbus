#!/usr/bin/env python3
"""LM Studio-backed room solver for the pair-solver demo."""

from __future__ import annotations

import asyncio
import json
import os
import sys
import time
import urllib.error
import urllib.request
from typing import Any

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "../../sdk/python/src"))

from tailbus import AsyncAgent, Manifest, RoomEvent

LLM_BASE_URL = os.environ.get("LLM_BASE_URL", "http://localhost:1234/v1")
LLM_MODEL = os.environ.get("LLM_MODEL", "")
MAX_TRANSCRIPT_CHARS = int(os.environ.get("PAIR_SOLVER_TRANSCRIPT_CHARS", "16000"))
ROOM_REPLAY_RETRIES = int(os.environ.get("PAIR_SOLVER_ROOM_REPLAY_RETRIES", "12"))
ROOM_REPLAY_DELAY = float(os.environ.get("PAIR_SOLVER_ROOM_REPLAY_DELAY", "0.5"))

DIM = "\033[2m"
BOLD = "\033[1m"
GREEN = "\033[32m"
RED = "\033[31m"
YELLOW = "\033[33m"
RESET = "\033[0m"
TAG = f"  {DIM}lmstudio-solver{RESET}"

SYSTEM_PROMPT = """You are LM Studio in a Tailbus shared room.

You are collaborating with another solver, not solving in isolation.
Read the transcript carefully, respond only to the current turn instruction, and improve on prior work when possible.
Return only the answer text that should be posted back into the room."""

agent = AsyncAgent(
    "lmstudio-solver",
    manifest=Manifest(
        description="Participates in a shared Tailbus room and critiques/finalizes solutions using LM Studio",
        commands=[],
        tags=["rooms", "solver", "lmstudio"],
        version="2.0.0",
    ),
    socket=os.environ.get("TAILBUS_SOCKET", "/tmp/tailbusd.sock"),
)

seen_turns: set[str] = set()


def say(msg: str) -> None:
    print(f"{TAG}  {msg}", flush=True)


def parse_json(payload: str) -> dict[str, Any] | None:
    try:
        value = json.loads(payload)
    except json.JSONDecodeError:
        return None
    return value if isinstance(value, dict) else None


def render_transcript(events: list[RoomEvent]) -> str:
    parts: list[str] = []
    for event in events:
        if event.event_type != "message_posted":
            continue
        payload = parse_json(event.payload)
        if not payload:
            continue
        kind = payload.get("kind")
        if kind == "problem_opened":
            parts.append(f"[problem] {payload.get('problem', '')}")
        elif kind == "turn_request":
            parts.append(
                f"[turn_request] round={payload.get('round')} target={payload.get('target')} "
                f"type={payload.get('response_type')} instruction={payload.get('instruction', '')}"
            )
        elif kind == "solver_reply":
            author = payload.get("author", event.sender_handle)
            parts.append(
                f"[solver_reply] author={author} round={payload.get('round')} "
                f"type={payload.get('response_type')} status={payload.get('status', 'unknown')}\n"
                f"{payload.get('content', '')}"
            )
        elif kind == "turn_timeout":
            parts.append(f"[turn_timeout] target={payload.get('target')} round={payload.get('round')}")
        elif kind == "final_summary":
            parts.append(f"[final_summary]\n{payload.get('final_answer', '')}")
    text = "\n\n".join(parts)
    if len(text) > MAX_TRANSCRIPT_CHARS:
        return text[-MAX_TRANSCRIPT_CHARS:]
    return text


def llm_call(system: str, user: str, temperature: float = 0.3, max_tokens: int = 2048) -> str:
    body: dict[str, Any] = {
        "messages": [
            {"role": "system", "content": system},
            {"role": "user", "content": user},
        ],
        "temperature": temperature,
        "max_tokens": max_tokens,
    }
    if LLM_MODEL:
        body["model"] = LLM_MODEL
    request = urllib.request.Request(
        f"{LLM_BASE_URL}/chat/completions",
        data=json.dumps(body).encode("utf-8"),
        headers={"Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(request, timeout=180) as response:
            result = json.loads(response.read())
            content = result["choices"][0]["message"]["content"]
            if "<think>" in content:
                parts = content.split("</think>")
                content = parts[-1].strip() if len(parts) > 1 else content
            return content.strip()
    except urllib.error.URLError as exc:
        return f"[LLM error] Could not reach LM Studio at {LLM_BASE_URL}: {exc.reason}"
    except Exception as exc:
        return f"[LLM error] {exc}"


async def replay_room_with_retry(room_id: str) -> list[RoomEvent]:
    last_error: Exception | None = None
    for attempt in range(ROOM_REPLAY_RETRIES):
        try:
            return await agent.replay_room(room_id)
        except Exception as exc:
            last_error = exc
            if attempt == ROOM_REPLAY_RETRIES - 1:
                break
            await asyncio.sleep(ROOM_REPLAY_DELAY)
    assert last_error is not None
    raise last_error


async def solve_turn(room_id: str, payload: dict[str, Any]) -> dict[str, Any]:
    events = await replay_room_with_retry(room_id)
    transcript = render_transcript(events)
    user_prompt = (
        f"Problem:\n{payload.get('problem', '')}\n\n"
        f"Transcript:\n{transcript}\n\n"
        f"Instruction:\n{payload.get('instruction', '')}\n"
    )
    started = time.monotonic()
    loop = asyncio.get_running_loop()
    result = (await loop.run_in_executor(None, llm_call, SYSTEM_PROMPT, user_prompt)).strip()
    elapsed = round(time.monotonic() - started, 1)
    if result.startswith("[LLM error]"):
        return {
            "kind": "solver_reply",
            "turn_id": payload["turn_id"],
            "author": agent.handle,
            "round": payload.get("round"),
            "response_type": payload.get("response_type"),
            "status": "error",
            "error": result,
            "content": "",
            "elapsed_sec": elapsed,
        }
    if result.startswith("```"):
        result = result.split("\n", 1)[1].rsplit("```", 1)[0].strip()
    return {
        "kind": "solver_reply",
        "turn_id": payload["turn_id"],
        "author": agent.handle,
        "round": payload.get("round"),
        "response_type": payload.get("response_type"),
        "status": "ok",
        "content": result,
        "elapsed_sec": elapsed,
    }


@agent.on_message
async def handle(msg: RoomEvent) -> None:
    if not isinstance(msg, RoomEvent):
        return
    if msg.event_type != "message_posted" or msg.content_type != "application/json":
        return
    payload = parse_json(msg.payload)
    if not payload or payload.get("kind") != "turn_request":
        return
    if payload.get("target") != agent.handle:
        return
    turn_id = str(payload.get("turn_id", ""))
    if not turn_id or turn_id in seen_turns:
        return
    seen_turns.add(turn_id)
    say(f"round {payload.get('round', '?')} {BOLD}{payload.get('response_type', 'turn')}{RESET}...")
    try:
        reply = await solve_turn(msg.room_id, payload)
    except Exception as exc:
        reply = {
            "kind": "solver_reply",
            "turn_id": turn_id,
            "author": agent.handle,
            "round": payload.get("round"),
            "response_type": payload.get("response_type"),
            "status": "error",
            "error": str(exc),
            "content": "",
            "elapsed_sec": 0.0,
        }
    if reply["status"] == "ok":
        say(f"{GREEN}✓{RESET} posted reply in {reply['elapsed_sec']:.1f}s")
    else:
        say(f"{YELLOW}!{RESET} {reply.get('error', 'solver error')}")
    await agent.post_room_message(
        msg.room_id,
        json.dumps(reply),
        content_type="application/json",
        trace_id=turn_id,
    )


async def main() -> None:
    say("connecting...")
    async with agent:
        await agent.register()
        say(f"{GREEN}ready{RESET} — listening for room turns")
        await agent.run_forever()


if __name__ == "__main__":
    asyncio.run(main())
