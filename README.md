# Tailbus

Agent communication mesh. Tailscale-style topology: a central coordination server for discovery, with a peer-to-peer gRPC data plane between node daemons.

Think of it as **Slack for autonomous agents** — agents register handles, open sessions, exchange messages, and resolve conversations, all routed through a decentralized mesh.

```
                       +-----------------+
                       |  tailbus-coord  |
                       |  (discovery)    |
                       +--------+--------+
                          peer map
                       /    updates     \
              +--------+--------+  +--------+--------+
              |    tailbusd     |  |    tailbusd     |
              |    (node-1)     |  |    (node-2)     |
              |  P2P gRPC <----|--|----> P2P gRPC   |
              +--------+--------+  +--------+--------+
                 |  Unix socket     Unix socket  |
                /     \      \      /     \       \
           agent-a   agent-b  \    /  agent-c   agent-d
                               +--+
                          +--------+--------+
                          | tailbus-relay   |
                          | (NAT fallback)  |
                          +-----------------+
```

## Features

- **Handle-based addressing** — agents register names like `marketing`, `sales`, `planner` and message each other without knowing which node they're on
- **Service manifests** — agents register with a structured `ServiceManifest` (description, commands, tags, version); query via `IntrospectHandle` or discover handles via `ListHandles` with tag filtering
- **@-mention auto-routing** — when a `text/*` message contains `@handle`, the daemon automatically opens a new session to each mentioned handle
- **Session lifecycle** — structured conversations with open / message / resolve states
- **P2P data plane** — messages flow directly between daemons via bidirectional gRPC streams, not through the coord server
- **Distributed message tracing** — every session gets a `trace_id`; spans are recorded at each hop (created, routed, sent, received, delivered)
- **Prometheus metrics** — counter and histogram metrics exported at `/metrics` for external monitoring
- **Real-time TUI dashboard** — terminal dashboard showing handles, peers, sessions, and live activity
- **DERP-style relay** — when peers can't reach each other directly (NAT, firewalls, different VPCs), messages are transparently forwarded through a relay server; daemons try direct P2P first and fall back automatically
- **mTLS everywhere** — all P2P, relay, and daemon-to-coord connections use mutual TLS with Ed25519 identity verification; coord uses TOFU (trust-on-first-use) cert pinning
- **Per-connection handle binding** — each gRPC connection owns its registered handles; RPCs enforce `from_handle` ownership; handles auto-cleanup on disconnect
- **Unix socket token auth** — daemon generates a random auth token file (mode 0600) on startup; CLI and agents present it automatically via gRPC per-RPC credentials; prevents co-tenant handle impersonation
- **Sequence numbers** — every envelope gets a per-session monotonic sequence number for ordering
- **Delivery ACKs with retry** — successful delivery generates an ACK back to sender; unacknowledged messages retry with backoff (5s timeout, 3 max retries)
- **Message persistence** — bbolt-backed store on each daemon; sessions and pending messages survive daemon restarts; ACKed messages purged automatically
- **MCP gateway** — HTTP server on daemon exposing handles as MCP tools; any MCP-compatible LLM (Claude, ChatGPT, Cursor) can discover and invoke tailbus agents with zero SDK code
- **Web chat UI** — browser-based chat interface embedded in the daemon binary via `go:embed`; select agents, send messages, view responses in real time at the MCP gateway address
- **OAuth login flow** — device authorization grant (RFC 8628) for browser-based login; `tailbusd` starts → opens browser → login with Google → machine joins mesh; JWT access tokens (1h) with automatic refresh (30d); credentials persisted at `~/.tailbus/credentials.json`
- **Coord admission control** — pre-auth token system (like `tailscale up --authkey`) gates which nodes can join the mesh; OAuth login works alongside pre-shared tokens; open mode (no tokens configured) preserves zero-config default
- **Health & readiness endpoints** — `/healthz`, `/readyz`, and `/debug/pprof/*` on daemon metrics port (alongside `/metrics`), coord, and relay servers
- **Docker Compose** — `docker compose up` for a full mesh with coord + 2 daemons + MCP gateway + example agents in 30 seconds

## Install

One-liner install from GitHub Releases (Linux and macOS):

```bash
curl -sSL https://raw.githubusercontent.com/alexanderfrey/tailbus/main/install.sh | sh
```

This detects your OS/architecture, downloads the latest release, and installs all three binaries to `/usr/local/bin` (or `~/.local/bin` if no sudo).

Then start the daemon — it handles login automatically:

