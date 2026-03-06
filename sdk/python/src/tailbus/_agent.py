"""AsyncAgent — core async implementation for the tailbus Python SDK."""

from __future__ import annotations

import asyncio
import inspect
from collections import deque
from typing import Any, Awaitable, Callable, Union

from ._errors import (
    AlreadyRegisteredError,
    BridgeDiedError,
    BridgeError,
    NotRegisteredError,
)
from ._process import build_command, find_binary, start_process, stop_process
from ._protocol import (
    Error,
    HandleEntry,
    HandleList,
    HandleMatch,
    HandleMatches,
    Introspected,
    Manifest,
    Message,
    Opened,
    RoomCreated,
    RoomEvent,
    RoomInfo,
    RoomList,
    RoomMembers,
    RoomOpResult,
    RoomPosted,
    RoomReplay,
    Registered,
    Resolved,
    Response,
    Sent,
    SessionInfo,
    SessionList,
    parse_response,
    serialize_command,
)

__all__ = ["AsyncAgent"]

MessageHandler = Union[
    Callable[[Union[Message, RoomEvent]], Awaitable[None]],
    Callable[[Union[Message, RoomEvent]], None],
]


class AsyncAgent:
    """Async tailbus agent that communicates via the JSON-lines bridge subprocess.

    Usage::

        async with AsyncAgent("my-agent") as agent:
            await agent.register()
            opened = await agent.open_session("other-agent", "hello")
            print(opened.session)
    """

    def __init__(
        self,
        handle: str,
        *,
        manifest: Manifest | None = None,
        binary: str = "tailbus",
        socket: str = "/tmp/tailbusd.sock",
    ) -> None:
        self._handle = handle
        self._manifest = manifest
        self._binary = binary
        self._socket = socket

        self._process: asyncio.subprocess.Process | None = None
        self._reader_task: asyncio.Task[None] | None = None
        self._pending: deque[asyncio.Future[Response]] = deque()
        self._is_registered = False
        self._is_started = False
        self._handler: MessageHandler | None = None
        self._run_forever_event: asyncio.Event | None = None

    # ── Properties ──────────────────────────────────────────────────

    @property
    def handle(self) -> str:
        return self._handle

    @property
    def is_registered(self) -> bool:
        return self._is_registered

    # ── Lifecycle ───────────────────────────────────────────────────

    async def start(self) -> None:
        """Start the bridge subprocess and background reader."""
        if self._is_started:
            return
        binary_path = find_binary(self._binary)
        cmd = build_command(binary_path, self._socket)
        self._process = await start_process(cmd)
        self._reader_task = asyncio.create_task(self._reader_loop())
        self._is_started = True

    async def close(self) -> None:
        """Stop the bridge subprocess and clean up."""
        if not self._is_started:
            return
        self._is_started = False

        if self._run_forever_event is not None:
            self._run_forever_event.set()

        if self._process is not None:
            await stop_process(self._process)
            self._process = None

        if self._reader_task is not None:
            self._reader_task.cancel()
            try:
                await self._reader_task
            except asyncio.CancelledError:
                pass
            self._reader_task = None

        # Fail any pending futures
        self._fail_pending(BridgeDiedError())

    async def __aenter__(self) -> AsyncAgent:
        await self.start()
        return self

    async def __aexit__(self, *exc: Any) -> None:
        await self.close()

    # ── Registration ────────────────────────────────────────────────

    async def register(self, *, manifest: Manifest | None = None) -> Registered:
        """Register this agent with the daemon."""
        if self._is_registered:
            raise AlreadyRegisteredError()

        m = manifest or self._manifest
        cmd: dict[str, Any] = {"type": "register", "handle": self._handle}
        if m is not None:
            cmd["manifest"] = m.to_dict()

        resp = await self._send_command(cmd)
        assert isinstance(resp, Registered)
        self._is_registered = True
        return resp

    # ── Session operations ──────────────────────────────────────────

    async def open_session(
        self,
        to: str,
        payload: str,
        *,
        content_type: str = "text/plain",
        trace_id: str = "",
    ) -> Opened:
        """Open a new session with another agent."""
        self._require_registered()
        cmd: dict[str, Any] = {
            "type": "open",
            "to": to,
            "payload": payload,
        }
        if content_type != "text/plain":
            cmd["content_type"] = content_type
        if trace_id:
            cmd["trace_id"] = trace_id

        resp = await self._send_command(cmd)
        assert isinstance(resp, Opened)
        return resp

    async def send(
        self,
        session: str,
        payload: str,
        *,
        content_type: str = "text/plain",
    ) -> Sent:
        """Send a message within an existing session."""
        self._require_registered()
        cmd: dict[str, Any] = {
            "type": "send",
            "session": session,
            "payload": payload,
        }
        if content_type != "text/plain":
            cmd["content_type"] = content_type

        resp = await self._send_command(cmd)
        assert isinstance(resp, Sent)
        return resp

    async def resolve(
        self,
        session: str,
        payload: str = "",
        *,
        content_type: str = "text/plain",
    ) -> Resolved:
        """Resolve (close) a session with an optional final message."""
        self._require_registered()
        cmd: dict[str, Any] = {
            "type": "resolve",
            "session": session,
        }
        if payload:
            cmd["payload"] = payload
        if content_type != "text/plain":
            cmd["content_type"] = content_type

        resp = await self._send_command(cmd)
        assert isinstance(resp, Resolved)
        return resp

    # ── Discovery ───────────────────────────────────────────────────

    async def introspect(self, handle: str) -> Introspected:
        """Introspect a handle's service manifest."""
        cmd: dict[str, Any] = {"type": "introspect", "handle": handle}
        resp = await self._send_command(cmd)
        assert isinstance(resp, Introspected)
        return resp

    async def list_handles(self, *, tags: list[str] | None = None) -> list[HandleEntry]:
        """List registered handles, optionally filtered by tags."""
        cmd: dict[str, Any] = {"type": "list"}
        if tags:
            cmd["tags"] = tags
        resp = await self._send_command(cmd)
        assert isinstance(resp, HandleList)
        return list(resp.entries)

    async def find_handles(
        self,
        *,
        capabilities: list[str] | None = None,
        domains: list[str] | None = None,
        tags: list[str] | None = None,
        command_name: str = "",
        version: str = "",
        limit: int = 0,
    ) -> list[HandleMatch]:
        """Find ranked handles matching structured discovery constraints."""
        cmd: dict[str, Any] = {"type": "find"}
        if capabilities:
            cmd["capabilities"] = capabilities
        if domains:
            cmd["domains"] = domains
        if tags:
            cmd["tags"] = tags
        if command_name:
            cmd["command_name"] = command_name
        if version:
            cmd["version"] = version
        if limit > 0:
            cmd["limit"] = limit
        resp = await self._send_command(cmd)
        assert isinstance(resp, HandleMatches)
        return list(resp.matches)

    async def list_sessions(self) -> list[SessionInfo]:
        """List active sessions for this agent."""
        self._require_registered()
        cmd: dict[str, Any] = {"type": "sessions"}
        resp = await self._send_command(cmd)
        assert isinstance(resp, SessionList)
        return list(resp.sessions)

    async def create_room(self, title: str, members: list[str] | None = None) -> str:
        """Create a shared room owned by this daemon."""
        self._require_registered()
        cmd: dict[str, Any] = {"type": "create_room", "title": title, "members": members or []}
        resp = await self._send_command(cmd)
        assert isinstance(resp, RoomCreated)
        return resp.room_id

    async def join_room(self, room_id: str) -> bool:
        """Join an existing room as this agent."""
        self._require_registered()
        resp = await self._send_command({"type": "join_room", "room_id": room_id})
        assert isinstance(resp, RoomOpResult)
        return resp.ok

    async def leave_room(self, room_id: str) -> bool:
        """Leave a shared room."""
        self._require_registered()
        resp = await self._send_command({"type": "leave_room", "room_id": room_id})
        assert isinstance(resp, RoomOpResult)
        return resp.ok

    async def post_room_message(
        self,
        room_id: str,
        payload: str,
        *,
        content_type: str = "text/plain",
        trace_id: str = "",
    ) -> RoomPosted:
        """Post a message to a shared room."""
        self._require_registered()
        cmd: dict[str, Any] = {"type": "post_room", "room_id": room_id, "payload": payload}
        if content_type != "text/plain":
            cmd["content_type"] = content_type
        if trace_id:
            cmd["trace_id"] = trace_id
        resp = await self._send_command(cmd)
        assert isinstance(resp, RoomPosted)
        return resp

    async def list_rooms(self) -> list[RoomInfo]:
        """List rooms this agent belongs to."""
        self._require_registered()
        resp = await self._send_command({"type": "list_rooms"})
        assert isinstance(resp, RoomList)
        return list(resp.rooms)

    async def list_room_members(self, room_id: str) -> list[str]:
        """List members of a room."""
        self._require_registered()
        resp = await self._send_command({"type": "room_members", "room_id": room_id})
        assert isinstance(resp, RoomMembers)
        return list(resp.members)

    async def replay_room(self, room_id: str, *, since_seq: int = 0) -> list[RoomEvent]:
        """Replay retained room events since the given room sequence."""
        self._require_registered()
        resp = await self._send_command({"type": "replay_room", "room_id": room_id, "since_seq": since_seq})
        assert isinstance(resp, RoomReplay)
        return list(resp.events)

    async def close_room(self, room_id: str) -> bool:
        """Close a room to new messages."""
        self._require_registered()
        resp = await self._send_command({"type": "close_room", "room_id": room_id})
        assert isinstance(resp, RoomOpResult)
        return resp.ok

    # ── Message handling ────────────────────────────────────────────

    def on_message(
        self, fn: MessageHandler
    ) -> MessageHandler:
        """Decorator to register a message handler.

        Usage::

            @agent.on_message
            async def handler(msg: Message):
                print(msg.payload)
        """
        self._handler = fn
        return fn

    async def run_forever(self) -> None:
        """Block until close() is called or the bridge dies."""
        self._run_forever_event = asyncio.Event()
        await self._run_forever_event.wait()

    # ── Internal ────────────────────────────────────────────────────

    def _require_registered(self) -> None:
        if not self._is_registered:
            raise NotRegisteredError()

    async def _send_command(self, cmd: dict[str, Any]) -> Response:
        """Send a command and wait for the correlated response."""
        if self._process is None or self._process.stdin is None:
            raise BridgeDiedError()

        future: asyncio.Future[Response] = asyncio.get_running_loop().create_future()
        self._pending.append(future)

        data = serialize_command(cmd)
        self._process.stdin.write(data)
        await self._process.stdin.drain()

        resp = await future
        if isinstance(resp, Error):
            raise BridgeError(resp.error, resp.request_type)
        return resp

    async def _reader_loop(self) -> None:
        """Background task reading lines from bridge stdout."""
        assert self._process is not None
        assert self._process.stdout is not None

        try:
            while True:
                line = await self._process.stdout.readline()
                if not line:
                    # EOF — bridge died
                    self._fail_pending(BridgeDiedError(self._process.returncode))
                    if self._run_forever_event is not None:
                        self._run_forever_event.set()
                    return

                text = line.decode("utf-8").strip()
                if not text:
                    continue

                try:
                    resp = parse_response(text)
                except (ValueError, KeyError):
                    continue

                if isinstance(resp, (Message, RoomEvent)):
                    asyncio.create_task(self._dispatch_message(resp))
                elif self._pending:
                    future = self._pending.popleft()
                    if not future.cancelled():
                        future.set_result(resp)
        except asyncio.CancelledError:
            return

    async def _dispatch_message(self, msg: Union[Message, RoomEvent]) -> None:
        """Dispatch an incoming message to the registered handler."""
        if self._handler is None:
            return
        try:
            result = self._handler(msg)
            if inspect.isawaitable(result):
                await result
        except Exception as exc:
            import sys
            print(f"[tailbus] handler error: {exc}", file=sys.stderr, flush=True)

    def _fail_pending(self, exc: Exception) -> None:
        """Fail all pending futures with the given exception."""
        while self._pending:
            future = self._pending.popleft()
            if not future.done():
                future.set_exception(exc)
