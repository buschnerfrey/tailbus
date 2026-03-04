#!/usr/bin/env python3
"""News agent — queries msh_raw_news + msh_news_embeddings directly.

Fetches recent articles, groups them by title similarity using the LLM
on the mesh (@analyst), and serves top stories to other agents.

For the demo, it simply returns recent articles with summaries — the
analyst agent handles the "make sense of it" part.

Requires: pip install asyncpg

Environment variables:
    TAILBUS_SOCKET  — daemon Unix socket (default: /tmp/tailbusd.sock)
    NEWS_DB_HOST    — Postgres host
    NEWS_DB_NAME    — database name (default: defaultdb)
    NEWS_DB_USER    — database user (default: tsdbadmin)
    NEWS_DB_PASS    — database password
    NEWS_DB_PORT    — database port (default: 5432)
    NEWS_MAX_AGE    — max article age in hours (default: 24)
"""

import asyncio
import json
import os
import sys
import time

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "../../sdk/python/src"))

import asyncpg
from tailbus import AsyncAgent, Manifest, CommandSpec, Message

DB_HOST = os.environ.get("NEWS_DB_HOST", "msh-h1.cluster.internal.markets.sh")
DB_NAME = os.environ.get("NEWS_DB_NAME", "defaultdb")
DB_USER = os.environ.get("NEWS_DB_USER", "tsdbadmin")
DB_PASS = os.environ.get("NEWS_DB_PASS", "")
DB_PORT = int(os.environ.get("NEWS_DB_PORT", "5432"))
MAX_AGE_HOURS = int(os.environ.get("NEWS_MAX_AGE", "24"))

agent = AsyncAgent(
    "news",
    manifest=Manifest(
        description="Recent news articles with summaries and entities from a live database",
        commands=[
            CommandSpec(
                name="recent",
                description="Get recent news articles with summaries, sorted by recency",
                parameters_schema=json.dumps({
                    "type": "object",
                    "properties": {
                        "count": {
                            "type": "integer",
                            "description": "Number of articles to return (default: 20)",
                        },
                        "hours": {
                            "type": "integer",
                            "description": "Look back N hours (default: 24)",
                        },
                    },
                }),
            ),
            CommandSpec(
                name="article",
                description="Get full details for a specific article by UUID",
                parameters_schema=json.dumps({
                    "type": "object",
                    "properties": {
                        "uuid": {"type": "string", "description": "Article UUID"},
                    },
                    "required": ["uuid"],
                }),
            ),
            CommandSpec(
                name="search",
                description="Search articles by keyword in title or summary",
                parameters_schema=json.dumps({
                    "type": "object",
                    "properties": {
                        "query": {"type": "string", "description": "Search term"},
                        "count": {"type": "integer", "description": "Max results (default: 10)"},
                    },
                    "required": ["query"],
                }),
            ),
        ],
        tags=["news", "data"],
        version="1.0.0",
    ),
    socket=os.environ.get("TAILBUS_SOCKET", "/tmp/tailbusd.sock"),
)

# DB pool
_pool: asyncpg.Pool | None = None

# Cache
_cache: dict = {"data": None, "ts": 0}
CACHE_TTL = 120

RECENT_QUERY = """
SELECT
    e.news_uuid,
    e.title,
    e.summary,
    e.summary_json,
    e.link,
    e.countries,
    e.companies,
    e.sectors_industries,
    e.tags,
    e.created_at,
    r.published
FROM msh_news_embeddings e
JOIN msh_raw_news r ON r.uuid = e.news_uuid
WHERE
    r.published > NOW() - make_interval(hours => $1)
    AND e.summary IS NOT NULL
    AND e.is_promotional IS NOT TRUE
    AND e.article_blocked IS NOT TRUE
ORDER BY r.published DESC
LIMIT $2
"""

ARTICLE_QUERY = """
SELECT
    e.news_uuid,
    e.title,
    e.summary,
    e.summary_json,
    e.link,
    e.countries,
    e.companies,
    e.persons,
    e.sectors_industries,
    e.facts,
    e.tags,
    e.created_at,
    r.published,
    r.article
FROM msh_news_embeddings e
JOIN msh_raw_news r ON r.uuid = e.news_uuid
WHERE e.news_uuid = $1
"""

SEARCH_QUERY = """
SELECT
    e.news_uuid,
    e.title,
    e.summary,
    e.link,
    e.tags,
    r.published
FROM msh_news_embeddings e
JOIN msh_raw_news r ON r.uuid = e.news_uuid
WHERE
    r.published > NOW() - make_interval(hours => $1)
    AND e.summary IS NOT NULL
    AND e.is_promotional IS NOT TRUE
    AND (e.title ILIKE '%' || $2 || '%' OR e.summary ILIKE '%' || $2 || '%')
ORDER BY r.published DESC
LIMIT $3
"""


def jsonb_to_dict(val):
    """Safely convert a jsonb/str field to a Python dict or list."""
    if val is None:
        return None
    if isinstance(val, (dict, list)):
        return val
    if isinstance(val, str):
        try:
            return json.loads(val)
        except (json.JSONDecodeError, TypeError):
            return None
    return None


