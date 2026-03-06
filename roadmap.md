# Tailbus → "Tailscale for Agents" Roadmap

## Where we are today

Working MVP with real security, NAT traversal, persistence, MCP integration, and first-class shared collaboration: coord server + node daemons + P2P gRPC transport + relay server + CLI + TUI dashboard + Prometheus metrics + distributed tracing + stdio bridge + MCP gateway + Docker Compose. Tailbus is strongest as the communication and control plane between heterogeneous agent systems, not as a replacement for agent runtimes or workflow frameworks. **Phase 1 hardening complete:** mTLS on all connections (P2P, relay, and coord), per-connection handle ownership on the Unix socket, Unix socket token auth, coord admission control (pre-auth tokens + OAuth/JWT), per-session sequence numbers, and delivery ACKs with retry. **Phase 2 reliability:** message persistence via bbolt — sessions and pending messages survive daemon restarts. **Phase 3 NAT traversal:** DERP-style relay server enables message delivery across NAT boundaries. **Phase 4 SDKs:** Python SDK (async/sync, zero deps) wrapping the stdio bridge. **Phase 5 protocol bridges:** MCP gateway exposes handles as MCP tools — any MCP-compatible LLM can use tailbus agents. **Phase 6 collaboration:** daemon-managed shared rooms with ordered replay, room-aware dashboard activity, and a room-based pair-solver example. **Phase 8 teams:** Team-based isolation — teams as the core multi-tenancy unit; team-scoped peer maps, handle lookup, and registration; invite codes for onboarding; CLI team management commands; REST API + browser OAuth + web dashboard at `tailbus.co/dashboard`. **Phase 9 observability:** Web chat UI embedded in daemon binary for browser-based agent interaction. **Phase 10 deployment:** Docker Compose with full mesh + web UI + LLM agents; multi-agent LLM collaboration example (researcher/critic/writer pipeline); cross-network deployment templates for multi-machine meshes. **OAuth login:** Device Authorization Grant (RFC 8628) for Tailscale-style `install → login → connected` UX; Google OIDC; JWT tokens with auto-refresh; `tailbus login/logout/status` CLI commands. **Cloud deployment:** `tailbus-coord` on Fly.io at `coord.tailbus.co` with embedded relay on port 7443; any machine can join with `tailbus login && tailbusd` and gets NAT traversal for free.

**What works:**
- Agents register handles, open sessions, exchange messages, resolve conversations
- Heterogeneous agents can collaborate across languages, machines, and department boundaries through one mesh
- All daemon-to-daemon traffic is mTLS with Ed25519 peer verification against the peer map
- All daemon-to-coord traffic is mTLS with TOFU cert pinning
- DERP-style relay server for NAT traversal — daemons try direct P2P first, fall back to relay transparently; relay embedded in coord server by default (also works standalone)
- Handle ownership is enforced per Unix socket connection (no impersonation)
- Unix socket token auth — daemon generates a random token file (mode 0600); CLI and agents present it automatically
- Coord admission control — pre-auth token system gates node registration; open mode (no tokens) preserves zero-config default
- Every envelope carries a monotonic sequence number; delivered messages generate ACKs; unacked messages retry (5s timeout, 3 retries)
- Message persistence — bbolt-backed store on each daemon; sessions and pending messages survive restart; ACKed messages purged automatically
- Shared rooms — daemon-managed multi-party conversations with ordered room events, replay, and home-daemon authority
- MCP gateway — HTTP server on daemon exposing handles as MCP tools; `tools/list` returns handle manifests, `tools/call` opens session + sends + waits for response
- Docker Compose — `docker compose up` gives you a full mesh with coord + 2 daemons + MCP gateway + web UI + 3 example Python agents
- Web chat UI — embedded in daemon binary, served at MCP gateway address; agent sidebar, per-agent chat, responsive dark theme
- `/healthz`, `/readyz`, and `/debug/pprof/*` endpoints on daemon metrics port, coord, and relay
- Python SDK (`sdk/python/`) — `AsyncAgent` and `SyncAgent` with zero external dependencies
- Pair-solver room demo — orchestrator + Codex + LM Studio collaborating through one shared room
- Service manifests, @-mention routing, tracing, Prometheus metrics, TUI dashboard with room activity and reconnect after daemon restart

