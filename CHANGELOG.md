# Changelog

All notable changes to this project will be documented in this file.

Format follows [Keep a Changelog](https://keepachangelog.com/).

## [Unreleased]

## [v0.3.9] — 2026-03-05

### Added
- Three-state peer connectivity in dashboard — peers reachable via relay now show `[relay]` instead of `[disconnected]`
- Message flow animation — animated arrows on topology edges when messages are routed or sessions opened
- Session chain detection — `FLOW: @briefing ──► @news ──► @analyst` summary line for multi-hop orchestration
- `connectivity` field on `PeerStatus` proto (`"direct"`, `"relay"`, `"offline"`)

## [v0.3.8] — 2026-03-05

### Added
- Auto-create a personal team for every user on signup — no manual `tailbus team create` needed
- `tailbus login` auto-activates the user's team in credentials so the daemon uses it immediately

## [v0.3.7] — 2026-03-05

### Fixed
- `tailbus team` commands failing with "error reading server preface: EOF" — CLI now uses TLS with an ephemeral client cert to connect to the coord server, matching the daemon's mTLS transport

## [v0.3.6] — 2026-03-04

### Added
- `tailbus stop` command — gracefully shuts down the daemon via Shutdown RPC over Unix socket

## [v0.3.5] — 2026-03-04

### Added
- Auto-start tailbusd via user-level services — systemd on Linux, launchd on macOS; no sudo required
- Reference service files in `init/` (systemd unit, macOS LaunchAgent plist)
- Install script upgrade path: restarts service automatically on re-install

## [v0.3.0] — 2026-03-04

### Added
- **Team management** — teams as the core multi-tenancy unit; team-scoped peer maps, handle lookup, and registration; invite codes with configurable TTL and max uses; CLI `tailbus team` commands (create, list, members, invite, join, switch, remove, role, delete)
- **REST API** — `/api/v1/` endpoints on coord for teams, members, invites, and nodes with JWT Bearer auth
- **Browser OAuth flow** — `GET /oauth/login` redirects to Google consent, returns tokens via URL fragment; no separate redirect URI needed
- **CORS middleware** — configurable `web_app_url` for cross-origin dashboard requests
- **Team admin dashboard** — web UI at `tailbus.co/dashboard` with Google sign-in, team list, create team, team detail (members table, nodes table, invite generation, role management, delete team)
- Per-handle health counters in TUI dashboard
- Mesh topology view in TUI dashboard (ASCII graph + compact mode)
- Install script now targets `~/.local/bin` with automatic PATH configuration
- Community governance files (LICENSE, CONTRIBUTING.md, SECURITY.md, issue/PR templates)
- CI pipeline (golangci-lint, go test with race + codecov, Python SDK tests)
- PyPI publish via trusted publishing (OIDC) in release pipeline

## [v0.2.0] — 2025-12-15

### Added
- **OAuth login flow** — Device Authorization Grant (RFC 8628), Google OIDC, JWT tokens with auto-refresh; `tailbus login/logout/status` CLI commands
- **Fly.io deployment** — `tailbus-coord` at `coord.tailbus.co`; embedded relay on port 7443
- **Embedded relay** — relay server runs inside coord via `relay_addr` config; no separate binary needed
- **Web chat UI** — browser-based dark-theme SPA embedded in daemon; agent sidebar, per-agent chat
- **MCP gateway** — HTTP server exposing handles as MCP tools via JSON-RPC 2.0
- **Message persistence** — bbolt-backed store; sessions and pending messages survive daemon restart
- **Docker Compose** — full mesh demo with coord + daemons + MCP gateway + web UI + example agents
- **Python SDK** — async/sync agent (`sdk/python/`), zero dependencies, 46 tests
- **Coord admission control** — pre-auth token system for node registration
- **Unix socket auth** — random token file (mode 0600) with automatic credential passing
- **Health endpoints** — `/healthz`, `/readyz`, `/debug/pprof/*` on daemon, coord, and relay
- **DERP-style relay server** — NAT traversal with mTLS on every hop; transparent fallback from direct P2P
- **Delivery guarantees** — per-session sequence numbers, ACK flow with retry (5s timeout, 3 retries)
- **mTLS everywhere** — P2P (Ed25519 peer verification), daemon-to-coord (TOFU cert pinning), Unix socket handle binding
- Multi-agent LLM example (researcher/critic/writer pipeline)
- Cross-network deployment templates for multi-machine meshes

## [v0.1.0] — 2025-10-01

### Added
- Initial MVP: coord server + node daemons + P2P gRPC transport
- Agent registration, sessions, message exchange, and resolution
- ServiceManifest with typed commands and JSON Schema
- Handle descriptions and @-mention auto-routing
- Stdio JSON-lines agent bridge for scripting and LLM tool-use
- Distributed message tracing and Prometheus metrics
- Real-time TUI dashboard
- Release pipeline and install script

[Unreleased]: https://github.com/alexanderfrey/tailbus/compare/v0.3.8...HEAD
[v0.3.8]: https://github.com/alexanderfrey/tailbus/compare/v0.3.7...v0.3.8
[v0.3.7]: https://github.com/alexanderfrey/tailbus/compare/v0.3.6...v0.3.7
[v0.3.6]: https://github.com/alexanderfrey/tailbus/compare/v0.3.5...v0.3.6
[v0.3.5]: https://github.com/alexanderfrey/tailbus/compare/v0.3.0...v0.3.5
[v0.3.0]: https://github.com/alexanderfrey/tailbus/compare/v0.2.0...v0.3.0
[v0.2.0]: https://github.com/alexanderfrey/tailbus/compare/v0.1.0...v0.2.0
[v0.1.0]: https://github.com/alexanderfrey/tailbus/releases/tag/v0.1.0
