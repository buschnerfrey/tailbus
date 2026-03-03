# Tailbus → "Tailscale for Agents" Roadmap

## Where we are today

Working MVP with real security, NAT traversal, persistence, and MCP integration: coord server + node daemons + P2P gRPC transport + relay server + CLI + TUI dashboard + Prometheus metrics + distributed tracing + stdio bridge + MCP gateway + Docker Compose. **Phase 1 hardening complete:** mTLS on all connections (P2P, relay, and coord), per-connection handle ownership on the Unix socket, Unix socket token auth, coord admission control (pre-auth tokens), per-session sequence numbers, and delivery ACKs with retry. **Phase 2 reliability:** message persistence via bbolt — sessions and pending messages survive daemon restarts. **Phase 3 NAT traversal:** DERP-style relay server enables message delivery across NAT boundaries. **Phase 4 SDKs:** Python SDK (async/sync, zero deps) wrapping the stdio bridge. **Phase 5 protocol bridges:** MCP gateway exposes handles as MCP tools — any MCP-compatible LLM can use tailbus agents. **Phase 10 deployment:** Docker Compose with coord + 2 daemons + MCP gateway + 3 example agents.

**What works:**
- Agents register handles, open sessions, exchange messages, resolve conversations
- All daemon-to-daemon traffic is mTLS with Ed25519 peer verification against the peer map
- All daemon-to-coord traffic is mTLS with TOFU cert pinning
- DERP-style relay server for NAT traversal — daemons try direct P2P first, fall back to relay transparently
- Handle ownership is enforced per Unix socket connection (no impersonation)
- Unix socket token auth — daemon generates a random token file (mode 0600); CLI and agents present it automatically
- Coord admission control — pre-auth token system gates node registration; open mode (no tokens) preserves zero-config default
- Every envelope carries a monotonic sequence number; delivered messages generate ACKs; unacked messages retry (5s timeout, 3 retries)
- Message persistence — bbolt-backed store on each daemon; sessions and pending messages survive restart; ACKed messages purged automatically
- MCP gateway — HTTP server on daemon exposing handles as MCP tools; `tools/list` returns handle manifests, `tools/call` opens session + sends + waits for response
- Docker Compose — `docker compose up` gives you a full mesh with coord + 2 daemons + MCP gateway + 3 example Python agents
- `/healthz`, `/readyz`, and `/debug/pprof/*` endpoints on daemon metrics port, coord, and relay
- Python SDK (`sdk/python/`) — `AsyncAgent` and `SyncAgent` with zero external dependencies
- Service manifests, @-mention routing, tracing, Prometheus metrics, TUI dashboard

**What's missing for real adoption:**
- No ACLs — unrestricted any-to-any messaging; need handle-level and tag-based policies
- Python SDK only; still no TypeScript/native Go SDKs
- No federation — `name@domain` is parsed but routing is single-coord only
- No OIDC/SSO identity — nodes authenticate with keypairs, not corporate IdPs

---

## Pillar 1: Everything Encrypted, Zero Config

*Tailscale's #1 insight: security should be the default, not an opt-in.*

### ~~P1.1 — P2P mTLS~~ ✓ DONE
- `GRPCTransport` accepts TLS cert + `PeerVerifier`
- Server requires client certs; client verifies server cert against peer map
- `PubKeyFromCert` extracts Ed25519 identity from cert Organization[0]
- `ResolverVerifier` validates peer certs against the handle resolver

### ~~P1.2 — Daemon-to-coord mTLS~~ ✓ DONE
- Coord server generates keypair, serves with mTLS
- `TOFUVerifier` saves cert fingerprint on first use, rejects mismatches
- Daemon's CoordClient uses mTLS + TOFU for coord connection

### ~~P1.3 — Unix socket handle binding~~ ✓ DONE
- `connTracker` stats.Handler assigns unique ID per gRPC connection
- `Register` binds handles to connections; `verifyOwnership` guards all RPCs
- Disconnect auto-cleans handles and subscriber channels

### ~~P1.4 — Unix socket authentication~~ ✓ DONE
- Daemon generates a 32-byte random auth token on `ServeUnix`, writes to `<socket>.token` (mode 0600)
- gRPC unary + stream interceptors verify `Bearer <token>` from metadata
- CLI and agent bridge auto-read the token file and attach per-RPC credentials
- Empty token = no auth (test mode / backward compat)
- Token file cleaned up on `GracefulStop`

