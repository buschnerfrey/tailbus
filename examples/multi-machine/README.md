# Cross-Network Multi-Machine Setup

Run the tailbus mesh across multiple physical machines. Agents on different
machines communicate through the mesh — the relay handles NAT traversal.

This example is self-contained in `examples/multi-machine/`; it no longer
depends on agent code from other example directories.

Best for: proving the mesh across real machines. Worst for quick onboarding.

## Why this example matters

This is the example that proves Tailbus beyond one laptop. It is not the first
demo to run, but it is the right one when you need to validate discovery and
agent traffic across real machines and real network boundaries.

## Requirements

- Docker and `docker compose` on both machines
- the repo cloned on both machines
- Machine A reachable from Machine B
- LM Studio on Machine A if you want the LLM-backed writer/researcher flow

## Quick start

On Machine A:

```bash
cd examples/multi-machine
export HOST_IP=192.168.1.100
docker compose -f machine-a.yml up --build
```

On Machine B:

```bash
cd examples/multi-machine
export COORD_IP=192.168.1.100
export HOST_IP=192.168.1.101
docker compose -f machine-b.yml up --build
```

## What to watch for

- agents from both machines appearing in the Machine A web UI
- researcher/critic staying local to Machine A while writer runs on Machine B
- direct P2P traffic when available, with relay fallback when it is not
- one logical workflow spanning two physical hosts

## Architecture

```
Machine A (your Mac)                Machine B (other computer)
┌──────────────────────┐            ┌──────────────────────┐
│  coord (discovery)   │◄──────────►│                      │
│  relay (NAT helper)  │            │                      │
│  daemon              │◄──P2P────►│  daemon              │
│  ├─ researcher       │  or relay  │  ├─ writer           │
│  ├─ critic           │            │  └─ echo             │
│  └─ web UI (:8080)   │            │                      │
└──────────────────────┘            └──────────────────────┘
```

The research pipeline spans both machines:
1. User asks researcher (Machine A) a question
2. Researcher generates findings, sends to critic (Machine A)
3. Critic reviews, sends back to researcher
4. Researcher sends findings + critique to writer (Machine B) ← cross-network!
5. Writer produces final output, returns to user

## Setup

### Machine A (primary — runs coord + relay)

```bash
# Clone the repo
git clone https://github.com/alexanderfrey/tailbus.git
cd tailbus

# Set your IP (LAN IP or public IP that Machine B can reach)
export HOST_IP=192.168.1.100

# Start everything
cd examples/multi-machine
docker compose -f machine-a.yml up --build
```

Make sure LM Studio is running on Machine A with a model loaded (port 1234).

### Machine B (secondary — joins the mesh)

```bash
# Clone the repo
git clone https://github.com/alexanderfrey/tailbus.git
cd tailbus

# Point to Machine A
export COORD_IP=192.168.1.100
export HOST_IP=192.168.1.101

# Start daemon + agents
cd examples/multi-machine
docker compose -f machine-b.yml up --build
```

If Machine B also has LM Studio, the writer agent will use it. Otherwise,
update `LLM_BASE_URL` in machine-b.yml to point to Machine A:

```yaml
environment:
  - LLM_BASE_URL=http://192.168.1.100:1234/v1
```

### Test

1. Open http://192.168.1.100:8080 (Machine A's web UI)
2. You should see agents from both machines in the sidebar
3. Click "researcher" and ask it to investigate a topic
4. Watch the Docker logs — the pipeline crosses machines

```bash
# Machine A logs
docker compose -f machine-a.yml logs -f researcher critic

# Machine B logs
docker compose -f machine-b.yml logs -f writer echo
```

### Quick connectivity test

```bash
# From any browser or machine that can reach Machine A:
curl -s http://192.168.1.100:8080/api/agents | python3 -m json.tool

# Should list agents from both machines
```

## Output and logs

- Machine A web UI: `http://<HOST_IP>:8080`
- Machine A logs: `docker compose -f machine-a.yml logs -f researcher critic`
- Machine B logs: `docker compose -f machine-b.yml logs -f writer echo`

## Firewall notes

Machine A needs these ports open:
- **8443** — coord server (TCP, required for all machines to join)
- **7443** — relay server (TCP, for NAT traversal)
- **9443** — P2P daemon (TCP, for direct connections)
- **8080** — web UI (TCP, for browser access)

Machine B needs:
- **9443** — P2P daemon (TCP, for direct connections from Machine A)

If direct P2P fails (firewalls, NAT), the relay on Machine A handles it
automatically. In that case only Machine A's ports need to be open.
