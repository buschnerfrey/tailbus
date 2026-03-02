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
                 Unix socket          Unix socket
                /     \                /     \
           agent-a   agent-b     agent-c   agent-d
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
- **mTLS-ready** — identity package generates keypairs and certs (MVP runs insecure gRPC)

## Install

One-liner install from GitHub Releases (Linux and macOS):

```bash
curl -sSL https://raw.githubusercontent.com/alexanderfrey/tailbus/main/install.sh | sh
```

This detects your OS/architecture, downloads the latest release, and installs all three binaries to `/usr/local/bin` (or `~/.local/bin` if no sudo).

## Prerequisites (building from source)

- **Go 1.25+** (no CGo required)
- **protoc** with `protoc-gen-go` and `protoc-gen-go-grpc` (only needed if modifying `.proto` files)

## Build

```bash
make build
```

This produces three binaries in `bin/`:

| Binary | Description |
|--------|-------------|
| `bin/tailbus-coord` | Coordination server (discovery + peer map) |
| `bin/tailbusd` | Node daemon (local agent server + P2P transport) |
| `bin/tailbus` | CLI tool for interacting with a local daemon |

Other Makefile targets:

```bash
make proto      # Regenerate protobuf Go code
make test       # Run unit tests
make test-all   # Run all tests including integration
make clean      # Remove binaries and generated code
```

## Quick Start

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
```

| Field | Default | Description |
|-------|---------|-------------|
| `listen_addr` | `:8443` | gRPC listen address |
| `data_dir` | `.` | Directory for SQLite database (pure-Go, no CGo) |

### Node daemon (`tailbusd`)

```toml
node_id = "node-1"
coord_addr = "127.0.0.1:8443"
advertise_addr = "127.0.0.1:9443"
listen_addr = ":9443"
socket_path = "/tmp/tailbusd-1.sock"
key_file = "/tmp/tailbusd-node1.key"
metrics_addr = ":9090"
```

| Field | Default | Description |
|-------|---------|-------------|
| `node_id` | hostname | Unique identifier for this node |
| `coord_addr` | `127.0.0.1:8443` | Coordination server address |
| `advertise_addr` | (required) | Address other daemons use to reach this node |
| `listen_addr` | `:9443` | P2P gRPC listen address |
| `socket_path` | `/tmp/tailbusd.sock` | Unix socket for local agent connections |
| `key_file` | `/tmp/tailbusd-{nodeID}.key` | Node keypair file (auto-generated if missing) |
| `metrics_addr` | `:9090` | Prometheus HTTP endpoint (empty string disables) |

All config fields can be overridden with command-line flags. Run any binary with `-help` to see available flags.

## CLI Reference

```
tailbus [flags] <command> [args]
```

**Global flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `-socket` | `/tmp/tailbusd.sock` | Path to local daemon Unix socket |

**Commands:**

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

When `metrics_addr` is configured (default `:9090`), the daemon exposes a `/metrics` endpoint:

```bash
curl http://localhost:9090/metrics
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
    store.go                  SQLite-backed persistence
    registry.go               Node registration
    peermap.go                Peer map distribution

  daemon/                   Node daemon
    daemon.go                 Main orchestrator (wires all components)
    agentserver.go            AgentAPI gRPC server (Unix socket)
    coordclient.go            Coord server gRPC client
    router.go                 Message routing (local vs remote)
    activitybus.go            In-process pub/sub for observability
    tracestore.go             Ring buffer trace span storage
    metrics.go                Prometheus collector + HTTP server

  transport/                P2P data plane
    transport.go              Transport interface
    grpc.go                   Bidirectional gRPC stream implementation

  handle/                   Handle resolution
  session/                  Session lifecycle state machine
  identity/                 Keypair generation and management
  config/                   TOML configuration loading
```

### Message flow

1. Agent calls `OpenSession` / `SendMessage` / `ResolveSession` via Unix socket
2. `AgentServer` creates the envelope, records a trace span, and passes it to `MessageRouter`
3. `MessageRouter` checks if destination handle is local:
   - **Local**: delivers directly to subscriber channels, records `ROUTED_LOCAL` span
   - **Remote**: resolves handle to peer address via `Resolver`, sends via `GRPCTransport`, records `ROUTED_REMOTE` span
4. `GRPCTransport` sends over bidirectional gRPC stream, fires `OnSend` callback (`SENT_TO_TRANSPORT` span)
5. Remote daemon receives via `OnReceive` callback (`RECEIVED_FROM_TRANSPORT` span), delivers to local subscribers (`DELIVERED_TO_SUBSCRIBER` span)

### Protocol

All inter-component communication uses **gRPC** with Protocol Buffers:

- **AgentAPI** — agents connect to their local daemon via **Unix socket**
- **CoordinationAPI** — daemons connect to the coord server via **TCP**
- **NodeTransport** — daemons connect to each other via **TCP** for P2P message exchange

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

### Example: Python agent

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
conn, _ := grpc.NewClient("unix:///tmp/tailbusd.sock",
    grpc.WithTransportCredentials(insecure.NewCredentials()))
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
```

The integration test spins up a full topology (coord server + 2 daemons + 2 agents) in-process and verifies the complete session lifecycle including tracing.

## License

See LICENSE file for details.
