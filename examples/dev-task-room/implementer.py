#!/usr/bin/env python3
"""Codex-backed implementation lead for the dev-task-room example."""

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
    MAX_AUTONOMOUS_REVISIONS,
    MAX_REPAIR_ATTEMPTS,
    RED,
    RESET,
    TURN_PROGRESS_INTERVAL,
    YELLOW,
    apply_request_for_iteration,
    apply_result_for_iteration,
    build_room_state,
    implementation_count,
    is_room_closed_error,
    latest_successful_implementation,
    parse_json,
    parse_json_object,
    progress_pinger,
    render_llm_transcript,
    replay_room_with_retry,
    review_for_iteration,
    run_codex_json,
    say,
    tmp_json_path,
)

SYSTEM_PROMPT = """You are the implementation lead in a Tailbus engineering room.

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
        description="Leads implementation in the engineering room and triggers apply when the room approves",
        commands=[],
        tags=["llm", "codex", "development", "implementation", "agentic"],
        version="2.0.0",
        capabilities=["dev.implement"],
        domains=["engineering"],
        input_types=["application/json"],
        output_types=["application/json"],
    ),
    socket=os.environ.get("TAILBUS_SOCKET", "/tmp/tailbusd.sock"),
)

handled_events: set[str] = set()
handled_actions: set[str] = set()
active_rooms: set[str] = set()


def action_key(task_id: str, action: str, iteration: int, reason: str = "") -> str:
    return f"{task_id}:{action}:{iteration}:{reason}"


def build_plan_summary(task: str, reason: str, iteration: int) -> str:
    if reason == "initial":
        return f"Implementer is taking first pass on iteration {iteration}: inspect workspace, draft changes, then ask the room to review."
    if reason == "revision":
        return f"Implementer is revising iteration {iteration} to address reviewer findings before asking for approval again."
    return f"Implementer is repairing iteration {iteration} after failed fixture tests."


def build_prompt(
    *,
    task: dict[str, object],
    workspace: dict[str, object],
    transcript: str,
    iteration: int,
    reason: str,
    previous_change_set: dict[str, object] | None = None,
    review: dict[str, object] | None = None,
    failing_apply: dict[str, object] | None = None,
) -> str:
    parts = [
        "Task:",
        str(task.get("task", "")),
        "",
        f"Iteration: {iteration}",
        f"Reason: {reason}",
        "",
        "Workspace snapshot:",
        str(workspace.get("workspace_snapshot", "")),
    ]
    if previous_change_set:
        parts.extend(["", "Previous change set:", json.dumps(previous_change_set, indent=2)])
    if review:
        parts.extend(
            [
                "",
                "Reviewer findings:",
                json.dumps(review.get("findings", []), indent=2),
                "",
                "Required changes:",
                json.dumps(review.get("required_changes", []), indent=2),
            ]
        )
    if failing_apply:
        parts.extend(
            [
                "",
                "Failed test output:",
                str(failing_apply.get("test_result", {}).get("output", "")),
            ]
        )
    if transcript:
        parts.extend(["", "Room transcript:", transcript])
    return "\n".join(parts)


def choose_next_action(room_state: dict[str, object]) -> dict[str, object] | None:
    if room_state.get("final_outcome"):
        return None
    task = dict(room_state.get("task", {}))
    workspace = room_state.get("workspace_prepare")
    if not task or not workspace:
        return None

    latest = latest_successful_implementation(room_state)
    if latest is None:
        return {"type": "implement", "iteration": 1, "reason": "initial"}

    current_iteration = int(latest.get("iteration", 0) or 0)
    review = review_for_iteration(room_state, current_iteration)
    if review is None:
        return None

    decision = str(review.get("decision", "revise"))
    if decision == "revise":
        revision_count = implementation_count(room_state, reason="revision")
        next_impl = current_iteration + 1
        if revision_count < MAX_AUTONOMOUS_REVISIONS:
            existing = latest_successful_implementation(
                {
                    **room_state,
                    "implementations": [item for item in room_state.get("implementations", []) if int(item.get("iteration", 0) or 0) == next_impl]
                }
            )
            if existing is None:
                return {
                    "type": "implement",
                    "iteration": next_impl,
                    "reason": "revision",
                    "review": review,
                    "previous_change_set": latest.get("change_set", {}),
                }
        return {
            "type": "final",
            "status": "failed",
            "summary": "Reviewer still requires changes after the room exhausted its revision budget.",
        }

    apply_request = apply_request_for_iteration(room_state, current_iteration)
    apply_result = apply_result_for_iteration(room_state, current_iteration)
    if apply_request is None and apply_result is None:
        return {
            "type": "apply",
            "iteration": current_iteration,
            "change_set": latest.get("change_set", {}),
        }
    if apply_result is None:
        return None

    test_result = dict(apply_result.get("test_result", {}))
    if test_result.get("status") == "ok":
        return {
            "type": "final",
            "status": "complete",
            "summary": str(latest.get("summary", "Implementation complete and tests passed.")),
        }

    repair_count = implementation_count(room_state, reason="repair")
    next_impl = current_iteration + 1
    if repair_count < MAX_REPAIR_ATTEMPTS:
        existing = latest_successful_implementation(
            {
                **room_state,
                "implementations": [item for item in room_state.get("implementations", []) if int(item.get("iteration", 0) or 0) == next_impl]
            }
        )
        if existing is None:
            return {
                "type": "implement",
                "iteration": next_impl,
                "reason": "repair",
                "previous_change_set": latest.get("change_set", {}),
                "failing_apply": apply_result,
            }
    return {
        "type": "final",
        "status": "failed",
        "summary": "Fixture tests still fail after the room exhausted its repair budget.",
    }


async def post_room(room_id: str, payload: dict[str, object]) -> None:
    trace_id = str(payload.get("turn_id", payload.get("task_id", "")))
    await agent.post_room_message(
        room_id,
        json.dumps(payload),
        content_type="application/json",
        trace_id=trace_id,
    )


async def run_implementation(
    room_id: str,
    task: dict[str, object],
    workspace: dict[str, object],
    room_state: dict[str, object],
    *,
    iteration: int,
    reason: str,
    review: dict[str, object] | None = None,
    previous_change_set: dict[str, object] | None = None,
    failing_apply: dict[str, object] | None = None,
) -> None:
    task_id = str(task.get("task_id", ""))
    turn_id = f"implement:{task_id}:{iteration}"
    await post_room(
        room_id,
        {
            "kind": "plan_proposed",
            "turn_id": turn_id,
            "task_id": task_id,
            "iteration": iteration,
            "reason": reason,
            "author": agent.handle,
            "summary": build_plan_summary(str(task.get("task", "")), reason, iteration),
        },
    )
    progress_state = {"summary": f"Implementer preparing {reason} prompt."}
    progress_task = asyncio.create_task(
        progress_pinger(
            agent,
            room_id=room_id,
            turn_id=turn_id,
            round_no=iteration,
            target_handle=agent.handle,
            target_capability="dev.implement",
            summary=lambda: str(progress_state["summary"]),
            interval=TURN_PROGRESS_INTERVAL,
        )
    )
    try:
        events = await replay_room_with_retry(agent, room_id)
        transcript = render_llm_transcript(events)
        prompt = build_prompt(
            task=task,
            workspace=workspace,
            transcript=transcript,
            iteration=iteration,
            reason=reason,
            previous_change_set=previous_change_set,
            review=review,
            failing_apply=failing_apply,
        )
        output_path = tmp_json_path("dev_task_implement", turn_id)
        say(agent.handle, f"prompt ready ({len(prompt)} chars); calling {BOLD}{CODEX_MODEL}{RESET}")
        progress_state["summary"] = f"Implementer waiting for Codex on iteration {iteration}."
        started = time.monotonic()
        raw = (await run_codex_json(SYSTEM_PROMPT + "\n\n" + prompt, output_path, CODEX_TIMEOUT)).strip()
        elapsed = round(time.monotonic() - started, 1)
        if raw.startswith("[codex error]"):
            reply = {
                "kind": "implement_reply",
                "turn_id": turn_id,
                "task_id": task_id,
                "iteration": iteration,
                "reason": reason,
                "author": agent.handle,
                "status": "error",
                "capability": "dev.implement",
                "summary": "",
                "error": raw,
                "elapsed_sec": elapsed,
            }
        else:
            change_set = parse_json_object(raw)
            if not change_set:
                reply = {
                    "kind": "implement_reply",
                    "turn_id": turn_id,
                    "task_id": task_id,
                    "iteration": iteration,
                    "reason": reason,
                    "author": agent.handle,
                    "status": "error",
                    "capability": "dev.implement",
                    "summary": "",
                    "error": "Codex did not return valid JSON",
                    "raw_output": raw[:2000],
                    "elapsed_sec": elapsed,
                }
            else:
                reply = {
                    "kind": "implement_reply",
                    "turn_id": turn_id,
                    "task_id": task_id,
                    "iteration": iteration,
                    "reason": reason,
                    "author": agent.handle,
                    "status": "ok",
                    "capability": "dev.implement",
                    "summary": str(change_set.get("summary", "Prepared change set.")),
                    "change_set": change_set,
                    "elapsed_sec": elapsed,
                }
        await post_room(room_id, reply)
        if reply["status"] == "ok":
            say(agent.handle, f"{GREEN}posted{RESET} implementation iteration {iteration} in {reply['elapsed_sec']:.1f}s")
        else:
            say(agent.handle, f"{RED}error{RESET}: {reply.get('error', 'unknown error')}")
    finally:
        progress_task.cancel()
        try:
            await progress_task
        except (asyncio.CancelledError, Exception):
            pass


async def request_apply(room_id: str, task: dict[str, object], iteration: int, change_set: dict[str, object]) -> None:
    task_id = str(task.get("task_id", ""))
    turn_id = f"apply:{task_id}:{iteration}"
    await post_room(
        room_id,
        {
            "kind": "delegation_request",
            "turn_id": turn_id,
            "task_id": task_id,
            "iteration": iteration,
            "author": agent.handle,
            "target_handle": "workspace-agent",
            "target_capability": "dev.workspace.apply",
            "summary": f"Implementation iteration {iteration} is approved; apply it in the bounded workspace and run tests.",
        },
    )
    await post_room(
        room_id,
        {
            "kind": "apply_request",
            "turn_id": turn_id,
            "task_id": task_id,
            "iteration": iteration,
            "author": agent.handle,
            "target_handle": "workspace-agent",
            "target_capability": "dev.workspace.apply",
            "instruction": "Apply the approved change set inside the bounded workspace and run the fixture tests.",
            "change_set": change_set,
        },
    )
    say(agent.handle, f"{GREEN}requested{RESET} apply for iteration {iteration}")


async def post_final_outcome(room_id: str, task: dict[str, object], status: str, summary: str) -> None:
    await post_room(
        room_id,
        {
            "kind": "final_outcome",
            "turn_id": f"final:{task.get('task_id', '')}:{status}",
            "task_id": task.get("task_id", ""),
            "author": agent.handle,
            "status": status,
            "summary": summary,
            "recorded_at": int(time.time()),
        },
    )
    say(agent.handle, f"{GREEN}final{RESET} outcome posted: {status}")


async def drive_room(room_id: str) -> None:
    if room_id in active_rooms:
        return
    active_rooms.add(room_id)
    try:
        while True:
            events = await replay_room_with_retry(agent, room_id)
            room_state = build_room_state(events)
            task = dict(room_state.get("task", {}))
            workspace = dict(room_state.get("workspace_prepare") or {})
            action = choose_next_action(room_state)
            if not action or not task:
                return
            if action["type"] == "implement":
                key = action_key(str(task.get("task_id", "")), "implement", int(action["iteration"]), str(action["reason"]))
                if key in handled_actions:
                    return
                handled_actions.add(key)
                try:
                    await run_implementation(
                        room_id,
                        task,
                        workspace,
                        room_state,
                        iteration=int(action["iteration"]),
                        reason=str(action["reason"]),
                        review=dict(action.get("review", {})) if action.get("review") else None,
                        previous_change_set=dict(action.get("previous_change_set", {})) if action.get("previous_change_set") else None,
                        failing_apply=dict(action.get("failing_apply", {})) if action.get("failing_apply") else None,
                    )
                except Exception:
                    handled_actions.discard(key)
                    raise
                continue
            if action["type"] == "apply":
                key = action_key(str(task.get("task_id", "")), "apply", int(action["iteration"]))
                if key in handled_actions:
                    return
                handled_actions.add(key)
                try:
                    await request_apply(room_id, task, int(action["iteration"]), dict(action.get("change_set", {})))
                except Exception:
                    handled_actions.discard(key)
                    raise
                return
            if action["type"] == "final":
                key = action_key(str(task.get("task_id", "")), "final", current_iteration := int(latest_successful_implementation(room_state).get("iteration", 0) or 0), str(action.get("status", "")))
                if key in handled_actions:
                    return
                handled_actions.add(key)
                try:
                    await post_final_outcome(room_id, task, str(action["status"]), str(action["summary"]))
                except Exception:
                    handled_actions.discard(key)
                    raise
                return
            return
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
    kind = str(payload.get("kind", ""))
    if kind not in {"task_opened", "workspace_prepare_reply", "review_reply", "apply_result"}:
        return
    event_key = f"{msg.room_id}:{msg.room_seq}:{kind}"
    if event_key in handled_events:
        return
    handled_events.add(event_key)
    if kind == "review_reply" and payload.get("target_handle") not in ("", agent.handle):
        return
    await drive_room(msg.room_id)


async def main() -> None:
    say(agent.handle, "connecting...")
    async with agent:
        await agent.register()
        say(agent.handle, f"{GREEN}ready{RESET} — capability dev.implement")
        await agent.run_forever()


if __name__ == "__main__":
    asyncio.run(main())
