#!/usr/bin/env python3
"""Steward for the dev-task-room example."""

from __future__ import annotations

import asyncio
import json
import os
import sys
import time
import uuid
from pathlib import Path
from typing import Any

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "../../sdk/python/src"))

from tailbus import AsyncAgent, CommandSpec, Manifest, Message, RoomEvent

from dev_task_common import (
    BOLD,
    DIM,
    GREEN,
    RED,
    RESET,
    YELLOW,
    build_output_markdown,
    build_room_state,
    format_match_line,
    latest_successful_implementation,
    parse_command_payload,
    replay_room_with_retry,
    render_markdown_transcript,
    review_for_iteration,
    say,
    test_plan_for_iteration,
)

OUTPUT_DIR = Path(os.environ.get("OUTPUT_DIR", Path(__file__).resolve().parent / "output"))
STATE_DIR = OUTPUT_DIR / "state"
TURN_TIMEOUT = float(os.environ.get("TURN_TIMEOUT", "600"))
ROOM_SETTLE_POLL = float(os.environ.get("DEV_TASK_ROOM_STEWARD_POLL", "1.0"))
ROOM_TOTAL_TIMEOUT = float(os.environ.get("DEV_TASK_ROOM_TOTAL_TIMEOUT", str(max(int(TURN_TIMEOUT * 4), 900))))
DISCOVERY_TIMEOUT = float(os.environ.get("DEV_TASK_ROOM_DISCOVERY_TIMEOUT", "20"))
DISCOVERY_POLL = float(os.environ.get("DEV_TASK_ROOM_DISCOVERY_POLL", "1.0"))

REQUIRED_CAPABILITIES: tuple[str, ...] = (
    "dev.implement",
    "dev.review",
    "dev.workspace.apply",
    "dev.test.plan",
)

agent = AsyncAgent(
    "task-orchestrator",
    manifest=Manifest(
        description="Creates a shared engineering room and stewards agentic collaboration until completion",
        commands=[
            CommandSpec(
                name="run_task",
                description="Run a development task in a shared Tailbus room",
                parameters_schema=json.dumps(
                    {
                        "type": "object",
                        "properties": {
                            "task": {"type": "string"},
                            "title": {"type": "string"},
                        },
                        "required": ["task"],
                    }
                ),
            )
        ],
        tags=["workflow", "rooms", "discovery", "development", "agentic"],
        version="2.0.0",
        capabilities=["workflow.orchestrate"],
        domains=["engineering"],
        input_types=["application/json"],
        output_types=["application/json"],
    ),
    socket=os.environ.get("TAILBUS_SOCKET", "/tmp/tailbusd.sock"),
)


def short(text: str, limit: int = 92) -> str:
    return text if len(text) <= limit else text[: limit - 3] + "..."


def now_ts() -> int:
    return int(time.time())


def task_state_path(task_id: str) -> Path:
    return STATE_DIR / f"{task_id}.json"


