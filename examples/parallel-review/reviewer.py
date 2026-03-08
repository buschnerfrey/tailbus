#!/usr/bin/env python3
"""Generic reviewer agent for the parallel-review example.

Configure via environment variables:
  REVIEWER_ROLE     = security | performance | style
  REVIEWER_HANDLE   = e.g. security-reviewer
  REVIEWER_CAP      = e.g. review.security
  TAILBUS_SOCKET    = daemon socket path
"""

from __future__ import annotations

import asyncio
import collections
import json
import os
import sys
import time

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "../../sdk/python/src"))

from tailbus import AsyncAgent, Manifest, RoomEvent

from review_common import (
    BOLD,
    GREEN,
    LLM_BASE_URL,
    REVIEW_TIMEOUT,
    TURN_PROGRESS_INTERVAL,
    RESET,
    YELLOW,
    is_room_closed_error,
    llm_stream_call,
    parse_json,
    parse_json_object,
    progress_pinger,
    say,
)

ROLE = os.environ.get("REVIEWER_ROLE", "security")
HANDLE = os.environ.get("REVIEWER_HANDLE", f"{ROLE}-reviewer")
CAPABILITY = os.environ.get("REVIEWER_CAP", f"review.{ROLE}")

SYSTEM_PROMPTS: dict[str, str] = {
    "security": """\
You are a security-focused code reviewer. Analyze the code for:
- SQL injection, XSS, command injection
- Authentication and authorization flaws
- Secret/credential exposure
- Input validation gaps
- Unsafe deserialization

Return exactly one JSON object:
{
  "summary": "one-line security assessment",
  "severity": "critical" or "high" or "medium" or "low" or "none",
  "findings": [{"issue": "...", "line_hint": "...", "severity": "...", "fix": "..."}],
  "recommendation": "overall recommendation"
}
Return JSON only.""",
    "performance": """\
You are a performance-focused code reviewer. Analyze the code for:
- Algorithmic complexity issues (O(n^2) or worse)
- Unnecessary memory allocations
- Missing caching opportunities
- Database query anti-patterns (N+1, missing indices)
- Blocking I/O in hot paths

Return exactly one JSON object:
{
  "summary": "one-line performance assessment",
  "severity": "critical" or "high" or "medium" or "low" or "none",
  "findings": [{"issue": "...", "line_hint": "...", "impact": "...", "fix": "..."}],
  "recommendation": "overall recommendation"
}
Return JSON only.""",
    "style": """\
You are a code style and maintainability reviewer. Analyze the code for:
- Naming conventions (clear, descriptive names)
- Function length and single-responsibility
- Code duplication
- Missing type hints
- Error handling patterns
- Documentation gaps

Return exactly one JSON object:
{
  "summary": "one-line style assessment",
  "severity": "critical" or "high" or "medium" or "low" or "none",
  "findings": [{"issue": "...", "line_hint": "...", "suggestion": "..."}],
  "recommendation": "overall recommendation"
}
Return JSON only.""",
}

agent = AsyncAgent(
    HANDLE,
    manifest=Manifest(
        description=f"Reviews code for {ROLE} concerns using LM Studio",
        commands=[],
        tags=["llm", "lmstudio", "review", ROLE],
        version="1.0.0",
        capabilities=[CAPABILITY],
        domains=["engineering"],
        input_types=["application/json"],
        output_types=["application/json"],
    ),
    socket=os.environ.get("TAILBUS_SOCKET", "/tmp/tailbusd.sock"),
)

seen_turns: set[str] = set()
seen_turns_order: collections.deque[str] = collections.deque()
MAX_SEEN_TURNS = 500


