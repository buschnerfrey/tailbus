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
    RESET,
    REVIEW_TIMEOUT,
    TURN_PROGRESS_INTERVAL,
    YELLOW,
    build_review_units,
    build_room_state,
    is_context_limit_error,
    is_room_closed_error,
    llm_call,
    parse_json,
    parse_json_object,
    progress_pinger,
    replay_room_with_retry,
    review_for_iteration,
    split_review_unit,
    wait_for_room_message,
    say,
)

CHUNK_SYSTEM_PROMPT = """You are the critical reviewer in a Tailbus engineering room.

Review only the scoped chunk of the proposed change set against the task.

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
- Review only the provided scope. Do not speculate about files or ranges outside it.
- Approve only if the provided scope is good enough at the chunk level.
- Return JSON only.
"""

SYNTHESIS_SYSTEM_PROMPT = """You are the final reviewer in a Tailbus engineering room.

You will receive the task, changed-path manifest, and completed chunk-review outputs.

Return exactly one JSON object:
{
  "summary": "short review summary",
  "decision": "approve" or "revise",
  "findings": ["bug or risk"],
  "required_changes": ["concrete requested fix"],
  "test_gaps": ["missing test coverage"]
}

Rules:
- Focus on cross-file issues, missing integration concerns, and deduping chunk findings.
- Do not re-review raw code that is not present.
- Approve only if the overall change set is good enough to apply in the local workspace.
- Return JSON only.
"""

REPAIR_SYSTEM_PROMPT = """You repair malformed model output into exactly one valid JSON object.

Return exactly one JSON object with this schema:
{
  "summary": "short review summary",
  "decision": "approve" or "revise",
  "findings": ["bug or risk"],
  "required_changes": ["concrete requested fix"],
  "test_gaps": ["missing test coverage"]
}

Rules:
- Preserve the intent of the provided review output.
- If the text contains extra prose, strip it.
- If fields are missing, infer the safest minimal value.
- Return JSON only.
"""

REPAIR_TIMEOUT = 45.0

agent = AsyncAgent(
    "critic",
    manifest=Manifest(
        description="Reviews change sets in-room, delegates test strategy, and posts approval or revision requests",
        commands=[],
        tags=["llm", "lmstudio", "development", "review", "agentic"],
        version="2.0.0",
        capabilities=["dev.review"],
        domains=["engineering"],
        input_types=["application/json"],
        output_types=["application/json"],
    ),
    socket=os.environ.get("TAILBUS_SOCKET", "/tmp/tailbusd.sock"),
)

handled_events: set[str] = set()
handled_reviews: set[str] = set()
active_rooms: set[str] = set()


def build_chunk_prompt(payload: dict[str, object], task_text: str) -> str:
    review_chunk = dict(payload.get("review_chunk", {}))
    parts = [
        "Task:",
        task_text,
        "",
        f"Chunk: {payload.get('chunk_id', '')}",
        f"Scope paths: {json.dumps(payload.get('scope_paths', []))}",
        f"Scope ranges: {json.dumps(payload.get('scope_ranges', []))}",
        f"Changed paths: {json.dumps(payload.get('change_paths', []))}",
        "",
        "Scoped sections:",
        json.dumps(review_chunk.get("sections", []), indent=2),
    ]
    return "\n".join(parts)


def build_synthesis_prompt(payload: dict[str, object], task_text: str) -> str:
    chunk_reviews = [
        {
            "chunk_id": item.get("chunk_id", ""),
            "summary": item.get("summary", ""),
            "scope_paths": item.get("scope_paths", []),
            "scope_ranges": item.get("scope_ranges", []),
            "findings": item.get("findings", []),
            "required_changes": item.get("required_changes", []),
            "test_gaps": item.get("test_gaps", []),
        }
        for item in list(payload.get("chunk_reviews", []))
    ]
    parts = [
        "Task:",
        task_text,
        "",
        f"Implementer summary: {payload.get('implement_summary', '')}",
        f"Changed paths: {json.dumps(payload.get('change_paths', []))}",
        f"Chunk review count: {payload.get('chunk_total', 0)}",
        "",
        "Chunk reviews:",
        json.dumps(chunk_reviews, indent=2),
    ]
    return "\n".join(parts)


