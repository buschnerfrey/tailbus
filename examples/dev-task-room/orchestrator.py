#!/usr/bin/env python3
"""Orchestrator for the dev-task-room example."""

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
    CYAN,
    DIM,
    GREEN,
    RED,
    RESET,
    YELLOW,
    build_output_markdown,
    format_match_line,
    parse_command_payload,
    parse_json,
    replay_room_with_retry,
    say,
)

OUTPUT_DIR = Path(os.environ.get("OUTPUT_DIR", Path(__file__).resolve().parent / "output"))
TURN_TIMEOUT = float(os.environ.get("TURN_TIMEOUT", "120"))

REQUIRED_CAPABILITIES: tuple[str, ...] = (
    "dev.implement",
    "dev.review",
    "dev.workspace.apply",
)

agent = AsyncAgent(
    "task-orchestrator",
    manifest=Manifest(
        description="Coordinates arbitrary development tasks between implementer, critic, and local workspace agent",
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
        tags=["workflow", "rooms", "discovery", "development"],
        version="1.0.0",
        capabilities=["workflow.orchestrate"],
        domains=["engineering"],
        input_types=["application/json"],
        output_types=["application/json"],
    ),
    socket=os.environ.get("TAILBUS_SOCKET", "/tmp/tailbusd.sock"),
)

pending_turns: dict[str, dict[str, Any]] = {}


def short(text: str, limit: int = 92) -> str:
    return text if len(text) <= limit else text[: limit - 3] + "..."


async def post_room(room_id: str, payload: dict[str, Any]) -> None:
    trace_id = str(payload.get("turn_id", payload.get("task_id", "")))
    await agent.post_room_message(
        room_id,
        json.dumps(payload),
        content_type="application/json",
        trace_id=trace_id,
    )


async def handle_room_event(event: RoomEvent) -> None:
    if event.event_type != "message_posted" or event.content_type != "application/json":
        return
    payload = parse_json(event.payload)
    if not payload:
        return
    turn_id = str(payload.get("turn_id", ""))
    if not turn_id:
        return
    pending = pending_turns.get(turn_id)
    if pending is None:
        return
    future = pending["future"]
    expected_kind = pending["expected_kind"]
    if future.done():
        return
    kind = str(payload.get("kind", ""))
    if kind == "turn_progress":
        pending["last_progress"] = time.monotonic()
        pending["last_summary"] = str(payload.get("summary", "working"))
        return
    if kind != expected_kind:
        return
    future.set_result(payload)


def expected_reply_kind(request_kind: str) -> str:
    if request_kind == "workspace_prepare_request":
        return "workspace_prepare_reply"
    if request_kind == "apply_request":
        return "apply_result"
    return request_kind.replace("_request", "_reply")


async def discover_handle(capability: str) -> dict[str, Any]:
    matches = await agent.find_handles(capabilities=[capability], limit=1)
    if not matches:
        raise RuntimeError(f"no handle found for capability {capability}")
    match = matches[0]
    return {
        "capability": capability,
        "handle": match.handle,
        "score": match.score,
        "reasons": list(match.match_reasons),
    }


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


