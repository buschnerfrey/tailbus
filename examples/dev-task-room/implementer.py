#!/usr/bin/env python3
"""Codex-backed implementer for the dev-task-room example."""

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
    CODEX_MODEL,
    CODEX_TIMEOUT,
    GREEN,
    TURN_PROGRESS_INTERVAL,
    RED,
    RESET,
    YELLOW,
    is_room_closed_error,
    parse_json,
    progress_pinger,
    replay_room_with_retry,
    room_task_from_events,
    run_codex_json,
    say,
    tmp_json_path,
    truncate_preserving_ends,
)

SYSTEM_PROMPT = """You are the implementation specialist in a Tailbus engineering room.

Return exactly one JSON object with this shape:
{
  "summary": "short summary of the implementation",
  "assumptions": ["..."],
  "files": [{"path": "relative/path.py", "content": "full file contents"}],
  "deleted_paths": ["optional/path.py"],
  "test_notes": ["short notes about test impact"]
}

Rules:
- Return JSON only. No markdown fences.
- Use whole-file contents.
- Paths must be relative.
- Stay within the provided workspace.
- If revising, address every required change explicitly.
"""

agent = AsyncAgent(
    "implementer",
    manifest=Manifest(
        description="Implements development tasks by returning whole-file change sets via Codex",
        commands=[],
        tags=["llm", "codex", "development", "implementation"],
        version="1.0.0",
        capabilities=["dev.implement"],
        domains=["engineering"],
        input_types=["application/json"],
        output_types=["application/json"],
    ),
    socket=os.environ.get("TAILBUS_SOCKET", "/tmp/tailbusd.sock"),
)

seen_turns: set[str] = set()
MAX_SEEN_TURNS = 500


def build_prompt(payload: dict[str, object], task: dict[str, object], transcript: str) -> str:
    review_findings = payload.get("review_findings", [])
    required_changes = payload.get("required_changes", [])
    previous_change_set = payload.get("previous_change_set", {})
    parts = [
        "Task:",
        str(payload.get("task", task.get("task", ""))),
        "",
        "Workspace snapshot:",
        str(payload.get("workspace_snapshot", "")),
    ]
    if review_findings:
        parts.extend(["", "Reviewer findings:", json.dumps(review_findings, indent=2)])
    if required_changes:
        parts.extend(["", "Required changes:", json.dumps(required_changes, indent=2)])
    if previous_change_set:
        parts.extend(["", "Previous change set:", json.dumps(previous_change_set, indent=2)])
    if transcript:
        parts.extend(["", "Room transcript:", transcript])
    return "\n".join(parts)


async def implement_turn(room_id: str, payload: dict[str, object]) -> dict[str, object]:
    events = await replay_room_with_retry(agent, room_id)
    task = room_task_from_events(events)
    transcript = "\n".join(
        event.payload for event in events if event.event_type == "message_posted" and event.content_type == "application/json"
    )
    transcript = truncate_preserving_ends(transcript, 12000)
    prompt = build_prompt(payload, task, transcript)
    output_path = tmp_json_path("dev_task_implement", str(payload.get("turn_id", "")))
    started = time.monotonic()
    raw = (await run_codex_json(SYSTEM_PROMPT + "\n\n" + prompt, output_path, CODEX_TIMEOUT)).strip()
    elapsed = round(time.monotonic() - started, 1)
    if raw.startswith("[codex error]"):
        return {
            "kind": "implement_reply",
            "turn_id": payload.get("turn_id", ""),
            "author": agent.handle,
            "status": "error",
            "capability": "dev.implement",
            "summary": "",
            "error": raw,
            "elapsed_sec": elapsed,
        }
    from dev_task_common import parse_json_object

    change_set = parse_json_object(raw)
    if not change_set:
        return {
            "kind": "implement_reply",
            "turn_id": payload.get("turn_id", ""),
            "author": agent.handle,
            "status": "error",
            "capability": "dev.implement",
            "summary": "",
            "error": "Codex did not return valid JSON",
            "raw_output": raw[:2000],
            "elapsed_sec": elapsed,
        }
    return {
        "kind": "implement_reply",
        "turn_id": payload.get("turn_id", ""),
        "author": agent.handle,
        "status": "ok",
        "capability": "dev.implement",
        "summary": str(change_set.get("summary", "Prepared change set.")),
        "change_set": change_set,
        "elapsed_sec": elapsed,
    }


@agent.on_message
async def handle(msg: RoomEvent) -> None:
    if not isinstance(msg, RoomEvent):
        return
    if msg.event_type != "message_posted" or msg.content_type != "application/json":
        return
    payload = parse_json(msg.payload)
    if not payload or payload.get("kind") != "implement_request":
        return
    if payload.get("target_handle") not in ("", agent.handle):
        return
    if payload.get("target_capability") not in ("", "dev.implement"):
        return
    turn_id = str(payload.get("turn_id", ""))
    if not turn_id or turn_id in seen_turns:
        return
    seen_turns.add(turn_id)
    if len(seen_turns) > MAX_SEEN_TURNS:
        seen_turns.clear()
        seen_turns.add(turn_id)
    model_label = CODEX_MODEL or "codex default model"
    say(agent.handle, f"implementing via {BOLD}{model_label}{RESET}")
    progress_task = asyncio.create_task(
        progress_pinger(
            agent,
            room_id=msg.room_id,
            turn_id=turn_id,
            round_no=int(payload.get("round", 0)),
            target_handle=agent.handle,
            target_capability="dev.implement",
            summary="Implementer is still preparing the change set.",
            interval=TURN_PROGRESS_INTERVAL,
        )
    )
    try:
        reply = await implement_turn(msg.room_id, payload)
    except Exception as exc:
        reply = {
            "kind": "implement_reply",
            "turn_id": turn_id,
            "author": agent.handle,
            "status": "error",
            "capability": "dev.implement",
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
        say(agent.handle, f"{GREEN}posted{RESET} implementation in {reply['elapsed_sec']:.1f}s")
    else:
        say(agent.handle, f"{RED}error{RESET}: {reply.get('error', 'unknown error')}")
    try:
        await agent.post_room_message(
            msg.room_id,
            json.dumps(reply),
            content_type="application/json",
            trace_id=turn_id,
        )
    except Exception as exc:
        if is_room_closed_error(exc):
            say(agent.handle, f"{YELLOW}room closed{RESET} before implementation reply could be posted")
        else:
            raise


async def main() -> None:
    say(agent.handle, "connecting...")
    async with agent:
        await agent.register()
        say(agent.handle, f"{GREEN}ready{RESET} — capability dev.implement")
        await agent.run_forever()


if __name__ == "__main__":
    asyncio.run(main())