def write_text_atomic(path: Path, content: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp_path = path.with_suffix(path.suffix + ".tmp")
    tmp_path.write_text(content, encoding="utf-8")
    tmp_path.replace(path)


def persist_task_state(state: dict[str, Any]) -> str:
    state["updated_at"] = now_ts()
    path = task_state_path(str(state["task_id"]))
    state.setdefault("artifacts", {})["state_file"] = str(path)
    try:
        write_text_atomic(path, json.dumps(state, indent=2, sort_keys=True) + "\n")
    except OSError as exc:
        say("task-state", f"{YELLOW}persist failed{RESET}: {exc}")
    return str(path)


def new_task_state(*, task_id: str, title: str, task: str) -> dict[str, Any]:
    state = {
        "version": 2,
        "task_id": task_id,
        "title": title,
        "task": task,
        "status": "starting",
        "phase": "initializing",
        "created_at": now_ts(),
        "updated_at": now_ts(),
        "room_id": "",
        "room_closed_at": None,
        "error": "",
        "discovered": [],
        "timeline": [],
        "results": {},
        "artifacts": {},
    }
    persist_task_state(state)
    return state


def set_task_phase(state: dict[str, Any], *, status: str | None = None, phase: str | None = None, error: str | None = None) -> None:
    if status is not None:
        state["status"] = status
    if phase is not None:
        state["phase"] = phase
    if error is not None:
        state["error"] = error
    persist_task_state(state)


def record_discovery(state: dict[str, Any], match: dict[str, Any]) -> None:
    state["discovered"].append(
        {
            "handle": match["handle"],
            "capability": match["capability"],
            "score": match["score"],
            "reasons": list(match.get("reasons", [])),
        }
    )
    persist_task_state(state)


def write_failure_markdown(
    *,
    task_info: dict[str, Any],
    room_id: str,
    error: str,
    events: list[RoomEvent],
) -> str:
    OUTPUT_DIR.mkdir(parents=True, exist_ok=True)
    stamp = time.strftime("%Y%m%d-%H%M%S")
    output_file = OUTPUT_DIR / f"dev-task-{stamp}-failed.md"
    lines = [
        f"# Dev Task Room Failed — {task_info.get('title', '')}",
        "",
        f"- Room: `{room_id}`" if room_id else "- Room: `(not created)`",
        f"- Task: {task_info.get('task', '')}",
        f"- Error: {error}",
        "",
    ]
    if events:
        lines.append(render_markdown_transcript(events))
        lines.append("")
    write_text_atomic(output_file, "\n".join(lines).rstrip() + "\n")
    return str(output_file)


async def post_room(room_id: str, payload: dict[str, Any]) -> None:
    trace_id = str(payload.get("turn_id", payload.get("task_id", "")))
    await agent.post_room_message(
        room_id,
        json.dumps(payload),
        content_type="application/json",
        trace_id=trace_id,
    )


async def discover_handle(capability: str) -> dict[str, Any]:
    deadline = time.monotonic() + DISCOVERY_TIMEOUT
    last_error = f"no handle found for capability {capability}"
    while True:
        matches = await agent.find_handles(capabilities=[capability], limit=1)
        if matches:
            match = matches[0]
            return {
                "capability": capability,
                "handle": match.handle,
                "score": match.score,
                "reasons": list(match.match_reasons),
            }
        if time.monotonic() >= deadline:
            raise RuntimeError(last_error)
        say(agent.handle, f"{YELLOW}waiting{RESET} for capability `{capability}` to register...")
        await asyncio.sleep(DISCOVERY_POLL)


async def post_discovery(room_id: str, task_id: str, match: dict[str, Any]) -> None:
    await post_room(
        room_id,
        {
            "kind": "collaborator_discovered",
            "task_id": task_id,
            "author": agent.handle,
            "target_handle": match["handle"],
            "target_capability": match["capability"],
            "score": match["score"],
            "match_reasons": list(match["reasons"]),
            "recorded_at": int(time.time()),
        },
    )


def build_room_title(task: str) -> str:
    trimmed = task if len(task) <= 60 else task[:57] + "..."
    return f"dev-task: {trimmed}"


def summarize_room_state(task_state: dict[str, Any], room_state: dict[str, Any]) -> None:
    implementation = latest_successful_implementation(room_state)
    review = review_for_iteration(room_state, int(implementation.get("iteration", 0) or 0)) if implementation else None
    test_plan = test_plan_for_iteration(room_state, int(implementation.get("iteration", 0) or 0)) if implementation else None
    apply_result = room_state.get("apply_results", [])[-1] if room_state.get("apply_results") else None
    final_outcome = room_state.get("final_outcome")

    timeline = {
        "plans": len(room_state.get("plans", [])),
        "delegations": len(room_state.get("delegations", [])),
        "implementations": len(room_state.get("implementations", [])),
        "reviews": len(room_state.get("reviews", [])),
        "test_plans": len(room_state.get("test_plans", [])),
        "apply_requests": len(room_state.get("apply_requests", [])),
        "apply_results": len(room_state.get("apply_results", [])),
    }
    task_state["timeline"] = [timeline]
    task_state["results"]["iteration"] = int(implementation.get("iteration", 0) or 0) if implementation else 0
    if implementation:
        task_state["results"]["implement_summary"] = implementation.get("summary", "")
    if review:
        task_state["results"]["review_summary"] = review.get("summary", "")
        task_state["results"]["review_decision"] = review.get("decision", "")
    if test_plan:
        task_state["results"]["test_plan_summary"] = test_plan.get("summary", "")
        task_state["results"]["required_tests"] = test_plan.get("required_tests", [])
    if apply_result:
        task_state["results"]["changed_paths"] = apply_result.get("changed_paths", [])
        task_state["results"]["test_status"] = apply_result.get("test_result", {}).get("status", "unknown")
        task_state["results"]["test_summary"] = apply_result.get("test_result", {}).get("summary", "")
    if final_outcome:
        task_state["results"]["final_outcome"] = {
            "status": final_outcome.get("status", "unknown"),
            "summary": final_outcome.get("summary", ""),
        }
        task_state["phase"] = f"final:{final_outcome.get('status', 'unknown')}"
    else:
        phase = "waiting-for-initiative"
        if room_state.get("plans") and not room_state.get("implementations"):
            phase = "planning"
        elif room_state.get("implementations") and not room_state.get("reviews"):
            phase = "reviewing"
        elif room_state.get("reviews") and not room_state.get("apply_results"):
            phase = "awaiting-apply"
        elif room_state.get("apply_results"):
            phase = "verifying"
        task_state["phase"] = phase
    persist_task_state(task_state)


async def wait_for_final_outcome(room_id: str, task_state: dict[str, Any]) -> tuple[dict[str, Any], list[RoomEvent], dict[str, Any]]:
    deadline = time.monotonic() + ROOM_TOTAL_TIMEOUT
    last_events: list[RoomEvent] = []
    last_state: dict[str, Any] = {}
    while time.monotonic() < deadline:
        events = await replay_room_with_retry(agent, room_id)
        room_state = build_room_state(events)
        summarize_room_state(task_state, room_state)
        if room_state.get("final_outcome"):
            return dict(room_state["final_outcome"]), events, room_state
        last_events = events
        last_state = room_state
        await asyncio.sleep(ROOM_SETTLE_POLL)
    raise TimeoutError(f"room did not reach final_outcome within {ROOM_TOTAL_TIMEOUT:.0f}s")


async def run_task_flow(task_text: str, title: str) -> dict[str, Any]:
    task_id = f"task-{uuid.uuid4().hex[:8]}"
    task_state = new_task_state(task_id=task_id, title=title or task_text, task=task_text)
    discovered = [await discover_handle(cap) for cap in REQUIRED_CAPABILITIES]
    room_id = await agent.create_room(build_room_title(title or task_text), members=[item["handle"] for item in discovered])
    task_state["room_id"] = room_id
    set_task_phase(task_state, status="running", phase="room_created")

    events: list[RoomEvent] = []
    room_state: dict[str, Any] = {}
    try:
        await post_room(
            room_id,
            {
                "kind": "task_opened",
                "task_id": task_id,
                "title": title or task_text,
                "task": task_text,
                "author": agent.handle,
                "opened_at": int(time.time()),
                "max_revisions": 2,
                "max_repairs": 1,
            },
        )
        for item in discovered:
            await post_discovery(room_id, task_id, item)
            say(agent.handle, format_match_line(item))
            record_discovery(task_state, item)

        final_outcome, events, room_state = await wait_for_final_outcome(room_id, task_state)
        implementation = latest_successful_implementation(room_state) or {}
        iteration = int(implementation.get("iteration", 0) or 0)
        review = review_for_iteration(room_state, iteration) or {}
        apply_result = room_state.get("apply_results", [])[-1] if room_state.get("apply_results") else {}
        test_result = dict(apply_result.get("test_result", {}))

        OUTPUT_DIR.mkdir(parents=True, exist_ok=True)
        stamp = time.strftime("%Y%m%d-%H%M%S")
        output_file = OUTPUT_DIR / f"dev-task-{stamp}.md"
        write_text_atomic(
            output_file,
            build_output_markdown(
                task_info={"title": title or task_text, "task": task_text},
                room_id=room_id,
                discovered=discovered,
                implement_reply=implementation,
                review_reply=review,
                apply_result=apply_result,
                test_result=test_result,
                events=events,
            ),
        )
        task_state["artifacts"]["markdown_output"] = str(output_file)
        task_state["results"]["output_file"] = str(output_file)
        task_state["results"]["room_id"] = room_id
        task_state["results"]["final_status"] = final_outcome.get("status", "unknown")
        set_task_phase(
            task_state,
            status="complete" if final_outcome.get("status") == "complete" else "failed",
            phase=f"final:{final_outcome.get('status', 'unknown')}",
            error="" if final_outcome.get("status") == "complete" else str(final_outcome.get("summary", "")),
        )
        return {
            "status": final_outcome.get("status", "unknown"),
            "room_id": room_id,
            "task_id": task_id,
            "summary": final_outcome.get("summary", ""),
            "changed_paths": apply_result.get("changed_paths", []),
            "workspace_root": apply_result.get("workspace_root", ""),
            "test_status": test_result.get("status", "unknown"),
            "test_summary": test_result.get("summary", ""),
            "output_file": str(output_file),
            "state_file": task_state["artifacts"]["state_file"],
        }
    except Exception as exc:
        set_task_phase(task_state, status="failed", phase="failed", error=str(exc))
        if room_id and not events:
            try:
                events = await replay_room_with_retry(agent, room_id)
            except Exception:
                events = []
        failure_output = write_failure_markdown(
            task_info={"title": title or task_text, "task": task_text},
            room_id=room_id,
            error=str(exc),
            events=events,
        )
        task_state["artifacts"]["failure_output"] = failure_output
        persist_task_state(task_state)
        raise
    finally:
        if room_id:
            say(agent.handle, f"{DIM}closing room:{RESET} {room_id[:8]} status={task_state.get('status', 'unknown')}")
            try:
                await agent.close_room(room_id)
            except Exception as exc:
                task_state["results"]["room_close_error"] = str(exc)
        task_state["room_closed_at"] = now_ts()
        persist_task_state(task_state)


@agent.on_message
async def handle(msg: Message | RoomEvent) -> None:
    if isinstance(msg, RoomEvent):
        return
    if msg.message_type != "session_open":
        return

    data = parse_command_payload(msg.payload)
    task_text = str(data.get("task") or data.get("prompt") or "").strip()
    title = str(data.get("title") or task_text).strip()
    if not task_text:
        await agent.resolve(
            msg.session,
            json.dumps({"error": "Missing task"}),
            content_type="application/json",
        )
        return

    say(agent.handle, f"task: {BOLD}{short(task_text, 120)}{RESET}")
    started = time.monotonic()
    try:
        result = await run_task_flow(task_text, title)
        result["total_time"] = round(time.monotonic() - started, 1)
        await agent.resolve(msg.session, json.dumps(result), content_type="application/json")
    except Exception as exc:
        say(agent.handle, f"{RED}error{RESET}: {exc}")
        await agent.resolve(
            msg.session,
            json.dumps({"error": str(exc), "status": "failed"}),
            content_type="application/json",
        )


async def main() -> None:
    say(agent.handle, "connecting...")
    async with agent:
        await agent.register()
        say(agent.handle, f"{GREEN}ready{RESET} — command: run_task")
        await agent.run_forever()


if __name__ == "__main__":
    asyncio.run(main())
