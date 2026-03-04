# News Briefing Demo

Three agents on two machines collaborate to produce a daily news briefing.

```
┌─────────────────────────┐         ┌─────────────────────────────────┐
│  REMOTE SERVER          │         │  YOUR MAC                       │
│                         │  mesh   │                                 │
│  @news                  │◄───────►│  @analyst (LM Studio)           │
│  (Postgres direct)      │         │  @briefing (orchestrator)       │
└─────────────────────────┘         └─────────────────────────────────┘
```

`@briefing` asks `@news` for recent articles, sends each to `@analyst`
for LLM-powered analysis, and assembles a formatted briefing. The agents
discover each other by handle across machines — no URLs, no auth tokens.

## Prerequisites

- `tailbusd` running on both machines (same mesh)
- Python `tailbus` SDK (`pip install tailbus`)
- `asyncpg` on the server (`pip install asyncpg`)
- LM Studio running on your Mac with a model loaded

## Run it

### On the remote server

```bash
pip install asyncpg tailbus

# Default credentials are baked in — override with env vars if needed
python news_agent.py
```

### On your Mac

```bash
pip install tailbus

# Terminal 1 — analyst (wraps LM Studio at localhost:1234)
python analyst_agent.py

# Terminal 2 — briefing orchestrator
python briefing_agent.py
```

### Trigger a briefing

```bash
tailbus send briefing '{"command": "generate", "arguments": {"count": 3}}'
```

Or from Claude/Cursor via MCP — all three agents show up as tools.

## What happens

1. `@briefing` on your Mac messages `@news` on the remote server
2. `@news` queries `msh_news_embeddings` + `msh_raw_news` for recent articles
3. `@briefing` sends each article to `@analyst` (local)
4. `@analyst` calls LM Studio → returns "what happened / why it matters / what to watch"
5. `@briefing` assembles the final briefing and prints it

## @news commands

| Command | Description |
|---------|-------------|
| `recent` | Get recent articles (params: `count`, `hours`) |
| `article` | Get full details by UUID |
| `search` | Keyword search in title/summary (params: `query`, `count`) |

## Environment variables

| Variable | Default | Where | Description |
|----------|---------|-------|-------------|
| `TAILBUS_SOCKET` | `/tmp/tailbusd.sock` | both | Daemon socket |
| `NEWS_DB_HOST` | `msh-h1.cluster.internal.markets.sh` | server | Postgres host |
| `NEWS_DB_NAME` | `defaultdb` | server | Database |
| `NEWS_DB_USER` | `tsdbadmin` | server | DB user |
| `NEWS_DB_PASS` | — | server | DB password |
| `NEWS_DB_PORT` | `5432` | server | DB port |
| `NEWS_MAX_AGE` | `24` | server | Default lookback (hours) |
| `LLM_BASE_URL` | `http://localhost:1234/v1` | mac | LM Studio API |
| `LLM_MODEL` | *(auto)* | mac | Model name |
| `BRIEFING_COUNT` | `5` | mac | Default article count |
