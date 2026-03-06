#!/usr/bin/env python3
"""Shared helpers for the incident-room example."""

from __future__ import annotations

import asyncio
import json
import os
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any

sys_path = os.path.join(os.path.dirname(__file__), "../../sdk/python/src")
if sys_path not in os.sys.path:
    os.sys.path.insert(0, sys_path)

from tailbus import AsyncAgent, CommandSpec, Manifest, RoomEvent

ROOM_REPLAY_RETRIES = int(os.environ.get("INCIDENT_ROOM_REPLAY_RETRIES", "12"))
ROOM_REPLAY_DELAY = float(os.environ.get("INCIDENT_ROOM_REPLAY_DELAY", "0.5"))
LLM_BASE_URL = os.environ.get("LLM_BASE_URL", "http://localhost:1234/v1")
LLM_MODEL = os.environ.get("LLM_MODEL", "")
CODEX_TIMEOUT = int(os.environ.get("CODEX_TIMEOUT", "90"))
CODEX_MODEL = os.environ.get("CODEX_MODEL", "gpt-5-mini")

DIM = "\033[2m"
BOLD = "\033[1m"
GREEN = "\033[32m"
YELLOW = "\033[33m"
RED = "\033[31m"
CYAN = "\033[36m"
RESET = "\033[0m"

