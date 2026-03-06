"""Tests for tailbus._agent — AsyncAgent with mocked subprocess."""

import asyncio
import json
import unittest
from unittest.mock import AsyncMock, patch

from tailbus._agent import AsyncAgent
from tailbus._errors import (
    AlreadyRegisteredError,
    BridgeDiedError,
    BridgeError,
    NotRegisteredError,
)
from tailbus._protocol import (
    HandleEntry,
    Manifest,
    Message,
    Opened,
    Registered,
    Resolved,
    RoomEvent,
    RoomPosted,
    Sent,
    SessionInfo,
)


class FakeStdout:
    """Fake async readline()-able stdout backed by an asyncio.Queue."""

    def __init__(self) -> None:
        self._queue: asyncio.Queue[bytes] = asyncio.Queue()

    async def readline(self) -> bytes:
        return await self._queue.get()

    def feed(self, line: str) -> None:
        """Enqueue a JSON line for the reader to consume."""
        self._queue.put_nowait((line.rstrip("\n") + "\n").encode("utf-8"))

    def feed_eof(self) -> None:
        """Signal EOF."""
        self._queue.put_nowait(b"")


class FakeStdin:
    """Fake async stdin that captures writes and auto-feeds responses."""

    def __init__(self, stdout: FakeStdout) -> None:
        self.written: list[bytes] = []
        self._stdout = stdout
        self._auto_responses: list[dict] = []

    def write(self, data: bytes) -> None:
        self.written.append(data)
        # Auto-feed the next queued response when a command is written
        if self._auto_responses:
            resp = self._auto_responses.pop(0)
            self._stdout.feed(json.dumps(resp))

    async def drain(self) -> None:
        pass

    def close(self) -> None:
        pass

    def queue_response(self, resp: dict) -> None:
        """Queue a response to be auto-fed when the next command is written."""
        self._auto_responses.append(resp)


class FakeProcess:
    """Fake asyncio.subprocess.Process for testing."""

    def __init__(self) -> None:
        self.stdout = FakeStdout()
        self.stdin = FakeStdin(self.stdout)
        self.returncode: int | None = None
        self._wait_event = asyncio.Event()

    async def wait(self) -> int:
        await self._wait_event.wait()
        return self.returncode or 0

    def terminate(self) -> None:
        self.returncode = -15
        self._wait_event.set()

    def kill(self) -> None:
        self.returncode = -9
        self._wait_event.set()


class AsyncAgentTestCase(unittest.TestCase):
    """Base test case for AsyncAgent tests."""

    def setUp(self) -> None:
        self.fake_process = FakeProcess()
        self.patcher_find = patch(
            "tailbus._agent.find_binary", return_value="/usr/bin/tailbus"
        )
        self.patcher_start = patch(
            "tailbus._agent.start_process",
            new=AsyncMock(return_value=self.fake_process),
        )
        self.patcher_stop = patch(
            "tailbus._agent.stop_process",
            new=AsyncMock(),
        )
        self.patcher_find.start()
        self.patcher_start.start()
        self.patcher_stop.start()

    def tearDown(self) -> None:
        self.patcher_find.stop()
        self.patcher_start.stop()
        self.patcher_stop.stop()

    def _queue(self, data: dict) -> None:
        """Queue a response to auto-feed when the next command is written to stdin."""
        self.fake_process.stdin.queue_response(data)

    def _feed(self, data: dict) -> None:
        """Feed a response directly into stdout (for messages that arrive unprompted)."""
        self.fake_process.stdout.feed(json.dumps(data))


class TestRegister(AsyncAgentTestCase):
    def test_register_succeeds(self) -> None:
        async def run() -> None:
            agent = AsyncAgent("test-agent")
            await agent.start()
            try:
                self._queue({"type": "registered", "handle": "test-agent"})
                result = await agent.register()
                self.assertIsInstance(result, Registered)
                self.assertEqual(result.handle, "test-agent")
                self.assertTrue(agent.is_registered)
            finally:
                await agent.close()

        asyncio.run(run())

    def test_register_with_manifest(self) -> None:
        async def run() -> None:
            manifest = Manifest(
                description="Test agent",
                tags=("test",),
                version="1.0",
                capabilities=("research.company",),
                domains=("finance",),
            )
            agent = AsyncAgent("test-agent", manifest=manifest)
            await agent.start()
            try:
                self._queue({"type": "registered", "handle": "test-agent"})
                await agent.register()

                # Verify the command sent includes manifest
                written = self.fake_process.stdin.written
                self.assertEqual(len(written), 1)
                cmd = json.loads(written[0])
                self.assertEqual(cmd["type"], "register")
                self.assertEqual(cmd["handle"], "test-agent")
                self.assertIn("manifest", cmd)
                self.assertEqual(cmd["manifest"]["description"], "Test agent")
                self.assertEqual(cmd["manifest"]["capabilities"], ["research.company"])
                self.assertEqual(cmd["manifest"]["domains"], ["finance"])
            finally:
                await agent.close()

        asyncio.run(run())

    def test_double_register_raises(self) -> None:
        async def run() -> None:
            agent = AsyncAgent("test-agent")
            await agent.start()
            try:
                self._queue({"type": "registered", "handle": "test-agent"})
                await agent.register()
                with self.assertRaises(AlreadyRegisteredError):
                    await agent.register()
            finally:
                await agent.close()

        asyncio.run(run())