```bash
tailbusd          # opens browser → login with Google → machine joins mesh
```

Or authenticate separately:

```bash
tailbus login     # authenticate without starting daemon
tailbus status    # check connection status
tailbus logout    # remove saved credentials
```

## Prerequisites (building from source)

- **Go 1.25+** (no CGo required)
- **protoc** with `protoc-gen-go` and `protoc-gen-go-grpc` (only needed if modifying `.proto` files)

## Build

```bash
make build
```

This produces four binaries in `bin/`:

| Binary | Description |
|--------|-------------|
| `bin/tailbus-coord` | Coordination server (discovery + peer map) |
| `bin/tailbusd` | Node daemon (local agent server + P2P transport) |
| `bin/tailbus` | CLI tool for interacting with a local daemon |
| `bin/tailbus-relay` | Relay server for NAT traversal |

Other Makefile targets:

```bash
make proto      # Regenerate protobuf Go code
make test       # Run unit tests
make test-all   # Run all tests including integration
make clean      # Remove binaries and generated code
```

## Quick Start (Docker Compose)

The fastest way to try tailbus — a full mesh with MCP gateway in 30 seconds:

```bash
docker compose up --build
```

This starts: coord server + 2 daemons + MCP gateway with web UI (port 8080) + 4 example Python agents (calculator, echo, orchestrator, LLM assistant).

Open http://localhost:8080 in your browser for the chat UI — select an agent from the sidebar and start chatting. The web UI auto-generates input forms for agents with structured commands (like calculator).

