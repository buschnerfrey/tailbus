"""SyncAgent — synchronous wrapper around AsyncAgent."""

from __future__ import annotations

import asyncio
import threading
from typing import Any, Callable

from ._agent import AsyncAgent
from ._protocol import (
    HandleEntry,
    HandleMatch,
    Introspected,
    Manifest,
    Message,
    Opened,
    RoomEvent,
    RoomInfo,
    RoomPosted,
    Registered,
    Resolved,
    Sent,
    SessionInfo,
)

__all__ = ["SyncAgent"]


class SyncAgent:
    """Synchronous tailbus agent.

    Creates a dedicated asyncio event loop on a daemon thread and delegates
    all operations to an underlying AsyncAgent.

    Usage::

        with SyncAgent("my-agent") as agent:
            agent.register()
            opened = agent.open_session("other-agent", "hello")
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

        self._loop: asyncio.AbstractEventLoop | None = None
        self._thread: threading.Thread | None = None
        self._async_agent: AsyncAgent | None = None

    # ── Properties ──────────────────────────────────────────────────

    @property
    def handle(self) -> str:
        return self._handle

    @property
    def is_registered(self) -> bool:
        if self._async_agent is None:
            return False
        return self._async_agent.is_registered

    # ── Lifecycle ───────────────────────────────────────────────────

    def start(self) -> None:
        """Start the background event loop and bridge subprocess."""
        self._loop = asyncio.new_event_loop()
        self._thread = threading.Thread(
            target=self._loop.run_forever,
            daemon=True,
            name="tailbus-sync",
        )
        self._thread.start()

        self._async_agent = AsyncAgent(
            self._handle,
            manifest=self._manifest,
            binary=self._binary,
            socket=self._socket,
        )
        self._run(self._async_agent.start())

    def close(self) -> None:
        """Stop the bridge subprocess and background event loop."""
        if self._async_agent is not None:
            self._run(self._async_agent.close())
            self._async_agent = None

        if self._loop is not None:
            self._loop.call_soon_threadsafe(self._loop.stop)
            if self._thread is not None:
                self._thread.join(timeout=5.0)
                self._thread = None
            self._loop.close()
            self._loop = None

    def __enter__(self) -> SyncAgent:
        self.start()
        return self

    def __exit__(self, *exc: Any) -> None:
        self.close()

    # ── Registration ────────────────────────────────────────────────

    def register(self, *, manifest: Manifest | None = None) -> Registered:
        """Register this agent with the daemon."""
        assert self._async_agent is not None
        return self._run(self._async_agent.register(manifest=manifest))

    # ── Session operations ──────────────────────────────────────────

    def open_session(
        self,
        to: str,
        payload: str,
        *,
        content_type: str = "text/plain",
        trace_id: str = "",
    ) -> Opened:
        """Open a new session with another agent."""
        assert self._async_agent is not None
        return self._run(
            self._async_agent.open_session(
                to, payload, content_type=content_type, trace_id=trace_id
            )
        )

    def send(
        self,
        session: str,
        payload: str,
        *,
        content_type: str = "text/plain",
    ) -> Sent:
        """Send a message within an existing session."""
        assert self._async_agent is not None
        return self._run(
            self._async_agent.send(session, payload, content_type=content_type)
        )

    def resolve(
        self,
        session: str,
        payload: str = "",
        *,
        content_type: str = "text/plain",
    ) -> Resolved:
        """Resolve (close) a session."""
        assert self._async_agent is not None
        return self._run(
            self._async_agent.resolve(session, payload, content_type=content_type)
        )

    # ── Discovery ───────────────────────────────────────────────────

    def introspect(self, handle: str) -> Introspected:
        """Introspect a handle's service manifest."""
        assert self._async_agent is not None
        return self._run(self._async_agent.introspect(handle))

    def list_handles(self, *, tags: list[str] | None = None) -> list[HandleEntry]:
        """List registered handles."""
        assert self._async_agent is not None
        return self._run(self._async_agent.list_handles(tags=tags))

    def find_handles(
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
        assert self._async_agent is not None
        return self._run(
            self._async_agent.find_handles(
                capabilities=capabilities,
                domains=domains,
                tags=tags,
                command_name=command_name,
                version=version,
                limit=limit,
            )
        )

    def list_sessions(self) -> list[SessionInfo]:
        """List active sessions."""
        assert self._async_agent is not None
        return self._run(self._async_agent.list_sessions())

    def create_room(self, title: str, members: list[str] | None = None) -> str:
        assert self._async_agent is not None
        return self._run(self._async_agent.create_room(title, members))

    def join_room(self, room_id: str) -> bool:
        assert self._async_agent is not None
        return self._run(self._async_agent.join_room(room_id))

    def leave_room(self, room_id: str) -> bool:
        assert self._async_agent is not None
        return self._run(self._async_agent.leave_room(room_id))

    def post_room_message(self, room_id: str, payload: str, *, content_type: str = "text/plain", trace_id: str = "") -> RoomPosted:
        assert self._async_agent is not None
        return self._run(self._async_agent.post_room_message(room_id, payload, content_type=content_type, trace_id=trace_id))

    def list_rooms(self) -> list[RoomInfo]:
        assert self._async_agent is not None
        return self._run(self._async_agent.list_rooms())

    def list_room_members(self, room_id: str) -> list[str]:
        assert self._async_agent is not None
        return self._run(self._async_agent.list_room_members(room_id))

    def replay_room(self, room_id: str, *, since_seq: int = 0) -> list[RoomEvent]:
        assert self._async_agent is not None
        return self._run(self._async_agent.replay_room(room_id, since_seq=since_seq))

    def close_room(self, room_id: str) -> bool:
        assert self._async_agent is not None
        return self._run(self._async_agent.close_room(room_id))

    # ── Message handling ────────────────────────────────────────────

    def on_message(self, fn: Callable[[Message | RoomEvent], None]) -> Callable[[Message | RoomEvent], None]:
        """Register a message handler (sync callback)."""
        assert self._async_agent is not None
        self._async_agent.on_message(fn)
        return fn

    def run_forever(self) -> None:
        """Block until close() is called or the bridge dies."""
        assert self._async_agent is not None
        self._run(self._async_agent.run_forever())

    # ── Internal ────────────────────────────────────────────────────

    def _run(self, coro: Any) -> Any:
        """Submit a coroutine to the background loop and wait for the result."""
        assert self._loop is not None
        future = asyncio.run_coroutine_threadsafe(coro, self._loop)
        return future.result()