def build_repair_prompt(raw_output: str, review_mode: str) -> str:
    return "\n".join(
        [
            f"Review mode: {review_mode}",
            "",
            "Normalize this previous reviewer output into valid JSON:",
            raw_output[:8000],
        ]
    )


async def repair_review_json(raw_output: str, review_mode: str) -> dict[str, object] | None:
    repaired = (
        await asyncio.wait_for(
            asyncio.shield(
                asyncio.to_thread(
                    llm_call,
                    REPAIR_SYSTEM_PROMPT,
                    build_repair_prompt(raw_output, review_mode),
                    max_tokens=2048,
                )
            ),
            timeout=min(REVIEW_TIMEOUT, REPAIR_TIMEOUT),
        )
    ).strip()
    if not repaired or repaired.startswith("[LLM error]"):
        return None
    return parse_json_object(repaired)


async def review_turn(payload: dict[str, object]) -> dict[str, object]:
    review_mode = str(payload.get("review_mode", "chunk"))
    task_text = str(payload.get("task", "")).strip()
    if review_mode == "synthesis":
        user_prompt = build_synthesis_prompt(payload, task_text)
        system_prompt = SYNTHESIS_SYSTEM_PROMPT
    else:
        user_prompt = build_chunk_prompt(payload, task_text)
        system_prompt = CHUNK_SYSTEM_PROMPT
    started = time.monotonic()
    raw = (
        await asyncio.wait_for(
            asyncio.shield(asyncio.to_thread(llm_call, system_prompt, user_prompt)),
            timeout=REVIEW_TIMEOUT,
        )
    ).strip()
    elapsed = round(time.monotonic() - started, 1)
    if raw.startswith("[LLM error]"):
        return {
            "status": "error",
            "summary": "",
            "error": raw,
            "elapsed_sec": elapsed,
        }
    result = parse_json_object(raw)
    if not result:
        try:
            result = await repair_review_json(raw, review_mode)
        except asyncio.TimeoutError:
            result = None
    if not result:
        return {
            "status": "error",
            "summary": "",
            "error": "LM Studio did not return valid review JSON",
            "elapsed_sec": elapsed,
        }
    return {
        "status": "ok",
        "summary": str(result.get("summary", "Review complete.")),
        "decision": str(result.get("decision", "revise")),
        "findings": list(result.get("findings", [])),
        "required_changes": list(result.get("required_changes", [])),
        "test_gaps": list(result.get("test_gaps", [])),
        "elapsed_sec": elapsed,
    }


def change_set_paths(change_set: dict[str, object]) -> list[str]:
    paths = [str(item.get("path", "")).strip() for item in change_set.get("files", [])]
    paths.extend(str(path).strip() for path in change_set.get("deleted_paths", []))
    return sorted(path for path in paths if path)


def serialize_review_unit(unit: dict[str, object]) -> dict[str, object]:
    return {
        "scope_paths": [str(item) for item in unit.get("scope_paths", [])],
        "scope_ranges": [str(item) for item in unit.get("scope_ranges", [])],
        "sections": [
            {
                "path": str(section.get("path", "")),
                "action": str(section.get("action", "")),
                "old_start_line": int(section.get("old_start_line", 0) or 0),
                "old_end_line": int(section.get("old_end_line", 0) or 0),
                "new_start_line": int(section.get("new_start_line", 0) or 0),
                "new_end_line": int(section.get("new_end_line", 0) or 0),
                "old_content": str(section.get("old_content", "")),
                "new_content": str(section.get("new_content", "")),
            }
            for section in unit.get("sections", [])
        ],
    }


def merge_unique(*groups: list[str]) -> list[str]:
    merged: list[str] = []
    seen: set[str] = set()
    for group in groups:
        for item in group:
            text = str(item).strip()
            if text and text not in seen:
                merged.append(text)
                seen.add(text)
    return merged


async def post_room(room_id: str, payload: dict[str, object]) -> None:
    await agent.post_room_message(
        room_id,
        json.dumps(payload),
        content_type="application/json",
        trace_id=str(payload.get("turn_id", payload.get("task_id", ""))),
    )