**What's missing for real adoption:**
- ~~No LICENSE file, CONTRIBUTING.md, or other open-source governance files~~ ✓ Added
- ~~No CI on PRs — no automated linting, testing, or coverage reporting~~ ✓ Added
- ~~Python SDK not published to PyPI~~ ✓ PyPI publish in release pipeline
- No ACLs or namespace ownership — unrestricted any-to-any messaging is not acceptable for shared company meshes
- Reliability story is still incomplete for larger deployments — no coord HA, no room failover, no dead-letter workflow, limited backpressure semantics
- Capability discovery is too weak — developers still need to know exact handles instead of asking for an agent by capability/version/tag
- Python SDK only; still no TypeScript/native Go SDKs
- Operational UX is still thin — dashboard is useful live, but audit history, search, DLQ visibility, and admin operations are not strong enough yet
- Enterprise identity is incomplete — Google OAuth works, but service accounts and broader SSO/provider support are still missing
- No federation — `name@domain` is parsed but routing is single-coord only
- CLI missing `--version`, `--json` output, and shell completions
- ~~No OIDC/SSO identity — nodes authenticate with keypairs, not corporate IdPs~~ ✓ OAuth login with Google OIDC; JWT tokens; device authorization flow

---

## Core product boundary

Tailbus is for:

- heterogeneous agents
- different machines and networks
- different departments and ownership boundaries
- different runtimes and languages
- shared identity, routing, rooms, policies, and observability

Tailbus is not:

- an agent reasoning framework
- a workflow DSL
- a memory layer
- a replacement for LangGraph, CrewAI, Jido, or similar runtimes

The product gap Tailbus addresses is the layer between those systems: the networked communication and control plane that lets independently running agent systems collaborate safely across boundaries.

---

## Adoption Milestones

### Developer adoption

Tailbus becomes a useful daily tool for individual developers when:

- setup is boring: install, login, start daemon, run agents, no manual recovery steps
- SDK coverage reaches the common languages for agent work: Python, TypeScript, and Go
- error behavior is explicit: structured errors, cancellation, timeouts, and clear delivery semantics
- docs go beyond the README: architecture, troubleshooting, examples, and protocol reference
- local operations are easy: `doctor`, version reporting, shell completions, Docker images, and good example coverage

### Team adoption

Tailbus becomes useful for a department or small internal platform team when:

- rooms, sessions, and delegation patterns are stable enough for real workflows
- capability discovery exists, so teams can route by role/version/tag instead of memorizing handles
- dashboards and logs answer operational questions quickly: who is active, what failed, what retried, what timed out
- policy exists at the team boundary: namespace ownership, ACLs, and auditability
- deployment paths are straightforward for laptops, servers, and basic cloud environments

### Enterprise adoption

Tailbus becomes a credible company-wide substrate when:

- policy and governance are strong: ACLs, team-scoped policies, reserved namespaces, audit trails
- reliability is explicit: coord failure model, room/home-daemon failure model, replay guarantees, DLQ/retry operations
- identity supports real organizations: additional SSO providers, service accounts, machine/user separation
- operational tooling is complete: search, history, admin console actions, alerting-friendly metrics, durable audit views
- multi-domain and eventually federated routing are designed from a stable single-domain base

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

### ~~P1.6 — OAuth login flow (OIDC/SSO identity)~~ ✓ DONE
- Device Authorization Grant (RFC 8628) — standard flow for CLI tools (same as `gh`, `tailscale`, `aws sso`)
- Google OIDC via `go-oidc/v3` + `golang.org/x/oauth2`; extensible to more providers
- JWT tokens (HMAC-SHA256): access tokens (1h) + refresh tokens (30d); auto-generated signing key at `<data_dir>/jwt.key`
- Coord serves OAuth HTTP endpoints: `POST /oauth/device/code`, `POST /oauth/device/token`, `GET /oauth/verify` (embedded HTML), `GET /oauth/callback`, `POST /oauth/refresh`
- Daemon `resolveAuth()`: checks explicit auth token → saved credentials → interactive device flow; credentials saved to `~/.tailbus/credentials.json` (mode 0600)
- Token refresh on heartbeat auth failures; daemon auto-reconnects without re-login
- CLI commands: `tailbus login` (device flow), `tailbus logout` (remove creds), `tailbus status` (show identity + connection)
- Admission controller accepts both JWTs (tokens starting with `eyJ`) and pre-shared tokens — fully backward compatible
- SQLite `users` and `node_users` tables track user↔node bindings
- Default coord address changed to `coord.tailbus.co:8443` for zero-config UX
- Pre-shared `auth_token` in config/flags skips OAuth entirely (CI, automation)