class TestSessionOps(AsyncAgentTestCase):
    def test_open_session(self) -> None:
        async def run() -> None:
            agent = AsyncAgent("test-agent")
            await agent.start()
            try:
                self._queue({"type": "registered", "handle": "test-agent"})
                await agent.register()

                self._queue(
                    {
                        "type": "opened",
                        "session": "s1",
                        "message_id": "m1",
                        "trace_id": "t1",
                    }
                )
                result = await agent.open_session("other", "hello")
                self.assertIsInstance(result, Opened)
                self.assertEqual(result.session, "s1")
            finally:
                await agent.close()

        asyncio.run(run())

    def test_send(self) -> None:
        async def run() -> None:
            agent = AsyncAgent("test-agent")
            await agent.start()
            try:
                self._queue({"type": "registered", "handle": "test-agent"})
                await agent.register()

                self._queue({"type": "sent", "message_id": "m2"})
                result = await agent.send("s1", "follow-up")
                self.assertIsInstance(result, Sent)
                self.assertEqual(result.message_id, "m2")
            finally:
                await agent.close()

        asyncio.run(run())

    def test_resolve(self) -> None:
        async def run() -> None:
            agent = AsyncAgent("test-agent")
            await agent.start()
            try:
                self._queue({"type": "registered", "handle": "test-agent"})
                await agent.register()

                self._queue({"type": "resolved", "message_id": "m3"})
                result = await agent.resolve("s1", "goodbye")
                self.assertIsInstance(result, Resolved)
                self.assertEqual(result.message_id, "m3")
            finally:
                await agent.close()

        asyncio.run(run())

    def test_send_before_register_raises(self) -> None:
        async def run() -> None:
            agent = AsyncAgent("test-agent")
            await agent.start()
            try:
                with self.assertRaises(NotRegisteredError):
                    await agent.send("s1", "hello")
            finally:
                await agent.close()

        asyncio.run(run())

    def test_open_sends_content_type_and_trace_id(self) -> None:
        async def run() -> None:
            agent = AsyncAgent("test-agent")
            await agent.start()
            try:
                self._queue({"type": "registered", "handle": "test-agent"})
                await agent.register()

                self._queue(
                    {
                        "type": "opened",
                        "session": "s1",
                        "message_id": "m1",
                        "trace_id": "custom-trace",
                    }
                )
                await agent.open_session(
                    "other",
                    '{"key":"val"}',
                    content_type="application/json",
                    trace_id="custom-trace",
                )
                cmd = json.loads(self.fake_process.stdin.written[-1])
                self.assertEqual(cmd["content_type"], "application/json")
                self.assertEqual(cmd["trace_id"], "custom-trace")
            finally:
                await agent.close()

        asyncio.run(run())


