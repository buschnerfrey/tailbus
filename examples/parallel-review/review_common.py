#!/usr/bin/env python3
"""Shared helpers for the parallel-review example."""

from __future__ import annotations

import asyncio
import json
import os
import sys
import urllib.error
import urllib.request
from typing import Any, Callable, Iterable

sys_path = os.path.join(os.path.dirname(__file__), "../../sdk/python/src")
if sys_path not in sys.path:
    sys.path.insert(0, sys_path)

from tailbus import AsyncAgent, BridgeError, RoomEvent  # noqa: E402,F401

LLM_BASE_URL = os.environ.get("LLM_BASE_URL", "http://127.0.0.1:1234/v1")
LLM_MODEL = os.environ.get("LLM_MODEL", "")
LLM_REQUEST_TIMEOUT = float(os.environ.get("LLM_REQUEST_TIMEOUT", "180"))
REVIEW_TIMEOUT = float(os.environ.get("REVIEW_TIMEOUT", "120"))
TURN_PROGRESS_INTERVAL = float(os.environ.get("TURN_PROGRESS_INTERVAL", "4"))
ROOM_REPLAY_RETRIES = int(os.environ.get("ROOM_REPLAY_RETRIES", "12"))
ROOM_REPLAY_DELAY = float(os.environ.get("ROOM_REPLAY_DELAY", "0.5"))

DIM = "\033[2m"
BOLD = "\033[1m"
GREEN = "\033[32m"
YELLOW = "\033[33m"
RED = "\033[31m"
CYAN = "\033[36m"
RESET = "\033[0m"

# Built-in code samples for review.
CODE_SAMPLES: dict[str, dict[str, str]] = {
    "insecure-api": {
        "title": "Insecure Flask API endpoint",
        "code": '''\
from flask import Flask, request, jsonify
import sqlite3

app = Flask(__name__)

@app.route("/users")
def get_users():
    """Fetch users by name from the database."""
    name = request.args.get("name", "")
    conn = sqlite3.connect("app.db")
    cursor = conn.execute(f"SELECT * FROM users WHERE name = '{name}'")
    rows = cursor.fetchall()
    conn.close()
    return jsonify([dict(zip(["id", "name", "email", "password_hash"], r)) for r in rows])

@app.route("/admin/delete", methods=["POST"])
def delete_user():
    """Delete a user by ID."""
    user_id = request.form["user_id"]
    conn = sqlite3.connect("app.db")
    conn.execute(f"DELETE FROM users WHERE id = {user_id}")
    conn.commit()
    conn.close()
    return jsonify({"status": "deleted"})

@app.route("/debug/env")
def debug_env():
    """Return all environment variables for debugging."""
    import os
    return jsonify(dict(os.environ))
''',
    },
    "slow-search": {
        "title": "Slow search and data processing",
        "code": '''\
import csv
import time

def find_duplicates(records: list[dict]) -> list[dict]:
    """Find duplicate records by email."""
    duplicates = []
    for i, rec_a in enumerate(records):
        for j, rec_b in enumerate(records):
            if i != j and rec_a["email"] == rec_b["email"]:
                if rec_a not in duplicates:
                    duplicates.append(rec_a)
    return duplicates

def search_logs(logs: list[str], keywords: list[str]) -> list[str]:
    """Find log lines containing any keyword."""
    results = []
    for line in logs:
        for keyword in keywords:
            if keyword in line:
                results.append(line)
                break
    return results

def aggregate_sales(transactions: list[dict]) -> dict:
    """Sum sales by product category."""
    result = {}
    for tx in transactions:
        category = tx["category"]
        if category not in result:
            result[category] = 0
        # Re-scan entire list to sum this category
        for t2 in transactions:
            if t2["category"] == category:
                result[category] += t2["amount"]
        result[category] = result[category] // transactions.count(tx)
    return result

def load_and_process(path: str) -> list[dict]:
    """Load CSV, parse dates, sort by timestamp."""
    with open(path) as f:
        data = list(csv.DictReader(f))
    # Bubble sort by date string
    for i in range(len(data)):
        for j in range(len(data) - 1):
            if data[j]["date"] > data[j + 1]["date"]:
                data[j], data[j + 1] = data[j + 1], data[j]
    return data
''',
    },
    "messy-code": {
        "title": "Poorly structured utility module",
        "code": '''\
import os, sys, json, re
from datetime import datetime

def p(d,k,v=None):
    """Get value from dict."""
    if k in d:
        return d[k]
    return v

def proc(data):
    r = []
    for i in range(len(data)):
        x = data[i]
        if type(x) == dict:
            if "name" in x:
                if len(x["name"]) > 0:
                    if x.get("active") == True:
                        if x.get("age") != None:
                            if x["age"] > 18:
                                n = x["name"].strip().lower()
                                a = x["age"]
                                e = x.get("email", "")
                                r.append({"n": n, "a": a, "e": e})
    return r

def fmt(ts):
    return str(datetime.fromtimestamp(ts))

def chk(s):
    if s == None: return False
    if s == "": return False
    if len(s) < 3: return False
    if not re.match(r"[a-zA-Z]", s[0]): return False
    return True

class M:
    def __init__(self, a, b, c, d, e):
        self.a = a
        self.b = b
        self.c = c
        self.d = d
        self.e = e

    def g(self):
        return {"a": self.a, "b": self.b, "c": self.c, "d": self.d, "e": self.e}

    def u(self, k, v):
        setattr(self, k, v)

def rF(p):
    f = open(p, "r")
    d = f.read()
    f.close()
    return d
''',
    },
}


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


def parse_json_object(text: str) -> dict[str, Any] | None:
    value = text.strip()
    if value.startswith("```"):
        first_newline = value.find("\n")
        if first_newline == -1:
            return None
        value = value[first_newline + 1:]
        if value.endswith("```"):
            value = value[:-3]
        value = value.strip()
    try:
        parsed = json.loads(value)
        return parsed if isinstance(parsed, dict) else None
    except json.JSONDecodeError:
        start = value.find("{")
        end = value.rfind("}")
        if start == -1 or end <= start:
            return None
        try:
            parsed = json.loads(value[start:end + 1])
            return parsed if isinstance(parsed, dict) else None
        except json.JSONDecodeError:
            return None


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
            return _collect_streamed_text(resp, on_chunk=on_chunk)
    except urllib.error.URLError as exc:
        return f"[LLM error] Could not reach LM Studio at {LLM_BASE_URL}: {exc.reason}"
    except Exception as exc:
        return f"[LLM error] {exc}"


def _collect_streamed_text(
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
        delta = choices[0].get("delta", {})
        text = str(delta.get("content", "") or "") if isinstance(delta, dict) else ""
        if not text:
            continue
        chunks.append(text)
        if on_chunk is not None:
            on_chunk("".join(chunks))
    return "".join(chunks)


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
