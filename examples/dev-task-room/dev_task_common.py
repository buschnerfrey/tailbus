#!/usr/bin/env python3
"""Shared helpers for the dev-task-room example."""

from __future__ import annotations

import asyncio
import json
import os
import shutil
import sys
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any, Callable

sys_path = os.path.join(os.path.dirname(__file__), "../../sdk/python/src")
if sys_path not in sys.path:
    sys.path.insert(0, sys_path)

from tailbus import AsyncAgent, BridgeError, Manifest, RoomEvent

ROOM_REPLAY_RETRIES = int(os.environ.get("DEV_TASK_ROOM_REPLAY_RETRIES", "12"))
ROOM_REPLAY_DELAY = float(os.environ.get("DEV_TASK_ROOM_REPLAY_DELAY", "0.5"))
LLM_BASE_URL = os.environ.get("LLM_BASE_URL", "http://localhost:1234/v1")
LLM_MODEL = os.environ.get("LLM_MODEL", "")
CODEX_TIMEOUT = int(os.environ.get("CODEX_TIMEOUT", "600"))
CODEX_MODEL = os.environ.get("CODEX_MODEL", "gpt-5.1-codex-mini")
MAX_SNAPSHOT_CHARS = int(os.environ.get("DEV_TASK_ROOM_SNAPSHOT_CHARS", "24000"))
TURN_PROGRESS_INTERVAL = float(os.environ.get("DEV_TASK_ROOM_PROGRESS_INTERVAL", "4"))
WORKSPACE_ROOT = Path(os.environ.get("WORKSPACE_ROOT", Path(__file__).resolve().parent / "workspace"))
WORKSPACE_TEMPLATE = Path(
    os.environ.get("WORKSPACE_TEMPLATE", Path(__file__).resolve().parent / "workspace-template")
)

DIM = "\033[2m"
BOLD = "\033[1m"
GREEN = "\033[32m"
YELLOW = "\033[33m"
RED = "\033[31m"
CYAN = "\033[36m"
RESET = "\033[0m"

SCENARIOS: dict[str, str] = {
    "snake-clone": "Build a simple Snake clone in Python using only the standard library. Include a playable interface, food spawning, score tracking, wall and self collision, restart handling, and unit tests for the core game logic.",
    "parser-edge-case": "Extend the CSV parser so quoted commas are handled correctly and add coverage for the edge case.",
    "todo-filter": "Make the todo status filter case-insensitive and ensure the tests cover mixed-case input.",
}

WORKSPACE_FILES: tuple[str, ...] = (
    "README.md",
    "client.py",
    "parser.py",
    "snake.py",
    "snake_game.py",
    "todo.py",
    "tests/test_client.py",
    "tests/test_parser.py",
    "tests/test_snake_game.py",
    "tests/test_todo.py",
)


def say(tag: str, msg: str) -> None:
    print(f"  {DIM}{tag}{RESET}  {msg}", flush=True)


def is_room_closed_error(exc: Exception) -> bool:
    return isinstance(exc, BridgeError) and "room" in str(exc).lower() and "is closed" in str(exc).lower()


def parse_json(payload: str) -> dict[str, Any] | None:
    try:
        value = json.loads(payload)
    except json.JSONDecodeError:
        return None
    return value if isinstance(value, dict) else None


def parse_command_payload(payload: str) -> dict[str, Any]:
    data = parse_json(payload)
    if not data:
        return {"task": payload}
    args = data.get("arguments", data)
    if isinstance(args, str):
        parsed = parse_json(args)
        return parsed or {"task": args}
    if isinstance(args, dict):
        return args
    return data


def ensure_workspace_exists() -> None:
    if WORKSPACE_ROOT.exists():
        return
    reset_workspace()


def reset_workspace() -> list[str]:
    if WORKSPACE_ROOT.exists():
        shutil.rmtree(WORKSPACE_ROOT)
    shutil.copytree(WORKSPACE_TEMPLATE, WORKSPACE_ROOT)
    return list(iter_workspace_files())


def iter_workspace_files() -> tuple[str, ...]:
    files: list[str] = []
    if not WORKSPACE_ROOT.exists():
        return ()
    for path in sorted(WORKSPACE_ROOT.rglob("*")):
        if path.is_file():
            files.append(path.relative_to(WORKSPACE_ROOT).as_posix())
    return tuple(files)