### ~~P1.5 — Coord admission control~~ ✓ DONE
- Pre-auth token system (like `tailscale up --authkey`): coord generates tokens, nodes present them at first registration
- Tokens stored as SHA-256 hashes in SQLite; supports single-use and expiring tokens
- Open mode preserved: if no tokens configured, all registrations allowed (zero-config default)
- Closed mode: if any tokens exist, `RegisterNodeRequest.auth_token` must match one
- `-auth-token` flag and `auth_tokens`/`auth_token` config fields on coord, daemon, and relay

### P1.6 — Handle namespace policies
- **Problem:** handle names are first-come-first-served with no revocation or reservation
- Coord-level handle reservations (config or API)
- Namespacing by org/team prefix (e.g., `team-a/billing`)
- Handle ownership records with expiration

---

## Pillar 2: Messages Don't Disappear

*The difference between a demo and production: delivery guarantees.*

### ~~P2.1 — Sequence numbers~~ ✓ DONE
- `uint64 sequence` and `string ack_id` fields on Envelope proto
- Per-session monotonic counter starting at 1
- Assigned in OpenSession, SendMessage, ResolveSession

### ~~P2.2 — ACK flow~~ ✓ DONE
- `AckTracker` tracks pending remote sends (5s timeout, 3 max retries)
- `DeliverToLocal` generates ACK envelopes back to sender
- Router tracks remote sends; never ACKs an ACK

### P2.3 — Backpressure
- **Problem:** subscriber channels (buffered 64) silently drop messages when full. Activity bus and peer map watcher channels do the same. This is a data loss vector with no signal to the sender.
- Replace silent drops with explicit slow-consumer advisory (`ENVELOPE_TYPE_NACK`)
- Configurable per-subscriber buffer depth (surface in manifest)
- Credit-based flow control on the `Exchange` stream (send permits)
- Observable metrics: drop count, buffer utilization per handle

### ~~P2.4 — Message persistence (store-and-forward)~~ ✓ DONE
- bbolt-backed `MessageStore` on each daemon (`internal/daemon/msgstore.go`)
- Pending outbound messages persisted until ACKed — survives daemon restart
- Sessions persisted via `SetPersistence` callbacks on the session store
- On startup: restores sessions to in-memory store, restores pending messages to ACK tracker for retry
- Resolved sessions evicted from disk on TTL sweep
- ACKs drive the persistence lifecycle: `Acknowledge()` removes from both in-memory and bbolt

### P2.5 — Dead letter queue
- Messages to unreachable handles after all retries → DLQ (local SQLite or file)
- CLI: `tailbus dlq list`, `tailbus dlq replay <session-id>`
- Auto-retry when handle comes back online (watch peer map for reappearance)

---

## Pillar 3: NAT Traversal — It Just Works

*This is the single highest-leverage missing feature. Without it, tailbus only works on a LAN. Tailscale's breakthrough was "it just works, even behind NAT."*

### ~~P3.1 — DERP-style relay server~~ ✓ DONE
- `tailbus-relay` binary: lightweight envelope forwarder using the existing `Exchange` bidirectional stream
- Relay identifies peers by mTLS cert pubkey, maps pubkeys to Exchange streams, forwards envelopes via `relay_target_key` field
- Coord distributes relay addresses in `PeerMapUpdate.relays` (separate from peers); relays register with `is_relay=true`
- `GRPCTransport.Send()` tries direct P2P first, falls back to relay on failure; failed direct addrs cached 60s
- Daemons proactively connect to all known relays (via `ConnectToRelays`) so they can receive forwarded messages
- Trust chain: `Daemon A ↔ mTLS ↔ Relay ↔ mTLS ↔ Daemon B` — each hop authenticated via `ResolverVerifier`

### P3.2 — Direct connection probing + upgrade
- On peer map update, attempt direct TCP to new peer
- If direct fails within timeout, mark peer as relay-only
- Periodically re-probe for direct (every 60s)
- Seamless upgrade: start on relay, upgrade to direct transparently
- The `peerConn` abstraction in `grpc.go` is the natural place for this

