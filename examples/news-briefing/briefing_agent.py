#!/usr/bin/env python3
"""Briefing agent — orchestrates @news and @analyst into a daily briefing.

Runs on THIS MAC. Asks @news (on the remote server) for recent articles,
sends a batch to @analyst (local, backed by LM Studio) for analysis,
and assembles the results into a formatted briefing.

This is the demo entry point. Run it, and watch three agents on two
machines collaborate to produce a news briefing.

Environment variables:
    TAILBUS_SOCKET  — daemon Unix socket (default: /tmp/tailbusd.sock)
    BRIEFING_COUNT  — number of articles to include (default: 5)
"""

import asyncio
import json
import os
import sys
from datetime import datetime

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "../../sdk/python/src"))

from tailbus import AsyncAgent, Manifest, CommandSpec, Message

BRIEFING_COUNT = int(os.environ.get("BRIEFING_COUNT", "5"))

agent = AsyncAgent(
    "briefing",
    manifest=Manifest(
        description="Produces a daily news briefing by orchestrating @news and @analyst",
        commands=[
            CommandSpec(
                name="generate",
                description="Generate a news briefing with top stories and LLM analysis",
                parameters_schema=json.dumps({
                    "type": "object",
                    "properties": {
                        "count": {
                            "type": "integer",
                            "description": "Number of articles to analyze (default: 5)",
                        },
                        "hours": {
                            "type": "integer",
                            "description": "Look back N hours (default: 24)",
                        },
                    },
                }),
            ),
        ],
        tags=["orchestration", "news"],
        version="1.0.0",
    ),
    socket=os.environ.get("TAILBUS_SOCKET", "/tmp/tailbusd.sock"),
)

# Track delegated sessions
pending: dict[str, asyncio.Future] = {}


@agent.on_message
async def handle(msg: Message):
    """Handle responses from delegated agents, or trigger a new briefing."""
    # Response from a delegated session?
    if msg.session in pending:
        fut = pending.pop(msg.session)
        if not fut.done():
            fut.set_result(msg.payload)
        return

    # Only session_open is a new briefing request; ignore resolves, acks, etc.
    if msg.message_type != "session_open":
        return

    # New request — generate a briefing
    try:
        data = json.loads(msg.payload)
        args = data.get("arguments", data)
        if isinstance(args, str):
            args = json.loads(args)
        count = int(args.get("count", BRIEFING_COUNT))
        hours = int(args.get("hours", 24))
    except (json.JSONDecodeError, AttributeError, TypeError):
        count = BRIEFING_COUNT
        hours = 24

    print(f"\n{'='*60}", flush=True)
    print(f"[briefing] generating briefing — {count} articles, last {hours}h", flush=True)
    print(f"{'='*60}\n", flush=True)

    # Step 1: Ask @news for recent articles (remote server)
    print("[briefing] asking @news for recent articles...", flush=True)
    try:
        raw = await delegate("news", json.dumps({
            "command": "recent",
            "arguments": {"count": count, "hours": hours},
        }), timeout=120)
        news_data = json.loads(raw)
        articles = news_data.get("articles", [])
        total = news_data.get("count", 0)
    except asyncio.TimeoutError:
        await agent.resolve(msg.session, json.dumps({
            "error": "@news did not respond — is it running on the server?",
        }), content_type="application/json")
        return
    except Exception as e:
        await agent.resolve(msg.session, json.dumps({
            "error": f"Failed to reach @news: {e}",
        }), content_type="application/json")
        return

    print(f"[briefing] got {len(articles)} articles from @news\n", flush=True)

    if not articles:
        await agent.resolve(msg.session, json.dumps({
            "briefing": "No recent articles found.",
        }), content_type="application/json")
        return

    # Step 2: Send each article to @analyst for LLM analysis (local)
    analyses = []
    for i, article in enumerate(articles, 1):
        title = article.get("title", "Untitled")
        print(f"[briefing] [{i}/{len(articles)}] → @analyst: {title[:60]}...", flush=True)

        try:
            raw = await delegate("analyst", json.dumps({
                "command": "analyze",
                "arguments": {
                    "title": title,
                    "summary": article.get("summary", ""),
                },
            }), timeout=120)
            result = json.loads(raw)
            analyses.append({
                "title": title,
                "link": article.get("link", ""),
                "published": article.get("published", ""),
                "tags": article.get("tags", []),
                "analysis": result.get("analysis", "No analysis available"),
            })
            print(f"[briefing] [{i}/{len(articles)}] done", flush=True)
        except asyncio.TimeoutError:
            analyses.append({
                "title": title,
                "link": article.get("link", ""),
                "published": article.get("published", ""),
                "tags": article.get("tags", []),
                "analysis": "[timeout — @analyst did not respond]",
            })
            print(f"[briefing] [{i}/{len(articles)}] timeout", flush=True)

    # Step 3: Format the briefing
    now = datetime.now().strftime("%B %d, %Y at %H:%M")
    lines = [f"# Daily News Briefing — {now}\n"]
    lines.append(f"*{len(analyses)} articles from the last {hours}h, analyzed by @analyst via LM Studio*\n")

    for i, item in enumerate(analyses, 1):
        lines.append(f"## {i}. {item['title']}")
        if item.get("tags"):
            lines.append(f"*{', '.join(item['tags'][:3])}*\n")
        else:
            lines.append("")
        lines.append(item["analysis"])
        lines.append("")

    lines.append("---")
    lines.append("*Generated by @briefing → @news (remote DB) + @analyst (local LLM)*")

    briefing_text = "\n".join(lines)

    print(f"\n{'='*60}", flush=True)
    print(briefing_text, flush=True)
    print(f"{'='*60}\n", flush=True)

    await agent.resolve(msg.session, json.dumps({
        "briefing": briefing_text,
        "article_count": len(analyses),
        "generated_at": now,
    }), content_type="application/json")


async def delegate(target: str, payload: str, timeout: float = 30) -> str:
    """Open a session to a target agent and wait for the response."""
    opened = await agent.open_session(target, payload, content_type="application/json")
    fut: asyncio.Future = asyncio.get_running_loop().create_future()
    pending[opened.session] = fut
    try:
        return await asyncio.wait_for(fut, timeout=timeout)
    except asyncio.TimeoutError:
        pending.pop(opened.session, None)
        raise


async def main():
    print("[briefing] starting orchestrator", flush=True)
    async with agent:
        await agent.register()
        print("[briefing] registered — send a message to @briefing to generate", flush=True)
        print("[briefing] will orchestrate: @news (remote DB) → @analyst (local LLM)", flush=True)
        await agent.run_forever()


if __name__ == "__main__":
    asyncio.run(main())