SCENARIOS: dict[str, dict[str, Any]] = {
    "checkout_db_regression": {
        "match_terms": ("checkout", "payment", "cart"),
        "root_cause": "A recent checkout-api release increased database connection pressure in eu-west and triggered `db_pool_timeout` failures during order confirmation.",
        "ops.logs.search": {
            "summary": "checkout-api is logging `db_pool_timeout` and `session_read_timeout` errors concentrated in eu-west traffic.",
            "evidence": [
                "Timeout errors increased 14x after 12:04 UTC.",
                "Most failures occur on the session lookup path before payment capture.",
                "Representative correlation id: chk-eu-timeout-4812.",
            ],
            "recommendation": "Reduce checkout-db connection pressure immediately or roll back the latest checkout-api release.",
        },
        "ops.metrics.query": {
            "summary": "Checkout success rate dropped sharply for EU traffic while other regions stayed mostly healthy.",
            "evidence": [
                "EU checkout success rate fell from 98.9% to 71.8%.",
                "p95 checkout latency rose from 420ms to 4.6s.",
                "Database connection saturation reached 92% on eu-west primaries.",
            ],
            "recommendation": "Prioritize mitigation in eu-west and watch success rate during rollback.",
        },
        "ops.release.history": {
            "summary": "The only relevant production change was release `r2026.03.06.4` to checkout-api a few minutes before the incident.",
            "evidence": [
                "checkout-api deployed at 12:01 UTC.",
                "Feature flag `checkout-session-batch-read` was enabled to 100% in eu-west.",
                "No billing or infra-wide releases correlate with the failure window.",
            ],
            "recommendation": "Roll back `r2026.03.06.4` or disable `checkout-session-batch-read`.",
        },
        "billing.account.lookup": {
            "summary": "Billing systems are healthy; failures happen before ledger write or invoice creation.",
            "evidence": [
                "No spike in failed captures after authorization handoff.",
                "Invoice and ledger workers are processing within normal latency bands.",
                "Affected accounts span multiple billing plans, so the issue is not account-tier specific.",
            ],
            "recommendation": "Treat this as a checkout application failure rather than a billing backend incident.",
        },
        "statuspage.compose": {
            "customer_update": "We’re investigating elevated checkout failures affecting some EU customers. We have identified a likely issue in a recent service change and are actively rolling out mitigation. Next update in 30 minutes.",
        },
    },
    "billing_webhook_delay": {
        "match_terms": ("invoice", "billing", "subscription", "webhook"),
        "root_cause": "A backlog in billing webhook workers is delaying subscription state updates after successful payment events.",
        "ops.logs.search": {
            "summary": "billing-webhooks workers are logging repeated retry exhaustion against the subscription-state queue.",
            "evidence": [
                "Retry warnings increased 11x after 09:17 UTC.",
                "Dead-letter queue entries reference delayed `subscription.updated` events.",
                "No matching application errors appear in checkout-api.",
            ],
            "recommendation": "Drain the subscription-state queue and scale webhook consumers.",
        },
        "ops.metrics.query": {
            "summary": "Webhook backlog is growing while payment capture latency remains normal.",
            "evidence": [
                "Webhook queue depth increased from 400 to 18,200 jobs.",
                "Payment authorization and capture success rates remain above 99%.",
                "Subscription update latency is now above 22 minutes p95.",
            ],
            "recommendation": "Scale consumers and replay delayed webhook jobs.",
        },
        "ops.release.history": {
            "summary": "The most relevant change was a config rollout for webhook concurrency limits earlier this morning.",
            "evidence": [
                "Config change `billing-webhooks-concurrency=4` deployed at 09:10 UTC.",
                "No code deploys to subscription-service occurred in the incident window.",
                "Throughput dropped immediately after the config rollout.",
            ],
            "recommendation": "Restore webhook concurrency to the previous setting and monitor backlog drain.",
        },
        "billing.account.lookup": {
            "summary": "Accounts are being charged successfully, but entitlement updates are delayed until webhook processing catches up.",
            "evidence": [
                "Successful payment records exist for sampled affected accounts.",
                "Subscription state transitions lag payment events by 15-25 minutes.",
                "Impact affects multiple plans and regions, not one customer segment.",
            ],
            "recommendation": "Communicate delayed activation rather than payment failure.",
        },
        "statuspage.compose": {
            "customer_update": "We’re investigating delays in subscription and billing status updates after successful purchases. Payments are processing, but account changes may take longer than usual while we work on mitigation. Next update in 30 minutes.",
        },
    },
    "api_gateway_latency": {
        "match_terms": (),
        "root_cause": "A shared API gateway capacity issue is increasing request latency and intermittent timeouts across one region.",
        "ops.logs.search": {
            "summary": "Gateway logs show upstream timeout bursts rather than one service-specific error signature.",
            "evidence": [
                "5xx responses are spread across multiple services behind the same gateway shard.",
                "Timeout bursts align to a single regional gateway pool.",
                "No one application dominates the error budget burn.",
            ],
            "recommendation": "Shift traffic away from the unhealthy gateway shard and add capacity.",
        },
        "ops.metrics.query": {
            "summary": "Regional latency and timeout rates are elevated across several APIs at the same time.",
            "evidence": [
                "p95 latency is up 6x on the affected regional gateway.",
                "5xx rate crossed 9% for the shared ingress tier.",
                "CPU saturation on gateway nodes exceeded 95%.",
            ],
            "recommendation": "Scale the ingress pool and shed non-critical traffic while capacity recovers.",
        },
        "ops.release.history": {
            "summary": "No single service deploy explains the incident; the strongest correlation is a regional infrastructure capacity reduction.",
            "evidence": [
                "No major application deploys in the last hour.",
                "Autoscaling on one gateway group lagged expected demand.",
                "Traffic rebalance settings changed earlier in the day.",
            ],
            "recommendation": "Restore gateway capacity and revert the traffic rebalance change if needed.",
        },
        "billing.account.lookup": {
            "summary": "Billing-specific systems are not uniquely impacted; failures match the broader API availability issue.",
            "evidence": [
                "Account lookup latency tracks the same regional gateway spikes.",
                "No billing queue buildup or payment processor errors stand out.",
                "Affected accounts are distributed across plans and products.",
            ],
            "recommendation": "Treat this as a shared platform incident, not a billing-only problem.",
        },
        "statuspage.compose": {
            "customer_update": "We’re investigating elevated latency and intermittent failures affecting some API requests in one region. We’re actively mitigating the issue and will provide another update in 30 minutes.",
        },
    },
}


def say(tag: str, msg: str) -> None:
    print(f"  {DIM}{tag}{RESET}  {msg}", flush=True)


def parse_json(payload: str) -> dict[str, Any] | None:
    try:
        value = json.loads(payload)
    except json.JSONDecodeError:
        return None
    return value if isinstance(value, dict) else None


def parse_command_payload(payload: str) -> dict[str, Any]:
    data = parse_json(payload)
    if not data:
        return {"incident": payload}
    args = data.get("arguments", data)
    if isinstance(args, str):
        parsed = parse_json(args)
        return parsed or {"incident": args}
    if isinstance(args, dict):
        return args
    return data


def scenario_key_for_incident(incident: dict[str, Any]) -> str:
    haystack = " ".join(
        str(incident.get(key, ""))
        for key in ("title", "customer_impact", "incident", "symptoms")
    ).lower()
    for key, scenario in SCENARIOS.items():
        terms = scenario.get("match_terms", ())
        if terms and any(term in haystack for term in terms):
            return key
    return "api_gateway_latency"


def scenario_for_incident(incident: dict[str, Any]) -> dict[str, Any]:
    return SCENARIOS[scenario_key_for_incident(incident)]