class TestDiscovery(AsyncAgentTestCase):
    def test_introspect(self) -> None:
        async def run() -> None:
            agent = AsyncAgent("test-agent")
            await agent.start()
            try:
                self._queue(
                    {
                        "type": "introspected",
                        "handle": "other",
                        "found": True,
                        "manifest": {"description": "Other agent"},
                    }
                )
                result = await agent.introspect("other")
                self.assertTrue(result.found)
                self.assertIsNotNone(result.manifest)
            finally:
                await agent.close()

        asyncio.run(run())

    def test_list_handles(self) -> None:
        async def run() -> None:
            agent = AsyncAgent("test-agent")
            await agent.start()
            try:
                self._queue(
                    {
                        "type": "handles",
                        "entries": [
                            {"handle": "a"},
                            {"handle": "b"},
                        ],
                    }
                )
                result = await agent.list_handles()
                self.assertEqual(len(result), 2)
                self.assertIsInstance(result[0], HandleEntry)
            finally:
                await agent.close()

        asyncio.run(run())

    def test_list_handles_with_tags(self) -> None:
        async def run() -> None:
            agent = AsyncAgent("test-agent")
            await agent.start()
            try:
                self._queue({"type": "handles", "entries": [{"handle": "a"}]})
                await agent.list_handles(tags=["sales"])
                cmd = json.loads(self.fake_process.stdin.written[-1])
                self.assertEqual(cmd["tags"], ["sales"])
            finally:
                await agent.close()

        asyncio.run(run())

    def test_find_handles(self) -> None:
        async def run() -> None:
            agent = AsyncAgent("test-agent")
            await agent.start()
            try:
                self._queue(
                    {
                        "type": "handle_matches",
                        "matches": [
                            {
                                "handle": "solver",
                                "score": 160,
                                "match_reasons": ["capability:code.solve", "command:solve"],
                            }
                        ],
                    }
                )
                result = await agent.find_handles(
                    capabilities=["code.solve"],
                    command_name="solve",
                    limit=1,
                )
                self.assertEqual(len(result), 1)
                self.assertEqual(result[0].handle, "solver")
                self.assertEqual(result[0].score, 160)
                cmd = json.loads(self.fake_process.stdin.written[-1])
                self.assertEqual(cmd["type"], "find")
                self.assertEqual(cmd["capabilities"], ["code.solve"])
                self.assertEqual(cmd["command_name"], "solve")
                self.assertEqual(cmd["limit"], 1)
            finally:
                await agent.close()

        asyncio.run(run())

    def test_list_sessions(self) -> None:
        async def run() -> None:
            agent = AsyncAgent("test-agent")
            await agent.start()
            try:
                self._queue({"type": "registered", "handle": "test-agent"})
                await agent.register()

                self._queue(
                    {
                        "type": "sessions",
                        "sessions": [
                            {
                                "session": "s1",
                                "from": "a",
                                "to": "b",
                                "state": "open",
                            }
                        ],
                    }
                )
                result = await agent.list_sessions()
                self.assertEqual(len(result), 1)
                self.assertIsInstance(result[0], SessionInfo)
            finally:
                await agent.close()

        asyncio.run(run())


class TestErrorHandling(AsyncAgentTestCase):
    def test_error_response_raises_bridge_error(self) -> None:
        async def run() -> None:
            agent = AsyncAgent("test-agent")
            await agent.start()
            try:
                agent._is_registered = True
                self._queue(
                    {
                        "type": "error",
                        "error": "session not found",
                        "request_type": "send",
                    }
                )
                with self.assertRaises(BridgeError) as ctx:
                    await agent.send("bad-session", "hello")
                self.assertEqual(ctx.exception.error, "session not found")
                self.assertEqual(ctx.exception.request_type, "send")
            finally:
                await agent.close()

        asyncio.run(run())

    def test_bridge_eof_fails_pending(self) -> None:
        async def run() -> None:
            agent = AsyncAgent("test-agent")
            await agent.start()
            try:
                agent._is_registered = True

                # The stdin write will trigger EOF on stdout
                self.fake_process.stdin._auto_responses = []

                async def eof_soon() -> None:
                    await asyncio.sleep(0.05)
                    self.fake_process.stdout.feed_eof()

                asyncio.create_task(eof_soon())

                with self.assertRaises(BridgeDiedError):
                    await agent.send("s1", "hello")
            finally:
                await agent.close()

        asyncio.run(run())