async def request_turn(
    *,
    room_id: str,
    task_id: str,
    round_no: int,
    kind: str,
    target_handle: str,
    target_capability: str,
    instruction: str,
    extra: dict[str, Any] | None = None,
) -> dict[str, Any]:
    turn_id = str(uuid.uuid4())
    payload = {
        "kind": kind,
        "turn_id": turn_id,
        "task_id": task_id,
        "round": round_no,
        "target_handle": target_handle,
        "target_capability": target_capability,
        "instruction": instruction,
        "requested_by": agent.handle,
        "requested_at": int(time.time()),
    }
    if extra:
        payload.update(extra)
    future: asyncio.Future[dict[str, Any]] = asyncio.get_running_loop().create_future()
    pending_turns[turn_id] = {
        "future": future,
        "expected_kind": expected_reply_kind(kind),
        "last_progress": time.monotonic(),
        "last_summary": "working",
    }
    await post_room(room_id, payload)
    say(agent.handle, f"{CYAN}→{RESET} @{target_handle} [{target_capability}]")
    say(agent.handle, f"   {DIM}{short(instruction)}{RESET}")
    try:
        while True:
            pending = pending_turns[turn_id]
            idle_for = time.monotonic() - float(pending["last_progress"])
            remaining = TURN_TIMEOUT - idle_for
            if remaining <= 0:
                raise asyncio.TimeoutError
            try:
                reply = await asyncio.wait_for(future, timeout=min(remaining, 5.0))
                break
            except asyncio.TimeoutError:
                if future.done():
                    reply = future.result()
                    break
                continue
        if reply.get("status") == "ok":
            say(agent.handle, f"{GREEN}←{RESET} @{target_handle} ({reply.get('elapsed_sec', 0):.1f}s)")
        else:
            say(agent.handle, f"{YELLOW}!{RESET} @{target_handle} returned {reply.get('status', 'unknown')}")
        return reply
    except asyncio.TimeoutError:
        await post_room(
            room_id,
            {
                "kind": "turn_timeout",
                "turn_id": turn_id,
                "task_id": task_id,
                "round": round_no,
                "target_handle": target_handle,
                "target_capability": target_capability,
                "seconds": TURN_TIMEOUT,
                "recorded_at": int(time.time()),
            },
        )
        say(agent.handle, f"{RED}timeout{RESET} waiting for @{target_handle}")
        pending = pending_turns.get(turn_id, {})
        last_summary = str(pending.get("last_summary", "working"))
        return {
            "kind": expected_reply_kind(kind),
            "turn_id": turn_id,
            "author": target_handle,
            "status": "timeout",
            "capability": target_capability,
            "summary": last_summary,
            "error": f"Timed out after {TURN_TIMEOUT:.0f}s without progress",
            "elapsed_sec": TURN_TIMEOUT,
        }
    finally:
        pending_turns.pop(turn_id, None)


def ensure_ok(reply: dict[str, Any], *, label: str) -> None:
    if reply.get("status") != "ok":
        message = reply.get("error") or reply.get("summary") or "unknown error"
        raise RuntimeError(f"{label} failed: {message}")


def final_review_reply(initial: dict[str, Any], revised: dict[str, Any] | None) -> dict[str, Any]:
    return revised or initial


def build_room_title(task: str) -> str:
    trimmed = task if len(task) <= 60 else task[:57] + "..."
    return f"dev-task: {trimmed}"