def incident_from_events(events: list[RoomEvent]) -> dict[str, Any]:
    for event in events:
        if event.event_type != "message_posted":
            continue
        payload = parse_json(event.payload)
        if payload and payload.get("kind") == "problem_opened":
            return payload
    return {}


def replies_from_events(events: list[RoomEvent]) -> list[dict[str, Any]]:
    replies: list[dict[str, Any]] = []
    for event in events:
        if event.event_type != "message_posted":
            continue
        payload = parse_json(event.payload)
        if payload and payload.get("kind") == "solver_reply":
            replies.append(payload)
    return replies


async def replay_room_with_retry(agent: AsyncAgent, room_id: str) -> list[RoomEvent]:
    last_error: Exception | None = None
    for attempt in range(ROOM_REPLAY_RETRIES):
        try:
            return await agent.replay_room(room_id)
        except Exception as exc:
            last_error = exc
            if attempt == ROOM_REPLAY_RETRIES - 1:
                break
            await asyncio.sleep(ROOM_REPLAY_DELAY)
    assert last_error is not None
    raise last_error


def build_specialist_reply(
    *,
    capability: str,
    handle: str,
    incident: dict[str, Any],
    events: list[RoomEvent],
    turn: dict[str, Any],
    elapsed_sec: float,
) -> dict[str, Any]:
    scenario = scenario_for_incident(incident)
    response = scenario.get(capability, {})
    content = ""
    if capability == "statuspage.compose":
        customer_update = response.get("customer_update", "We are investigating the issue.")
        content = customer_update
        return {
            "kind": "solver_reply",
            "turn_id": turn.get("turn_id", ""),
            "author": handle,
            "round": turn.get("round", 0),
            "response_type": turn.get("response_type", capability),
            "status": "ok",
            "capability": capability,
            "customer_update": customer_update,
            "content": content,
            "elapsed_sec": round(elapsed_sec, 1),
        }

    summary = response.get("summary", "No specialist summary available.")
    evidence = list(response.get("evidence", []))
    recommendation = response.get("recommendation", "")
    content_lines = [summary]
    if evidence:
        content_lines.append("")
        content_lines.extend(f"- {item}" for item in evidence)
    if recommendation:
        content_lines.append("")
        content_lines.append(f"Recommendation: {recommendation}")
    content = "\n".join(content_lines)

    return {
        "kind": "solver_reply",
        "turn_id": turn.get("turn_id", ""),
        "author": handle,
        "round": turn.get("round", 0),
        "response_type": turn.get("response_type", capability),
        "status": "ok",
        "capability": capability,
        "summary": summary,
        "evidence": evidence,
        "recommendation": recommendation,
        "scenario": scenario_key_for_incident(incident),
        "content": content,
        "elapsed_sec": round(elapsed_sec, 1),
    }


async def run_specialist(
    *,
    handle: str,
    description: str,
    capability: str,
    domains: list[str],
    tags: list[str],
) -> None:
    agent = AsyncAgent(
        handle,
        manifest=Manifest(
            description=description,
            commands=[],
            tags=tags,
            version="1.0.0",
            capabilities=[capability],
            domains=domains,
            input_types=["application/json"],
            output_types=["application/json"],
        ),
        socket=os.environ.get("TAILBUS_SOCKET", "/tmp/tailbusd.sock"),
    )
    seen_turns: set[str] = set()

    @agent.on_message
    async def handle_event(msg: RoomEvent) -> None:
        if not isinstance(msg, RoomEvent):
            return
        if msg.event_type != "message_posted" or msg.content_type != "application/json":
            return
        payload = parse_json(msg.payload)
        if not payload or payload.get("kind") != "turn_request":
            return
        turn_id = str(payload.get("turn_id", ""))
        if not turn_id or turn_id in seen_turns:
            return
        target_handle = payload.get("target_handle", "")
        target_capability = payload.get("target_capability", "")
        if target_handle not in ("", handle) and target_capability not in ("", capability):
            return
        if target_handle and target_handle != handle:
            return
        if not target_handle and target_capability and target_capability != capability:
            return

        seen_turns.add(turn_id)
        say(handle, f"{BOLD}{payload.get('response_type', 'turn')}{RESET} for {capability}")
        started = time.monotonic()
        try:
            events = await replay_room_with_retry(agent, msg.room_id)
            incident = incident_from_events(events)
            reply = build_specialist_reply(
                capability=capability,
                handle=handle,
                incident=incident,
                events=events,
                turn=payload,
                elapsed_sec=time.monotonic() - started,
            )
        except Exception as exc:
            reply = {
                "kind": "solver_reply",
                "turn_id": turn_id,
                "author": handle,
                "round": payload.get("round", 0),
                "response_type": payload.get("response_type", capability),
                "status": "error",
                "capability": capability,
                "error": str(exc),
                "content": "",
                "elapsed_sec": round(time.monotonic() - started, 1),
            }
        if reply["status"] == "ok":
            say(handle, f"{GREEN}posted{RESET} reply in {reply['elapsed_sec']:.1f}s")
        else:
            say(handle, f"{YELLOW}error{RESET}: {reply.get('error', 'unknown error')}")
        await agent.post_room_message(
            msg.room_id,
            json.dumps(reply),
            content_type="application/json",
            trace_id=turn_id,
        )

    say(handle, "connecting...")
    async with agent:
        await agent.register()
        say(handle, f"{GREEN}ready{RESET} — capability {capability}")
        await agent.run_forever()


