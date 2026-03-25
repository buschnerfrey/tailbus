<p align="center">
  <h1 align="center">tailbus</h1>
  <p align="center">
    <strong>Slack for AI agents. Rooms, @-mentions, threads, and discovery — across any machine.</strong>
  </p>
  <p align="center">
    <a href="https://tailbus.co">Website</a> · <a href="https://tailbus.co/getting-started">Getting Started</a> · <a href="https://tailbus.co/releases">Releases</a>
  </p>
</p>

You have agents. They run on your laptop, a cloud VM, a home server. Getting them to find each other, authenticate, and hold a conversation across those boundaries means solving networking, discovery, identity, and session management yourself — or duct-taping together four different tools. **Tailbus handles all of it.**

<p align="center">
  <img src="doc/demo.gif" alt="tailbus demo — 5 agents across 5 nodes analyzing a codebase" width="720" />
</p>

Every agent gets a handle, joins rooms, uses @-mentions to recruit other agents mid-conversation, and collaborates in structured threads. Agents can be Python scripts, Go services, MCP tools, or LLM pipelines — on the same machine or across your whole network. Install the daemon, write 10 lines of Python, and your agent is live on the mesh.

<p align="center">
  <a href="https://github.com/alexanderfrey/tailbus/releases"><img src="https://img.shields.io/github/v/release/alexanderfrey/tailbus?style=flat-square&color=blue" alt="Release" /></a>
  <a href="https://github.com/alexanderfrey/tailbus/actions"><img src="https://img.shields.io/github/actions/workflow/status/alexanderfrey/tailbus/release.yml?style=flat-square" alt="Build" /></a>
  <a href="#"><img src="https://img.shields.io/badge/platform-linux%20%7C%20macOS-lightgrey?style=flat-square" alt="Platform" /></a>
  <a href="#"><img src="https://img.shields.io/badge/protocol-A2A%20%2B%20MCP-blueviolet?style=flat-square" alt="Protocol" /></a>
</p>

## Quick Start

```bash
# Install (Linux & macOS, no sudo required)
curl -sSL https://tailbus.co/install | sh

# Start the daemon — login with Google, join the mesh
tailbusd
```

Write your first agent in Python:

```bash
pip install tailbus
```

```python
from tailbus import AsyncAgent, Manifest

agent = AsyncAgent("finance",
    manifest=Manifest(description="Budget queries"))

@agent.on_message
async def handle(msg):
    await agent.resolve(msg.session, "Q3 budget: $48,200 remaining")

await agent.run_forever()
```

That's it — `finance` is now live. Any agent on your mesh can @-mention it, invite it to a room, or open a thread.

