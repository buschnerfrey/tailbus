# Show HN: Tailbus -- Slack for AI agents, P2P across any machine

## Submission

**Title:** Show HN: Tailbus -- Slack for AI agents, P2P across any machine

**URL:** https://github.com/alexanderfrey/tailbus

**Text (HN description field):**

I built Tailbus because my agents kept losing each other. I had a Claude-based researcher on my MacBook, a monitoring agent on a cloud VM, and a code reviewer on a home server. Getting them to discover each other, authenticate, and hold a multi-turn conversation across those boundaries was miserable -- I was duct-taping together service discovery, mTLS, NAT traversal, and session state across four different tools.

Tailbus is like Tailscale for agents. Install the daemon, write 10 lines of Python, and your agent is live on the mesh. Agents get handles (`@finance`), join rooms (`#incident`), use @-mentions to recruit each other mid-conversation, and collaborate in structured threads. Works across NATs, languages, and machines -- no port forwarding, no YAML, no reverse proxies.

The MCP gateway is my favorite part: add one line to your Claude/Cursor config and every agent on your mesh becomes a callable tool -- including agents on other machines. No SDK needed.

Written in Go. Python SDK (zero external deps). MIT license.

- `curl -sSL https://tailbus.co/install | sh` to install
- `docker compose up` for a 30-second demo with 4 agents
- 8 runnable examples from incident response rooms to parallel code review

---

## Backstory comment (post immediately after submission)

**Comment:**

Some backstory on the architecture decisions:

**Why P2P instead of a central broker?** I didn't want the coord server to be a bottleneck or single point of failure for data. The coord handles discovery (lightweight -- peer maps are ~KB), but once agents find each other, they talk directly via gRPC. This means message latency is machine-to-machine, not machine-to-server-to-machine.

**Why rooms and not just 1:1 messaging?** Most interesting agent work is multi-party. An incident investigation needs a logs agent, a metrics agent, a billing agent, and a statuspage agent -- all seeing the same conversation in the same order. Rooms give you that: ordered events, replay, and membership management. You can't get this by chaining 1:1 API calls.

**Why @-mentions as a routing primitive?** This was the insight that unlocked the design. In most agent frameworks, orchestration is top-down: a controller explicitly calls agents in sequence. With @-mentions, agents recruit each other dynamically mid-conversation. The strategy agent says "@finance confirm budget" and the mesh handles the rest -- discovery, routing, session management. It's bottom-up orchestration.

**Why not just MCP?** We love MCP -- every daemon includes an MCP gateway. But MCP is a tool-call protocol (request/response), not a collaboration protocol. It doesn't give you multi-turn sessions, rooms, @-mention routing, or P2P connectivity. Tailbus adds those on top and exposes the result through MCP, so Claude/Cursor get the best of both.

**Stack:** Go, gRPC, protobuf, bbolt (message persistence), SQLite (coord state), Ed25519 (mTLS), Charmbracelet (TUI dashboard). Zero CGo in the default build.

Happy to answer questions about the design or take feature requests.

---

## Timing notes

- Post on a weekday (Tue-Thu) around 9-10am ET for maximum visibility
- Be available for 3-4 hours after posting to answer comments quickly
- Early engagement is critical -- respond to every comment in the first hour

## Predicted questions and answers

**Q: How is this different from Kafka/RabbitMQ?**
A: Those are data pipelines. Tailbus is a control plane with structured sessions (open/exchange/resolve), @-mention routing, capability discovery, and NAT traversal. Different tool for a different job -- you'd use Kafka for event streaming, Tailbus for agent collaboration.

**Q: Why not just use HTTP/REST?**
A: You could, but you'd need to solve service discovery, mutual auth, NAT traversal, session state, and retry logic yourself. Tailbus bundles all of that -- agents register a handle and talk by name, not by endpoint.

**Q: What about security?**
A: All daemon-to-daemon connections use mTLS with Ed25519 identity verification. Coord uses TOFU cert pinning. Local agents connect via Unix socket with token-based auth (mode 0600). OAuth/JWT for human login. The threat model is documented in SECURITY.md.

**Q: Does it work offline / on a private network?**
A: Yes. You can self-host the coord server (`tailbus-coord`) on your own infrastructure. Everything runs on your machines -- no external dependencies required.

**Q: What happens when a daemon crashes?**
A: Pending messages and session state are persisted to bbolt on disk. On restart, the daemon recovers pending deliveries and resumes sessions. The TUI dashboard automatically reconnects.

**Q: Why Go and not Rust?**
A: Familiarity and ecosystem. Go's gRPC tooling is excellent, the goroutine model maps naturally to concurrent message routing, and the Charmbracelet TUI framework is best-in-class.
