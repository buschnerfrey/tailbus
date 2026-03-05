#!/usr/bin/env python3
"""Analyst agent — runs on THIS MAC where LM Studio is running.

Takes a news cluster (title + summary + article summaries) and
produces a concise analysis using the local LLM.

Environment variables:
    TAILBUS_SOCKET  — daemon Unix socket (default: /tmp/tailbusd.sock)
    LLM_BASE_URL    — OpenAI-compatible API (default: http://localhost:1234/v1)
    LLM_MODEL       — model name (default: whatever is loaded in LM Studio)
"""

import asyncio
import json
import os
import sys
import time
import urllib.request
import urllib.error

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "../../sdk/python/src"))

from tailbus import AsyncAgent, Manifest, CommandSpec, Message

LLM_BASE_URL = os.environ.get("LLM_BASE_URL", "http://localhost:1234/v1")
LLM_MODEL = os.environ.get("LLM_MODEL", "")

agent = AsyncAgent(
    "analyst",
    manifest=Manifest(
        description="Analyzes news clusters using a local LLM and returns a concise take",
        commands=[
            CommandSpec(
                name="analyze",
                description="Analyze a news story and return a brief, opinionated summary",
                parameters_schema=json.dumps({
                    "type": "object",
                    "properties": {
                        "title": {"type": "string", "description": "Cluster headline"},
                        "summary": {"type": "string", "description": "Cluster summary"},
                        "articles": {
                            "type": "array",
                            "items": {"type": "string"},
                            "description": "Individual article summaries",
                        },
                    },
                    "required": ["title", "summary"],
                }),
            ),
        ],
        tags=["llm", "analysis"],
        version="1.0.0",
    ),
    socket=os.environ.get("TAILBUS_SOCKET", "/tmp/tailbusd.sock"),
)


SYSTEM_PROMPT = """You are a sharp news analyst. Given a news cluster (a headline, summary, and optionally individual article summaries), produce:

1. **What happened** — one sentence, factual
2. **Why it matters** — one sentence on the significance
3. **What to watch** — one sentence on what comes next

Keep it under 100 words total. No filler. Write like a smart colleague giving a quick briefing."""


def llm_analyze(title: str, summary: str, articles: list[str] | None = None) -> str:
    """Call LM Studio to analyze a news cluster."""
    user_content = f"# {title}\n\n{summary}"
    if articles:
        user_content += "\n\n## Source articles:\n"
        for i, a in enumerate(articles[:5], 1):  # Cap at 5 to stay within context
            if a:
                user_content += f"{i}. {a}\n"

    body: dict = {
        "messages": [
            {"role": "system", "content": SYSTEM_PROMPT},
            {"role": "user", "content": user_content},
        ],
        "temperature": 0.4,
        "max_tokens": 256,
    }
    if LLM_MODEL:
        body["model"] = LLM_MODEL

    data = json.dumps(body).encode()
    req = urllib.request.Request(
        f"{LLM_BASE_URL}/chat/completions",
        data=data,
        headers={"Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(req, timeout=120) as resp:
            result = json.loads(resp.read())
            return result["choices"][0]["message"]["content"]
    except urllib.error.URLError as e:
        return f"[analyst error] Could not reach LM Studio at {LLM_BASE_URL}: {e.reason}"
    except Exception as e:
        return f"[analyst error] {e}"


@agent.on_message
async def handle(msg: Message):
    """Analyze a news cluster using the local LLM."""
    print(f"[analyst] incoming from @{msg.from_handle} session={msg.session[:8]} type={msg.message_type}", flush=True)
    try:
        data = json.loads(msg.payload)
    except json.JSONDecodeError:
        print(f"[analyst] {msg.session[:8]} bad JSON payload", flush=True)
        await agent.resolve(msg.session, json.dumps({
            "error": "Expected JSON with title and summary",
        }), content_type="application/json")
        return

    # Handle both direct and MCP-style payloads
    args = data.get("arguments", data)
    if isinstance(args, str):
        try:
            args = json.loads(args)
        except (json.JSONDecodeError, AttributeError):
            pass

    title = args.get("title", "Untitled")
    summary = args.get("summary", "")
    articles = args.get("articles", [])

    if not summary:
        print(f"[analyst] {msg.session[:8]} no summary provided, skipping", flush=True)
        await agent.resolve(msg.session, json.dumps({
            "error": "No summary provided — nothing to analyze",
        }), content_type="application/json")
        return

    print(f"[analyst] {msg.session[:8]} analyzing: {title[:60]} (summary: {len(summary)} chars)", flush=True)
    print(f"[analyst] {msg.session[:8]} calling LM Studio at {LLM_BASE_URL}...", flush=True)

    t0 = time.monotonic()
    loop = asyncio.get_running_loop()
    analysis = await loop.run_in_executor(None, llm_analyze, title, summary, articles)
    elapsed = time.monotonic() - t0

    is_error = analysis.startswith("[analyst error]")
    if is_error:
        print(f"[analyst] {msg.session[:8]} LLM error after {elapsed:.1f}s: {analysis}", flush=True)
    else:
        print(f"[analyst] {msg.session[:8]} done in {elapsed:.1f}s ({len(analysis)} chars)", flush=True)

    await agent.resolve(msg.session, json.dumps({
        "title": title,
        "analysis": analysis,
    }), content_type="application/json")


async def main():
    print(f"[analyst] starting, LLM API: {LLM_BASE_URL}", flush=True)
    async with agent:
        await agent.register()
        print("[analyst] registered, listening for messages...", flush=True)
        await agent.run_forever()


if __name__ == "__main__":
    asyncio.run(main())