def read_workspace_snapshot() -> str:
    ensure_workspace_exists()
    chunks: list[str] = []
    for rel_path in select_workspace_snapshot_files(iter_workspace_files()):
        path = WORKSPACE_ROOT / rel_path
        if not path.exists():
            continue
        try:
            content = path.read_text(encoding="utf-8")
        except UnicodeDecodeError:
            continue
        chunks.append(f"## {rel_path}\n{content.rstrip()}\n")
    snapshot = "\n".join(chunks).strip()
    if len(snapshot) > MAX_SNAPSHOT_CHARS:
        return snapshot[: MAX_SNAPSHOT_CHARS - 32] + "\n\n[workspace snapshot truncated]"
    return snapshot


def select_workspace_snapshot_files(files: tuple[str, ...]) -> tuple[str, ...]:
    selected: list[str] = []
    seen: set[str] = set()

    for rel_path in WORKSPACE_FILES:
        if rel_path in files and rel_path not in seen:
            selected.append(rel_path)
            seen.add(rel_path)

    for rel_path in files:
        if rel_path in seen:
            continue
        parts = Path(rel_path).parts
        if any(part.startswith(".") or part == "__pycache__" for part in parts):
            continue
        suffix = Path(rel_path).suffix
        if suffix not in {".py", ".md", ".txt"}:
            continue
        selected.append(rel_path)
        seen.add(rel_path)

    return tuple(selected)


def room_task_from_events(events: list[RoomEvent]) -> dict[str, Any]:
    for event in events:
        if event.event_type != "message_posted":
            continue
        payload = parse_json(event.payload)
        if payload and payload.get("kind") == "task_opened":
            return payload
    return {}