### P3.3 — NAT hole-punching (stretch)
- UDP-based STUN for NAT type detection
- Symmetric NAT → relay; other NATs → attempt hole-punch via coord-mediated exchange
- Fall back to relay on failure — relay is always the safety net

---

## Pillar 4: Polyglot SDKs

*If it's not easy to integrate from Python/TS, it won't get adopted. 90% of AI agents are Python.*

### ~~P4.1 — Python SDK (Phase 1: subprocess bridge)~~ ✓ DONE
- `sdk/python/` — zero-dependency async/sync Python SDK wrapping the stdio JSON-lines bridge
- `AsyncAgent` (async/await) and `SyncAgent` (threading wrapper) with context manager support
- Full protocol coverage: register (with manifests), open/send/resolve sessions, introspect, list handles/sessions
- `@agent.on_message` decorator for incoming messages, `run_forever()` for long-running agents
- FIFO future correlation for request/response; interleaved `Message` events routed to handler
- Exception hierarchy: `BridgeError`, `BridgeDiedError`, `NotRegisteredError`, `AlreadyRegisteredError`
- 46 tests (protocol serialization, async agent with mocked subprocess, sync wrapper)
- Phase 2 (future): native gRPC client using generated protobuf stubs for better performance

### P4.2 — TypeScript/Node SDK
- `npm install @tailbus/sdk`
- Same phased approach: stdio bridge first, native gRPC later
- Promise-based API with async iterators for subscriptions

### P4.3 — Go SDK (native gRPC)
- `go get github.com/alexanderfrey/tailbus/sdk/go`
- Direct gRPC to Unix socket (no subprocess overhead)
- Context-aware, integrates with Go cancellation patterns

### P4.4 — Rust SDK
- `cargo add tailbus`
- Native gRPC via tonic
- Tokio async runtime integration

### P4.5 — SDK feature parity contract
- All SDKs must support: register, subscribe, open, send, resolve, list, introspect
- Shared integration test suite (language-agnostic test harness using the CLI)
- Versioned SDK ↔ daemon compatibility matrix

---

## Pillar 5: Protocol Bridges — Meet Agents Where They Are

*The mesh is only valuable if external agents can join it.*

### P5.1 — A2A gateway
- HTTP endpoint on daemon speaking Google's Agent-to-Agent protocol
- Tasks ↔ sessions translation (A2A task lifecycle maps to session lifecycle)
- Auto-generate AgentCards from ServiceManifest data
- Bridge external A2A agents as synthetic handles in the mesh

### ~~P5.2 — MCP gateway~~ ✓ DONE
- HTTP server on daemon (`internal/mcp/gateway.go`) exposing handles as MCP tools
- JSON-RPC 2.0 over HTTP POST at `/mcp` (MCP streamable HTTP transport)
- SSE endpoint at `GET /mcp` for server-initiated messages
- `tools/list` → each handle's commands become individual MCP tools (e.g., `calculator.add`); handles without commands get a generic send tool
- `tools/call` → opens session to target agent, sends payload, waits for response (30s timeout), returns result
- Registers as internal `_mcp_gateway` handle for response routing
- Enable with `-mcp :8080` flag or `mcp_addr` in TOML config

### P5.3 — HTTP webhook bridge
- Register webhook URLs as handles: `tailbus webhook register --handle alerts --url https://...`
- Inbound: POST to webhook on message delivery
- Outbound: receive webhook POSTs and inject as messages
- Enables integration with Slack, PagerDuty, CI/CD, etc.

### P5.4 — WebSocket gateway
- Browser-friendly real-time connection to the mesh
- Enables web-based agent UIs, dashboards, chat interfaces
- Auth via JWT tokens issued by coord

---

## Pillar 6: Richer Agent Semantics

*Move beyond two-party request-response.*

### P6.1 — Multi-party sessions
- `parent_session_id` field in session for linked session trees
- Agent A opens session with B, B delegates to C with back-link
- Status propagation up the tree

### P6.2 — Error envelope
- `ENVELOPE_TYPE_ERROR` with structured `code` + `message` + `details`
- Standard error codes: `NOT_FOUND`, `PERMISSION_DENIED`, `RATE_LIMITED`, `INTERNAL`
- Agents can reject sessions cleanly

