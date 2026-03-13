#!/usr/bin/env python3
"""LLM-backed chat agent for the chat-room example.

Configure via environment variables:
  AGENT_HANDLE   = atlas | nova | ...
  AGENT_STYLE    = analytical | creative
  TAILBUS_SOCKET = daemon socket path
"""

from __future__ import annotations

import asyncio
import json
import os
import re
import sys
import time
import urllib.error
import urllib.request
import uuid
from typing import Any, Callable, Iterable

sys_path = os.path.join(os.path.dirname(__file__), "../../sdk/python/src")
if sys_path not in sys.path:
    sys.path.insert(0, sys_path)

from tailbus import AsyncAgent, BridgeError, Manifest, RoomEvent

HANDLE = os.environ.get("AGENT_HANDLE", "atlas")
STYLE = os.environ.get("AGENT_STYLE", "analytical")
LLM_BASE_URL = os.environ.get("LLM_BASE_URL", "http://127.0.0.1:1234/v1")
LLM_MODEL = os.environ.get("LLM_MODEL", "")
LLM_REQUEST_TIMEOUT = float(os.environ.get("LLM_REQUEST_TIMEOUT", "120"))
REPLY_TIMEOUT = float(os.environ.get("REPLY_TIMEOUT", "90"))
LLM_MAX_TOKENS = int(os.environ.get("LLM_MAX_TOKENS", "1024"))
LLM_RETRY_ATTEMPTS = int(os.environ.get("LLM_RETRY_ATTEMPTS", "2"))
LLM_RETRY_BACKOFF = float(os.environ.get("LLM_RETRY_BACKOFF", "1.0"))
PROGRESS_INTERVAL = float(os.environ.get("PROGRESS_INTERVAL", "4"))
PROGRESS_POST_TIMEOUT = float(os.environ.get("PROGRESS_POST_TIMEOUT", "5"))

DIM = "\033[2m"
BOLD = "\033[1m"
GREEN = "\033[32m"
YELLOW = "\033[33m"
RESET = "\033[0m"

STYLE_PROMPTS: dict[str, str] = {
    "analytical": """\
You are Atlas, a thoughtful and precise conversationalist in a shared chat room.
You give well-reasoned, structured answers. You cite best practices and consider
trade-offs. You're the senior engineer in the room — thorough but not verbose.
Keep responses to 2-4 sentences unless the topic demands more.""",
    "creative": """\
You are Nova, a bold and creative conversationalist in a shared chat room.
You give opinionated, concise answers. You challenge assumptions and suggest
unconventional approaches. You're the startup CTO — direct, practical, sometimes
contrarian. Keep responses to 1-3 sentences. Be punchy.""",
}


def say(msg: str) -> None:
    print(f"  {DIM}{HANDLE}{RESET}  {msg}", flush=True)


def is_room_closed_error(exc: Exception) -> bool:
    return isinstance(exc, BridgeError) and "room" in str(exc).lower() and "is closed" in str(exc).lower()


agent = AsyncAgent(
    HANDLE,
    manifest=Manifest(
        description=f"Chat agent with {STYLE} personality",
        commands=[],
        tags=["llm", "chat", STYLE],
        version="1.0.0",
        capabilities=["chat.converse"],
        domains=["general"],
        input_types=["application/json"],
        output_types=["application/json"],
    ),
    socket=os.environ.get("TAILBUS_SOCKET", "/tmp/tailbusd.sock"),
)


def extract_mentions(text: str) -> list[str]:
    return re.findall(r"@(\w[\w-]*)", text)


def should_respond(payload: dict[str, Any]) -> bool:
    author = payload.get("author", "")
    if author == HANDLE:
        return False
    # Don't respond to other agents (prevent loops).
    if payload.get("is_agent"):
        return False
    mentions = extract_mentions(str(payload.get("text", "")))
    if mentions and HANDLE not in mentions:
        return False
    return True


def build_conversation(events: list[RoomEvent], limit: int = 20) -> list[dict[str, str]]:
    """Build an LLM messages list from room history."""
    messages: list[dict[str, str]] = []
    for event in events:
        if event.event_type != "message_posted" or event.content_type != "application/json":
            continue
        try:
            payload = json.loads(event.payload)
        except json.JSONDecodeError:
            continue
        if payload.get("kind") != "chat":
            continue
        author = payload.get("author", "unknown")
        text = str(payload.get("text", ""))
        if not text:
            continue
        if author == HANDLE:
            messages.append({"role": "assistant", "content": text})
        else:
            messages.append({"role": "user", "content": f"[{author}] {text}"})
    return messages[-limit:]


def _normalize_whitespace(text: str) -> str:
    return re.sub(r"\s+", " ", text or "").strip()


def _last_complete_sentences(text: str, limit: int = 3) -> str:
    matches = re.findall(r"[^.!?]*[.!?]", text or "")
    cleaned = [_normalize_whitespace(m) for m in matches if _normalize_whitespace(m)]
    if not cleaned:
        return ""
    return " ".join(cleaned[-limit:]).strip()