def llm_call(system: str, user: str, *, temperature: float = 0.2, max_tokens: int = 4096) -> str:
    body: dict[str, Any] = {
        "messages": [
            {"role": "system", "content": system},
            {"role": "user", "content": user},
        ],
        "temperature": temperature,
        "max_tokens": max_tokens,
    }
    if LLM_MODEL:
        body["model"] = LLM_MODEL

    data = json.dumps(body).encode("utf-8")
    req = urllib.request.Request(
        f"{LLM_BASE_URL}/chat/completions",
        data=data,
        headers={"Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(req, timeout=180) as resp:
            result = json.loads(resp.read())
            content = str(result["choices"][0]["message"]["content"])
            if "<think>" in content:
                parts = content.split("</think>")
                content = parts[-1].strip() if len(parts) > 1 else content
            return content
    except urllib.error.URLError as exc:
        return f"[LLM error] Could not reach LM Studio at {LLM_BASE_URL}: {exc.reason}"
    except Exception as exc:  # pragma: no cover - defensive example code
        return f"[LLM error] {exc}"


def llm_stream_call(
    system: str,
    user: str,
    *,
    on_chunk: Callable[[str], None] | None = None,
    temperature: float = 0.2,
    max_tokens: int = 4096,
) -> str:
    body: dict[str, Any] = {
        "messages": [
            {"role": "system", "content": system},
            {"role": "user", "content": user},
        ],
        "temperature": temperature,
        "max_tokens": max_tokens,
        "stream": True,
    }
    if LLM_MODEL:
        body["model"] = LLM_MODEL

    data = json.dumps(body).encode("utf-8")
    req = urllib.request.Request(
        f"{LLM_BASE_URL}/chat/completions",
        data=data,
        headers={"Content-Type": "application/json"},
    )
    chunks: list[str] = []
    try:
        with urllib.request.urlopen(req, timeout=180) as resp:
            for raw_line in resp:
                line = raw_line.decode("utf-8", errors="ignore").strip()
                if not line or not line.startswith("data:"):
                    continue
                payload = line[5:].strip()
                if payload == "[DONE]":
                    break
                try:
                    event = json.loads(payload)
                except json.JSONDecodeError:
                    continue
                choices = event.get("choices", [])
                if not choices:
                    continue
                choice = choices[0]
                delta = choice.get("delta", {})
                text = ""
                if isinstance(delta, dict):
                    text = str(delta.get("content", "") or "")
                if not text and "message" in choice:
                    message = choice.get("message", {})
                    if isinstance(message, dict):
                        text = str(message.get("content", "") or "")
                if not text:
                    continue
                chunks.append(text)
                if on_chunk is not None:
                    on_chunk("".join(chunks))
        return "".join(chunks)
    except urllib.error.URLError as exc:
        return f"[LLM error] Could not reach LM Studio at {LLM_BASE_URL}: {exc.reason}"
    except Exception as exc:  # pragma: no cover - defensive example code
        return f"[LLM error] {exc}"


def strip_code_fences(text: str) -> str:
    value = text.strip()
    if value.startswith("```"):
        first_newline = value.find("\n")
        if first_newline == -1:
            return ""
        value = value[first_newline + 1 :]
        if value.endswith("```"):
            value = value[:-3]
        value = value.strip()
    return value


def truncate_preserving_ends(text: str, limit_chars: int, marker: str = "\n...\n") -> str:
    if len(text) <= limit_chars:
        return text
    if limit_chars <= len(marker):
        return text[:limit_chars]
    remaining = limit_chars - len(marker)
    head = remaining // 2
    tail = remaining - head
    return text[:head] + marker + text[-tail:]


def parse_json_object(text: str) -> dict[str, Any] | None:
    value = strip_code_fences(text)
    try:
        parsed = json.loads(value)
        return parsed if isinstance(parsed, dict) else None
    except json.JSONDecodeError:
        start = value.find("{")
        end = value.rfind("}")
        if start == -1 or end <= start:
            return None
        try:
            parsed = json.loads(value[start : end + 1])
            return parsed if isinstance(parsed, dict) else None
        except json.JSONDecodeError:
            return None


async def run_codex_json(prompt: str, output_file: str, timeout: int = CODEX_TIMEOUT) -> str:
    try:
        cmd = ["codex", "exec", prompt]
        if CODEX_MODEL:
            cmd.extend(["-m", CODEX_MODEL])
        cmd.extend(["-o", output_file, "--ephemeral"])
        proc = await asyncio.create_subprocess_exec(
            *cmd,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )
        stdout, stderr = await asyncio.wait_for(proc.communicate(), timeout=timeout)
        if proc.returncode != 0:
            err = stderr.decode().strip() if stderr else "unknown error"
            if os.path.exists(output_file):
                content = Path(output_file).read_text(encoding="utf-8").strip()
                if content:
                    return content
            return f"[codex error] exit code {proc.returncode}: {err}"
        if os.path.exists(output_file):
            return Path(output_file).read_text(encoding="utf-8")
        return stdout.decode().strip() or "[codex error] no output produced"
    except asyncio.TimeoutError:
        try:
            proc.kill()
        except (NameError, ProcessLookupError):
            pass
        return f"[codex error] timed out after {timeout}s"
    except FileNotFoundError:
        return "[codex error] 'codex' CLI not found"
    except Exception as exc:  # pragma: no cover - defensive example code
        return f"[codex error] {exc}"
    finally:
        if os.path.exists(output_file):
            try:
                os.unlink(output_file)
            except OSError:
                pass


async def replay_room_with_retry(agent: AsyncAgent, room_id: str) -> list[RoomEvent]:
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


def render_llm_transcript(events: list[RoomEvent], *, limit_chars: int = 16000) -> str:
    parts: list[str] = []
    for event in events:
        if event.event_type != "message_posted":
            continue
        payload = parse_json(event.payload)
        if not payload:
            continue
        kind = str(payload.get("kind", "unknown"))
        if kind == "task_opened":
            parts.append(
                "[task_opened]\n"
                f"title={payload.get('title', '')}\n"
                f"task={payload.get('task', '')}"
            )
        elif kind == "collaborator_discovered":
            parts.append(
                "[collaborator_discovered]\n"
                f"target_handle={payload.get('target_handle', '')}\n"
                f"target_capability={payload.get('target_capability', '')}\n"
                f"reasons={','.join(str(item) for item in payload.get('match_reasons', []))}"
            )
        elif kind.endswith("_request"):
            parts.append(
                f"[{kind}]\n"
                f"target_handle={payload.get('target_handle', '')}\n"
                f"target_capability={payload.get('target_capability', '')}\n"
                f"instruction={payload.get('instruction', '')}"
            )
        elif kind.endswith("_reply") or kind in ("apply_result", "test_result", "final_summary"):
            parts.append(f"[{kind}]\n{json.dumps(payload, indent=2, sort_keys=True)}")
    text = "\n\n".join(parts)
    if len(text) > limit_chars:
        return text[-limit_chars:]
    return text


def render_markdown_transcript(events: list[RoomEvent]) -> str:
    lines = ["## Room Transcript", ""]
    for event in events:
        prefix = f"- seq {event.room_seq}: "
        if event.event_type != "message_posted":
            lines.append(prefix + f"{event.event_type} by `{event.sender_handle}`")
            continue
        payload = parse_json(event.payload)
        if not payload:
            lines.append(prefix + f"`{event.sender_handle}` posted raw payload")
            continue
        kind = str(payload.get("kind", "unknown"))
        if kind == "task_opened":
            lines.append(prefix + f"task opened `{payload.get('title', '')}`")
            lines.append(f"  task: {payload.get('task', '')}")
        elif kind == "collaborator_discovered":
            lines.append(
                prefix
                + f"discovered `{payload.get('target_handle', '?')}` for `{payload.get('target_capability', '?')}`"
            )
        elif kind.endswith("_request"):
            lines.append(prefix + f"{kind} -> `{payload.get('target_handle', payload.get('target_capability', '?'))}`")
            lines.append(f"  instruction: {payload.get('instruction', '')}")
        elif kind == "implement_reply":
            lines.append(prefix + f"implementer reply status={payload.get('status', 'unknown')}")
            lines.append(f"  summary: {payload.get('summary', '')}")
        elif kind == "review_reply":
            lines.append(prefix + f"review reply status={payload.get('status', 'unknown')}")
            if payload.get("findings"):
                for finding in payload["findings"]:
                    lines.append(f"  finding: {finding}")
        elif kind == "apply_result":
            lines.append(prefix + f"apply result status={payload.get('status', 'unknown')}")
            for path in payload.get("changed_paths", []):
                lines.append(f"  changed: {path}")
        elif kind == "test_result":
            lines.append(prefix + f"test result status={payload.get('status', 'unknown')}")
            if payload.get("summary"):
                lines.append(f"  summary: {payload['summary']}")
        elif kind == "final_summary":
            lines.append(prefix + "final summary posted")
        else:
            lines.append(prefix + kind)
    return "\n".join(lines)


def format_match_line(match: dict[str, Any]) -> str:
    reasons = ", ".join(str(item) for item in match.get("reasons", [])) or "none"
    return (
        f"- `{match['handle']}` for `{match['capability']}`"
        f" (score {match['score']}; reasons: {reasons})"
    )


async def progress_pinger(
    agent: AsyncAgent,
    *,
    room_id: str,
    turn_id: str,
    round_no: int,
    target_handle: str,
    target_capability: str,
    summary: str | Callable[[], str],
    interval: float = TURN_PROGRESS_INTERVAL,
) -> None:
    elapsed = 0.0
    while True:
        await asyncio.sleep(interval)
        elapsed += interval
        payload = {
            "kind": "turn_progress",
            "turn_id": turn_id,
            "round": round_no,
            "author": agent.handle,
            "target_handle": target_handle,
            "target_capability": target_capability,
            "status": "working",
            "summary": summary() if callable(summary) else summary,
            "elapsed_sec": round(elapsed, 1),
        }
        try:
            await agent.post_room_message(
                room_id,
                json.dumps(payload),
                content_type="application/json",
                trace_id=turn_id,
            )
        except Exception as exc:
            if is_room_closed_error(exc):
                return
            raise


def build_output_markdown(
    *,
    task_info: dict[str, Any],
    room_id: str,
    discovered: list[dict[str, Any]],
    implement_reply: dict[str, Any],
    review_reply: dict[str, Any],
    apply_result: dict[str, Any],
    test_result: dict[str, Any],
    events: list[RoomEvent],
) -> str:
    lines = [
        f"# Dev Task Room — {task_info.get('title', '')}",
        "",
        f"- Room: `{room_id}`",
        f"- Task: {task_info.get('task', '')}",
        "",
        "## Discovered Collaborators",
    ]
    lines.extend(format_match_line(item) for item in discovered)
    lines.extend(
        [
            "",
            "## Implementer Summary",
            implement_reply.get("summary", "No summary available."),
            "",
            "## Review Outcome",
            review_reply.get("summary", "No review summary available."),
        ]
    )
    findings = review_reply.get("findings", [])
    if findings:
        lines.append("")
        lines.append("Findings:")
        lines.extend(f"- {item}" for item in findings)
    lines.extend(
        [
            "",
            "## Apply Result",
            f"- Status: {apply_result.get('status', 'unknown')}",
        ]
    )
    for path in apply_result.get("changed_paths", []):
        lines.append(f"- Changed: `{path}`")
    lines.extend(
        [
            "",
            "## Test Result",
            f"- Status: {test_result.get('status', 'unknown')}",
            f"- Summary: {test_result.get('summary', '')}",
            "",
            render_markdown_transcript(events),
            "",
        ]
    )
    return "\n".join(lines).rstrip() + "\n"


def tmp_json_path(prefix: str, turn_id: str) -> str:
    return str(Path(tempfile.gettempdir()) / f"{prefix}_{turn_id[:8]}.json")
