#!/usr/bin/env python3
"""Shared helpers for the dev-task-room example."""

from __future__ import annotations

import asyncio
import json
import os
import re
import shutil
import sys
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any, Callable, Iterable

sys_path = os.path.join(os.path.dirname(__file__), "../../sdk/python/src")
if sys_path not in sys.path:
    sys.path.insert(0, sys_path)

from tailbus import AsyncAgent, BridgeError, Manifest, RoomEvent

ROOM_REPLAY_RETRIES = int(os.environ.get("DEV_TASK_ROOM_REPLAY_RETRIES", "12"))
ROOM_REPLAY_DELAY = float(os.environ.get("DEV_TASK_ROOM_REPLAY_DELAY", "0.5"))
LLM_BASE_URL = os.environ.get("LLM_BASE_URL", "http://localhost:1234/v1")
LLM_MODEL = os.environ.get("LLM_MODEL", "")
LLM_REQUEST_TIMEOUT = float(os.environ.get("LLM_REQUEST_TIMEOUT", "180"))
REVIEW_TIMEOUT = float(os.environ.get("REVIEW_TIMEOUT", "120"))
REVIEW_MAX_INPUT_TOKENS = int(os.environ.get("REVIEW_MAX_INPUT_TOKENS", "2500"))
REVIEW_CHARS_PER_TOKEN = float(os.environ.get("REVIEW_CHARS_PER_TOKEN", "3.0"))
REVIEW_MIN_SECTION_LINES = int(os.environ.get("REVIEW_MIN_SECTION_LINES", "80"))
REVIEW_SECTION_OVERLAP_LINES = int(os.environ.get("REVIEW_SECTION_OVERLAP_LINES", "20"))
CODEX_TIMEOUT = int(os.environ.get("CODEX_TIMEOUT", "600"))
CODEX_MODEL = os.environ.get("CODEX_MODEL", "gpt-5.1-codex-mini")
MAX_SNAPSHOT_CHARS = int(os.environ.get("DEV_TASK_ROOM_SNAPSHOT_CHARS", "24000"))
ROOM_SNAPSHOT_PREVIEW_CHARS = int(os.environ.get("DEV_TASK_ROOM_SNAPSHOT_PREVIEW_CHARS", "8000"))
TURN_PROGRESS_INTERVAL = float(os.environ.get("DEV_TASK_ROOM_PROGRESS_INTERVAL", "4"))
WORKSPACE_ROOT = Path(os.environ.get("WORKSPACE_ROOT", Path(__file__).resolve().parent / "workspace"))
WORKSPACE_TEMPLATE = Path(
    os.environ.get("WORKSPACE_TEMPLATE", Path(__file__).resolve().parent / "workspace-template")
)
OUTPUT_DIR = Path(os.environ.get("OUTPUT_DIR", Path(__file__).resolve().parent / "output"))
ARTIFACTS_DIR = OUTPUT_DIR / "artifacts"
MAX_AUTONOMOUS_REVISIONS = int(os.environ.get("DEV_TASK_ROOM_MAX_REVISIONS", "2"))
MAX_REPAIR_ATTEMPTS = int(os.environ.get("DEV_TASK_ROOM_MAX_REPAIRS", "1"))
DEBUG_ENABLED = os.environ.get("DEV_TASK_ROOM_DEBUG", "").strip().lower() in {"1", "true", "yes", "on"}

DIM = "\033[2m"
BOLD = "\033[1m"
GREEN = "\033[32m"
YELLOW = "\033[33m"
RED = "\033[31m"
CYAN = "\033[36m"
RESET = "\033[0m"

