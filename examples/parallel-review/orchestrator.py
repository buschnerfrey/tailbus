#!/usr/bin/env python3
"""Orchestrator for the parallel-review example.

Fans out code review to 3 specialized reviewers (security, performance, style)
simultaneously, then synthesizes a combined report.
"""

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

from review_common import (
    BOLD,
    CODE_SAMPLES,
    CYAN,
    DIM,
    GREEN,
    RED,
    RESET,
    YELLOW,
    parse_json,
    say,
)

OUTPUT_DIR = Path(os.environ.get("OUTPUT_DIR", Path(__file__).resolve().parent / "output"))
TURN_TIMEOUT = float(os.environ.get("TURN_TIMEOUT", "300"))

REQUIRED_CAPABILITIES: tuple[str, ...] = (
    "review.security",
    "review.performance",
    "review.style",
)

agent = AsyncAgent(
    "review-orchestrator",
    manifest=Manifest(
        description="Coordinates parallel code reviews across security, performance, and style reviewers",
        commands=[
            CommandSpec(
                name="review",
                description="Review a code sample with all 3 reviewers in parallel",
                parameters_schema=json.dumps({
                    "type": "object",
                    "properties": {
                        "scenario": {"type": "string"},
                        "code": {"type": "string"},
                        "title": {"type": "string"},
                    },
                }),
            )
        ],
        tags=["workflow", "rooms", "discovery", "review"],
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
    return text if len(text) <= limit else text[:limit - 3] + "..."


def write_text_atomic(path: Path, content: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp_path = path.with_suffix(path.suffix + ".tmp")
    tmp_path.write_text(content, encoding="utf-8")
    tmp_path.replace(path)


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
    if future.done():
        return
    kind = str(payload.get("kind", ""))
    if kind == "turn_progress":
        pending["last_progress"] = time.monotonic()
        summary = str(payload.get("summary", "working"))
        elapsed = float(payload.get("elapsed_sec", 0) or 0)
        say(
            agent.handle,
            f"{CYAN}↺{RESET} @{pending.get('target_handle', '?')} "
            f"{elapsed:.1f}s {short(summary, 120)}",
        )
        return
    if kind != "review_reply":
        return
    future.set_result(payload)


async def request_review(
    *,
    room_id: str,
    target_handle: str,
    target_capability: str,
    code: str,
    title: str,
) -> dict[str, Any]:
    turn_id = str(uuid.uuid4())
    payload = {
        "kind": "review_request",
        "turn_id": turn_id,
        "round": 1,
        "target_handle": target_handle,
        "target_capability": target_capability,
        "code": code,
        "title": title,
        "requested_by": agent.handle,
        "requested_at": int(time.time()),
    }
    future: asyncio.Future[dict[str, Any]] = asyncio.get_running_loop().create_future()
    pending_turns[turn_id] = {
        "future": future,
        "last_progress": time.monotonic(),
        "target_handle": target_handle,
    }
    await agent.post_room_message(
        room_id,
        json.dumps(payload),
        content_type="application/json",
        trace_id=turn_id,
    )
    say(agent.handle, f"{CYAN}→{RESET} @{target_handle} [{target_capability}]")
    try:
        while True:
            pending = pending_turns[turn_id]
            idle_for = time.monotonic() - float(pending["last_progress"])
            remaining = TURN_TIMEOUT - idle_for
            if remaining <= 0:
                raise asyncio.TimeoutError
            try:
                reply = await asyncio.wait_for(asyncio.shield(future), timeout=min(remaining, 5.0))
                break
            except asyncio.TimeoutError:
                continue
        if reply.get("status") == "ok":
            say(agent.handle, f"{GREEN}←{RESET} @{target_handle} ({reply.get('elapsed_sec', 0):.1f}s)")
        else:
            say(agent.handle, f"{YELLOW}!{RESET} @{target_handle}: {reply.get('error', 'unknown')}")
        return reply
    except asyncio.TimeoutError:
        say(agent.handle, f"{RED}timeout{RESET} waiting for @{target_handle}")
        return {
            "kind": "review_reply",
            "turn_id": turn_id,
            "author": target_handle,
            "status": "timeout",
            "capability": target_capability,
            "error": f"Timed out after {TURN_TIMEOUT:.0f}s",
            "elapsed_sec": TURN_TIMEOUT,
        }
    finally:
        pending_turns.pop(turn_id, None)


def build_report(
    *,
    title: str,
    code: str,
    room_id: str,
    reviews: list[dict[str, Any]],
) -> str:
    lines = [
        f"# Parallel Code Review — {title}",
        "",
        f"- Room: `{room_id}`",
        f"- Reviewers: {len(reviews)}",
        "",
        "## Code Under Review",
        "```python",
        code.rstrip(),
        "```",
        "",
    ]
    for review in reviews:
        role = review.get("role", "unknown")
        elapsed = review.get("elapsed_sec", 0)
        lines.append(f"## {role.title()} Review ({elapsed:.1f}s)")
        if review.get("status") != "ok":
            lines.append(f"**Error:** {review.get('error', 'unknown')}")
            lines.append("")
            continue
        result = review.get("review", {})
        lines.append(f"**Summary:** {result.get('summary', 'N/A')}")
        lines.append(f"**Severity:** {result.get('severity', 'N/A')}")
        lines.append("")
        findings = result.get("findings", [])
        if findings:
            lines.append("**Findings:**")
            for f in findings:
                if isinstance(f, dict):
                    issue = f.get("issue", f.get("suggestion", str(f)))
                    lines.append(f"- {issue}")
                else:
                    lines.append(f"- {f}")
            lines.append("")
        rec = result.get("recommendation", "")
        if rec:
            lines.append(f"**Recommendation:** {rec}")
            lines.append("")
    return "\n".join(lines).rstrip() + "\n"


async def run_review_flow(code: str, title: str) -> dict[str, Any]:
    discovered = []
    for cap in REQUIRED_CAPABILITIES:
        matches = await agent.find_handles(capabilities=[cap], limit=1)
        if not matches:
            raise RuntimeError(f"no handle found for capability {cap}")
        match = matches[0]
        discovered.append({
            "capability": cap,
            "handle": match.handle,
            "score": match.score,
        })
        say(agent.handle, f"  {DIM}{match.handle}{RESET} for {cap} (score {match.score})")

    members = [d["handle"] for d in discovered]
    trimmed_title = title if len(title) <= 60 else title[:57] + "..."
    room_id = await agent.create_room(f"review: {trimmed_title}", members=members)

    await agent.post_room_message(
        room_id,
        json.dumps({
            "kind": "task_opened",
            "title": title,
            "code": code,
            "author": agent.handle,
            "opened_at": int(time.time()),
        }),
        content_type="application/json",
    )

    # Fan out all 3 reviews in parallel — this is the key feature.
    say(agent.handle, f"{BOLD}fanning out 3 reviews in parallel{RESET}")
    tasks = [
        request_review(
            room_id=room_id,
            target_handle=d["handle"],
            target_capability=d["capability"],
            code=code,
            title=title,
        )
        for d in discovered
    ]
    results = await asyncio.gather(*tasks, return_exceptions=True)

    reviews: list[dict[str, Any]] = []
    for i, result in enumerate(results):
        if isinstance(result, Exception):
            reviews.append({
                "role": discovered[i]["capability"].split(".")[-1],
                "status": "error",
                "error": str(result),
                "elapsed_sec": 0,
            })
        else:
            reviews.append(result)

    await agent.post_room_message(
        room_id,
        json.dumps({
            "kind": "final_summary",
            "author": agent.handle,
            "reviews": [
                {"role": r.get("role", "?"), "status": r.get("status", "?"), "elapsed_sec": r.get("elapsed_sec", 0)}
                for r in reviews
            ],
            "recorded_at": int(time.time()),
        }),
        content_type="application/json",
    )

    OUTPUT_DIR.mkdir(parents=True, exist_ok=True)
    stamp = time.strftime("%Y%m%d-%H%M%S")
    output_file = OUTPUT_DIR / f"review-{stamp}.md"
    report = build_report(title=title, code=code, room_id=room_id, reviews=reviews)
    write_text_atomic(output_file, report)

    try:
        await agent.close_room(room_id)
    except Exception:
        pass

    ok_count = sum(1 for r in reviews if r.get("status") == "ok")
    return {
        "status": "complete",
        "room_id": room_id,
        "reviews_ok": ok_count,
        "reviews_total": len(reviews),
        "output_file": str(output_file),
    }


@agent.on_message
async def handle(msg: Message | RoomEvent) -> None:
    if isinstance(msg, RoomEvent):
        await handle_room_event(msg)
        return
    if msg.message_type != "session_open":
        return

    data = parse_json(msg.payload) or {}
    args = data.get("arguments", data)
    if isinstance(args, str):
        args = parse_json(args) or {"scenario": args}

    scenario = str(args.get("scenario", "")).strip()
    code = str(args.get("code", "")).strip()
    title = str(args.get("title", "")).strip()

    if scenario and scenario in CODE_SAMPLES:
        sample = CODE_SAMPLES[scenario]
        code = sample["code"]
        title = title or sample["title"]
    if not code:
        await agent.resolve(
            msg.session,
            json.dumps({"error": "Missing code or scenario"}),
            content_type="application/json",
        )
        return

    title = title or "code review"
    say(agent.handle, f"reviewing: {BOLD}{short(title, 120)}{RESET}")
    started = time.monotonic()
    try:
        result = await run_review_flow(code, title)
        result["total_time"] = round(time.monotonic() - started, 1)
        say(agent.handle, f"{GREEN}done{RESET} in {result['total_time']:.1f}s — {result['output_file']}")
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
        say(agent.handle, f"{GREEN}ready{RESET} — command: review")
        await agent.run_forever()


if __name__ == "__main__":
    asyncio.run(main())
