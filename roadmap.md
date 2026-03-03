# Tailbus → "Tailscale for Agents" Roadmap

## Where we are today

Working MVP with real security and NAT traversal: coord server + node daemons + P2P gRPC transport + relay server + CLI + TUI dashboard + Prometheus metrics + distributed tracing + stdio bridge. **Phase 1 hardening complete:** mTLS on all connections (P2P, relay, and coord), per-connection handle ownership on the Unix socket, per-session sequence numbers, and delivery ACKs with retry. **Phase 3 NAT traversal started:** DERP-style relay server enables message delivery across NAT boundaries.

**What works:**
- Agents register handles, open sessions, exchange messages, resolve conversations
- All daemon-to-daemon traffic is mTLS with Ed25519 peer verification against the peer map
- All daemon-to-coord traffic is mTLS with TOFU cert pinning
- DERP-style relay server for NAT traversal — daemons try direct P2P first, fall back to relay transparently
- Handle ownership is enforced per Unix socket connection (no impersonation)
- Every envelope carries a monotonic sequence number; delivered messages generate ACKs; unacked messages retry (5s timeout, 3 retries)
- Service manifests, @-mention routing, tracing, Prometheus metrics, TUI dashboard

**What's missing for real adoption:**
- No persistence — daemon restart loses all sessions and pending messages
- No Unix socket authentication — any process with fs access can connect
- No SDKs beyond Go — agents must use the JSON-lines subprocess bridge
- No federation — `name@domain` is parsed but routing is single-coord only

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

### P1.4 — Unix socket authentication
- **Problem:** any process with filesystem access to the socket can connect and register any handle name. All the mTLS work is undermined if a co-tenant process can impersonate handles locally.
- SO_PEERCRED / peer PID verification on Linux (get calling process UID/GID)
- Alternatively: daemon generates a random auth token file (mode 0600) on startup; agents must present the token on first RPC
- Future: plug into org IdP via OIDC for multi-user machines

### P1.5 — Coord admission control
- **Problem:** the coord accepts any node that presents a valid mTLS cert. There's no gating on *who* can join the mesh.
- Registration token system (like `tailscale up --authkey`) — coord generates pre-auth keys, nodes present them at first registration
- Public key allowlist/denylist on coord config
- Foundation for org-level node management

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

### P2.4 — Message persistence (store-and-forward)
- **Problem:** daemon restart = total amnesia. All sessions, pending outbound messages, and trace spans are lost. The ACK flow is useless if the retry state doesn't survive restarts.
- Embedded store (bbolt or badger) on each daemon for pending outbound messages
- Store-and-forward: if remote peer is offline, queue locally, deliver on reconnect
- Session recovery: reload open sessions from disk on daemon restart
- Replay unacked messages from persistent log after crash
- ACKs (P2.2) drive the persistence lifecycle: ACKed = safe to delete from store

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

### P4.1 — Python SDK ← **HIGH PRIORITY**
- `pip install tailbus`
- Phase 1: wrap the stdio JSON-lines bridge (zero native deps, works today)
- Phase 2: native gRPC client using generated protobuf stubs (better perf, no subprocess)
- Async/await API: `agent.register("my-handle")`, `async for msg in agent.subscribe()`
- Auto-starts `tailbus agent` subprocess in phase 1, manages lifecycle

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

### P5.2 — MCP gateway
- Expose tailbus handles as MCP tools
- MCP `call_tool` → session open + send + resolve
- MCP `list_tools` → populated from handle manifests/commands
- Enables any MCP-compatible LLM to use tailbus agents

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

### P9.2 — Health endpoints + pprof
- `/healthz` and `/readyz` on the metrics port
- `net/http/pprof` for runtime profiling
- Trivial to add, operators expect them

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

### P10.1 — Docker images
- `tailbus-coord`, `tailbusd`, `tailbus` images
- Multi-arch (amd64/arm64)
- `docker-compose.yml` for local dev (coord + 2 daemons)

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
| **P1.4 — Unix socket auth** | Security hole that undermines all the mTLS work. Any co-tenant process can impersonate handles. | Small |
| **P9.2 — Health endpoints** | Trivial to add, operators expect them from day one. | Tiny |

### Next — Reliability & Developer Experience

| Item | Why | Effort |
|------|-----|--------|
| **P2.4 — Message persistence** | Daemon restart losing all state is not production-grade. ACKs are useless without durable retry. | Large |
| **P4.1 — Python SDK** | Meet agents where they live. 90% of AI agents are Python. | Medium |
| **P2.3 — Backpressure** | Silent message drops are a time bomb. Need explicit signals. | Medium |
| **P1.5 — Coord admission control** | Prevent unauthorized nodes from joining the mesh. | Small |

### Soon — Platform Features

| Item | Why | Effort |
|------|-----|--------|
| **P3.2 — Direct connection probing** | Relay works but is slower; upgrade to direct when possible. | Medium |
| **P5.1 — A2A gateway** | Interop with the emerging A2A standard. | Medium |
| **P5.2 — MCP gateway** | Let any MCP-compatible LLM use tailbus agents. | Medium |
| **P4.2 — TypeScript SDK** | Second most common agent language. | Medium |
| **P9.1 — OpenTelemetry** | Custom tracing works at small scale; OTel needed for production debugging. | Medium |

### Later — Enterprise & Scale

| Item | Why | Effort |
|------|-----|--------|
| **P7.1-P7.2 — ACLs** | Required for multi-team deployments. | Large |
| **P8.1 — Domain isolation** | Required for multi-tenant SaaS. | Large |
| **P6.1-P6.3 — Rich semantics** | Multi-party sessions, delegation, error envelopes. | Large |
| **P8.2 — Federation** | Massive scope; get single-domain right first. | Very large |
| **P10.1-P10.4 — Deployment** | Docker, systemd, K8s. Important but not differentiating. | Medium |

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