SCENARIOS: dict[str, str] = {
    "focus-timer": "Build a small self-contained Python focus timer app in about 100 lines using only the standard library. Make it runnable from the terminal, support configurable work and break durations, show a live countdown, and include a couple of unit tests for the core timer-state logic.",
    "snake-clone": "Build a simple Snake clone in Python using only the standard library. Include a playable interface, food spawning, score tracking, wall and self collision, restart handling, and unit tests for the core game logic.",
    "parser-edge-case": "Extend the CSV parser so quoted commas are handled correctly and add coverage for the edge case.",
    "todo-filter": "Make the todo status filter case-insensitive and ensure the tests cover mixed-case input.",
    "client-timeout": "Add a timeout parameter to the HTTP client, thread it through the client API, and update the tests to cover both the default behavior and an explicit timeout override.",
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


def debug_say(tag: str, msg: str) -> None:
    if DEBUG_ENABLED:
        say(tag, msg)


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
    for rel_path, content in read_workspace_file_map().items():
        chunks.append(f"## {rel_path}\n{content.rstrip()}\n")
    snapshot = "\n".join(chunks).strip()
    if len(snapshot) > MAX_SNAPSHOT_CHARS:
        return snapshot[: MAX_SNAPSHOT_CHARS - 32] + "\n\n[workspace snapshot truncated]"
    return snapshot


def read_workspace_file_map() -> dict[str, str]:
    ensure_workspace_exists()
    file_map: dict[str, str] = {}
    for rel_path in select_workspace_snapshot_files(iter_workspace_files()):
        path = WORKSPACE_ROOT / rel_path
        if not path.exists():
            continue
        try:
            file_map[rel_path] = path.read_text(encoding="utf-8")
        except UnicodeDecodeError:
            continue
    return file_map


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


def _safe_artifact_token(value: str) -> str:
    cleaned = re.sub(r"[^A-Za-z0-9._-]+", "-", value.strip())
    return cleaned.strip(".-") or "artifact"


def write_text_atomic(path: Path, content: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp_path = path.with_suffix(path.suffix + ".tmp")
    tmp_path.write_text(content, encoding="utf-8")
    tmp_path.replace(path)


def _artifact_base_dir(task_id: str) -> Path:
    return ARTIFACTS_DIR / _safe_artifact_token(task_id or "room")


def write_json_artifact(task_id: str, group: str, name: str, payload: dict[str, Any]) -> str:
    path = _artifact_base_dir(task_id) / _safe_artifact_token(group) / f"{_safe_artifact_token(name)}.json"
    write_text_atomic(path, json.dumps(payload, indent=2, sort_keys=True) + "\n")
    return str(path)


def read_json_artifact(path_str: str) -> dict[str, Any]:
    path = Path(path_str).resolve()
    artifacts_root = ARTIFACTS_DIR.resolve()
    if path != artifacts_root and artifacts_root not in path.parents:
        raise ValueError(f"artifact path escapes artifact root: {path_str}")
    payload = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(payload, dict):
        raise ValueError(f"artifact is not a JSON object: {path_str}")
    return payload


def summarize_change_set(change_set: dict[str, Any]) -> dict[str, Any]:
    files = list(change_set.get("files", []))
    deleted_paths = [str(path).strip() for path in change_set.get("deleted_paths", []) if str(path).strip()]
    file_entries: list[dict[str, Any]] = []
    total_chars = 0
    for item in files:
        path = str(item.get("path", "")).strip()
        content = str(item.get("content", ""))
        if not path:
            continue
        total_chars += len(content)
        file_entries.append(
            {
                "path": path,
                "chars": len(content),
                "lines": len(content.splitlines()),
            }
        )
    return {
        "summary": str(change_set.get("summary", "")),
        "assumptions": [str(item) for item in change_set.get("assumptions", [])],
        "test_notes": [str(item) for item in change_set.get("test_notes", [])],
        "files": file_entries,
        "deleted_paths": deleted_paths,
        "file_count": len(file_entries),
        "approx_total_chars": total_chars,
    }


def store_workspace_context(task_id: str, turn_id: str) -> dict[str, Any]:
    files = reset_workspace()
    workspace_file_map = read_workspace_file_map()
    snapshot = read_workspace_snapshot()
    artifact_path = write_json_artifact(
        task_id,
        "workspace",
        turn_id,
        {
            "workspace_files": files,
            "workspace_file_map": workspace_file_map,
            "workspace_snapshot": snapshot,
        },
    )
    return {
        "workspace_files": files,
        "workspace_snapshot": truncate_preserving_ends(snapshot, ROOM_SNAPSHOT_PREVIEW_CHARS),
        "workspace_artifact": artifact_path,
        "workspace_snapshot_chars": len(snapshot),
    }


def load_workspace_context(payload: dict[str, Any]) -> dict[str, Any]:
    context = dict(payload)
    artifact_path = str(payload.get("workspace_artifact", "")).strip()
    if artifact_path:
        artifact = read_json_artifact(artifact_path)
        context["workspace_files"] = list(artifact.get("workspace_files", context.get("workspace_files", [])))
        context["workspace_file_map"] = dict(artifact.get("workspace_file_map", {}))
        context["workspace_snapshot"] = str(artifact.get("workspace_snapshot", context.get("workspace_snapshot", "")))
    else:
        context["workspace_file_map"] = dict(payload.get("workspace_file_map", {}))
        context["workspace_snapshot"] = str(payload.get("workspace_snapshot", ""))
    return context


def store_change_set(task_id: str, turn_id: str, change_set: dict[str, Any]) -> dict[str, Any]:
    artifact_path = write_json_artifact(task_id, "changesets", turn_id, change_set)
    summary = summarize_change_set(change_set)
    summary["change_set_artifact"] = artifact_path
    return summary


def load_change_set(payload: dict[str, Any]) -> dict[str, Any]:
    change_set = payload.get("change_set")
    if isinstance(change_set, dict):
        return dict(change_set)
    artifact_path = str(payload.get("change_set_artifact", "")).strip()
    if artifact_path:
        return read_json_artifact(artifact_path)
    return {}


def room_task_from_events(events: list[RoomEvent]) -> dict[str, Any]:
    for event in events:
        if event.event_type != "message_posted":
            continue
        payload = parse_json(event.payload)
        if payload and payload.get("kind") == "task_opened":
            return payload
    return {}


def room_messages(events: list[RoomEvent]) -> list[dict[str, Any]]:
    messages: list[dict[str, Any]] = []
    for event in events:
        if event.event_type != "message_posted" or event.content_type != "application/json":
            continue
        payload = parse_json(event.payload)
        if not payload:
            continue
        item = dict(payload)
        item["_room_seq"] = event.room_seq
        item["_sender_handle"] = event.sender_handle
        messages.append(item)
    return messages


def _messages_by_kind(messages: list[dict[str, Any]], kind: str) -> list[dict[str, Any]]:
    return [item for item in messages if str(item.get("kind", "")) == kind]


def build_room_state(events: list[RoomEvent]) -> dict[str, Any]:
    messages = room_messages(events)
    state: dict[str, Any] = {
        "messages": messages,
        "task": {},
        "discovered": [],
        "workspace_prepare": None,
        "plans": [],
        "delegations": [],
        "implementations": [],
        "test_plan_requests": [],
        "test_plans": [],
        "reviews": [],
        "apply_requests": [],
        "apply_results": [],
        "final_outcome": None,
    }
    for payload in messages:
        kind = str(payload.get("kind", ""))
        if kind == "task_opened":
            state["task"] = payload
        elif kind == "collaborator_discovered":
            state["discovered"].append(payload)
        elif kind == "workspace_prepare_reply":
            state["workspace_prepare"] = payload
        elif kind == "plan_proposed":
            state["plans"].append(payload)
        elif kind == "delegation_request":
            state["delegations"].append(payload)
        elif kind == "implement_reply":
            state["implementations"].append(payload)
        elif kind == "test_plan_request":
            state["test_plan_requests"].append(payload)
        elif kind == "test_plan_reply":
            state["test_plans"].append(payload)
        elif kind == "review_reply":
            state["reviews"].append(payload)
        elif kind == "apply_request":
            state["apply_requests"].append(payload)
        elif kind == "apply_result":
            state["apply_results"].append(payload)
        elif kind == "final_outcome":
            state["final_outcome"] = payload
    return state


def latest_successful_implementation(state: dict[str, Any]) -> dict[str, Any] | None:
    successful = [item for item in state.get("implementations", []) if item.get("status") == "ok"]
    return successful[-1] if successful else None


def implementation_for_iteration(state: dict[str, Any], iteration: int) -> dict[str, Any] | None:
    for item in reversed(state.get("implementations", [])):
        if int(item.get("iteration", 0) or 0) == iteration:
            return item
    return None


def review_for_iteration(state: dict[str, Any], iteration: int) -> dict[str, Any] | None:
    for item in reversed(state.get("reviews", [])):
        if int(item.get("iteration", 0) or 0) == iteration:
            return item
    return None


def test_plan_for_iteration(state: dict[str, Any], iteration: int) -> dict[str, Any] | None:
    for item in reversed(state.get("test_plans", [])):
        if int(item.get("iteration", 0) or 0) == iteration:
            return item
    return None


def apply_request_for_iteration(state: dict[str, Any], iteration: int) -> dict[str, Any] | None:
    for item in reversed(state.get("apply_requests", [])):
        if int(item.get("iteration", 0) or 0) == iteration:
            return item
    return None


def apply_result_for_iteration(state: dict[str, Any], iteration: int) -> dict[str, Any] | None:
    for item in reversed(state.get("apply_results", [])):
        if int(item.get("iteration", 0) or 0) == iteration:
            return item
    return None


def implementation_count(state: dict[str, Any], *, reason: str | None = None) -> int:
    items = state.get("implementations", [])
    if reason is None:
        return len(items)
    return sum(1 for item in items if str(item.get("reason", "")) == reason)


def current_iteration(state: dict[str, Any]) -> int:
    latest = latest_successful_implementation(state)
    return int(latest.get("iteration", 0) or 0) if latest else 0


def next_iteration(state: dict[str, Any]) -> int:
    return current_iteration(state) + 1


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
        with urllib.request.urlopen(req, timeout=LLM_REQUEST_TIMEOUT) as resp:
            result = json.loads(resp.read())
            content = str(result["choices"][0]["message"]["content"])
            if "<think>" in content:
                parts = content.split("</think>")
                content = parts[-1].strip() if len(parts) > 1 else content
            return content
    except urllib.error.HTTPError as exc:
        return format_llm_http_error(exc)
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
    try:
        with urllib.request.urlopen(req, timeout=LLM_REQUEST_TIMEOUT) as resp:
            return collect_streamed_llm_text(resp, on_chunk=on_chunk)
    except urllib.error.HTTPError as exc:
        return format_llm_http_error(exc)
    except urllib.error.URLError as exc:
        return f"[LLM error] Could not reach LM Studio at {LLM_BASE_URL}: {exc.reason}"
    except Exception as exc:  # pragma: no cover - defensive example code
        return f"[LLM error] {exc}"


def format_llm_http_error(exc: urllib.error.HTTPError) -> str:
    body = ""
    try:
        raw = exc.read()
    except Exception:
        raw = b""
    if raw:
        text = raw.decode("utf-8", errors="ignore").strip()
        if text:
            try:
                parsed = json.loads(text)
            except json.JSONDecodeError:
                body = text[:800]
            else:
                if isinstance(parsed, dict):
                    message = parsed.get("error") or parsed.get("message") or parsed
                    body = json.dumps(message, ensure_ascii=True)[:800]
                else:
                    body = json.dumps(parsed, ensure_ascii=True)[:800]
    detail = f" status={exc.code}"
    if body:
        detail += f" body={body}"
    return f"[LLM error] LM Studio request failed at {LLM_BASE_URL}:{detail}"


def collect_streamed_llm_text(
    lines: Iterable[bytes],
    *,
    on_chunk: Callable[[str], None] | None = None,
) -> str:
    chunks: list[str] = []
    for raw_line in lines:
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


def review_prompt_char_budget() -> int:
    return max(2400, int(REVIEW_MAX_INPUT_TOKENS * REVIEW_CHARS_PER_TOKEN))


def review_content_char_budget() -> int:
    return max(1200, review_prompt_char_budget() - 1800)


def is_context_limit_error(text: str) -> bool:
    value = text.lower()
    return "n_ctx" in value or "context length" in value or "prompt is too long" in value


def _line_slice(lines: list[str], start: int, end: int) -> str:
    return "".join(lines[start:end])


def _line_range(lines: list[str], start: int, end: int) -> tuple[int, int]:
    if not lines:
        return (0, 0)
    return (start + 1, min(end, len(lines)))


def _section_chars(old_content: str, new_content: str, path: str) -> int:
    return len(path) + len(old_content) + len(new_content) + 200


def _build_review_section(
    *,
    path: str,
    action: str,
    old_lines: list[str],
    new_lines: list[str],
    start: int,
    end: int,
) -> dict[str, Any]:
    old_content = _line_slice(old_lines, start, end)
    new_content = _line_slice(new_lines, start, end)
    old_start, old_end = _line_range(old_lines, start, end)
    new_start, new_end = _line_range(new_lines, start, end)
    return {
        "path": path,
        "action": action,
        "old_start_line": old_start,
        "old_end_line": old_end,
        "new_start_line": new_start,
        "new_end_line": new_end,
        "old_content": old_content,
        "new_content": new_content,
        "estimated_chars": _section_chars(old_content, new_content, path),
    }


def build_review_units(change_set: dict[str, Any], workspace_files: dict[str, str]) -> list[dict[str, Any]]:
    units: list[dict[str, Any]] = []
    budget = review_content_char_budget()

    for file_item in change_set.get("files", []):
        path = str(file_item.get("path", "")).strip()
        if not path:
            continue
        new_content = str(file_item.get("content", ""))
        old_content = workspace_files.get(path, "")
        action = "modify" if path in workspace_files else "create"
        units.extend(split_review_sections(path=path, action=action, old_content=old_content, new_content=new_content, max_chars=budget))

    for deleted_path in change_set.get("deleted_paths", []):
        path = str(deleted_path).strip()
        if not path:
            continue
        old_content = workspace_files.get(path, "")
        units.extend(split_review_sections(path=path, action="delete", old_content=old_content, new_content="", max_chars=budget))

    return units


def split_review_unit(unit: dict[str, Any]) -> list[dict[str, Any]]:
    sections = list(unit.get("sections", []))
    if len(sections) != 1:
        return [unit]
    section = dict(sections[0])
    return split_review_sections(
        path=str(section.get("path", "")),
        action=str(section.get("action", "modify")),
        old_content=str(section.get("old_content", "")),
        new_content=str(section.get("new_content", "")),
        max_chars=max(review_content_char_budget() // 2, 800),
        start_line=max(int(section.get("old_start_line", 1)), int(section.get("new_start_line", 1)), 1),
    )


def split_review_sections(
    *,
    path: str,
    action: str,
    old_content: str,
    new_content: str,
    max_chars: int,
    start_line: int = 1,
) -> list[dict[str, Any]]:
    old_lines = old_content.splitlines(keepends=True)
    new_lines = new_content.splitlines(keepends=True)
    total_lines = max(len(old_lines), len(new_lines), 1)
    full_section = _build_review_section(
        path=path,
        action=action,
        old_lines=old_lines,
        new_lines=new_lines,
        start=0,
        end=total_lines,
    )
    if full_section["estimated_chars"] <= max_chars:
        return [{"scope_paths": [path], "scope_ranges": [range_label(full_section)], "sections": [full_section]}]

    units: list[dict[str, Any]] = []
    min_lines = max(1, REVIEW_MIN_SECTION_LINES)
    overlap = max(0, REVIEW_SECTION_OVERLAP_LINES)
    index = 0

    while index < total_lines:
        end = min(total_lines, index + min_lines)
        candidate = _build_review_section(path=path, action=action, old_lines=old_lines, new_lines=new_lines, start=index, end=end)

        while end < total_lines:
            grown = _build_review_section(path=path, action=action, old_lines=old_lines, new_lines=new_lines, start=index, end=end + 1)
            if grown["estimated_chars"] > max_chars:
                break
            candidate = grown
            end += 1

        while candidate["estimated_chars"] > max_chars and end - index > 1:
            end -= 1
            candidate = _build_review_section(path=path, action=action, old_lines=old_lines, new_lines=new_lines, start=index, end=end)

        units.append({"scope_paths": [path], "scope_ranges": [range_label(candidate)], "sections": [candidate]})
        if end >= total_lines:
            break
        next_index = max(index + 1, end - overlap)
        if next_index <= index:
            next_index = end
        index = next_index

    return units


def range_label(section: dict[str, Any]) -> str:
    path = str(section.get("path", ""))
    old_start = int(section.get("old_start_line", 0))
    old_end = int(section.get("old_end_line", 0))
    new_start = int(section.get("new_start_line", 0))
    new_end = int(section.get("new_end_line", 0))
    return f"{path} old:{old_start}-{old_end} new:{new_start}-{new_end}"


async def run_codex_json(prompt: str, output_file: str, timeout: int = CODEX_TIMEOUT) -> str:
    try:
        cmd = ["codex", "exec", prompt]
        if CODEX_MODEL:
            cmd.extend(["-m", CODEX_MODEL])
        cmd.extend(["-o", output_file, "--ephemeral"])
        say("codex", f"spawning: codex exec -m {CODEX_MODEL or '(default)'} -o {output_file} (timeout={timeout}s)")
        proc = await asyncio.create_subprocess_exec(
            *cmd,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )
        say("codex", f"pid {proc.pid} started, waiting for output...")
        stdout, stderr = await asyncio.wait_for(proc.communicate(), timeout=timeout)
        if proc.returncode != 0:
            err = stderr.decode().strip() if stderr else "unknown error"
            say("codex", f"{RED}exit code {proc.returncode}{RESET}: {err[:200]}")
            if os.path.exists(output_file):
                content = Path(output_file).read_text(encoding="utf-8").strip()
                if content:
                    say("codex", f"output file exists ({len(content)} chars), using it despite error")
                    return content
            return f"[codex error] exit code {proc.returncode}: {err}"
        if os.path.exists(output_file):
            content = Path(output_file).read_text(encoding="utf-8")
            say("codex", f"done, output file has {len(content)} chars")
            return content
        out = stdout.decode().strip()
        say("codex", f"done, stdout has {len(out)} chars (no output file)")
        return out or "[codex error] no output produced"
    except asyncio.TimeoutError:
        say("codex", f"{RED}timed out{RESET} after {timeout}s")
        try:
            proc.kill()
        except (NameError, ProcessLookupError):
            pass
        return f"[codex error] timed out after {timeout}s"
    except FileNotFoundError:
        say("codex", f"{RED}codex CLI not found{RESET} in PATH")
        return "[codex error] 'codex' CLI not found"
    except Exception as exc:  # pragma: no cover - defensive example code
        say("codex", f"{RED}unexpected error{RESET}: {exc}")
        return f"[codex error] {exc}"
    finally:
        if os.path.exists(output_file):
            try:
                os.unlink(output_file)
            except OSError:
                pass


ROOM_REPLAY_TIMEOUT = float(os.environ.get("DEV_TASK_ROOM_REPLAY_TIMEOUT", "15"))
ROOM_REPLAY_CACHE_MAX = int(os.environ.get("DEV_TASK_ROOM_REPLAY_CACHE_MAX", "2048"))
_ROOM_REPLAY_CACHE: dict[str, list[RoomEvent]] = {}


async def replay_room_with_retry(agent: AsyncAgent, room_id: str) -> list[RoomEvent]:
    last_error: Exception | None = None
    for attempt in range(ROOM_REPLAY_RETRIES):
        try:
            cached = _ROOM_REPLAY_CACHE.get(room_id, [])
            since_seq = cached[-1].room_seq if cached else 0
            debug_say(
                agent.handle,
                f"replay_room attempt {attempt + 1}/{ROOM_REPLAY_RETRIES} for {room_id[:12]}... since_seq={since_seq}",
            )
            events = await asyncio.wait_for(
                agent.replay_room(room_id, since_seq=since_seq),
                timeout=ROOM_REPLAY_TIMEOUT,
            )
            if since_seq == 0:
                merged = list(events)
            elif not events:
                merged = cached
            else:
                merged = cached + list(events)
            if len(merged) > ROOM_REPLAY_CACHE_MAX:
                merged = merged[-ROOM_REPLAY_CACHE_MAX:]
            _ROOM_REPLAY_CACHE[room_id] = merged
            debug_say(
                agent.handle,
                f"replay_room returned {len(events)} delta event(s), cached={len(merged)}",
            )
            return list(merged)
        except asyncio.TimeoutError:
            last_error = TimeoutError(
                f"replay_room timed out after {ROOM_REPLAY_TIMEOUT}s on attempt {attempt + 1}"
            )
            debug_say(agent.handle, f"{YELLOW}replay_room timed out{RESET} after {ROOM_REPLAY_TIMEOUT}s (attempt {attempt + 1})")
        except Exception as exc:
            last_error = exc
            debug_say(agent.handle, f"{YELLOW}replay_room failed{RESET} (attempt {attempt + 1}): {exc}")
        if attempt < ROOM_REPLAY_RETRIES - 1:
            await asyncio.sleep(ROOM_REPLAY_DELAY)
    assert last_error is not None
    debug_say(agent.handle, f"{RED}replay_room gave up{RESET} after {ROOM_REPLAY_RETRIES} attempts: {last_error}")
    raise last_error


async def wait_for_room_message(
    agent: AsyncAgent,
    room_id: str,
    predicate: Callable[[dict[str, Any]], bool],
    *,
    timeout: float,
    poll_interval: float = 1.0,
) -> tuple[dict[str, Any], list[RoomEvent]]:
    deadline = time.monotonic() + timeout
    while True:
        events = await replay_room_with_retry(agent, room_id)
        for payload in reversed(room_messages(events)):
            if predicate(payload):
                return payload, events
        if time.monotonic() >= deadline:
            raise TimeoutError(f"timed out waiting for room message after {timeout:.0f}s")
        await asyncio.sleep(poll_interval)


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
        elif kind in ("plan_proposed", "delegation_request"):
            parts.append(f"[{kind}]\n{json.dumps(payload, indent=2, sort_keys=True)}")
        elif kind.endswith("_request"):
            parts.append(
                f"[{kind}]\n"
                f"target_handle={payload.get('target_handle', '')}\n"
                f"target_capability={payload.get('target_capability', '')}\n"
                f"instruction={payload.get('instruction', '')}"
            )
        elif kind.endswith("_reply") or kind in ("apply_result", "test_result", "final_summary", "final_outcome"):
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
        elif kind == "plan_proposed":
            lines.append(prefix + f"plan proposed by `{payload.get('author', event.sender_handle)}`")
            lines.append(f"  summary: {payload.get('summary', '')}")
        elif kind == "delegation_request":
            lines.append(
                prefix
                + f"`{payload.get('author', event.sender_handle)}` delegated to `{payload.get('target_handle', payload.get('target_capability', '?'))}`"
            )
            lines.append(f"  reason: {payload.get('summary', payload.get('instruction', ''))}")
        elif kind.endswith("_request"):
            lines.append(prefix + f"{kind} -> `{payload.get('target_handle', payload.get('target_capability', '?'))}`")
            lines.append(f"  instruction: {payload.get('instruction', '')}")
        elif kind == "implement_reply":
            lines.append(
                prefix
                + f"implementer reply iteration={payload.get('iteration', '?')} status={payload.get('status', 'unknown')}"
            )
            lines.append(f"  summary: {payload.get('summary', '')}")
        elif kind == "test_plan_reply":
            lines.append(prefix + f"test plan iteration={payload.get('iteration', '?')} decision={payload.get('decision', 'unknown')}")
            for item in payload.get("required_tests", []):
                lines.append(f"  required test: {item}")
        elif kind == "review_reply":
            lines.append(
                prefix
                + f"review reply iteration={payload.get('iteration', '?')} decision={payload.get('decision', 'unknown')}"
            )
            if payload.get("findings"):
                for finding in payload["findings"]:
                    lines.append(f"  finding: {finding}")
        elif kind == "apply_result":
            lines.append(
                prefix
                + f"apply result iteration={payload.get('iteration', '?')} status={payload.get('status', 'unknown')}"
            )
            for path in payload.get("changed_paths", []):
                lines.append(f"  changed: {path}")
            if payload.get("test_result", {}).get("summary"):
                lines.append(f"  tests: {payload['test_result']['summary']}")
        elif kind == "final_outcome":
            lines.append(prefix + f"final outcome status={payload.get('status', 'unknown')}")
            lines.append(f"  summary: {payload.get('summary', '')}")
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
            say(target_handle, f"{YELLOW}progress ping failed{RESET}: {exc}")
            continue


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
    final_outcome = build_room_state(events).get("final_outcome")
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
        ]
    )
    if final_outcome:
        lines.extend(
            [
                "## Final Outcome",
                f"- Status: {final_outcome.get('status', 'unknown')}",
                f"- Summary: {final_outcome.get('summary', '')}",
                "",
            ]
        )
    lines.extend([render_markdown_transcript(events), ""])
    return "\n".join(lines).rstrip() + "\n"


def tmp_json_path(prefix: str, turn_id: str) -> str:
    return str(Path(tempfile.gettempdir()) / f"{prefix}_{turn_id[:8]}.json")