### P1.7 — Handle namespace policies
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
- **Embedded in coord:** relay can be started inside `tailbus-coord` via `relay_addr` config — no separate binary needed; relay registers itself in the store with heartbeat to avoid reaper eviction
- **Insecure mode support:** relay reads peer pubkey from `x-tailbus-pubkey` gRPC metadata header (hex-encoded), falling back to mTLS cert extraction; daemons send pubkey metadata on relay connect; enables relay behind edge TLS (e.g. Fly.io with `insecure_grpc=true`)

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
- FIFO future correlation for request/response; `Message` events dispatched concurrently via `create_task` (supports agent-to-agent delegation without deadlock)
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

### ~~P6.1 — Shared rooms~~ ✓ DONE
- First-class daemon-managed `room` abstraction above 1:1 sessions
- Coord tracks `room_id -> home_node_id` so any daemon can route room operations to the authoritative home daemon
- Ordered append-only room event log with replay, membership, and close semantics
- Pair-solver demo shows sequential multi-agent collaboration in one shared room
- Dashboard surfaces room creation, room posts, room membership, and active turn state

### P6.2 — Linked session trees
- `parent_session_id` field in session for linked session trees
- Agent A opens session with B, B delegates to C with back-link
- Status propagation up the tree

### P6.3 — Error envelope
- `ENVELOPE_TYPE_ERROR` with structured `code` + `message` + `details`
- Standard error codes: `NOT_FOUND`, `PERMISSION_DENIED`, `RATE_LIMITED`, `INTERNAL`
- Agents can reject sessions cleanly

### P6.4 — Task delegation pattern
- First-class A→B→C delegation with progress tracking
- Parent session sees aggregated status from child sessions
- Timeout propagation: if parent times out, children are cancelled

### P6.5 — Fan-out & aggregation
- Orchestrator helper: open N parallel sub-sessions, collect results
- Configurable: wait-all, wait-first, wait-quorum
- Library in SDK, not a daemon feature

### P6.6 — Capability discovery and routing
- **Problem:** handle lookup is exact-address routing, not discovery. Once a mesh has dozens of agents, developers cannot be expected to memorize handles.
- Keep direct handle routing as-is for exact addressing
- Add first-class discovery on top of it, not instead of it

#### P6.6a — Structured capability metadata
- Extend `ServiceManifest` with searchable fields beyond `description`, `tags`, and `version`
- Add stable `capabilities` identifiers such as `document.summarize`, `research.company`, `sql.query`
- Add `domains` for business scope such as `finance`, `legal`, `sales`
- Add `input_types` and `output_types` so callers can filter by payload contract
- Keep `tags` as loose labels; do not make them the primary discovery contract

#### P6.6b — Discovery query API
- Add `FindHandles` / `FindAgents` as a first-class coord and daemon operation
- Query fields should include:
  - required capability IDs
  - optional tags and domains
  - optional command name
  - version constraints
  - team scope / visibility rules
- Return ranked candidates with reasons, not just an unordered list

#### P6.6c — Ranking and selection
- Rank candidates by:
  - exact capability match
  - version match
  - policy visibility
  - recent health / availability
  - locality / latency preference
- Support deterministic selection rules so orchestrators can say:
  - pick best match
  - pick latest compatible version
  - prefer local node
  - prefer a specific team or domain

