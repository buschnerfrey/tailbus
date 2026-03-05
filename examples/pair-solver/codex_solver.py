#!/usr/bin/env python3
"""Codex-backed room solver for the pair-solver demo."""

from __future__ import annotations

import asyncio
import json
import os
import sys
import tempfile
import time
from pathlib import Path
from typing import Any

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "../../sdk/python/src"))

from tailbus import AsyncAgent, Manifest, RoomEvent

CODEX_TIMEOUT = int(os.environ.get("CODEX_TIMEOUT", "180"))
MAX_TRANSCRIPT_CHARS = int(os.environ.get("PAIR_SOLVER_TRANSCRIPT_CHARS", "16000"))
ROOM_REPLAY_RETRIES = int(os.environ.get("PAIR_SOLVER_ROOM_REPLAY_RETRIES", "12"))
ROOM_REPLAY_DELAY = float(os.environ.get("PAIR_SOLVER_ROOM_REPLAY_DELAY", "0.5"))

DIM = "\033[2m"
BOLD = "\033[1m"
GREEN = "\033[32m"
RED = "\033[31m"
YELLOW = "\033[33m"
RESET = "\033[0m"
TAG = f"  {DIM}codex-solver{RESET}"

agent = AsyncAgent(
    "codex-solver",
    manifest=Manifest(
        description="Participates in a shared Tailbus room and proposes/refines solutions using Codex CLI",
        commands=[],
        tags=["rooms", "solver", "codex"],
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
            status = payload.get("status", "unknown")
            parts.append(
                f"[solver_reply] author={author} round={payload.get('round')} "
                f"type={payload.get('response_type')} status={status}\n{payload.get('content', '')}"
            )
        elif kind == "turn_timeout":
            parts.append(f"[turn_timeout] target={payload.get('target')} round={payload.get('round')}")
        elif kind == "final_summary":
            parts.append(f"[final_summary]\n{payload.get('final_answer', '')}")
    text = "\n\n".join(parts)
    if len(text) > MAX_TRANSCRIPT_CHARS:
        return text[-MAX_TRANSCRIPT_CHARS:]
    return text


async def run_codex(prompt: str, output_file: str, timeout: int) -> str:
    try:
        proc = await asyncio.create_subprocess_exec(
            "codex",
            "exec",
            prompt,
            "-o",
            output_file,
            "--ephemeral",
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )
        stdout, stderr = await asyncio.wait_for(proc.communicate(), timeout=timeout)
        if proc.returncode != 0:
            err = stderr.decode().strip() if stderr else "unknown error"
            if os.path.exists(output_file):
                with open(output_file, "r", encoding="utf-8") as handle:
                    content = handle.read().strip()
                if content:
                    return content
            return f"[codex error] exit code {proc.returncode}: {err}"
        if os.path.exists(output_file):
            with open(output_file, "r", encoding="utf-8") as handle:
                return handle.read()
        return stdout.decode().strip() or "[codex error] no output produced"
    except asyncio.TimeoutError:
        proc.kill()
        return f"[codex error] timed out after {timeout}s"
    except FileNotFoundError:
        return "[codex error] 'codex' CLI not found"
    except Exception as exc:
        return f"[codex error] {exc}"
    finally:
        if os.path.exists(output_file):
            try:
                os.unlink(output_file)
            except OSError:
                pass


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
    prompt = (
        "You are Codex in a Tailbus shared room.\n\n"
        "Work only on the current turn.\n"
        "Read the transcript, incorporate prior solver feedback, and answer the instruction directly.\n"
        "Return only the response text that should be posted back into the room. No markdown fences.\n\n"
        f"Problem:\n{payload.get('problem', '')}\n\n"
        f"Transcript:\n{transcript}\n\n"
        f"Instruction:\n{payload.get('instruction', '')}\n"
    )
    output_file = str(Path(tempfile.gettempdir()) / f"pair_solver_codex_{payload['turn_id'][:8]}.txt")
    started = time.monotonic()
    result = (await run_codex(prompt, output_file, CODEX_TIMEOUT)).strip()
    elapsed = round(time.monotonic() - started, 1)
    if result.startswith("[codex error]"):
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
