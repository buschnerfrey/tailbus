#!/usr/bin/env python3
"""LM-backed test planning specialist for the dev-task-room example."""

from __future__ import annotations

import asyncio
import collections
import json
import os
import sys
import time

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "../../sdk/python/src"))

from tailbus import AsyncAgent, Manifest, RoomEvent

from dev_task_common import (
    BOLD,
    GREEN,
    LLM_BASE_URL,
    load_change_set,
    RESET,
    REVIEW_TIMEOUT,
    TURN_PROGRESS_INTERVAL,
    YELLOW,
    is_room_closed_error,
    llm_call,
    parse_json,
    parse_json_object,
    progress_pinger,
    say,
)

SYSTEM_PROMPT = """You are the test strategist in a Tailbus engineering room.

Return exactly one JSON object:
{
  "summary": "short test strategy summary",
  "decision": "approve" or "revise",
  "required_tests": ["concrete test that must exist or change"],
  "risks": ["important behavior or regression risk"]
}

Rules:
- Focus on whether the proposed change set has enough validation.
- Prefer concrete test additions over vague suggestions.
- Use decision=revise if the change set is missing important coverage.
- Return JSON only.
"""

agent = AsyncAgent(
    "test-strategist",
    manifest=Manifest(
        description="Plans targeted validation steps and flags missing coverage in the engineering room",
        commands=[],
        tags=["llm", "lmstudio", "development", "testing"],
        version="1.0.0",
        capabilities=["dev.test.plan"],
        domains=["engineering"],
        input_types=["application/json"],
        output_types=["application/json"],
    ),
    socket=os.environ.get("TAILBUS_SOCKET", "/tmp/tailbusd.sock"),
)

seen_turns: set[str] = set()
seen_turns_order: collections.deque[str] = collections.deque()
MAX_SEEN_TURNS = 500


def build_prompt(payload: dict[str, object]) -> str:
    change_set = load_change_set(payload)
    parts = [
        "Task:",
        str(payload.get("task", "")),
        "",
        f"Iteration: {payload.get('iteration', 0)}",
        f"Implement summary: {payload.get('implement_summary', '')}",
        f"Changed paths: {json.dumps(payload.get('change_paths', []))}",
        "",
        "Proposed change set:",
        json.dumps(change_set, indent=2),
    ]
    findings = payload.get("review_findings", [])
    if findings:
        parts.extend(["", "Existing review findings:", json.dumps(findings, indent=2)])
    return "\n".join(parts)


async def plan_tests(payload: dict[str, object]) -> dict[str, object]:
    progress_state = payload.get("_progress_state")
    if isinstance(progress_state, dict):
        progress_state["summary"] = "Test strategist preparing prompt."
    prompt = build_prompt(payload)
    started = time.monotonic()
    raw = (
        await asyncio.wait_for(
            asyncio.shield(asyncio.to_thread(llm_call, SYSTEM_PROMPT, prompt)),
            timeout=REVIEW_TIMEOUT,
        )
    ).strip()
    elapsed = round(time.monotonic() - started, 1)
    if raw.startswith("[LLM error]"):
        return {
            "kind": "test_plan_reply",
            "turn_id": payload.get("turn_id", ""),
            "task_id": payload.get("task_id", ""),
            "iteration": int(payload.get("iteration", 0) or 0),
            "author": agent.handle,
            "status": "error",
            "capability": "dev.test.plan",
            "summary": "",
            "error": raw,
            "elapsed_sec": elapsed,
        }
    result = parse_json_object(raw)
    if not result:
        return {
            "kind": "test_plan_reply",
            "turn_id": payload.get("turn_id", ""),
            "task_id": payload.get("task_id", ""),
            "iteration": int(payload.get("iteration", 0) or 0),
            "author": agent.handle,
            "status": "error",
            "capability": "dev.test.plan",
            "summary": "",
            "error": "LM Studio did not return valid JSON",
            "raw_output": raw[:2000],
            "elapsed_sec": elapsed,
        }
    return {
        "kind": "test_plan_reply",
        "turn_id": payload.get("turn_id", ""),
        "task_id": payload.get("task_id", ""),
        "iteration": int(payload.get("iteration", 0) or 0),
        "author": agent.handle,
        "status": "ok",
        "capability": "dev.test.plan",
        "summary": str(result.get("summary", "Test strategy complete.")),
        "decision": str(result.get("decision", "approve")),
        "required_tests": list(result.get("required_tests", [])),
        "risks": list(result.get("risks", [])),
        "elapsed_sec": elapsed,
    }


@agent.on_message
async def handle(msg: RoomEvent) -> None:
    if not isinstance(msg, RoomEvent):
        return
    if msg.event_type != "message_posted" or msg.content_type != "application/json":
        return
    payload = parse_json(msg.payload)
    if not payload or payload.get("kind") != "test_plan_request":
        return
    if payload.get("target_handle") not in ("", agent.handle):
        return
    if payload.get("target_capability") not in ("", "dev.test.plan"):
        return
    turn_id = str(payload.get("turn_id", ""))
    if not turn_id or turn_id in seen_turns:
        return
    seen_turns.add(turn_id)
    seen_turns_order.append(turn_id)
    while len(seen_turns) > MAX_SEEN_TURNS:
        seen_turns.discard(seen_turns_order.popleft())
    say(agent.handle, f"planning via {BOLD}{LLM_BASE_URL}{RESET}")
    progress_state = {"summary": "Test strategist waiting for LM Studio response."}
    progress_task = asyncio.create_task(
        progress_pinger(
            agent,
            room_id=msg.room_id,
            turn_id=turn_id,
            round_no=int(payload.get("iteration", 0)),
            target_handle=agent.handle,
            target_capability="dev.test.plan",
            summary=lambda: str(progress_state["summary"]),
            interval=TURN_PROGRESS_INTERVAL,
        )
    )
    try:
        payload["_progress_state"] = progress_state
        reply = await plan_tests(payload)
    except Exception as exc:
        reply = {
            "kind": "test_plan_reply",
            "turn_id": turn_id,
            "task_id": payload.get("task_id", ""),
            "iteration": int(payload.get("iteration", 0) or 0),
            "author": agent.handle,
            "status": "error",
            "capability": "dev.test.plan",
            "summary": "",
            "error": str(exc),
            "elapsed_sec": 0.0,
        }
    finally:
        progress_task.cancel()
        try:
            await progress_task
        except (asyncio.CancelledError, Exception):
            pass
    try:
        await agent.post_room_message(
            msg.room_id,
            json.dumps(reply),
            content_type="application/json",
            trace_id=turn_id,
        )
    except Exception as exc:
        if is_room_closed_error(exc):
            say(agent.handle, f"{YELLOW}room closed{RESET} before test plan reply could be posted")
        else:
            raise
    if reply["status"] == "ok":
        say(agent.handle, f"{GREEN}posted{RESET} test strategy in {reply['elapsed_sec']:.1f}s")
    else:
        say(agent.handle, f"{YELLOW}error{RESET}: {reply.get('error', 'unknown error')}")


async def main() -> None:
    say(agent.handle, "connecting...")
    async with agent:
        await agent.register()
        say(agent.handle, f"{GREEN}ready{RESET} — capability dev.test.plan")
        await agent.run_forever()


if __name__ == "__main__":
    asyncio.run(main())