def _extract_from_sentence_labels(text: str) -> str:
    matches = re.findall(r"Sentence\s+\d+\s*:\s*(.+)", text, flags=re.IGNORECASE)
    cleaned = []
    for match in matches:
        line = _normalize_whitespace(match)
        if line:
            cleaned.append(line)
    return " ".join(cleaned[:3]).strip()


def _extract_after_marker(text: str, marker: str) -> str:
    idx = text.lower().rfind(marker.lower())
    if idx < 0:
        return ""
    tail = text[idx + len(marker):]
    lines: list[str] = []
    for raw_line in tail.splitlines():
        line = _normalize_whitespace(raw_line.replace("*", ""))
        if not line:
            if lines:
                break
            continue
        if re.match(r"^\d+\.\s", line):
            break
        lines.append(line)
        if line.endswith((".", "!", "?")):
            break
    return " ".join(lines).strip()


def _extract_candidate_lines(text: str) -> str:
    banned = (
        "thinking process",
        "analyze",
        "constraint",
        "draft",
        "option",
        "selecting",
        "final polish",
        "output generation",
        "wait,",
        "let's go with",
        "actually,",
        "task:",
    )
    candidates: list[str] = []
    for raw_line in text.splitlines():
        line = _normalize_whitespace(raw_line.replace("*", ""))
        if not line:
            continue
        if line.startswith("<think>") or line.startswith("</think>"):
            continue
        if re.match(r"^\d+\.\s", line):
            continue
        lowered = line.lower()
        if any(token in lowered for token in banned):
            continue
        if line.endswith((".", "!", "?")):
            candidates.append(line)
    if not candidates:
        return ""
    return " ".join(candidates[-3:]).strip()


def extract_final_chat_text(raw: str, finish_reason: str = "") -> str:
    text = (raw or "").strip()
    if not text:
        return ""

    if "<think>" in text and "</think>" in text:
        after = text.split("</think>", 1)[1].strip()
        if after:
            text = after

    if "<think>" in text or "Thinking Process:" in text:
        for marker in ("Final Answer:", "Final answer:", "Answer:", "Response:", "Let's go with:"):
            candidate = _extract_after_marker(text, marker)
            if candidate:
                return candidate
        candidate = _extract_from_sentence_labels(text)
        if candidate:
            return candidate
        candidate = _extract_candidate_lines(text)
        if candidate:
            return candidate

    text = _normalize_whitespace(text)
    if finish_reason == "length":
        candidate = _last_complete_sentences(text)
        if candidate:
            return candidate
    return text


def _truncate_error_body(text: str, limit: int = 220) -> str:
    clean = _normalize_whitespace(text)
    if len(clean) <= limit:
        return clean
    return clean[:limit].rstrip() + "..."


def _lm_body(system: str, conversation: list[dict[str, str]], max_tokens: int) -> dict[str, Any]:
    body: dict[str, Any] = {
        "messages": [{"role": "system", "content": system}] + conversation,
        "temperature": 0.7,
        "max_tokens": max_tokens,
        "stream": False,
    }
    if LLM_MODEL:
        body["model"] = LLM_MODEL
    return body


def _request_lm_completion(body: dict[str, Any]) -> dict[str, Any]:
    data = json.dumps(body).encode("utf-8")
    req = urllib.request.Request(
        f"{LLM_BASE_URL}/chat/completions",
        data=data,
        headers={"Content-Type": "application/json"},
    )
    with urllib.request.urlopen(req, timeout=LLM_REQUEST_TIMEOUT) as resp:
        return json.load(resp)


def llm_call(system: str, conversation: list[dict[str, str]]) -> str:
    payload: dict[str, Any] | None = None
    last_error = ""
    for attempt in range(1, max(1, LLM_RETRY_ATTEMPTS) + 1):
        try:
            payload = _request_lm_completion(_lm_body(system, conversation, LLM_MAX_TOKENS))
            break
        except urllib.error.HTTPError as exc:
            body = exc.read().decode("utf-8", errors="ignore")
            last_error = f"LM Studio request failed at {LLM_BASE_URL}: status={exc.code} body={_truncate_error_body(body)}"
        except urllib.error.URLError as exc:
            last_error = f"could not reach LM Studio at {LLM_BASE_URL}: {exc.reason}"
        except Exception as exc:
            last_error = str(exc)

        if attempt < max(1, LLM_RETRY_ATTEMPTS):
            time.sleep(LLM_RETRY_BACKOFF)

    if payload is None:
        return f"[error: {last_error}]"
    choices = payload.get("choices", [])
    if not choices:
        return ""
    choice = choices[0] or {}
    message = choice.get("message", {}) or {}
    raw = str(message.get("content", "") or "")
    finish_reason = str(choice.get("finish_reason", "") or "")
    return extract_final_chat_text(raw, finish_reason)