def row_to_article(row, full: bool = False) -> dict:
    """Convert a DB row to a JSON-serializable article dict."""
    art = {
        "uuid": str(row["news_uuid"]),
        "title": row["title"] or "",
        "summary": row["summary"] or "",
        "link": row["link"] or "",
        "published": row["published"].isoformat() if row["published"] else "",
        "tags": list(row["tags"]) if row["tags"] else [],
    }

    # Include structured entities when available
    countries = jsonb_to_dict(row.get("countries"))
    companies = jsonb_to_dict(row.get("companies"))
    sectors = jsonb_to_dict(row.get("sectors_industries"))

    if countries:
        art["countries"] = countries
    if companies:
        art["companies"] = companies
    if sectors:
        art["sectors"] = sectors

    if full:
        art["summary_json"] = row.get("summary_json") or ""
        persons = jsonb_to_dict(row.get("persons"))
        facts = jsonb_to_dict(row.get("facts"))
        if persons:
            art["persons"] = persons
        if facts:
            art["facts"] = facts
        if row.get("article"):
            # Truncate full article to keep payload reasonable
            article_text = row["article"] or ""
            art["article"] = article_text[:2000]

    return art


async def fetch_recent(hours: int = None, count: int = 20) -> list[dict]:
    """Fetch recent articles from the database."""
    h = hours or MAX_AGE_HOURS

    # Use cache for default queries
    cache_key = f"{h}:{count}"
    now = time.time()
    if (
        _cache.get("key") == cache_key
        and _cache["data"] is not None
        and (now - _cache["ts"]) < CACHE_TTL
    ):
        return _cache["data"]

    if not _pool:
        return []

    try:
        rows = await _pool.fetch(RECENT_QUERY, h, count)
    except Exception as e:
        print(f"[news] DB error: {e}", flush=True)
        return _cache["data"] or []

    articles = [row_to_article(row) for row in rows]

    _cache["data"] = articles
    _cache["ts"] = now
    _cache["key"] = cache_key
    return articles


@agent.on_message
async def handle(msg: Message):
    """Route incoming requests to the right query."""
    try:
        data = json.loads(msg.payload)
    except json.JSONDecodeError:
        data = {"command": "recent"}

    command = data.get("command", "recent")
    args = data.get("arguments", data)

    if isinstance(args, str):
        try:
            parsed = json.loads(args)
            command = parsed.get("command", command)
            args = parsed.get("arguments", parsed)
        except (json.JSONDecodeError, AttributeError):
            pass

    if not isinstance(args, dict):
        args = {}

    if command == "recent":
        count = int(args.get("count", 20))
        hours = int(args.get("hours", MAX_AGE_HOURS))
        articles = await fetch_recent(hours=hours, count=count)
        await agent.resolve(
            msg.session,
            json.dumps({"articles": articles, "count": len(articles)}),
            content_type="application/json",
        )

    elif command == "article":
        uuid_str = args.get("uuid", "")
        if not uuid_str:
            await agent.resolve(msg.session, json.dumps({"error": "uuid required"}),
                                content_type="application/json")
            return
        try:
            import uuid as uuid_mod
            uid = uuid_mod.UUID(uuid_str)
            rows = await _pool.fetch(ARTICLE_QUERY, uid)
            if rows:
                art = row_to_article(rows[0], full=True)
                await agent.resolve(msg.session, json.dumps(art),
                                    content_type="application/json")
            else:
                await agent.resolve(msg.session, json.dumps({"error": "not found"}),
                                    content_type="application/json")
        except Exception as e:
            await agent.resolve(msg.session, json.dumps({"error": str(e)}),
                                content_type="application/json")

    elif command == "search":
        query = args.get("query", "")
        count = int(args.get("count", 10))
        if not query:
            await agent.resolve(msg.session, json.dumps({"error": "query required"}),
                                content_type="application/json")
            return
        try:
            rows = await _pool.fetch(SEARCH_QUERY, MAX_AGE_HOURS, query, count)
            articles = [row_to_article(row) for row in rows]
            await agent.resolve(
                msg.session,
                json.dumps({"articles": articles, "count": len(articles), "query": query}),
                content_type="application/json",
            )
        except Exception as e:
            await agent.resolve(msg.session, json.dumps({"error": str(e)}),
                                content_type="application/json")

    else:
        await agent.resolve(
            msg.session,
            json.dumps({"error": f"Unknown command: {command}",
                         "available": ["recent", "article", "search"]}),
            content_type="application/json",
        )


async def main():
    global _pool

    dsn = f"postgresql://{DB_USER}:{DB_PASS}@{DB_HOST}:{DB_PORT}/{DB_NAME}"
    print(f"[news] connecting to {DB_HOST}:{DB_PORT}/{DB_NAME}...", flush=True)

    _pool = await asyncpg.create_pool(dsn, min_size=1, max_size=5, command_timeout=30)
    print("[news] database connected", flush=True)

    articles = await fetch_recent()
    print(f"[news] {len(articles)} recent articles loaded", flush=True)

    async with agent:
        await agent.register()
        print("[news] registered on mesh — @news is live", flush=True)
        try:
            await agent.run_forever()
        finally:
            await _pool.close()


if __name__ == "__main__":
    asyncio.run(main())