If you have [LM Studio](https://lmstudio.ai/) running on port 1234, the **assistant** agent will route messages to your local LLM.

Test with curl:

```bash
# List available agents as MCP tools
curl -s localhost:8080/mcp \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}' | jq '.result.tools[].name'

# Call the calculator
curl -s localhost:8080/mcp \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"calculator.add","arguments":{"a":2,"b":3}}}' | jq

# Call the echo agent (runs on a different node — demonstrates cross-node P2P)
curl -s localhost:8080/mcp \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"message":"hello tailbus"}}}' | jq

# Call the orchestrator (delegates to calculator and echo)
curl -s localhost:8080/mcp \
  -d '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"orchestrator.compute","arguments":{"operation":"multiply","a":6,"b":7}}}' | jq
```

## Multi-Agent LLM Collaboration

The `examples/multi-agent/` directory demonstrates three LLM-powered agents collaborating through the mesh — all using a single local LM Studio instance:

| Agent | Role |
|-------|------|
| **researcher** | Investigates topics, orchestrates the pipeline |
| **critic** | Reviews findings for accuracy, completeness, bias |
| **writer** | Synthesizes research + critique into polished output |

```bash
cd examples/multi-agent
docker compose up --build
```

Open http://localhost:8080, click **researcher**, and ask it to investigate any topic. The pipeline runs: researcher → critic → writer, with each step as a separate LLM call through a separate agent on the mesh.

For cross-network deployment (mesh spanning multiple machines), see `examples/multi-machine/`.

## Quick Start (from source)

### 1. Start the coordination server

```bash
./bin/tailbus-coord -listen :8443 -data-dir /tmp/tailbus-coord
```

Or with a config file:

```bash
./bin/tailbus-coord -config examples/dev/coord.toml
```

### 2. Start two node daemons

```bash
# Terminal 2 — node-1
./bin/tailbusd -config examples/dev/daemon1.toml

# Terminal 3 — node-2
./bin/tailbusd -config examples/dev/daemon2.toml
```

Or using flags directly:

```bash
./bin/tailbusd \
  -node-id node-1 \
  -coord 127.0.0.1:8443 \
  -advertise 127.0.0.1:9443 \
  -listen :9443 \
  -socket /tmp/tailbusd-1.sock
```

### (Optional) Start a relay server

If daemons are behind NAT or can't reach each other directly, start a relay:

```bash
./bin/tailbus-relay -listen :7443 -coord 127.0.0.1:8443
```

Or with a config file:

```bash
./bin/tailbus-relay -config examples/dev/relay.toml
```

The relay registers with the coord server and appears in the peer map. Daemons discover it automatically and fall back to it when direct P2P connections fail.

### 3. Register agents and exchange messages

```bash
# Register handles on each node
./bin/tailbus -socket /tmp/tailbusd-1.sock register marketing
./bin/tailbus -socket /tmp/tailbusd-2.sock register sales

# Subscribe to incoming messages (blocking — run in separate terminals)
./bin/tailbus -socket /tmp/tailbusd-1.sock subscribe marketing
./bin/tailbus -socket /tmp/tailbusd-2.sock subscribe sales

# Open a session from marketing to sales
./bin/tailbus -socket /tmp/tailbusd-1.sock open marketing sales "Need Q4 numbers"
# Output: Session: <session-id>  Message: <message-id>

# Reply from sales
./bin/tailbus -socket /tmp/tailbusd-2.sock send <session-id> sales "Q4 revenue: $1.2M"

# Resolve the session
./bin/tailbus -socket /tmp/tailbusd-1.sock resolve <session-id> marketing "Thanks!"
```

### 4. Observe

```bash
# Launch the TUI dashboard
./bin/tailbus -socket /tmp/tailbusd-1.sock dashboard

# List sessions for a handle
./bin/tailbus -socket /tmp/tailbusd-1.sock sessions marketing

# View a distributed trace
./bin/tailbus -socket /tmp/tailbusd-1.sock trace <trace-id>

# Scrape Prometheus metrics
curl http://localhost:9090/metrics
```

## 3-Machine Demo

The `examples/demo/` directory contains a ready-made travel agency scenario across 3 machines:

| Machine | Role | Agents |
|---------|------|--------|
| A | Coord + daemon | `concierge` (orchestrator) |
| B | Daemon only | `flights`, `hotels` (booking) |
| C | Daemon only | `weather`, `currency` (data) |

Quick version:

```bash
# Install on all 3 machines
curl -sSL https://raw.githubusercontent.com/alexanderfrey/tailbus/main/install.sh | sh

# Machine A: start coord + daemon, register agents
tailbus-coord -config coord.toml
tailbusd -config machine-a.toml       # after replacing __COORD_IP__ and __MY_IP__
./register-agents.sh machine-a

# Machine B & C: start daemon, register agents
tailbusd -config machine-b.toml
./register-agents.sh machine-b

# From any machine: discover and interact
tailbus list                           # all 5 agents across the mesh
tailbus list booking                   # filter by tag
tailbus introspect flights             # full manifest
tailbus open concierge flights "Search NYC to London, Dec 20-27"
```

See [`examples/demo/README.md`](examples/demo/README.md) for the full step-by-step walkthrough.

## Configuration

Both the coord server and daemon accept TOML config files via `-config`. Example files are in `examples/dev/`.

### Coordination server (`tailbus-coord`)

```toml
listen_addr = ":8443"
data_dir = "/tmp/tailbus-coord"
key_file = "/tmp/tailbus-coord/coord.key"
# auth_tokens = ["changeme"]

# OAuth configuration (enables browser-based login)
oauth_http_addr = ":8080"
# jwt_secret = ""  # optional override, auto-generated if empty

# [[oauth_providers]]
# name = "google"
# issuer = "https://accounts.google.com"
# client_id = "YOUR_CLIENT_ID.apps.googleusercontent.com"
# client_secret = "YOUR_CLIENT_SECRET"
```

| Field | Default | Description |
|-------|---------|-------------|
| `listen_addr` | `:8443` | gRPC listen address |
| `data_dir` | `.` | Directory for SQLite database (pure-Go, no CGo) |
| `key_file` | `{data_dir}/coord.key` | Coord keypair file for mTLS (auto-generated if missing) |
| `auth_tokens` | `[]` | Pre-auth tokens for admission control; if set, nodes must present one to register |
| `oauth_http_addr` | `:8080` | HTTP listen address for OAuth endpoints (device flow + callback) |
| `jwt_secret` | (auto) | HMAC-SHA256 signing key for JWTs; auto-generated at `{data_dir}/jwt.key` if empty |
| `oauth_providers` | `[]` | OIDC providers for browser login (see example above) |

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `-health-addr` | `:8080` | Health/readiness/pprof HTTP endpoint (empty string disables) |
| `-auth-token` | (none) | Comma-separated pre-auth tokens (merged with config file tokens) |

### Relay server (`tailbus-relay`)

```toml
relay_id = "relay-1"
coord_addr = "127.0.0.1:8443"
listen_addr = ":7443"
key_file = "/tmp/tailbus-relay.key"
# auth_token = "changeme"
```

| Field | Default | Description |
|-------|---------|-------------|
| `relay_id` | `relay-{hostname}` | Unique identifier for this relay |
| `coord_addr` | `127.0.0.1:8443` | Coordination server address |
| `listen_addr` | `:7443` | gRPC listen address for daemon connections |
| `key_file` | `/tmp/tailbus-relay-{id}.key` | Relay keypair file (auto-generated if missing) |
| `auth_token` | (none) | Auth token for coord admission control |

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `-health-addr` | `:8080` | Health/readiness/pprof HTTP endpoint (empty string disables) |
| `-auth-token` | (none) | Auth token for coord admission control |

### Node daemon (`tailbusd`)

```toml
node_id = "node-1"
coord_addr = "coord.tailbus.co:8443"
advertise_addr = "127.0.0.1:9443"
listen_addr = ":9443"
socket_path = "/tmp/tailbusd-1.sock"
key_file = "/tmp/tailbusd-node1.key"
metrics_addr = ":9090"
mcp_addr = ":8080"
# auth_token = "changeme"        # skip OAuth, use pre-shared token
# credential_file = "~/.tailbus/credentials.json"
```

| Field | Default | Description |
|-------|---------|-------------|
| `node_id` | hostname | Unique identifier for this node |
| `coord_addr` | `coord.tailbus.co:8443` | Coordination server address |
| `advertise_addr` | (required) | Address other daemons use to reach this node |
| `listen_addr` | `:9443` | P2P gRPC listen address |
| `socket_path` | `/tmp/tailbusd.sock` | Unix socket for local agent connections |
| `key_file` | `/tmp/tailbusd-{nodeID}.key` | Node keypair file (auto-generated if missing) |
| `metrics_addr` | `:9090` | Prometheus + health/pprof HTTP endpoint (empty string disables) |
| `mcp_addr` | (none) | MCP gateway HTTP listen address (empty string disables) |
| `auth_token` | (none) | Pre-shared auth token for coord; if set, skips OAuth login entirely |
| `credential_file` | `~/.tailbus/credentials.json` | Path to saved OAuth credentials |

All config fields can be overridden with command-line flags. Run any binary with `-help` to see available flags.

## CLI Reference

```
tailbus [flags] <command> [args]
```

**Global flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `-socket` | `/tmp/tailbusd.sock` | Path to local daemon Unix socket |

**Auth commands** (no daemon connection needed):

| Command | Usage | Description |
|---------|-------|-------------|
| `login` | `login [--coord addr]` | Run device auth flow, save credentials, print email |
| `logout` | `logout` | Remove saved credentials |
| `status` | `status` | Show login status, email, token expiry, daemon connection |

**Mesh commands** (requires running daemon):

| Command | Usage | Description |
|---------|-------|-------------|
| `register` | `register <handle> [-description "..."] [-tags "a,b"] [-version "1.0"]` | Register an agent handle with optional manifest |
| `introspect` | `introspect <handle>` | Show the full service manifest for a handle |
| `list` | `list [tags]` | List all handles, optionally filtered by comma-separated tags |
| `open` | `open <from> <to> <message>` | Open a new session with an initial message |
| `send` | `send <session-id> <from> <message>` | Send a message within an existing session |
| `subscribe` | `subscribe <handle>` | Stream incoming messages (blocks until Ctrl-C) |
| `resolve` | `resolve <session-id> <from> [message]` | Resolve (close) a session with an optional final message |
| `sessions` | `sessions <handle>` | List sessions involving a handle |
| `dashboard` | `dashboard` | Launch interactive TUI dashboard |
| `trace` | `trace <trace-id>` | Display distributed trace spans for a trace ID |
| `agent` | `agent` | Stdio JSON-lines bridge for scripting and LLM agents |

## Distributed Tracing

Every session is assigned a `trace_id` (auto-generated UUID, or agent-provided for external correlation). The trace ID propagates on every envelope in that session. Spans are recorded at each hop:

| Action | Where | Description |
|--------|-------|-------------|
| `MESSAGE_CREATED` | AgentServer | Message created (open, send, or resolve) |
| `ROUTED_LOCAL` | MessageRouter | Delivered to a local subscriber |
| `ROUTED_REMOTE` | MessageRouter | Forwarded to a remote peer |
| `SENT_TO_TRANSPORT` | GRPCTransport | Successfully sent over P2P stream |
| `RECEIVED_FROM_TRANSPORT` | Daemon | Received from P2P stream |
| `DELIVERED_TO_SUBSCRIBER` | AgentServer | Delivered to agent subscriber channel |

Spans are stored in an in-memory ring buffer (10,000 spans per node). Query via CLI:

```bash
$ ./bin/tailbus trace 1ee8ae5a-61fe-495f-a9e0-ae4395ef2f40

Trace 1ee8ae5a-61fe-495f-a9e0-ae4395ef2f40 (6 spans):

  15:51:35.345  TRACE_ACTION_MESSAGE_CREATED      msg:42c03d06  node:node-1
  15:51:35.346  TRACE_ACTION_SENT_TO_TRANSPORT     msg:42c03d06  node:node-1
  15:51:35.346  TRACE_ACTION_ROUTED_REMOTE         msg:42c03d06  node:node-1
  15:51:35.349  TRACE_ACTION_MESSAGE_CREATED       msg:9ceb8279  node:node-1
  15:51:35.349  TRACE_ACTION_SENT_TO_TRANSPORT     msg:9ceb8279  node:node-1
  15:51:35.349  TRACE_ACTION_ROUTED_REMOTE         msg:9ceb8279  node:node-1
```

Or programmatically via the `GetTrace` gRPC RPC on the AgentAPI.

> **Note:** `GetTrace` returns spans from the local node only. For cross-node traces, query each node's daemon separately.

## Prometheus Metrics

When `metrics_addr` is configured (default `:9090`), the daemon exposes `/metrics`, `/healthz`, `/readyz`, and `/debug/pprof/*` endpoints:

```bash
curl http://localhost:9090/metrics     # Prometheus metrics
curl http://localhost:9090/healthz     # → {"status":"ok"}
curl http://localhost:9090/readyz      # → {"status":"ready"} (after coord registration)
curl http://localhost:9090/debug/pprof/ # pprof index
```

**Counters** (read from ActivityBus atomics at scrape time, no double-counting):

| Metric | Description |
|--------|-------------|
| `tailbus_messages_routed_total` | Total messages routed (local + remote) |
| `tailbus_messages_delivered_local_total` | Messages delivered to local subscribers |
| `tailbus_messages_sent_remote_total` | Messages sent to remote peers |
| `tailbus_messages_received_remote_total` | Messages received from remote peers |
| `tailbus_sessions_opened_total` | Total sessions opened |
| `tailbus_sessions_resolved_total` | Total sessions resolved |

**Histograms:**

| Metric | Description |
|--------|-------------|
| `tailbus_message_routing_duration_seconds` | Time to route a message (resolve + deliver/send) |
| `tailbus_session_lifetime_seconds` | Duration from session open to resolve |

To disable metrics, pass `--metrics ""` or set `metrics_addr = ""` in the config file.

## TUI Dashboard

The interactive dashboard provides a real-time view of the local daemon:

```bash
./bin/tailbus dashboard
```

**Panels:**
- **Handles** — registered agent handles with subscriber counts
- **Peers** — remote nodes with connection status
- **Sessions** — open and resolved sessions
- **Activity** — live feed of message routes, session events, and registrations (includes trace ID prefixes)

**Keyboard shortcuts:**
- `q` / `Ctrl+C` — quit
- `r` — refresh status
- `c` — clear activity feed

## MCP Gateway

The MCP (Model Context Protocol) gateway lets any MCP-compatible LLM client discover and invoke tailbus agents as tools — no SDK needed.

Enable with `-mcp :8080` on the daemon (or `mcp_addr = ":8080"` in config). The gateway:

- Registers as an internal `_mcp_gateway` handle on the mesh
- Exposes all handles as MCP tools via `tools/list`
- Maps each handle's `CommandSpec` to an individual tool (e.g., `calculator.add`, `calculator.multiply`)
- Handles without commands get a generic tool named after the handle
- `tools/call` opens a session, sends the payload, waits for the response (30s timeout), and returns the result

**Endpoints:**

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/mcp` | JSON-RPC 2.0 requests (`initialize`, `tools/list`, `tools/call`, `ping`) |
| `GET`  | `/mcp` | SSE stream for server-initiated messages |
| `GET`  | `/api/agents` | REST: list all agents as JSON |
| `POST` | `/api/send` | REST: send a message to an agent (`{"handle":"…","message":"…"}`) |
| `GET`  | `/` | Web chat UI (embedded static files) |

**Example: configure as MCP server in Claude Desktop**

```json
{
  "mcpServers": {
    "tailbus": {
      "url": "http://localhost:8080/mcp"
    }
  }
}
```

## Architecture

```
proto/tailbus/v1/           Protocol buffer definitions
  messages.proto              Envelope, EnvelopeType, ServiceManifest, CommandSpec
  agent.proto                 AgentAPI service (local daemon <-> agents)
  coord.proto                 CoordinationAPI service (daemon <-> coord)
  transport.proto             NodeTransport service (daemon <-> daemon P2P)

internal/
  coord/                    Coordination server
    server.go                 gRPC server implementation
    store.go                  SQLite-backed persistence (nodes, handles, auth_tokens, users)
    registry.go               Node registration
    peermap.go                Peer map distribution
    jwt.go                    JWT issuing and validation (HMAC-SHA256)
    oauth.go                  RFC 8628 device authorization flow + OIDC
    admission.go              Node admission (pre-shared tokens + JWT)
    oauth_web/verify.html     Embedded verification page (go:embed)

  auth/                     Client-side authentication
    credentials.go            Persistent credential storage (~/.tailbus/credentials.json)
    device_flow.go            Device authorization client (RFC 8628)
    refresh.go                Token refresh logic

  daemon/                   Node daemon
    daemon.go                 Main orchestrator (wires all components)
    agentserver.go            AgentAPI gRPC server (Unix socket, handle binding)
    coordclient.go            Coord server gRPC client (mTLS + TOFU)
    router.go                 Message routing (local vs remote)
    acktracker.go             Delivery ACK tracking and retry
    msgstore.go               bbolt-backed persistence for sessions and pending messages
    activitybus.go            In-process pub/sub for observability
    tracestore.go             Ring buffer trace span storage
    metrics.go                Prometheus collector + HTTP server (includes health routes)

  mcp/                      MCP gateway + web UI
    gateway.go                HTTP+SSE server exposing handles as MCP tools
    embed.go                  go:embed for static web assets
    web/index.html            Single-page chat UI (HTML/CSS/JS)

  health/                   Health, readiness, and pprof endpoints

  transport/                P2P data plane
    transport.go              Transport interface
    grpc.go                   Bidirectional gRPC stream implementation (mTLS + relay fallback)
    peerverifier.go           Peer certificate verification against peer map + relays

  relay/                    NAT traversal relay
    server.go                 DERP-style relay: Exchange handler, stream mapping, forwarding

  handle/                   Handle resolution
  session/                  Session lifecycle state machine (with sequence counters)
  identity/                 Keypair generation, mTLS certs, and TOFU verification
  config/                   TOML configuration loading
```

### Message flow

1. Agent calls `OpenSession` / `SendMessage` / `ResolveSession` via Unix socket (ownership of `from_handle` is verified against the connection)
2. `AgentServer` creates the envelope with a monotonic sequence number, records a trace span, and passes it to `MessageRouter`
3. `MessageRouter` checks if destination handle is local:
   - **Local**: delivers directly to subscriber channels, records `ROUTED_LOCAL` span
   - **Remote**: resolves handle to peer address via `Resolver`, sends via `GRPCTransport` (mTLS), records `ROUTED_REMOTE` span, registers with `AckTracker`
4. `GRPCTransport` attempts direct P2P first. On failure, falls back to relay (stamps `relay_target_key` on the envelope). Failed direct addresses are cached for 60s.
5. Direct or relay: fires `OnSend` callback (`SENT_TO_TRANSPORT` span)
6. Remote daemon receives via `OnReceive` callback (`RECEIVED_FROM_TRANSPORT` span), delivers to local subscribers (`DELIVERED_TO_SUBSCRIBER` span)
7. On successful delivery, an `ACK` envelope is sent back to the sender; the `AckTracker` removes the message from pending (both in-memory and bbolt). Unacknowledged messages are retried (5s timeout, up to 3 retries). On daemon restart, pending messages and sessions are restored from bbolt and retries resume.

### Protocol

All inter-component communication uses **gRPC** with Protocol Buffers:

- **AgentAPI** — agents connect to their local daemon via **Unix socket** (with token auth and per-connection handle ownership enforcement)
- **CoordinationAPI** — daemons connect to the coord server via **mTLS over TCP** (TOFU cert pinning on first connect)
- **NodeTransport** — daemons connect to each other via **mTLS over TCP** (peer certs verified against the peer map); when direct connection fails, daemons connect to a relay server using the same `Exchange` stream and mTLS

## Stdio Agent Bridge

The `tailbus agent` subcommand provides a **JSON-lines stdio bridge** so any process can communicate with tailbus by reading/writing newline-delimited JSON on stdin/stdout — no protobuf or gRPC client needed. This is ideal for scripting languages, LLM tool-use agents, and quick integrations.

```bash
# Start the bridge (reads JSON commands from stdin, writes JSON to stdout, logs to stderr)
./bin/tailbus agent
```

### Protocol

**Inbound (stdin)** — one JSON object per line:

```jsonl
{"type":"register","handle":"marketing","manifest":{"description":"Marketing team agent","tags":["marketing","comms"],"version":"1.0","commands":[{"name":"campaign","description":"Run a campaign"}]}}
{"type":"register","handle":"simple-agent","description":"Legacy plain description"}
{"type":"introspect","handle":"sales"}
{"type":"list"}
{"type":"list","tags":["marketing"]}
{"type":"open","to":"sales","payload":"Need Q4 numbers"}
{"type":"open","to":"sales","payload":"{}","content_type":"application/json","trace_id":"my-id"}
{"type":"send","session":"<id>","payload":"Q4 revenue: $1.2M"}
{"type":"resolve","session":"<id>","payload":"Thanks!"}
{"type":"resolve","session":"<id>"}
{"type":"sessions"}
```

- `register` must be the first command (one registration per process)
- `manifest` on `register` is optional — structured description of capabilities (description, commands, tags, version)
- `description` on `register` is deprecated but still supported for backward compatibility
- `introspect` returns the full manifest for a handle (no registration required)
- `list` returns all known handles; optionally filter by `tags` array
- `describe` is still accepted as an alias for `introspect`
- `content_type` defaults to `text/plain` if omitted
- `trace_id` on `open` is optional (auto-generated if omitted)
- `payload` on `resolve` is optional

**Outbound (stdout)** — one JSON object per line:

```jsonl
{"type":"registered","handle":"marketing"}
{"type":"introspected","handle":"sales","found":true,"manifest":{"description":"Sales team agent","tags":["sales"],"version":"1.0","commands":[{"name":"quote","description":"Get a quote"}]}}
{"type":"handles","entries":[{"handle":"marketing","manifest":{"description":"Marketing team agent","tags":["marketing"]}},{"handle":"sales","manifest":{"description":"Sales team agent","tags":["sales"]}}]}
{"type":"opened","session":"<id>","message_id":"<id>","trace_id":"<id>"}
{"type":"sent","message_id":"<id>"}
{"type":"resolved","message_id":"<id>"}
{"type":"message","session":"<id>","from":"sales","to":"marketing","payload":"hi","content_type":"text/plain","message_type":"session_open","trace_id":"<id>","message_id":"<id>","sent_at":1709391095}
{"type":"sessions","sessions":[{"session":"<id>","from":"a","to":"b","state":"open"}]}
{"type":"error","error":"session not found","request_type":"send"}
```

- Incoming messages from other agents appear as `type: "message"`
- `message_type` is one of: `session_open`, `message`, `session_resolve`, `ack`
- Errors include `request_type` for correlation
- The bridge exits on stdin EOF or SIGINT

### Example: Python agent (raw subprocess)

```python
import subprocess, json

proc = subprocess.Popen(
    ["./bin/tailbus", "-socket", "/tmp/tailbusd.sock", "agent"],
    stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True
)

def send(cmd):
    proc.stdin.write(json.dumps(cmd) + "\n")
    proc.stdin.flush()
    return json.loads(proc.stdout.readline())

print(send({"type": "register", "handle": "my-python-agent"}))
print(send({"type": "open", "to": "sales", "payload": "hello from python"}))
```

## Python SDK

The `tailbus` Python package (`sdk/python/`) wraps the stdio bridge in a clean async/sync API with zero external dependencies. Requires Python 3.10+.

### Install

```bash
pip install sdk/python/         # from source
# or: pip install tailbus       # once published to PyPI
```

### Async usage

```python
from tailbus import AsyncAgent, Manifest, CommandSpec
import asyncio

async def main():
    manifest = Manifest(
        description="Marketing agent",
        commands=(CommandSpec("campaign", "Run a campaign"),),
        tags=("marketing",),
    )
    async with AsyncAgent("marketing", manifest=manifest) as agent:
        await agent.register()

        # Discover other agents
        handles = await agent.list_handles()
        print([h.handle for h in handles])

        # Open a session and exchange messages
        opened = await agent.open_session("sales", "Need Q4 numbers")
        await agent.send(opened.session, "follow-up details")
        await agent.resolve(opened.session, "Thanks!")

asyncio.run(main())
```

### Sync usage

```python
from tailbus import SyncAgent

with SyncAgent("my-agent") as agent:
    agent.register()
    opened = agent.open_session("sales", "hello")
    agent.send(opened.session, "follow-up")
    agent.resolve(opened.session, "done")
```

### Handling incoming messages

```python
from tailbus import AsyncAgent, Message
import asyncio

async def main():
    async with AsyncAgent("responder") as agent:
        await agent.register()

        @agent.on_message
        async def handler(msg: Message):
            print(f"{msg.from_handle}: {msg.payload}")
            if msg.message_type == "session_open":
                await agent.send(msg.session, "got it!")
                await agent.resolve(msg.session)

        await agent.run_forever()

asyncio.run(main())
```

## @-Mention Auto-Routing

When a `text/*` message contains `@handle` patterns, the daemon automatically opens new sessions from the sender to each mentioned handle. This enables Twitter-style routing:

```bash
# Register two agents
./bin/tailbus -socket /tmp/tailbusd-1.sock register marketing
./bin/tailbus -socket /tmp/tailbusd-1.sock register sales

# Subscribe to incoming messages
./bin/tailbus -socket /tmp/tailbusd-1.sock subscribe sales &

# Send a message mentioning @sales — a new session is auto-opened to sales
./bin/tailbus -socket /tmp/tailbusd-1.sock open marketing planner "@sales can you send Q4 numbers?"
# sales subscriber receives a session_open with the full message
```

Rules:
- Only `text/*` content types are scanned for mentions
- The sender (`fromHandle`) and direct recipient (`toHandle`) are excluded from mention scanning
- Each mention opens an independent session with the original payload
- Mention routing is best-effort: failures are logged but never block the primary message

## Building External Agents

External agents connect to the local daemon's Unix socket using any gRPC client. The AgentAPI proto defines the full interface:

```protobuf
service AgentAPI {
  rpc Register(RegisterRequest) returns (RegisterResponse);
  rpc IntrospectHandle(IntrospectHandleRequest) returns (IntrospectHandleResponse);
  rpc ListHandles(ListHandlesRequest) returns (ListHandlesResponse);
  rpc OpenSession(OpenSessionRequest) returns (OpenSessionResponse);
  rpc SendMessage(SendMessageRequest) returns (SendMessageResponse);
  rpc Subscribe(SubscribeRequest) returns (stream IncomingMessage);
  rpc ResolveSession(ResolveSessionRequest) returns (ResolveSessionResponse);
  rpc ListSessions(ListSessionsRequest) returns (ListSessionsResponse);
  rpc GetNodeStatus(GetNodeStatusRequest) returns (GetNodeStatusResponse);
  rpc WatchActivity(WatchActivityRequest) returns (stream ActivityEvent);
  rpc GetTrace(GetTraceRequest) returns (GetTraceResponse);
}
```

Example in Go:

```go
// Read auth token (auto-generated by daemon)
opts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
if token, err := os.ReadFile("/tmp/tailbusd.sock.token"); err == nil {
    opts = append(opts, grpc.WithPerRPCCredentials(/* Bearer token credentials */))
}
conn, _ := grpc.NewClient("unix:///tmp/tailbusd.sock", opts...)
client := agentpb.NewAgentAPIClient(conn)