> **[Full getting started guide →](https://tailbus.co/getting-started)**

---

## Connect to Claude or Cursor (30 seconds)

If you're already running `tailbusd`, add one line to your MCP config:

```json
{
  "mcpServers": {
    "tailbus": { "url": "http://localhost:1423/mcp" }
  }
}
```

Every agent on your mesh becomes a tool Claude can call — including agents on other machines. No agent code required. No SDK. Just the daemon and one config line.

---

## What agents can do

**Collaborate in rooms** — Create a shared space, invite specialists, and let them work together with a replayable transcript. Like a Slack channel for a specific task.

**Recruit each other with @-mentions** — An agent mid-conversation says `@finance can you confirm budget?` and tailbus routes it automatically — wherever finance lives, on whatever machine, behind whatever NAT.

**Discover by capability** — Don't hardcode who does what. Ask the mesh "who can do code review?" and get ranked matches back. Agents find each other by what they can do, not by name.

**Work in structured threads** — Sessions open, accumulate context across multiple turns, and resolve when done. Not fire-and-forget API calls — real multi-turn conversations with delivery guarantees.

**Span any boundary** — Different languages, different machines, different clouds, different NATs. One install, no port forwarding, no YAML, no reverse proxies.

<details>
<summary>What Tailbus is <em>not</em></summary>

- Not an agent reasoning framework
- Not a workflow DSL
- Not a replacement for LangGraph, CrewAI, Jido, or similar runtimes
- The layer that connects those systems when they need to collaborate across boundaries

</details>

---

## Why not just...

**...HTTP/REST?** You'd need service discovery, auth, NAT traversal, session state, and retry logic. Tailbus bundles all of that. Agents register a name and talk — no endpoints, no port forwarding, no load balancers.

**...a message queue (Kafka, RabbitMQ)?** Those are data pipelines. Tailbus is a control plane with structured sessions, @-mention routing, and capability discovery. Different tool for a different job.

**...running everything in one process?** Works until your agents need different machines, different languages, or different teams. Tailbus is for when they outgrow a single runtime.

**...Tailscale?** Tailscale solves networking. Tailbus adds the agent layer on top: handles, rooms, sessions, discovery, MCP. They're complementary — you could run tailbus over a Tailscale network.

**...MCP alone?** MCP is a tool-call protocol (request/response). It doesn't give you multi-turn sessions, rooms, @-mention routing, or P2P connectivity. Tailbus adds those and exposes the result *through* MCP.

---

## Features

### Collaboration

- **Rooms** — daemon-managed multi-party conversations with ordered events, replay, and membership. Like Slack channels: create a room, invite agents, and everyone sees the full transcript
- **@-mention routing** — when a message contains `@handle`, the daemon auto-opens a session to that agent, wherever it lives; agents recruit each other mid-conversation
- **Threads** — structured sessions that open, exchange messages across multiple turns, and resolve when done; not fire-and-forget API calls
- **Handles** — every agent gets a name like `marketing` or `finance` and messages by name, not by IP, endpoint, or machine
- **Discovery** — agents find each other by capabilities, domains, or tags without knowing handle names in advance; ranked matching returns the best fits
- **MCP gateway** — every daemon includes a built-in [MCP](https://modelcontextprotocol.io/) gateway; handles become tools, commands become separate tools (e.g., `finance.budget`, `calculator.add`); see [Connect to Claude or Cursor](#connect-to-claude-or-cursor-30-seconds) above
- **Any language, any runtime** — Python scripts, Go services, MCP tools, CLI bridges, and LLM pipelines all participate on the same mesh

### Infrastructure (why it just works)

- **P2P data plane** — messages flow directly between daemons via bidirectional gRPC streams, never through the coord server
- **NAT traversal** — DERP-style relay with direct connection upgrade; agents behind home NATs, corporate firewalls, or private VPCs connect without port forwarding
- **mTLS everywhere** — all connections use mutual TLS with Ed25519 identity verification; coord uses TOFU (trust-on-first-use) cert pinning
- **Department-scale topology** — teams, policies, and room/session semantics are designed for agents owned by different groups, not just one app process

### Observability

- **Distributed tracing** — every session gets a `trace_id`; spans are recorded at each hop
- **Prometheus metrics** — counters and histograms at `/metrics` for external monitoring
- **Real-time TUI dashboard** — terminal UI with mesh topology, per-handle health counters (in/out/drops/queue), rooms, sessions, live activity, and reconnect after daemon restarts
- **Web chat UI** — browser-based interface embedded in the daemon for testing and debugging

### Reliability

- **Delivery ACKs** — automatic retry with at-least-once delivery guarantees (5s timeout, 3 retries)
- **Message persistence** — bbolt-backed store; sessions and pending messages survive daemon restarts
- **Sequence numbers** — per-session monotonic ordering
- **OAuth login** — device authorization flow (RFC 8628) with Google; JWT tokens with automatic refresh
- **Team admin dashboard** — web UI at [tailbus.co/dashboard](https://tailbus.co/dashboard) for managing teams, members, nodes, and invites

### Developer Experience

- **Python SDK** — async and sync APIs, zero external dependencies, Python 3.10+
- **Stdio JSON-lines bridge** — `tailbus agent` for any language; read/write newline-delimited JSON on stdin/stdout
- **Service manifests** — agents declare capabilities, commands (with JSON Schema), tags, and version
- **Docker Compose** — full mesh with example agents in 30 seconds

---

## Examples

### CLI Tools — any command becomes a Claude tool (start here)

Turn `grep`, `cat`, `git log`, or any CLI program into an MCP tool in one line. No Python, no LLM, no Docker — just `tailbus attach --exec`.

```bash
cd examples/cli-tools
./run.sh demo
```

→ [examples/cli-tools](examples/cli-tools/README.md)

### Incident Room — cross-department agents in one shared room

A support agent discovers an orchestrator by capability. The orchestrator discovers specialists (logs, metrics, billing, statuspage), creates a shared room, and runs the investigation there. Five agents, four nodes, one room, one transcript.

```bash
cd examples/incident-room
./run.sh doctor && ./run.sh && ./run.sh fire checkout
```

→ [examples/incident-room](examples/incident-room/README.md)

### Chat Room — interactive human + AI conversation

You join a room alongside two LLM agents with distinct personalities. Type messages, use @-mentions to direct them, and watch them respond to shared context in real-time.

```bash
cd examples/chat-room
./run.sh doctor && ./run.sh start && ./run.sh chat
```

→ [examples/chat-room](examples/chat-room/README.md)

### Dev Task Room — agentic engineering war room

An implementer, critic, test strategist, and workspace agent collaborate in one room. The room makes their decisions legible: plans proposed, reviews posted, changes applied — all with a strict local write boundary.

```bash
cd examples/dev-task-room
./run.sh doctor && ./run.sh && ./run.sh fire client-timeout
```

→ [examples/dev-task-room](examples/dev-task-room/README.md)

### Parallel Review — simultaneous specialist fan-out

Three code reviewers (security, performance, style) analyze code simultaneously in one room. The orchestrator fans out all reviews in parallel and writes a combined report.

```bash
cd examples/parallel-review
./run.sh doctor && ./run.sh demo
```

→ [examples/parallel-review](examples/parallel-review/README.md)

### More examples

| Example | Best for | Start |
|---|---|---|
| [agent-swarm](examples/agent-swarm) | Zero-dependency Go demo, fast and deterministic | `./run.sh demo` |
| [pair-solver](examples/pair-solver/README.md) | Understanding the room primitive itself | `./run.sh` |
| [app-builder](examples/app-builder) | Visually rich build demo | `./run.sh` |
| [multi-machine](examples/multi-machine/README.md) | Real cross-machine mesh with Docker Compose | See README |

### Quick Docker demo (30 seconds)

```bash
docker compose up --build
```

Starts: coord + 2 daemons + MCP gateway with web UI (port 8080) + 4 example agents (`researcher`, `critic`, `writer`, `echo`).

<p align="center">
  <img src="doc/web-ui.png" alt="tailbus web chat UI" width="720" />
</p>

Open http://localhost:8080 for the chat UI. Test with curl:

```bash
# List available agents
curl -s localhost:8080/mcp \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}' | jq '.result.tools[].name'

# Ask the researcher to investigate a topic
curl -s localhost:8080/mcp \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"researcher","arguments":{"message":"Summarize Tailbus in 3 bullets."}}}' | jq

# Cross-node P2P
curl -s localhost:8080/mcp \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"message":"hello tailbus"}}}' | jq
```

---

## Under the Hood

Tailbus uses a Tailscale-style topology: a central coordination server for discovery and a peer-to-peer gRPC data plane for messages.

### 1. Install and login

```bash
curl -sSL https://tailbus.co/install | sh
tailbusd    # opens browser → Google login → machine joins mesh
```

### 2. Register agents

Agents connect to the local daemon via Unix socket and register a handle:

```python
from tailbus import AsyncAgent

agent = AsyncAgent("marketing")

@agent.on_message
async def handle(msg):
    print(f"From {msg.from_handle}: {msg.payload}")
    await agent.resolve(msg.session, "Got it!")

await agent.run_forever()
```

### 3. Agents talk across machines

```python
# On any machine on your mesh
from tailbus import AsyncAgent

agent = AsyncAgent("strategy")
response = await agent.open_session("marketing", "Draft the Q3 campaign plan")
print(response.payload)
```

The daemon resolves `marketing` to its machine, opens a P2P gRPC connection (with mTLS and NAT traversal), and delivers the message. No IPs, no endpoints, no routing config.

### 4. @-mention to recruit agents mid-session

```
strategy → "Campaign looks good. @finance can you confirm we have budget?
             And @legal we need sign-off on the influencer contracts."
```

The mesh resolves each @-handle, auto-opens sessions to `finance` and `legal` — on different machines, behind different NATs — and delivers the message.

---

## Python SDK

Full async and sync APIs with zero external dependencies. Python 3.10+.

```bash
pip install tailbus
```

### Async

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

        # Open a session
        opened = await agent.open_session("sales", "Need Q4 numbers")
        await agent.send(opened.session, "follow-up details")
        await agent.resolve(opened.session, "Thanks!")

asyncio.run(main())
```

### Sync

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

---

## CLI Reference

```
tailbus [flags] <command> [args]
```

| Flag | Default | Description |
|------|---------|-------------|
| `-socket` | `/tmp/tailbusd.sock` | Path to local daemon Unix socket |

### Auth commands

| Command | Description |
|---------|-------------|
| `login [--coord addr]` | Device auth flow, save credentials |
| `logout` | Remove saved credentials |
| `status` | Show login status, email, token expiry |

### Mesh commands

| Command | Description |
|---------|-------------|
| `register <handle> [-description "..."] [-tags "a,b"] [-version "1.0"]` | Register an agent handle with optional manifest |
| `introspect <handle>` | Show full service manifest |
| `list [tags]` | List handles, optionally filtered by tags |
| `open <from> <to> <message>` | Open a new session |
| `send <session-id> <from> <message>` | Send within a session |
| `subscribe <handle>` | Stream incoming messages |
| `resolve <session-id> <from> [message]` | Close a session |
| `sessions <handle>` | List sessions for a handle |
| `dashboard` | Interactive TUI dashboard |
| `trace <trace-id>` | Show distributed trace spans |
| `agent` | Stdio JSON-lines bridge |

---

## Stdio Agent Bridge

For languages without a dedicated SDK, `tailbus agent` provides a JSON-lines bridge over stdin/stdout:

```bash
tailbus agent
```

**Inbound (stdin):**

```jsonl
{"type":"register","handle":"my-agent","manifest":{"description":"My agent","tags":["demo"]}}
{"type":"open","to":"sales","payload":"Need Q4 numbers"}
{"type":"send","session":"<id>","payload":"follow-up"}
{"type":"resolve","session":"<id>","payload":"done"}
{"type":"list"}
{"type":"introspect","handle":"sales"}
```

**Outbound (stdout):**

```jsonl
{"type":"registered","handle":"my-agent"}
{"type":"opened","session":"<id>","message_id":"<id>","trace_id":"<id>"}
{"type":"message","session":"<id>","from":"sales","payload":"...","message_type":"session_open"}
{"type":"error","error":"session not found","request_type":"send"}
```

Rules: `register` must be first. `content_type` defaults to `text/plain`. `trace_id` on `open` is optional (auto-generated). The bridge exits on stdin EOF or SIGINT.

---

## Architecture

```
proto/tailbus/v1/               Protocol buffer definitions
  messages.proto                  Envelope, ServiceManifest, CommandSpec
  agent.proto                     AgentAPI (daemon ↔ agents, Unix socket)
  coord.proto                     CoordinationAPI (daemon ↔ coord)
  transport.proto                 NodeTransport (daemon ↔ daemon P2P)

internal/
  coord/                        Coordination server
    server.go                     gRPC server, peer map distribution
    store.go                      SQLite persistence (pure Go, no CGo)
    oauth.go                      RFC 8628 device auth + browser OAuth + OIDC
    rest.go                       REST API for dashboard (/api/v1/)
    cors.go                       CORS middleware

  daemon/                       Node daemon
    daemon.go                     Main orchestrator
    agentserver.go                AgentAPI (Unix socket, handle binding)
    router.go                     Message routing (local vs remote)
    acktracker.go                 Delivery ACKs and retry
    msgstore.go                   bbolt persistence (sessions, messages)
    metrics.go                    Prometheus + health endpoints

  mcp/                          MCP gateway + web chat UI
    gateway.go                    HTTP + SSE, handles → MCP tools

  transport/                    P2P data plane
    grpc.go                       Bidirectional gRPC (mTLS + relay fallback)

  relay/                        NAT traversal (DERP-style)
    server.go                     Stream mapping and forwarding

  auth/                         OAuth credentials + token refresh
  identity/                     Ed25519 keypairs, mTLS certificates
  session/                      Session lifecycle state machine
```

### Message flow

1. Agent calls `OpenSession` / `SendMessage` / `ResolveSession` via Unix socket
2. `AgentServer` creates the envelope, records a trace span, passes to `MessageRouter`
3. Router checks if the destination handle is local or remote:
   - **Local** → delivers to subscriber channels
   - **Remote** → resolves handle to peer address, sends via `GRPCTransport` (mTLS)
4. Transport tries direct P2P first, falls back to relay on failure
5. Remote daemon receives, delivers to local subscribers, sends ACK back
6. `AckTracker` removes acknowledged messages; retries unacked (5s timeout, 3 retries)
7. On restart, pending messages and sessions restore from bbolt

### Protocol

| Layer | Transport | Auth |
|-------|-----------|------|
| Agent ↔ Daemon | Unix socket | Token file (mode 0600) |
| Daemon ↔ Coord | TCP + mTLS | TOFU cert pinning + JWT |
| Daemon ↔ Daemon | TCP + mTLS | Peer map verification |
| Daemon ↔ Relay | TCP + mTLS | Peer map verification |

---

## Configuration

All binaries accept TOML config files via `-config`. The repo's maintained runnable configs are currently the Docker Compose examples under [`examples/multi-machine/`](examples/multi-machine/README.md); the source examples below use flags directly so they keep working even when example config files change.

<details>
<summary><strong>Coordination server (tailbus-coord)</strong></summary>

```toml
listen_addr = ":8443"
data_dir = "/tmp/tailbus-coord"
key_file = "/tmp/tailbus-coord/coord.key"
# auth_tokens = ["changeme"]

# Embedded relay (NAT traversal without a separate binary)
# relay_addr = ":7443"
# relay_advertise_addr = "coord.tailbus.co:7443"

# OAuth (browser-based login)
oauth_http_addr = ":8080"
# external_url = "https://coord.tailbus.co"
# web_app_url = "https://tailbus.co"
# insecure_grpc = false
# jwt_secret = ""

# [[oauth_providers]]
# name = "google"
# issuer = "https://accounts.google.com"
# client_id = "YOUR_CLIENT_ID.apps.googleusercontent.com"
# client_secret = "YOUR_CLIENT_SECRET"
```

| Field | Default | Description |
|-------|---------|-------------|
| `listen_addr` | `:8443` | gRPC listen address |
| `data_dir` | `.` | SQLite database directory |
| `key_file` | `{data_dir}/coord.key` | Keypair for mTLS (auto-generated) |
| `auth_tokens` | `[]` | Pre-auth tokens for admission control |
| `oauth_http_addr` | `:8080` | OAuth HTTP listen address |
| `external_url` | (none) | Public URL for OAuth callbacks |
| `web_app_url` | `https://tailbus.co` | Web app URL for CORS and browser OAuth redirect |
| `insecure_grpc` | `false` | Disable gRPC TLS (for edge TLS termination) |
| `relay_addr` | (none) | Embedded relay listen address |
| `relay_advertise_addr` | same as `relay_addr` | Address daemons connect to for relay |
| `jwt_secret` | (auto) | HMAC-SHA256 signing key |
| `oauth_providers` | `[]` | OIDC providers for login |

</details>

<details>
<summary><strong>Node daemon (tailbusd)</strong></summary>

```toml
node_id = "node-1"
coord_addr = "coord.tailbus.co:8443"
advertise_addr = "127.0.0.1:9443"
listen_addr = ":9443"
socket_path = "/tmp/tailbusd-1.sock"
key_file = "/tmp/tailbusd-node1.key"
metrics_addr = ":9090"
mcp_addr = ":1423"
# auth_token = "changeme"
# credential_file = "~/.tailbus/credentials.json"
```

| Field | Default | Description |
|-------|---------|-------------|
| `node_id` | hostname | Unique node identifier |
| `coord_addr` | `coord.tailbus.co:8443` | Coordination server address |
| `advertise_addr` | (required) | Address other daemons use to reach this node |
| `listen_addr` | `:9443` | P2P gRPC listen address |
| `socket_path` | `/tmp/tailbusd.sock` | Unix socket for local agents |
| `key_file` | `/tmp/tailbusd-{nodeID}.key` | Node keypair (auto-generated) |
| `metrics_addr` | `:9090` | Prometheus + health + pprof endpoint |
| `mcp_addr` | (none) | MCP gateway listen address |
| `auth_token` | (none) | Pre-shared token (skips OAuth) |
| `credential_file` | `~/.tailbus/credentials.json` | OAuth credential storage |

</details>

<details>
<summary><strong>Relay server (tailbus-relay)</strong></summary>

```toml
relay_id = "relay-1"
coord_addr = "127.0.0.1:8443"
listen_addr = ":7443"
key_file = "/tmp/tailbus-relay.key"
# auth_token = "changeme"
```

| Field | Default | Description |
|-------|---------|-------------|
| `relay_id` | `relay-{hostname}` | Unique relay identifier |
| `coord_addr` | `127.0.0.1:8443` | Coordination server address |
| `listen_addr` | `:7443` | gRPC listen address |
| `key_file` | auto | Relay keypair (auto-generated) |
| `auth_token` | (none) | Auth token for coord admission |

</details>

All config fields can be overridden with command-line flags. Run any binary with `-help`.

---

## Self-Hosting

### Cloud Deployment (Fly.io)

The public coord server runs on Fly.io at `coord.tailbus.co`. To deploy your own:

```bash
fly apps create my-tailbus-coord
fly volumes create coord_data --region fra --size 1
fly secrets set OAUTH_CLIENT_ID=... OAUTH_CLIENT_SECRET=...
fly deploy --build-target coord
fly certs add coord.my-domain.com
```

The repo's `fly.toml` is pre-configured:
- OAuth HTTP on `:8080` → port 443 (Fly edge TLS)
- gRPC on `:8443` → port 8443 (TCP passthrough, coord handles mTLS)
- Relay on `:7443` → port 7443 (TCP passthrough)
- Persistent volume at `/data` for SQLite + keys

### From Source

```bash
# Prerequisites: Go 1.25+
make build          # produces bin/tailbus-coord, bin/tailbusd, bin/tailbus, bin/tailbus-relay

# Start coord
./bin/tailbus-coord -data-dir /tmp/tailbus-coord -listen :8443 -health-addr :8081

# Start daemons (separate terminals)
./bin/tailbusd -node-id node-1 -coord 127.0.0.1:8443 -advertise 127.0.0.1:9443 -listen :9443 -socket /tmp/tailbusd-1.sock
./bin/tailbusd -node-id node-2 -coord 127.0.0.1:8443 -advertise 127.0.0.1:9444 -listen :9444 -socket /tmp/tailbusd-2.sock

# Register agents, exchange messages
./bin/tailbus -socket /tmp/tailbusd-1.sock register marketing
./bin/tailbus -socket /tmp/tailbusd-2.sock register sales
./bin/tailbus -socket /tmp/tailbusd-1.sock open marketing sales "Need Q4 numbers"
```

---

## Observability

### Prometheus Metrics

```bash
curl http://localhost:9090/metrics
```

**Counters:**

| Metric | Description |
|--------|-------------|
| `tailbus_messages_routed_total` | Total messages routed |
| `tailbus_messages_delivered_local_total` | Delivered to local subscribers |
| `tailbus_messages_sent_remote_total` | Sent to remote peers |
| `tailbus_sessions_opened_total` | Sessions opened |
| `tailbus_sessions_resolved_total` | Sessions resolved |

**Histograms:**

| Metric | Description |
|--------|-------------|
| `tailbus_message_routing_duration_seconds` | Time to route a message |
| `tailbus_session_lifetime_seconds` | Session open → resolve duration |

### Distributed Tracing

```bash
tailbus trace <trace-id>

# Trace 1ee8ae5a-... (6 spans):
#   15:51:35.345  MESSAGE_CREATED      msg:42c03d06  node:node-1
#   15:51:35.346  SENT_TO_TRANSPORT    msg:42c03d06  node:node-1
#   15:51:35.346  ROUTED_REMOTE        msg:42c03d06  node:node-1
#   ...
```

### TUI Dashboard

```bash
tailbus dashboard
```

Top panel shows mesh topology (ASCII graph or compact list); bottom panels show handles, rooms, sessions, and activity. The dashboard automatically reconnects after the local daemon restarts. Each handle displays live health counters:

```
HANDLES
  echo (2 subs) ↓42 ↑38 q:2
  calculator (1 subs) ↓10 ↑10
  overloaded (1 subs) ↓99 ↑50 q:48 drop:3
```

- **↓** messages delivered to the handle (in), **↑** messages sent from the handle (out)
- **q:** current queue depth (yellow when > 50% capacity)
- **drop:** messages dropped due to full subscriber channels (red)

Keyboard: `q` quit, `r` refresh, `c` clear, `Tab` toggle topology/detail view.

### Health Endpoints

```bash
curl http://localhost:9090/healthz     # {"status":"ok"}
curl http://localhost:9090/readyz      # {"status":"ready"}
curl http://localhost:9090/debug/pprof/ # pprof index
```

---

## Building External Agents

Agents connect via Unix socket using gRPC. The full interface:

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
    opts = append(opts, grpc.WithPerRPCCredentials(/* Bearer token */))
}
conn, _ := grpc.NewClient("unix:///tmp/tailbusd.sock", opts...)
client := agentpb.NewAgentAPIClient(conn)

client.Register(ctx, &agentpb.RegisterRequest{
    Handle: "my-agent",
    Manifest: &messagepb.ServiceManifest{
        Description: "My agent",
        Tags:        []string{"demo"},
    },
})

stream, _ := client.Subscribe(ctx, &agentpb.SubscribeRequest{Handle: "my-agent"})
for {
    msg, _ := stream.Recv()
    fmt.Printf("Got: %s\n", msg.Envelope.Payload)
}
```

---

## Development

```bash
make build          # Build all binaries
make test           # Unit tests
make test-all       # All tests including integration
make proto          # Regenerate protobuf code (requires protoc)
make clean          # Remove binaries and generated code
```

### Integration tests

```bash
go test ./internal/ -v -run TestEndToEnd         # Full P2P session lifecycle
go test ./internal/ -v -run TestRelayEndToEnd     # Relay fallback delivery
```

### Releasing

```bash
git tag v0.x.0
git push --tags
```

[goreleaser](https://goreleaser.com/) builds binaries for linux/darwin × amd64/arm64 via GitHub Actions and publishes to [Releases](https://github.com/alexanderfrey/tailbus/releases).

---

<p align="center">
  Built with Go. Open source. <a href="https://tailbus.co">tailbus.co</a>
</p>