async def run_task_flow(task_text: str, title: str) -> dict[str, Any]:
    task_id = f"task-{uuid.uuid4().hex[:8]}"
    discovered = [await discover_handle(cap) for cap in REQUIRED_CAPABILITIES]
    members = [item["handle"] for item in discovered]
    room_id = await agent.create_room(build_room_title(title or task_text), members=members)

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
            },
        )
        for item in discovered:
            await post_discovery(room_id, task_id, item)
            say(agent.handle, format_match_line(item))

        workspace = next(item for item in discovered if item["capability"] == "dev.workspace.apply")
        implementer = next(item for item in discovered if item["capability"] == "dev.implement")
        critic = next(item for item in discovered if item["capability"] == "dev.review")

        prepare_reply = await request_turn(
            room_id=room_id,
            task_id=task_id,
            round_no=0,
            kind="workspace_prepare_request",
            target_handle=workspace["handle"],
            target_capability=workspace["capability"],
            instruction="Reset the local fixture workspace and return a snapshot of the current files.",
        )
        ensure_ok(prepare_reply, label="workspace prepare")
        workspace_snapshot = str(prepare_reply.get("workspace_snapshot", ""))

        implement_reply = await request_turn(
            room_id=room_id,
            task_id=task_id,
            round_no=1,
            kind="implement_request",
            target_handle=implementer["handle"],
            target_capability=implementer["capability"],
            instruction="Implement the task by returning a structured whole-file change set for the bounded workspace.",
            extra={"task": task_text, "workspace_snapshot": workspace_snapshot},
        )
        ensure_ok(implement_reply, label="implementation")

        review_reply = await request_turn(
            room_id=room_id,
            task_id=task_id,
            round_no=1,
            kind="review_request",
            target_handle=critic["handle"],
            target_capability=critic["capability"],
            instruction="Review the proposed change set for correctness, regressions, and missing tests. Approve or request revision.",
            extra={
                "task": task_text,
                "workspace_snapshot": workspace_snapshot,
                "change_set": implement_reply.get("change_set", {}),
            },
        )
        ensure_ok(review_reply, label="review")

        final_implement_reply = implement_reply
        final_review = review_reply
        if review_reply.get("decision") == "revise":
            final_implement_reply = await request_turn(
                room_id=room_id,
                task_id=task_id,
                round_no=2,
                kind="implement_request",
                target_handle=implementer["handle"],
                target_capability=implementer["capability"],
                instruction="Revise the change set to address the reviewer findings exactly.",
                extra={
                    "task": task_text,
                    "workspace_snapshot": workspace_snapshot,
                    "review_findings": review_reply.get("findings", []),
                    "required_changes": review_reply.get("required_changes", []),
                    "previous_change_set": implement_reply.get("change_set", {}),
                },
            )
            ensure_ok(final_implement_reply, label="revision")
            final_review = await request_turn(
                room_id=room_id,
                task_id=task_id,
                round_no=2,
                kind="review_request",
                target_handle=critic["handle"],
                target_capability=critic["capability"],
                instruction="Re-review the revised change set. Approve if the issues are addressed.",
                extra={
                    "task": task_text,
                    "workspace_snapshot": workspace_snapshot,
                    "change_set": final_implement_reply.get("change_set", {}),
                },
            )
            ensure_ok(final_review, label="re-review")
            if final_review.get("decision") == "revise":
                raise RuntimeError("critic still requires revision after the second implementation round")

        apply_reply = await request_turn(
            room_id=room_id,
            task_id=task_id,
            round_no=3,
            kind="apply_request",
            target_handle=workspace["handle"],
            target_capability=workspace["capability"],
            instruction="Apply the approved change set inside the local fixture workspace and run the fixture tests.",
            extra={"change_set": final_implement_reply.get("change_set", {})},
        )
        ensure_ok(apply_reply, label="apply")
        test_result = dict(apply_reply.get("test_result", {}))

        await post_room(
            room_id,
            {
                "kind": "final_summary",
                "task_id": task_id,
                "author": agent.handle,
                "summary": final_implement_reply.get("summary", ""),
                "review_summary": final_review.get("summary", ""),
                "apply_status": apply_reply.get("status", "unknown"),
                "test_status": test_result.get("status", "unknown"),
                "recorded_at": int(time.time()),
            },
        )

        events = await replay_room_with_retry(agent, room_id)
        OUTPUT_DIR.mkdir(parents=True, exist_ok=True)
        stamp = time.strftime("%Y%m%d-%H%M%S")
        output_file = OUTPUT_DIR / f"dev-task-{stamp}.md"
        output_file.write_text(
            build_output_markdown(
                task_info={"title": title or task_text, "task": task_text},
                room_id=room_id,
                discovered=discovered,
                implement_reply=final_implement_reply,
                review_reply=final_review_reply(review_reply, final_review),
                apply_result=apply_reply,
                test_result=test_result,
                events=events,
            ),
            encoding="utf-8",
        )
        return {
            "status": "complete",
            "room_id": room_id,
            "task_id": task_id,
            "summary": final_implement_reply.get("summary", ""),
            "review": final_review.get("summary", ""),
            "changed_paths": apply_reply.get("changed_paths", []),
            "workspace_root": apply_reply.get("workspace_root", ""),
            "test_status": test_result.get("status", "unknown"),
            "test_summary": test_result.get("summary", ""),
            "output_file": str(output_file),
        }
    finally:
        try:
            await agent.close_room(room_id)
        except Exception:
            pass


@agent.on_message
async def handle(msg: Message | RoomEvent) -> None:
    if isinstance(msg, RoomEvent):
        await handle_room_event(msg)
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