async def request_test_plan(room_id: str, task: dict[str, object], implementation: dict[str, object]) -> dict[str, object]:
    task_id = str(task.get("task_id", ""))
    iteration = int(implementation.get("iteration", 0) or 0)
    turn_id = f"test-plan:{task_id}:{iteration}"
    await post_room(
        room_id,
        {
            "kind": "delegation_request",
            "turn_id": turn_id,
            "task_id": task_id,
            "iteration": iteration,
            "author": agent.handle,
            "target_handle": "test-strategist",
            "target_capability": "dev.test.plan",
            "summary": f"Critic wants a targeted validation plan for implementation iteration {iteration}.",
        },
    )
    await post_room(
        room_id,
        {
            "kind": "test_plan_request",
            "turn_id": turn_id,
            "task_id": task_id,
            "iteration": iteration,
            "author": agent.handle,
            "target_handle": "test-strategist",
            "target_capability": "dev.test.plan",
            "task": task.get("task", ""),
            "implement_summary": implementation.get("summary", ""),
            "change_paths": change_set_paths(dict(implementation.get("change_set", {}))),
            "change_set": implementation.get("change_set", {}),
        },
    )
    reply, _ = await wait_for_room_message(
        agent,
        room_id,
        lambda payload: payload.get("kind") == "test_plan_reply" and payload.get("turn_id") == turn_id,
        timeout=REVIEW_TIMEOUT + 30,
    )
    return dict(reply)


async def review_iteration(room_id: str, task: dict[str, object], workspace: dict[str, object], implementation: dict[str, object]) -> None:
    task_id = str(task.get("task_id", ""))
    iteration = int(implementation.get("iteration", 0) or 0)
    turn_id = f"review:{task_id}:{iteration}"
    await post_room(
        room_id,
        {
            "kind": "review_request",
            "turn_id": turn_id,
            "task_id": task_id,
            "iteration": iteration,
            "author": agent.handle,
            "target_handle": agent.handle,
            "target_capability": "dev.review",
            "instruction": f"Review implementation iteration {iteration} and decide whether the room can apply it.",
        },
    )
    progress_state = {"summary": f"Critic reviewing iteration {iteration}."}
    progress_task = asyncio.create_task(
        progress_pinger(
            agent,
            room_id=room_id,
            turn_id=turn_id,
            round_no=iteration,
            target_handle=agent.handle,
            target_capability="dev.review",
            summary=lambda: str(progress_state["summary"]),
            interval=TURN_PROGRESS_INTERVAL,
        )
    )
    try:
        test_plan = await request_test_plan(room_id, task, implementation)
        change_set = dict(implementation.get("change_set", {}))
        workspace_file_map = {
            str(path): str(content) for path, content in dict(workspace.get("workspace_file_map", {})).items()
        }
        units = build_review_units(change_set, workspace_file_map)
        if not units:
            raise RuntimeError("no reviewable files in change set")
        change_paths = change_set_paths(change_set)
        chunk_reviews: list[dict[str, object]] = []
        queue: list[tuple[str, dict[str, object]]] = [(f"chunk-{idx:03d}", unit) for idx, unit in enumerate(units, start=1)]
        while queue:
            chunk_id, unit = queue.pop(0)
            progress_state["summary"] = f"Critic reviewing {chunk_id} of iteration {iteration}."
            reply = await review_turn(
                {
                    "task": task.get("task", ""),
                    "review_mode": "chunk",
                    "chunk_id": chunk_id,
                    "change_paths": change_paths,
                    "review_chunk": serialize_review_unit(unit),
                    "scope_paths": list(unit.get("scope_paths", [])),
                    "scope_ranges": list(unit.get("scope_ranges", [])),
                }
            )
            if reply.get("status") == "ok":
                chunk_reviews.append(
                    {
                        "chunk_id": chunk_id,
                        "summary": reply.get("summary", ""),
                        "decision": reply.get("decision", "revise"),
                        "findings": reply.get("findings", []),
                        "required_changes": reply.get("required_changes", []),
                        "test_gaps": reply.get("test_gaps", []),
                        "scope_paths": list(unit.get("scope_paths", [])),
                        "scope_ranges": list(unit.get("scope_ranges", [])),
                    }
                )
                continue
            replacement = None
            if is_context_limit_error(str(reply.get("error", ""))):
                replacement = split_review_unit(unit)
            if replacement and len(replacement) > 1:
                for offset, split_unit in enumerate(replacement, start=1):
                    queue.insert(offset - 1, (f"{chunk_id}-{offset}", split_unit))
                continue
            raise RuntimeError(str(reply.get("error", "review failed")))

        progress_state["summary"] = f"Critic synthesizing iteration {iteration}."
        synthesis = await review_turn(
            {
                "task": task.get("task", ""),
                "review_mode": "synthesis",
                "implement_summary": implementation.get("summary", ""),
                "change_paths": change_paths,
                "chunk_reviews": chunk_reviews,
                "chunk_total": len(chunk_reviews),
            }
        )
        if synthesis.get("status") != "ok":
            raise RuntimeError(str(synthesis.get("error", "review synthesis failed")))

        combined_decision = "revise" if (
            synthesis.get("decision") == "revise" or test_plan.get("decision") == "revise"
        ) else "approve"
        summary = str(synthesis.get("summary", "Review complete."))
        if test_plan.get("summary"):
            summary = f"{summary} Test strategy: {test_plan.get('summary', '')}"
        reply = {
            "kind": "review_reply",
            "turn_id": turn_id,
            "task_id": task_id,
            "iteration": iteration,
            "author": agent.handle,
            "target_handle": "implementer",
            "status": "ok",
            "capability": "dev.review",
            "summary": summary,
            "decision": combined_decision,
            "findings": merge_unique(list(synthesis.get("findings", [])), list(test_plan.get("risks", []))),
            "required_changes": merge_unique(
                list(synthesis.get("required_changes", [])),
                list(test_plan.get("required_tests", [])),
            ),
            "test_gaps": merge_unique(
                list(synthesis.get("test_gaps", [])),
                list(test_plan.get("required_tests", [])),
            ),
            "test_plan_summary": test_plan.get("summary", ""),
            "elapsed_sec": round(sum(float(item.get("elapsed_sec", 0) or 0) for item in chunk_reviews) + float(synthesis.get("elapsed_sec", 0) or 0), 1),
        }
        await post_room(room_id, reply)
        say(agent.handle, f"{GREEN}posted{RESET} review for iteration {iteration}")
    finally:
        progress_task.cancel()
        try:
            await progress_task
        except (asyncio.CancelledError, Exception):
            pass