#### P6.6d — Invocation model
- Keep handle = transport address
- Treat capabilities as the search index that resolves to one or more handles
- Let orchestrators discover first, then open sessions or post room requests to the selected handle
- Expose discovery in SDKs and CLI so users can do `tailbus find --capability research.company`

#### P6.6e — Later extensions
- Virtual handles / aliases such as `finance.research` that resolve dynamically to matching agents
- Optional semantic search over descriptions and examples after the structured model is solid
- Richer command reflection beyond raw JSON Schema once the discovery model is stable

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

## Pillar 8: Teams, Multi-Tenancy & Federation

*Tailscale's tailnet is the killer abstraction — a private network you manage like a team. Tailbus needs the same: a team owns a mesh, members share agents, policies scope to teams.*

### ~~P8.1 — Teams & user management~~ ✓ DONE
- Teams as the core isolation unit: `teams`, `team_members`, `team_invites` tables in SQLite
- `team_id` on nodes; coord validates team membership on registration
- Handles scoped to team — two teams can each have a `calculator` handle without collision (composite PK on handles table)
- Peer map filtering is the single enforcement point: `BuildForTeam()` returns only team peers + relays; nodes in different teams never learn about each other
- JWT claims include `team_id`; preserved through token refresh
- Default behavior: `team_id=""` everywhere = personal mode = current behavior unchanged (full backward compat)
- Team management RPCs: `CreateTeam`, `ListTeams`, `GetTeamMembers`, `RemoveTeamMember`, `UpdateTeamMemberRole`, `DeleteTeam`
- CLI: `tailbus team {create, list, members, invite, join, switch, remove, role, delete}`
- Owner safety: can't remove self, can't demote last owner, can't create invites unless owner

### ~~P8.2 — Team invitations & onboarding~~ ✓ DONE
- Invite codes: 8-char hex strings with configurable max uses and TTL (default 7 days)
- `CreateTeamInvite` RPC (owner only) + `AcceptTeamInvite` RPC (any authenticated user)
- Codes shared out-of-band (like `tailscale up --authkey`) — no email service needed
- `tailbus team invite <name> [--uses N] [--ttl 7d]` generates code, `tailbus team join <code>` accepts
- Invite consumption is atomic (checks expiry + use count in transaction)
- `tailbus team switch <name>` writes active team to credentials; daemon reads on startup

### P8.3 — Team-scoped policies
- **Problem:** ACLs (P7.1–P7.2) need a team boundary. Without it, policies are global and unmanageable at scale.
- Policies are per-team — each team has its own ACL file/config
- Team admins manage policies; members can't modify them
- Default policy per team: `allow * -> *` (open within team, deny cross-team)
- Cross-team messaging requires explicit bilateral policy (`allow team-a:analytics -> team-b:dashboard`)
- Handle visibility: by default handles are only visible to members of the owning team
- Shared handles: explicit opt-in to make a handle visible to other teams or public

### P8.4 — Domain isolation
- Coord server scoped to a domain (like a Tailscale tailnet)
- Handles are unique within a domain
- Config: `domain = "acme.com"` in coord config
- Teams exist within a domain; domain is the trust boundary

### P8.5 — Federation (cross-domain)
- DNS SRV records for coord server discovery: `_tailbus._tcp.acme.com`
- When routing to `agent@otherdomain.com`, look up their coord via DNS
- Cross-domain trust policies (which orgs can message which)
- Proxy through relay or establish direct P2P with foreign node

### ~~P8.6 — Admin API & console~~ ✓ DONE
- REST API on coord (`/api/v1/`) for teams, members, invites, nodes — JWT Bearer auth, same HTTP port as OAuth
- Browser OAuth flow: `GET /oauth/login` → Google consent → redirect to `{webAppURL}/dashboard#tokens`
- CORS middleware for cross-origin requests from the web app
- Dashboard UI at `tailbus.co/dashboard`: team list, create team, team detail (members, nodes, invites), role management, delete team
- Auth: localStorage tokens with auto-refresh on 401; Google sign-in via browser OAuth redirect
- Config: `web_app_url` in coord TOML (default `https://tailbus.co`)

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

