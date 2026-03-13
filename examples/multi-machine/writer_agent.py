#!/usr/bin/env python3
"""Writer agent for the multi-machine example."""

import asyncio
import json
import os
import sys
import urllib.request

sys.path.insert(0, "/sdk/python/src")
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "sdk", "python", "src"))

from tailbus import AsyncAgent, Manifest, Message

LLM_BASE_URL = os.environ.get("LLM_BASE_URL", "http://host.docker.internal:1234/v1")
LLM_MODEL = os.environ.get("LLM_MODEL", "")

SYSTEM_PROMPT = """You are a professional writer. Turn research findings and critique into a polished final document.

Your output should be clear, concise, and actionable. Do not include hidden reasoning."""

agent = AsyncAgent(
    "writer",
    manifest=Manifest(
        description="Professional writer — synthesizes research and critique into polished output",
        tags=["llm", "writing"],
        version="1.0.0",
    ),
    socket=os.environ.get("TAILBUS_SOCKET", "/tmp/tailbusd.sock"),
)


def llm_call(messages: list[dict]) -> str:
    body = {"messages": messages, "temperature": 0.7, "max_tokens": 2048}
    if LLM_MODEL:
        body["model"] = LLM_MODEL
    data = json.dumps(body).encode()
    req = urllib.request.Request(
        f"{LLM_BASE_URL}/chat/completions",
        data=data,
        headers={"Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(req, timeout=120) as resp:
            result = json.loads(resp.read())
            content = result["choices"][0]["message"]["content"]
            if "<think>" in content:
                parts = content.split("</think>")
                content = parts[-1].strip() if len(parts) > 1 else content
            return content
    except Exception as exc:
        return f"[LLM error] {exc}"


@agent.on_message
async def handle(msg: Message):
    try:
        data = json.loads(msg.payload)
        topic = data.get("topic", "unknown topic")
        findings = data.get("findings", "")
        critique = data.get("critique", "")
    except (json.JSONDecodeError, TypeError):
        topic = "unknown topic"
        findings = msg.payload
        critique = ""

    print(f"[writer] writing final output on: {topic}", flush=True)

    prompt = f"Topic: {topic}\n\nResearch findings:\n{findings}\n\n"
    if critique:
        prompt += f"Critical review (address these concerns):\n{critique}\n\n"
    prompt += "Write the final polished document."

    loop = asyncio.get_running_loop()
    output = await loop.run_in_executor(
        None,
        llm_call,
        [
            {"role": "system", "content": SYSTEM_PROMPT},
            {"role": "user", "content": prompt},
        ],
    )

    print("[writer] final document ready", flush=True)
    await agent.resolve(msg.session, output)


async def main():
    print(f"[writer] starting, LLM API: {LLM_BASE_URL}", flush=True)
    async with agent:
        await agent.register()
        print("[writer] registered, listening for messages...", flush=True)
        await agent.run_forever()


if __name__ == "__main__":
    asyncio.run(main())