def build_internal_summary(
    incident: dict[str, Any],
    specialist_replies: list[dict[str, Any]],
    hypothesis: str,
    customer_update: str,
) -> str:
    lines = [
        f"Incident: {incident.get('title', 'Unnamed incident')}",
        f"Severity: {incident.get('severity', 'sev?')}",
        f"Customer impact: {incident.get('customer_impact', '')}",
        "",
        "Hypothesis:",
        hypothesis,
        "",
        "Specialist findings:",
    ]
    for reply in specialist_replies:
        if reply.get("capability") == "statuspage.compose":
            continue
        lines.append(f"- {reply.get('author', '?')} [{reply.get('capability', '?')}]")
        if reply.get("summary"):
            lines.append(f"  Summary: {reply['summary']}")
        for evidence in reply.get("evidence", []):
            lines.append(f"  Evidence: {evidence}")
        if reply.get("recommendation"):
            lines.append(f"  Recommendation: {reply['recommendation']}")
    lines.extend(
        [
            "",
            "Customer update:",
            customer_update,
        ]
    )
    return "\n".join(lines)


def render_markdown_transcript(events: list[RoomEvent]) -> str:
    lines = ["## Room Transcript", ""]
    for event in events:
        prefix = f"- seq {event.room_seq}: "
        if event.event_type != "message_posted":
            lines.append(prefix + f"{event.event_type} by `{event.sender_handle}`")
            continue
        payload = parse_json(event.payload)
        if not payload:
            lines.append(prefix + f"`{event.sender_handle}` posted `{event.payload}`")
            continue
        kind = payload.get("kind", "unknown")
        if kind == "problem_opened":
            lines.append(prefix + f"incident opened `{payload.get('title', '')}`")
        elif kind == "investigation_started":
            members = payload.get("members", [])
            if isinstance(members, list) and members:
                lines.append(prefix + f"investigation started with {', '.join(f'`{item}`' for item in members)}")
            else:
                lines.append(prefix + "investigation started")
        elif kind == "specialist_discovered":
            lines.append(
                prefix
                + f"discovered `{payload.get('target_handle', '?')}`"
                + f" for `{payload.get('target_capability', '?')}`"
            )
            reasons = payload.get("match_reasons", [])
            if isinstance(reasons, list) and reasons:
                lines.append(f"  reasons: {', '.join(str(item) for item in reasons)}")
        elif kind == "turn_request":
            lines.append(
                prefix
                + f"turn request to `{payload.get('target_handle', payload.get('target_capability', '?'))}`"
                + f" [{payload.get('response_type', '?')}]"
            )
            lines.append(f"  instruction: {payload.get('instruction', '')}")
        elif kind == "solver_reply":
            lines.append(
                prefix
                + f"reply from `{payload.get('author', event.sender_handle)}`"
                + f" status={payload.get('status', 'unknown')}"
            )
            if payload.get("summary"):
                lines.append(f"  summary: {payload['summary']}")
            if payload.get("customer_update"):
                lines.append(f"  customer_update: {payload['customer_update']}")
        elif kind == "final_summary":
            lines.append(prefix + "final summary posted")
        elif kind == "turn_timeout":
            lines.append(prefix + f"turn timeout for `{payload.get('target_handle', '?')}`")
        else:
            lines.append(prefix + kind)
    return "\n".join(lines)