### ~~P9.5 — Web chat UI~~ ✓ DONE
- Browser-based chat interface embedded in the daemon binary via `go:embed` (`internal/mcp/web/index.html`)
- Served at the MCP gateway address alongside MCP endpoints
- Dark-theme responsive SPA: agent sidebar with auto-refresh, per-agent chat history, real-time send/receive
- REST API: `GET /api/agents` (list agents), `POST /api/send` (send message, wait for response)
- Works on desktop and mobile — zero install, just open the URL
- TUI dashboard includes mesh topology view (ASCII graph + compact mode, Tab toggle)
- Future: session inspector, trace viewer in web UI

---

## Pillar 10: Deployment & Operations

*Must be trivially deployable anywhere.*

### ~~P10.1 — Docker images & compose~~ ✓ DONE
- Multi-stage `Dockerfile` with `coord`, `daemon`, and `relay` targets
- `docker-compose.yml`: coord + 2 daemons + MCP gateway + web UI (port 8080) + 4 example agents
- Example agents: `calculator` (tool-style with JSON Schema), `echo` (simplest agent), `orchestrator` (multi-agent delegation), `assistant` (LLM-powered via LM Studio)
- `docker compose up --build` → full mesh in 30 seconds
- Cross-node messaging demonstrated: echo agent on daemon2, others on daemon1
- Web chat UI with command-aware forms: auto-generates input fields from JSON Schema for structured agents

### ~~P10.5 — Multi-agent LLM example~~ ✓ DONE
- `examples/multi-agent/`: three LLM-powered agents (researcher, critic, writer) collaborating through the mesh
- Pipeline: user → researcher (generates findings) → critic (reviews) → writer (polishes) → user
- All agents share a single LM Studio backend; each has its own system prompt and role
- Demonstrates agent-to-agent delegation via `open_session` + `pending_responses` future pattern
- Separate `docker-compose.yml` for standalone deployment

### ~~P10.6 — Cross-network deployment~~ ✓ DONE
- `examples/multi-machine/`: compose files for running the mesh across multiple physical machines
- `machine-a.yml`: coord + relay + daemon + LLM agents (primary machine)
- `machine-b.yml`: daemon + agents, connects to Machine A's coord (secondary machine)
- Relay included for automatic NAT traversal when direct P2P fails
- README with firewall notes and step-by-step setup instructions

### ~~P10.7 — Cloud deployment (Fly.io)~~ ✓ DONE
- `tailbus-coord` deployed to Fly.io at `coord.tailbus.co` (Frankfurt region)
- `fly.toml` + `deploy/coord.toml` for one-command deployment
- OAuth HTTP on port 443 via Fly edge TLS; gRPC on port 8443 via TCP passthrough; embedded relay on port 7443
- Persistent Fly Volume at `/data` for SQLite + keys
- Google OAuth credentials via `fly secrets set` (env var overrides: `OAUTH_CLIENT_ID`, `OAUTH_CLIENT_SECRET`)
- Health check on `:8081` (separate from OAuth HTTP on `:8080`)
- Configurable `external_url` for OAuth callbacks behind edge TLS
- Configurable `insecure_grpc` for disabling gRPC server TLS (when edge handles it)
- Smart OAuth URL auto-detection in daemon/CLI: localhost → `http://localhost:8080`, remote → `https://{host}`
- End-to-end verified: `tailbus login` → Google OAuth → `tailbusd` → registered with coord

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

## Pillar 11: Community & Developer Adoption

*The technology is strong. The project needs to look and feel community-ready.*

### ~~P11.1 — Open-source governance files~~ ✓ DONE
- `LICENSE` (MIT), `CONTRIBUTING.md`, `SECURITY.md`
- `.github/ISSUE_TEMPLATE/` (bug report, feature request), `.github/PULL_REQUEST_TEMPLATE.md`

### ~~P11.2 — CI pipeline~~ ✓ DONE
- `ci.yml` workflow: `golangci-lint`, `go test -race` with codecov, Python SDK tests — runs on push to main and PRs
- Dependabot config for Go modules, Python deps, and GitHub Actions
- `make lint` target

### ~~P11.3 — Publish Python SDK to PyPI~~ ✓ DONE
- PyPI publish job in `release.yml` using trusted publishing (OIDC)
- `sdk/python/README.md` with standalone SDK documentation

