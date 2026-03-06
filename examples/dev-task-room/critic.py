#!/usr/bin/env python3
"""LM Studio-backed critic for the dev-task-room example."""

from __future__ import annotations

import asyncio
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
    TURN_PROGRESS_INTERVAL,
    RESET,
    YELLOW,
    llm_stream_call,
    parse_json,
    parse_json_object,
    progress_pinger,
    replay_room_with_retry,
    room_task_from_events,
    say,
)

SYSTEM_PROMPT = """You are the critical reviewer in a Tailbus engineering room.

Review the proposed change set against the task and workspace.

Return exactly one JSON object:
{
  "summary": "short review summary",
  "decision": "approve" or "revise",
  "findings": ["bug or risk"],
  "required_changes": ["concrete requested fix"],
  "test_gaps": ["missing test coverage"]
}

Rules:
- Be strict about correctness and regression risk.
- Approve only if the change set is good enough to apply in the local workspace.
- Return JSON only.
"""

agent = AsyncAgent(
    "critic",
    manifest=Manifest(
        description="Reviews development change sets for bugs, regressions, and missing tests using LM Studio",
        commands=[],
        tags=["llm", "lmstudio", "development", "review"],
        version="1.0.0",
        capabilities=["dev.review"],
        domains=["engineering"],
        input_types=["application/json"],
        output_types=["application/json"],
    ),
    socket=os.environ.get("TAILBUS_SOCKET", "/tmp/tailbusd.sock"),
)

seen_turns: set[str] = set()


async def review_turn(room_id: str, payload: dict[str, object]) -> dict[str, object]:
    progress_state = payload.get("_progress_state")
    events = await replay_room_with_retry(agent, room_id)
    task = room_task_from_events(events)
    user_prompt = (
        f"Task:\n{payload.get('task', task.get('task', ''))}\n\n"
        f"Workspace snapshot:\n{payload.get('workspace_snapshot', '')}\n\n"
        f"Proposed change set:\n{json.dumps(payload.get('change_set', {}), indent=2)}\n"
    )
    started = time.monotonic()
    def on_chunk(text: str) -> None:
        if isinstance(progress_state, dict):
            cleaned = " ".join(text.strip().split())
            if cleaned:
                progress_state["summary"] = f"Critic streaming: {cleaned[-120:]}"

    raw = (await asyncio.to_thread(llm_stream_call, SYSTEM_PROMPT, user_prompt, on_chunk=on_chunk)).strip()
    elapsed = round(time.monotonic() - started, 1)
    if raw.startswith("[LLM error]"):
        return {
            "kind": "review_reply",
            "turn_id": payload.get("turn_id", ""),
            "author": agent.handle,
            "status": "error",
            "capability": "dev.review",
            "summary": "",
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
            "capability": "dev.review",
            "summary": "",
            "error": "LM Studio did not return valid JSON",
            "raw_output": raw[:2000],
            "elapsed_sec": elapsed,
        }
    return {
        "kind": "review_reply",
        "turn_id": payload.get("turn_id", ""),
        "author": agent.handle,
        "status": "ok",
        "capability": "dev.review",
        "summary": str(result.get("summary", "Review complete.")),
        "decision": str(result.get("decision", "revise")),
        "findings": list(result.get("findings", [])),
        "required_changes": list(result.get("required_changes", [])),
        "test_gaps": list(result.get("test_gaps", [])),
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
    if payload.get("target_capability") not in ("", "dev.review"):
        return
    turn_id = str(payload.get("turn_id", ""))
    if not turn_id or turn_id in seen_turns:
        return
    seen_turns.add(turn_id)
    say(agent.handle, f"reviewing via {BOLD}{LLM_BASE_URL}{RESET}")
    progress_state = {"summary": "Critic started streaming review output."}
    progress_task = asyncio.create_task(
        progress_pinger(
            agent,
            room_id=msg.room_id,
            turn_id=turn_id,
            round_no=int(payload.get("round", 0)),
            target_handle=agent.handle,
            target_capability="dev.review",
            summary=lambda: str(progress_state["summary"]),
            interval=TURN_PROGRESS_INTERVAL,
        )
    )
    try:
        payload["_progress_state"] = progress_state
        reply = await review_turn(msg.room_id, payload)
    except Exception as exc:
        reply = {
            "kind": "review_reply",
            "turn_id": turn_id,
            "author": agent.handle,
            "status": "error",
            "capability": "dev.review",
            "summary": "",
            "error": str(exc),
            "elapsed_sec": 0.0,
        }
    finally:
        progress_task.cancel()
        try:
            await progress_task
        except asyncio.CancelledError:
            pass
    if reply["status"] == "ok":
        say(agent.handle, f"{GREEN}posted{RESET} review in {reply['elapsed_sec']:.1f}s")
    else:
        say(agent.handle, f"{YELLOW}error{RESET}: {reply.get('error', 'unknown error')}")
    await agent.post_room_message(
        msg.room_id,
        json.dumps(reply),
        content_type="application/json",
        trace_id=turn_id,
    )


async def main() -> None:
    say(agent.handle, "connecting...")
    async with agent:
        await agent.register()
        say(agent.handle, f"{GREEN}ready{RESET} — capability dev.review")
        await agent.run_forever()


if __name__ == "__main__":
    asyncio.run(main())