async def drive_room(room_id: str) -> None:
    if room_id in active_rooms:
        return
    active_rooms.add(room_id)
    try:
        events = await replay_room_with_retry(agent, room_id)
        room_state = build_room_state(events)
        if room_state.get("final_outcome"):
            return
        task = dict(room_state.get("task", {}))
        workspace = dict(room_state.get("workspace_prepare") or {})
        implementation = latest = None
        for item in reversed(room_state.get("implementations", [])):
            if item.get("status") == "ok":
                implementation = dict(item)
                break
        if not task or not workspace or implementation is None:
            return
        iteration = int(implementation.get("iteration", 0) or 0)
        if review_for_iteration(room_state, iteration) is not None:
            return
        key = f"{task.get('task_id', '')}:review:{iteration}"
        if key in handled_reviews:
            return
        handled_reviews.add(key)
        try:
            await review_iteration(room_id, task, workspace, implementation)
        except Exception:
            handled_reviews.discard(key)
            raise
    finally:
        active_rooms.discard(room_id)


@agent.on_message
async def handle(msg: RoomEvent) -> None:
    if not isinstance(msg, RoomEvent):
        return
    if msg.event_type != "message_posted" or msg.content_type != "application/json":
        return
    payload = parse_json(msg.payload)
    if not payload:
        return
    if payload.get("kind") != "implement_reply" or payload.get("status") != "ok":
        return
    event_key = f"{msg.room_id}:{msg.room_seq}:implement_reply"
    if event_key in handled_events:
        return
    handled_events.add(event_key)
    try:
        await drive_room(msg.room_id)
    except Exception as exc:
        if is_room_closed_error(exc):
            say(agent.handle, f"{YELLOW}room closed{RESET} before review could finish")
        else:
            raise


async def main() -> None:
    say(agent.handle, "connecting...")
    async with agent:
        await agent.register()
        say(agent.handle, f"{GREEN}ready{RESET} — capability dev.review")
        await agent.run_forever()


if __name__ == "__main__":
    asyncio.run(main())