### P11.4 — CLI polish
- **Problem:** Missing quality-of-life features developers expect: no `--version`, no machine-readable output, no shell completions.
- Add `--version` / `tailbus version` command
- Add `--json` output mode for scripting (machine-readable output for `list`, `sessions`, `status`, `trace`)
- Add `tailbus completion bash/zsh/fish` for shell completions
- Add `tailbus doctor` command — diagnose connectivity, check daemon status, verify coord reachability
- Improve per-subcommand help with flag descriptions

### P11.5 — Install experience
- **Problem:** No checksum verification (security risk), no uninstall path, no Homebrew formula.
- Add SHA256 checksum verification to `install.sh` (download checksums from release, verify before installing)
- Add uninstall support (`tailbus uninstall` or `install.sh --uninstall`)
- Add version pinning (`install.sh --version v0.5.0`)
- Add Homebrew formula — `brew install tailbus` is the expected path on macOS
- Add Windows support notes (WSL instructions or native binary)

### P11.6 — Documentation site
- **Problem:** README is excellent but there's no deeper documentation. Developers who get past the README need architecture deep-dives, troubleshooting guides, and protocol specs.
- Create `docs/` directory or documentation site (GitHub Pages, Docusaurus, or similar)
- Architecture deep-dive — how coord, daemon, transport, and relay interact
- Troubleshooting guide — common errors, connectivity issues, firewall rules
- Security model — detailed explanation of mTLS chain, TOFU, JWT flow
- Protocol specification — formal stdio bridge JSON schema
- Migration guide for version upgrades
- Add READMEs to `examples/docker/` and `examples/multi-agent/`

### ~~P11.7 — CHANGELOG and release notes~~ ✓ DONE
- `CHANGELOG.md` following Keep a Changelog format, backfilled v0.1.0 and v0.2.0

### P11.8 — Docker image publishing
- **Problem:** Docker images only available via local `docker compose build`. No registry images for production use.
- Add Docker image build/push to release pipeline (GHCR or Docker Hub)
- Multi-arch images (amd64, arm64) via `docker buildx`
- Tags: `latest`, `vX.Y.Z`, `vX.Y`

### P11.9 — Community channels
- **Problem:** No place for users to ask questions, share builds, or report issues informally.
- Create Discord server or GitHub Discussions
- Add community links to README
- "Built with tailbus" showcase — encourage users to share agent setups
- Blog / dev log — architecture decisions, use cases, competitive positioning

---

## Suggested Execution Order

### ~~Immediate — Community Readiness~~ ✓ DONE

All four items complete: governance files, CI pipeline, PyPI publish, CHANGELOG.

### Now — Developer Adoption Baseline

| Item | Why | Effort |
|------|-----|--------|
| **P11.4 — CLI polish** | Missing --version, --json, completions, doctor. Table stakes for CLI tools. | Medium |
| **P11.5 — Install experience** | No checksums, no uninstall, no Homebrew. | Small |
| **P4.2 — TypeScript SDK** | Second most common agent language. Doubles addressable audience. | Medium |
| **P4.3 — Go SDK** | Critical for infrastructure and platform teams embedding Tailbus directly. | Medium |
| **P11.6 — Documentation site** | README is strong, but deeper docs are now the bottleneck. | Medium |
| **P11.8 — Docker image publishing** | No registry images for production use. | Small |
| **P6.3 — Error envelope** | Developers need explicit failure semantics, not ad hoc strings. | Small |
| **P2.3 — Backpressure** | Silent message drops are a time bomb. Need explicit signals. | Medium |

### Next — Team Adoption Baseline

