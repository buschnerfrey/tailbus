# Changelog

All notable changes to this project will be documented in this file.

Format follows [Keep a Changelog](https://keepachangelog.com/).

## [Unreleased]

### Added
- Per-handle health counters in TUI dashboard
- Mesh topology view in TUI dashboard (ASCII graph + compact mode)
- Install script now targets `~/.local/bin` with automatic PATH configuration

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

[Unreleased]: https://github.com/alexanderfrey/tailbus/compare/v0.2.0...HEAD
[v0.2.0]: https://github.com/alexanderfrey/tailbus/compare/v0.1.0...v0.2.0
[v0.1.0]: https://github.com/alexanderfrey/tailbus/releases/tag/v0.1.0