### P6.3 — Task delegation pattern
- First-class A→B→C delegation with progress tracking
- Parent session sees aggregated status from child sessions
- Timeout propagation: if parent times out, children are cancelled

### P6.4 — Fan-out & aggregation
- Orchestrator helper: open N parallel sub-sessions, collect results
- Configurable: wait-all, wait-first, wait-quorum
- Library in SDK, not a daemon feature

### P6.5 — Agent capability negotiation
- Extend ServiceManifest beyond static declarations
- Typed command parameters (not just JSON schema strings — think gRPC service reflection)
- Capability queries: "find me an agent that can do X" as a first-class coord operation
- Version-aware routing: route to latest version of a handle, or pin to specific

---

## Pillar 7: Policies & Access Control

*Tailscale ACLs as code. Tailbus needs the same.*

### P7.1 — Handle-level ACLs
- Declarative policy file (HuJSON like Tailscale, or YAML)
- Rules: `allow marketing -> sales`, `deny * -> admin`
- Enforced at the daemon level on `Route()`

### P7.2 — Tag-based policies
- Agents declare tags in manifest; policies reference tags
- `allow tag:frontend -> tag:backend`
- Wildcard, deny-by-default

### P7.3 — Rate limiting
- Per-handle message rate limits (configurable in policy)
- Per-session message count limits
- Daemon enforces, returns `RATE_LIMITED` error envelope

### P7.4 — Audit log
- Append-only log of all routing decisions
- Who sent what to whom, when, allowed/denied
- Exportable for compliance

---

## Pillar 8: Multi-Tenancy & Federation

*The `name@domain` format is already in the handle parser. Wire it up.*

### P8.1 — Domain isolation
- Coord server scoped to a domain (like a Tailscale tailnet)
- Handles are unique within a domain
- Config: `domain = "acme.com"` in coord config

### P8.2 — Federation (cross-domain)
- DNS SRV records for coord server discovery: `_tailbus._tcp.acme.com`
- When routing to `agent@otherdomain.com`, look up their coord via DNS
- Cross-domain trust policies (which orgs can message which)
- Proxy through relay or establish direct P2P with foreign node

### P8.3 — Org admin API
- REST API on coord for managing nodes, handles, policies
- Token-based auth for automation
- Foundation for a web admin console

---

## Pillar 9: Observability That Scales

*Current tracing is in-memory per-node. Need cross-cluster visibility.*

### P9.1 — OpenTelemetry export
- Replace custom trace store with OTEL SDK
- Export spans to Jaeger/Tempo/Datadog
- Trace ID propagation already works — just needs an OTEL bridge
- Cross-node trace correlation becomes automatic (currently requires querying each node)

### ~~P9.2 — Health endpoints + pprof~~ ✓ DONE
- `internal/health` package: `RegisterRoutes(mux, readyFn)` and `Serve(ctx, addr, readyFn, logger)`
- `/healthz` → always 200; `/readyz` → calls readyFn (503 if not ready); `/debug/pprof/*` for profiling
- Daemon: routes added to existing metrics HTTP server; readyFn set after coord registration
- Coord and relay: standalone health server via `-health-addr` flag (default `:8080`)

### P9.3 — Structured event log
- Every routing decision, connection, registration as structured JSON
- Configurable output: file, stdout, syslog
- Foundation for the audit log (P7.4)

### P9.4 — Grafana dashboard
- Pre-built dashboard JSON for the existing Prometheus metrics
- Panels: message throughput, session lifetime histograms, active handles, peer connectivity
- Alerting rules: peer disconnected, message drop rate, DLQ depth

### P9.5 — Web dashboard
- Replace TUI-only dashboard with a web UI
- Real-time topology visualization (nodes, handles, connections)
- Message flow animation, session inspector, trace viewer
- Served by daemon on configurable port

---

## Pillar 10: Deployment & Operations

*Must be trivially deployable anywhere.*

### ~~P10.1 — Docker images & compose~~ ✓ DONE
- Multi-stage `Dockerfile` with `coord`, `daemon`, and `relay` targets
- `docker-compose.yml`: coord + 2 daemons + MCP gateway (port 8080) + 3 example Python agents
- Example agents: `calculator` (tool-style with JSON Schema), `echo` (simplest agent), `orchestrator` (multi-agent delegation)
- `docker compose up --build` → full mesh in 30 seconds
- Cross-node messaging demonstrated: echo agent on daemon2, others on daemon1