| Item | Why | Effort |
|------|-----|--------|
| **P7.1 — Handle-level ACLs** | Once multiple groups share the mesh, unrestricted messaging becomes a blocker. | Medium |
| **P7.2 — Tag-based policies** | Teams need rules based on capability classes, not one-off handle names. | Medium |
| **P8.3 — Team-scoped policies** | ACLs are only manageable when scoped cleanly to teams. | Medium |
| **P6.2 — Linked session trees** | Delegation exists ad hoc today; teams need explicit parent/child workflow semantics. | Large |
| **P6.4 — Task delegation pattern** | Common orchestration workflows need a first-class pattern. | Medium |
| **P6.6 — Capability discovery and routing** | Teams need to route by capability, tag, and version instead of memorizing handles. | Large |
| **P9.1 — OpenTelemetry** | Production debugging needs standard observability export, not only custom tracing. | Medium |
| **P5.1 — A2A gateway** | Interop with external agent ecosystems matters once the core team story is stable. | Medium |
| **P3.2 — Direct connection probing** | Relay works but is slower; upgrade to direct when possible. | Medium |

### Later — Enterprise Rollout

| Item | Why | Effort |
|------|-----|--------|
| ~~P8.1 — Teams & user management~~ | ✓ Done | — |
| ~~P8.2 — Team invitations~~ | ✓ Done | — |
| **P7.1-P7.2 — ACLs** | Required for multi-team deployments and compliance review. | Large |
| **P8.3 — Team-scoped policies** | ACLs need team boundaries to be manageable. | Medium |
| **P1.7 — Handle namespace policies** | Companies need ownership, reservation, and revocation for critical handles. | Medium |
| **P2.5 — Dead letter queue** | Operations teams need somewhere failed messages go besides logs. | Medium |
| ~~P8.6 — Admin API & console~~ | ✓ Done | — |
| **Dashboard + admin UX expansion** | Search, audit history, room/session drill-down, replay and retry actions are still missing. | Large |
| **Enterprise identity expansion** | Google OAuth is not enough; service accounts and broader SSO are required. | Large |
| **P8.4 — Domain isolation** | Required for multi-tenant SaaS. | Large |
| **P8.5 — Federation** | Massive scope; get single-domain right first. | Very large |
| **P11.9 — Community channels** | Discord/Discussions, blog, showcase. Growth lever after core is solid. | Small |
| **P10.2-P10.4 — Systemd, K8s, Helm** | Important but not differentiating. | Medium |

### Done

| Item | Status |
|------|--------|
| ~~P3.1 — Relay server~~ | ✓ NAT traversal via DERP-style relay |
| ~~P1.1–P1.6 — Security & Auth~~ | ✓ mTLS, Unix socket auth, OAuth/JWT, admission control |
| ~~P2.1–P2.2 — Delivery guarantees~~ | ✓ Sequence numbers, ACK flow |
| ~~P2.4 — Message persistence~~ | ✓ bbolt-backed store; survives restart |
| ~~P4.1 — Python SDK~~ | ✓ Async/sync, zero deps, 46 tests |
| ~~P5.2 — MCP gateway~~ | ✓ Handles as MCP tools via HTTP+SSE |
| ~~P9.2 — Health endpoints~~ | ✓ /healthz, /readyz, pprof |
| ~~P9.5 — Web chat UI~~ | ✓ Embedded dark-theme SPA |
| ~~P10.1 — Docker compose~~ | ✓ Full mesh + example agents |
| ~~P10.5–P10.7 — Deployment~~ | ✓ Multi-agent, multi-machine, Fly.io |
| ~~OIDC/SSO Identity~~ | ✓ OAuth login, Google OIDC, JWT tokens |
| ~~P11.1 — Governance files~~ | ✓ LICENSE, CONTRIBUTING.md, SECURITY.md, issue/PR templates |
| ~~P11.2 — CI pipeline~~ | ✓ ci.yml (lint + test-go + test-python), Dependabot, make lint |
| ~~P11.3 — PyPI publish~~ | ✓ Trusted publishing in release.yml, SDK README |
| ~~P11.7 — CHANGELOG~~ | ✓ Keep a Changelog format, backfilled v0.1.0 and v0.2.0 |
| ~~P8.1 — Teams & user management~~ | ✓ Team-scoped peer maps, handles, registration; JWT team claims |
| ~~P8.2 — Team invitations~~ | ✓ Invite codes with TTL/max-uses; CLI team management |
| ~~P8.6 — Admin API & console~~ | ✓ REST API, browser OAuth, web dashboard at tailbus.co/dashboard |

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