async def do_review(room_id: str, payload: dict[str, object]) -> dict[str, object]:
    code = str(payload.get("code", ""))
    title = str(payload.get("title", "code"))
    system_prompt = SYSTEM_PROMPTS.get(ROLE, SYSTEM_PROMPTS["security"])
    user_prompt = f"Review this code ({title}):\n\n```\n{code}\n```"

    progress_state: dict[str, str] = {"summary": f"{ROLE} reviewer analyzing..."}

    def on_chunk(text: str) -> None:
        cleaned = " ".join(text.strip().split())
        if cleaned:
            progress_state["summary"] = f"{ROLE}: {cleaned[-120:]}"

    started = time.monotonic()
    try:
        raw = (
            await asyncio.wait_for(
                asyncio.shield(
                    asyncio.to_thread(llm_stream_call, system_prompt, user_prompt, on_chunk=on_chunk)
                ),
                timeout=REVIEW_TIMEOUT,
            )
        ).strip()
    except asyncio.TimeoutError:
        elapsed = round(time.monotonic() - started, 1)
        return {
            "kind": "review_reply",
            "turn_id": payload.get("turn_id", ""),
            "author": agent.handle,
            "status": "error",
            "capability": CAPABILITY,
            "role": ROLE,
            "error": f"Review timed out after {REVIEW_TIMEOUT:.0f}s",
            "elapsed_sec": elapsed,
        }

    elapsed = round(time.monotonic() - started, 1)
    if raw.startswith("[LLM error]"):
        return {
            "kind": "review_reply",
            "turn_id": payload.get("turn_id", ""),
            "author": agent.handle,
            "status": "error",
            "capability": CAPABILITY,
            "role": ROLE,
            "error": raw,
            "elapsed_sec": elapsed,
        }

    result = parse_json_object(raw)
    if not result:
        return {
            "kind": "review_reply",
            "turn_id": payload.get("turn_id", ""),
            "author": agent.handle,
            "status": "error",
            "capability": CAPABILITY,
            "role": ROLE,
            "error": "LLM did not return valid JSON",
            "raw_output": raw[:2000],
            "elapsed_sec": elapsed,
        }

    return {
        "kind": "review_reply",
        "turn_id": payload.get("turn_id", ""),
        "author": agent.handle,
        "status": "ok",
        "capability": CAPABILITY,
        "role": ROLE,
        "review": result,
        "elapsed_sec": elapsed,
    }


@agent.on_message
async def handle(msg: RoomEvent) -> None:
    if not isinstance(msg, RoomEvent):
        return
    if msg.event_type != "message_posted" or msg.content_type != "application/json":
        return
    payload = parse_json(msg.payload)
    if not payload or payload.get("kind") != "review_request":
        return
    if payload.get("target_handle") not in ("", agent.handle):
        return
    if payload.get("target_capability") not in ("", CAPABILITY):
        return
    turn_id = str(payload.get("turn_id", ""))
    if not turn_id or turn_id in seen_turns:
        return
    seen_turns.add(turn_id)
    seen_turns_order.append(turn_id)
    while len(seen_turns) > MAX_SEEN_TURNS:
        seen_turns.discard(seen_turns_order.popleft())

    say(agent.handle, f"reviewing ({ROLE}) via {BOLD}{LLM_BASE_URL}{RESET}")
    progress_task = asyncio.create_task(
        progress_pinger(
            agent,
            room_id=msg.room_id,
            turn_id=turn_id,
            round_no=int(payload.get("round", 0)),
            target_handle=agent.handle,
            target_capability=CAPABILITY,
            summary=f"{ROLE} reviewer analyzing...",
            interval=TURN_PROGRESS_INTERVAL,
        )
    )
    try:
        reply = await do_review(msg.room_id, payload)
    except Exception as exc:
        reply = {
            "kind": "review_reply",
            "turn_id": turn_id,
            "author": agent.handle,
            "status": "error",
            "capability": CAPABILITY,
            "role": ROLE,
            "error": str(exc),
            "elapsed_sec": 0.0,
        }
    finally:
        progress_task.cancel()
        try:
            await progress_task
        except (asyncio.CancelledError, Exception):
            pass

    if reply["status"] == "ok":
        say(agent.handle, f"{GREEN}posted{RESET} {ROLE} review in {reply['elapsed_sec']:.1f}s")
    else:
        say(agent.handle, f"{YELLOW}error{RESET}: {reply.get('error', 'unknown')}")
    try:
        await agent.post_room_message(
            msg.room_id,
            json.dumps(reply),
            content_type="application/json",
            trace_id=turn_id,
        )
    except Exception as exc:
        if is_room_closed_error(exc):
            say(agent.handle, f"{YELLOW}room closed{RESET}")
        else:
            raise


async def main() -> None:
    say(agent.handle, "connecting...")
    async with agent:
        await agent.register()
        say(agent.handle, f"{GREEN}ready{RESET} — capability {CAPABILITY}")
        await agent.run_forever()


if __name__ == "__main__":
    asyncio.run(main())