// Register with a service manifest
client.Register(ctx, &agentpb.RegisterRequest{
    Handle: "my-agent",
    Manifest: &messagepb.ServiceManifest{
        Description: "My agent",
        Tags:        []string{"demo"},
    },
})

// Subscribe to messages
stream, _ := client.Subscribe(ctx, &agentpb.SubscribeRequest{Handle: "my-agent"})
for {
    msg, _ := stream.Recv()
    fmt.Printf("Got: %s\n", msg.Envelope.Payload)
}
```

## Development

```bash
# Run all tests (including integration)
make test-all

# Regenerate proto code after editing .proto files
make proto

# Build all binaries
make build
```

### Releasing

Releases are built by [goreleaser](https://goreleaser.com/) via GitHub Actions. To publish a new release:

```bash
git tag v0.1.0
git push --tags
```

This builds binaries for linux/darwin x amd64/arm64 and publishes them to GitHub Releases. The `install.sh` script automatically picks up the latest release.

To test the build locally:

```bash
goreleaser build --snapshot --clean
```

### Running integration tests only

```bash
go test ./internal/ -v -run TestEndToEnd
go test ./internal/ -v -run TestRelayEndToEnd
```

The integration tests spin up full topologies in-process: `TestEndToEnd` verifies the complete session lifecycle with direct P2P (coord + 2 daemons + 2 agents), and `TestRelayEndToEnd` verifies message delivery through the relay when direct P2P is unreachable.

## License

See LICENSE file for details.