def render_llm_transcript(events: list[RoomEvent], *, limit_chars: int = 12000) -> str:
    parts: list[str] = []
    for event in events:
        if event.event_type != "message_posted":
            continue
        payload = parse_json(event.payload)
        if not payload:
            continue
        kind = payload.get("kind", "unknown")
        if kind == "problem_opened":
            parts.append(
                "[incident_opened]\n"
                f"title={payload.get('title', '')}\n"
                f"severity={payload.get('severity', '')}\n"
                f"impact={payload.get('customer_impact', '')}"
            )
        elif kind == "investigation_started":
            parts.append(
                "[investigation_started]\n"
                f"members={','.join(str(item) for item in payload.get('members', []))}"
            )
        elif kind == "specialist_discovered":
            parts.append(
                "[specialist_discovered]\n"
                f"target_handle={payload.get('target_handle', '')}\n"
                f"target_capability={payload.get('target_capability', '')}\n"
                f"reasons={','.join(str(item) for item in payload.get('match_reasons', []))}"
            )
        elif kind == "turn_request":
            parts.append(
                "[turn_request]\n"
                f"target_handle={payload.get('target_handle', '')}\n"
                f"target_capability={payload.get('target_capability', '')}\n"
                f"response_type={payload.get('response_type', '')}\n"
                f"instruction={payload.get('instruction', '')}"
            )
        elif kind == "solver_reply":
            parts.append(
                "[solver_reply]\n"
                f"author={payload.get('author', '')}\n"
                f"capability={payload.get('capability', '')}\n"
                f"status={payload.get('status', '')}\n"
                f"summary={payload.get('summary', '')}\n"
                f"content={payload.get('content', '')}"
            )
        elif kind == "hypothesis":
            parts.append(f"[hypothesis]\nsummary={payload.get('summary', '')}")
        elif kind == "final_summary":
            parts.append(f"[final_summary]\ncustomer_update={payload.get('customer_update', '')}")
        elif kind == "turn_timeout":
            parts.append(
                "[turn_timeout]\n"
                f"target_handle={payload.get('target_handle', '')}\n"
                f"target_capability={payload.get('target_capability', '')}"
            )
    text = "\n\n".join(parts)
    if len(text) > limit_chars:
        return text[-limit_chars:]
    return text


def llm_call(system: str, user: str, *, temperature: float = 0.2, max_tokens: int = 1200) -> str:
    body: dict[str, Any] = {
        "messages": [
            {"role": "system", "content": system},
            {"role": "user", "content": user},
        ],
        "temperature": temperature,
        "max_tokens": max_tokens,
    }
    if LLM_MODEL:
        body["model"] = LLM_MODEL
    request = urllib.request.Request(
        f"{LLM_BASE_URL}/chat/completions",
        data=json.dumps(body).encode("utf-8"),
        headers={"Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(request, timeout=180) as response:
            result = json.loads(response.read())
            content = result["choices"][0]["message"]["content"]
            if "<think>" in content:
                parts = content.split("</think>")
                content = parts[-1].strip() if len(parts) > 1 else content
            return content.strip()
    except urllib.error.URLError as exc:
        return f"[LLM error] Could not reach LM Studio at {LLM_BASE_URL}: {exc.reason}"
    except Exception as exc:
        return f"[LLM error] {exc}"


async def run_codex_prompt(prompt: str, *, timeout: int | None = None, model: str | None = None, prefix: str = "incident_room_codex") -> str:
    timeout = timeout or CODEX_TIMEOUT
    model = model or CODEX_MODEL
    output_file = str(Path(tempfile.gettempdir()) / f"{prefix}_{int(time.time() * 1000)}.txt")
    args = [
        "codex",
        "exec",
        prompt,
        "-o",
        output_file,
        "--ephemeral",
    ]
    if model:
        args.extend(["-m", model])
    try:
        proc = await asyncio.create_subprocess_exec(
            *args,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )
        stdout, stderr = await asyncio.wait_for(proc.communicate(), timeout=timeout)
        if proc.returncode != 0:
            err = stderr.decode().strip() if stderr else "unknown error"
            if os.path.exists(output_file):
                with open(output_file, "r", encoding="utf-8") as handle:
                    content = handle.read().strip()
                if content:
                    return content
            return f"[codex error] exit code {proc.returncode}: {err}"
        if os.path.exists(output_file):
            with open(output_file, "r", encoding="utf-8") as handle:
                return handle.read().strip()
        return stdout.decode().strip() or "[codex error] no output produced"
    except asyncio.TimeoutError:
        proc.kill()
        return f"[codex error] timed out after {timeout}s"
    except FileNotFoundError:
        return "[codex error] 'codex' CLI not found"
    except Exception as exc:
        return f"[codex error] {exc}"
    finally:
        if os.path.exists(output_file):
            try:
                os.unlink(output_file)
            except OSError:
                pass