### P10.2 — Systemd units
- `tailbus-coord.service`, `tailbusd.service`
- Socket activation for daemon
- `install.sh` optionally installs units

### P10.3 — Kubernetes operator (stretch)
- CRD: `TailbusCluster`, `TailbusAgent`
- Operator deploys coord + daemon sidecars
- Auto-discovery via K8s service mesh

### P10.4 — Helm chart
- Configurable coord + daemon deployment
- Ingress for A2A/WebSocket gateways
- PersistentVolume for coord SQLite

---

## Suggested Execution Order

### Now — Trust & Reliability (unblock production usage)

| Item | Why | Effort |
|------|-----|--------|
| ~~**P3.1 — Relay server**~~ | ✓ Done. NAT traversal works via DERP-style relay. | ~~Large~~ |
| ~~**P1.4 — Unix socket auth**~~ | ✓ Done. Token-file auth on Unix socket. | ~~Small~~ |
| ~~**P9.2 — Health endpoints**~~ | ✓ Done. `/healthz`, `/readyz`, pprof on all binaries. | ~~Tiny~~ |

### Next — Reliability & Developer Experience

| Item | Why | Effort |
|------|-----|--------|
| ~~**P2.4 — Message persistence**~~ | ✓ Done. bbolt-backed store; sessions and pending messages survive restart. | ~~Large~~ |
| ~~**P4.1 — Python SDK**~~ | ✓ Done. Async/sync SDK wrapping stdio bridge, zero deps, 46 tests. | ~~Medium~~ |
| **P2.3 — Backpressure** | Silent message drops are a time bomb. Need explicit signals. | Medium |
| ~~**P1.5 — Coord admission control**~~ | ✓ Done. Pre-auth token admission on coord. | ~~Small~~ |

### Soon — Platform Features

| Item | Why | Effort |
|------|-----|--------|
| **P7.1 — Handle-level ACLs** | When >1 team uses the mesh, unrestricted messaging is a non-starter. | Medium |
| **P6.2 — Error envelope** | Without structured errors, every failure is silent. | Small |
| ~~**P5.2 — MCP gateway**~~ | ✓ Done. Handles exposed as MCP tools via HTTP+SSE. | ~~Medium~~ |
| **P4.2 — TypeScript SDK** | Second most common agent language. | Medium |
| **P9.1 — OpenTelemetry** | Custom tracing works at small scale; OTel needed for production debugging. | Medium |
| **P5.1 — A2A gateway** | Interop with the emerging A2A standard. | Medium |
| **P3.2 — Direct connection probing** | Relay works but is slower; upgrade to direct when possible. | Medium |

### Later — Enterprise & Scale

| Item | Why | Effort |
|------|-----|--------|
| **P7.1-P7.2 — ACLs** | Required for multi-team deployments. | Large |
| **P8.1 — Domain isolation** | Required for multi-tenant SaaS. | Large |
| **P6.1-P6.3 — Rich semantics** | Multi-party sessions, delegation, error envelopes. | Large |
| **OIDC/SSO Identity** | Enterprise adoption requires "use your existing IdP." | Large |
| **P8.2 — Federation** | Massive scope; get single-domain right first. | Very large |
| ~~**P10.1 — Docker compose**~~ | ✓ Done. Multi-stage Dockerfile + compose with 3 example agents. | ~~Small~~ |
| **P10.2-P10.4 — Systemd, K8s, Helm** | Important but not differentiating. | Medium |

---

## Test Coverage Gaps to Address

These should be addressed opportunistically alongside feature work:

| Gap | Risk |
|-----|------|
| `cmd/tailbus/agent.go` (JSON bridge) — zero tests | Stdin/stdout parsing bugs, command dispatch errors |
| `@-mention routing` — no unit tests for `extractMentions`, `openMentionSession` | Regex edge cases, goroutine leaks |
| `internal/daemon/router.go` — no dedicated unit tests | Only exercised through integration test |
| No benchmark tests anywhere | Performance regressions go undetected |

---

The guiding principle throughout: **zero config for the common case**, expert knobs for power users. Every feature should work out of the box with sensible defaults, just like Tailscale.