def _collect_stream(lines: Iterable[bytes]) -> str:
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
        delta = choices[0].get("delta", {})
        text = str(delta.get("content", "") or "") if isinstance(delta, dict) else ""
        if text:
            chunks.append(text)
    result = "".join(chunks).strip()
    if "<think>" in result:
        parts = result.split("</think>")
        result = parts[-1].strip() if len(parts) > 1 else result
    return result


def build_turn_payload(kind: str, turn_id: str, round_no: int, *, status: str = "", summary: str = "") -> str:
    payload: dict[str, Any] = {
        "kind": kind,
        "target_handle": HANDLE,
        "turn_id": turn_id,
        "round": round_no,
    }
    if status:
        payload["status"] = status
    if summary:
        payload["summary"] = summary
    return json.dumps(payload)


async def post_turn_event(room_id: str, kind: str, turn_id: str, round_no: int, *, status: str = "", summary: str = "") -> None:
    await asyncio.wait_for(
        agent.post_room_message(
            room_id,
            build_turn_payload(kind, turn_id, round_no, status=status, summary=summary),
            content_type="application/json",
        ),
        timeout=PROGRESS_POST_TIMEOUT,
    )


async def progress_pinger(room_id: str, turn_id: str, round_no: int, state: dict[str, str]) -> None:
    while True:
        await asyncio.sleep(PROGRESS_INTERVAL)
        try:
            await post_turn_event(
                room_id,
                "turn_progress",
                turn_id,
                round_no,
                status="working",
                summary=state.get("summary", "Waiting for LM Studio response."),
            )
        except asyncio.CancelledError:
            raise
        except Exception as exc:
            if not is_room_closed_error(exc):
                say(f"{YELLOW}progress ping failed{RESET}: {exc}")


@agent.on_message
async def handle(msg: RoomEvent) -> None:
    if not isinstance(msg, RoomEvent):
        return
    if msg.event_type != "message_posted" or msg.content_type != "application/json":
        return
    try:
        payload = json.loads(msg.payload)
    except json.JSONDecodeError:
        return
    if payload.get("kind") != "chat":
        return
    if not should_respond(payload):
        return

    author = payload.get("author", "someone")
    text = str(payload.get("text", ""))
    say(f"replying to {BOLD}{author}{RESET}: {text[:80]}")

    # Replay room for conversation context.
    try:
        events = await agent.replay_room(msg.room_id)
    except Exception:
        events = []

    system = STYLE_PROMPTS.get(STYLE, STYLE_PROMPTS["analytical"])
    system += (
        "\n\nYou are in a chat room. Other participants may include humans and other AI agents. "
        "Messages from others are prefixed with [their_name]. Respond naturally to the conversation. "
        "If someone @mentions you specifically, make sure to address their question directly."
    )
    conversation = build_conversation(events)
    turn_id = str(uuid.uuid4())
    round_no = int(msg.room_seq)
    progress_state = {"summary": "Waiting for LM Studio response."}

    try:
        await post_turn_event(
            msg.room_id,
            "turn_request",
            turn_id,
            round_no,
            summary=f"{HANDLE} is preparing a reply.",
        )
    except Exception as exc:
        if not is_room_closed_error(exc):
            say(f"{YELLOW}failed to post turn request{RESET}: {exc}")

    progress_task = asyncio.create_task(progress_pinger(msg.room_id, turn_id, round_no, progress_state))

    started = time.monotonic()
    reply_status = "ok"
    try:
        response = await asyncio.wait_for(
            asyncio.shield(asyncio.to_thread(llm_call, system, conversation)),
            timeout=REPLY_TIMEOUT,
        )
    except asyncio.TimeoutError:
        response = "[timed out generating response]"
        reply_status = "error"

    progress_task.cancel()
    try:
        await progress_task
    except asyncio.CancelledError:
        pass

    if not response:
        try:
            await post_turn_event(msg.room_id, "chat_reply", turn_id, round_no, status="error")
        except Exception as exc:
            if not is_room_closed_error(exc):
                say(f"{YELLOW}failed to post reply status{RESET}: {exc}")
        return

    elapsed = round(time.monotonic() - started, 1)
    say(f"{GREEN}replied{RESET} in {elapsed:.1f}s")

    reply = {
        "kind": "chat",
        "author": HANDLE,
        "text": response,
        "is_agent": True,
        "elapsed_sec": elapsed,
    }
    try:
        await agent.post_room_message(
            msg.room_id,
            json.dumps(reply),
            content_type="application/json",
        )
        await post_turn_event(msg.room_id, "chat_reply", turn_id, round_no, status=reply_status)
    except Exception as exc:
        if not is_room_closed_error(exc):
            raise


async def main() -> None:
    say("connecting...")
    async with agent:
        await agent.register()
        say(f"{GREEN}ready{RESET} — {STYLE} chat agent")
        await agent.run_forever()


if __name__ == "__main__":
    asyncio.run(main())