class TestMessageDispatch(AsyncAgentTestCase):
    def test_async_handler(self) -> None:
        async def run() -> None:
            agent = AsyncAgent("test-agent")
            await agent.start()
            try:
                received: list[Message] = []

                @agent.on_message
                async def handler(msg: Message) -> None:
                    received.append(msg)

                self._queue({"type": "registered", "handle": "test-agent"})
                await agent.register()

                # Feed an incoming message directly (not in response to a command)
                self._feed(
                    {
                        "type": "message",
                        "session": "s1",
                        "from": "alice",
                        "to": "test-agent",
                        "payload": "hello",
                        "content_type": "text/plain",
                        "message_type": "session_open",
                        "trace_id": "t1",
                        "message_id": "m1",
                        "sent_at": 1700000000,
                    }
                )
                # Queue a response for the next command (after the message)
                self._queue({"type": "sent", "message_id": "m2"})
                result = await agent.send("s1", "reply")

                self.assertIsInstance(result, Sent)
                self.assertEqual(len(received), 1)
                self.assertEqual(received[0].from_handle, "alice")
                self.assertEqual(received[0].payload, "hello")
            finally:
                await agent.close()

        asyncio.run(run())

    def test_sync_handler(self) -> None:
        async def run() -> None:
            agent = AsyncAgent("test-agent")
            await agent.start()
            try:
                received: list[Message] = []

                @agent.on_message
                def handler(msg: Message) -> None:
                    received.append(msg)

                self._queue({"type": "registered", "handle": "test-agent"})
                await agent.register()

                # Feed message then queue response
                self._feed(
                    {
                        "type": "message",
                        "session": "s1",
                        "from": "bob",
                        "to": "test-agent",
                        "payload": "hey",
                        "content_type": "text/plain",
                        "message_type": "message",
                        "trace_id": "",
                        "message_id": "m1",
                        "sent_at": 0,
                    }
                )
                self._queue({"type": "sent", "message_id": "m2"})
                await agent.send("s1", "reply")

                self.assertEqual(len(received), 1)
                self.assertEqual(received[0].from_handle, "bob")
            finally:
                await agent.close()

        asyncio.run(run())

    def test_interleaved_messages_and_responses(self) -> None:
        async def run() -> None:
            agent = AsyncAgent("test-agent")
            await agent.start()
            try:
                received: list[Message] = []

                @agent.on_message
                async def handler(msg: Message) -> None:
                    received.append(msg)

                self._queue({"type": "registered", "handle": "test-agent"})
                await agent.register()

                # First: message arrives, then command response
                self._feed(
                    {
                        "type": "message",
                        "session": "s1",
                        "from": "x",
                        "to": "test-agent",
                        "payload": "first",
                        "content_type": "text/plain",
                        "message_type": "message",
                        "trace_id": "",
                        "message_id": "im1",
                        "sent_at": 0,
                    }
                )
                self._queue({"type": "sent", "message_id": "m1"})
                result1 = await agent.send("s1", "r1")
                self.assertEqual(result1.message_id, "m1")

                # Second round
                self._feed(
                    {
                        "type": "message",
                        "session": "s1",
                        "from": "x",
                        "to": "test-agent",
                        "payload": "second",
                        "content_type": "text/plain",
                        "message_type": "message",
                        "trace_id": "",
                        "message_id": "im2",
                        "sent_at": 0,
                    }
                )
                self._queue({"type": "sent", "message_id": "m2"})
                result2 = await agent.send("s1", "r2")
                self.assertEqual(result2.message_id, "m2")

                self.assertEqual(len(received), 2)
                self.assertEqual(received[0].payload, "first")
                self.assertEqual(received[1].payload, "second")
            finally:
                await agent.close()

        asyncio.run(run())

    def test_room_event_dispatch(self) -> None:
        async def run() -> None:
            agent = AsyncAgent("test-agent")
            await agent.start()
            try:
                received: list[RoomEvent] = []

                @agent.on_message
                async def handler(msg: Message | RoomEvent) -> None:
                    if isinstance(msg, RoomEvent):
                        received.append(msg)

                self._queue({"type": "registered", "handle": "test-agent"})
                await agent.register()

                self._feed(
                    {
                        "type": "room_event",
                        "room_id": "room-1",
                        "room_seq": 1,
                        "sender": "alice",
                        "payload": "hello room",
                        "content_type": "text/plain",
                        "event_type": "message_posted",
                        "event_id": "evt-1",
                        "sent_at": 1700000000,
                    }
                )
                await asyncio.sleep(0.01)

                self.assertEqual(len(received), 1)
                self.assertEqual(received[0].room_id, "room-1")
            finally:
                await agent.close()

        asyncio.run(run())


class TestRoomOps(AsyncAgentTestCase):
    def test_room_command_round_trip(self) -> None:
        async def run() -> None:
            agent = AsyncAgent("test-agent")
            await agent.start()
            try:
                self._queue({"type": "registered", "handle": "test-agent"})
                await agent.register()

                self._queue({"type": "room_created", "room_id": "room-1"})
                room_id = await agent.create_room("design-review", ["other"])
                self.assertEqual(room_id, "room-1")

                self._queue({"type": "room_posted", "event_id": "evt-1", "room_seq": 2})
                posted = await agent.post_room_message("room-1", "hello room")
                self.assertIsInstance(posted, RoomPosted)
                self.assertEqual(posted.event_id, "evt-1")

                written = [json.loads(item) for item in self.fake_process.stdin.written]
                self.assertEqual(written[1]["type"], "create_room")
                self.assertEqual(written[2]["type"], "post_room")
                self.assertEqual(written[2]["room_id"], "room-1")
            finally:
                await agent.close()

        asyncio.run(run())


class TestContextManager(AsyncAgentTestCase):
    def test_async_context_manager(self) -> None:
        async def run() -> None:
            async with AsyncAgent("test-agent") as agent:
                self.assertTrue(agent._is_started)
                self._queue({"type": "registered", "handle": "test-agent"})
                await agent.register()
                self.assertTrue(agent.is_registered)
            self.assertFalse(agent._is_started)

        asyncio.run(run())


if __name__ == "__main__":
    unittest.main()
