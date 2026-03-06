"""Dataclasses and JSON serialization for the tailbus stdio bridge protocol."""

from __future__ import annotations

import json
from dataclasses import dataclass, field
from typing import Any, Union

__all__ = [
    "CommandSpec",
    "Manifest",
    "Registered",
    "Opened",
    "Sent",
    "Resolved",
    "Message",
    "RoomEvent",
    "RoomInfo",
    "RoomCreated",
    "RoomPosted",
    "RoomReplay",
    "RoomMembers",
    "RoomList",
    "RoomOpResult",
    "Introspected",
    "HandleEntry",
    "HandleList",
    "HandleMatch",
    "HandleMatches",
    "SessionInfo",
    "SessionList",
    "Error",
    "Response",
    "serialize_command",
    "parse_response",
]


# ── Outbound (command helpers) ──────────────────────────────────────


@dataclass(frozen=True, slots=True)
class CommandSpec:
    """A command that an agent exposes."""

    name: str
    description: str
    parameters_schema: str = ""

    def to_dict(self) -> dict[str, Any]:
        d: dict[str, Any] = {"name": self.name, "description": self.description}
        if self.parameters_schema:
            d["parameters_schema"] = self.parameters_schema
        return d


@dataclass(frozen=True, slots=True)
class Manifest:
    """Service manifest describing an agent's capabilities."""

    description: str = ""
    commands: tuple[CommandSpec, ...] = ()
    tags: tuple[str, ...] = ()
    version: str = ""
    capabilities: tuple[str, ...] = ()
    domains: tuple[str, ...] = ()
    input_types: tuple[str, ...] = ()
    output_types: tuple[str, ...] = ()

    def to_dict(self) -> dict[str, Any]:
        d: dict[str, Any] = {}
        if self.description:
            d["description"] = self.description
        if self.commands:
            d["commands"] = [c.to_dict() for c in self.commands]
        if self.tags:
            d["tags"] = list(self.tags)
        if self.version:
            d["version"] = self.version
        if self.capabilities:
            d["capabilities"] = list(self.capabilities)
        if self.domains:
            d["domains"] = list(self.domains)
        if self.input_types:
            d["input_types"] = list(self.input_types)
        if self.output_types:
            d["output_types"] = list(self.output_types)
        return d

    @classmethod
    def from_dict(cls, data: dict[str, Any] | None) -> Manifest | None:
        if data is None:
            return None
        commands = tuple(
            CommandSpec(
                name=c["name"],
                description=c.get("description", ""),
                parameters_schema=c.get("parameters_schema", ""),
            )
            for c in data.get("commands", [])
        )
        return cls(
            description=data.get("description", ""),
            commands=commands,
            tags=tuple(data.get("tags", [])),
            version=data.get("version", ""),
            capabilities=tuple(data.get("capabilities", [])),
            domains=tuple(data.get("domains", [])),
            input_types=tuple(data.get("input_types", [])),
            output_types=tuple(data.get("output_types", [])),
        )


# ── Inbound (parsed responses) ─────────────────────────────────────


@dataclass(frozen=True, slots=True)
class Registered:
    """Confirmation that the agent was registered."""

    handle: str


@dataclass(frozen=True, slots=True)
class Opened:
    """Confirmation that a session was opened."""

    session: str
    message_id: str
    trace_id: str


@dataclass(frozen=True, slots=True)
class Sent:
    """Confirmation that a message was sent."""

    message_id: str


@dataclass(frozen=True, slots=True)
class Resolved:
    """Confirmation that a session was resolved."""

    message_id: str


@dataclass(frozen=True, slots=True)
class Message:
    """An incoming message from another agent."""

    session: str
    from_handle: str
    to_handle: str
    payload: str
    content_type: str
    message_type: str
    trace_id: str
    message_id: str
    sent_at: int


@dataclass(frozen=True, slots=True)
class RoomEvent:
    """An incoming room event."""

    room_id: str
    room_seq: int
    sender_handle: str
    subject_handle: str
    payload: str
    content_type: str
    event_type: str
    trace_id: str
    event_id: str
    sent_at: int
    members: tuple[str, ...] = ()


@dataclass(frozen=True, slots=True)
class RoomInfo:
    """Metadata for a shared room."""

    room_id: str
    title: str
    created_by: str
    home_node_id: str
    members: tuple[str, ...]
    status: str
    next_seq: int
    created_at: int
    updated_at: int


@dataclass(frozen=True, slots=True)
class RoomCreated:
    room_id: str


@dataclass(frozen=True, slots=True)
class RoomPosted:
    event_id: str
    room_seq: int


@dataclass(frozen=True, slots=True)
class RoomReplay:
    events: tuple[RoomEvent, ...] = ()


@dataclass(frozen=True, slots=True)
class RoomMembers:
    members: tuple[str, ...] = ()


@dataclass(frozen=True, slots=True)
class RoomList:
    rooms: tuple[RoomInfo, ...] = ()


@dataclass(frozen=True, slots=True)
class RoomOpResult:
    ok: bool


@dataclass(frozen=True, slots=True)
class Introspected:
    """Result of introspecting a handle."""

    handle: str
    found: bool
    manifest: Manifest | None = None


@dataclass(frozen=True, slots=True)
class HandleEntry:
    """A single entry in a handle list."""

    handle: str
    manifest: Manifest | None = None


@dataclass(frozen=True, slots=True)
class HandleList:
    """List of registered handles."""

    entries: tuple[HandleEntry, ...] = ()


@dataclass(frozen=True, slots=True)
class HandleMatch:
    """A ranked discovery match."""

    handle: str
    manifest: Manifest | None = None
    score: int = 0
    match_reasons: tuple[str, ...] = ()


@dataclass(frozen=True, slots=True)
class HandleMatches:
    """Ranked discovery matches."""

    matches: tuple[HandleMatch, ...] = ()


@dataclass(frozen=True, slots=True)
class SessionInfo:
    """Information about an active session."""

    session: str
    from_handle: str
    to_handle: str
    state: str


@dataclass(frozen=True, slots=True)
class SessionList:
    """List of active sessions."""

    sessions: tuple[SessionInfo, ...] = ()


@dataclass(frozen=True, slots=True)
class Error:
    """An error response from the bridge."""

    error: str
    request_type: str


Response = Union[
    Registered,
    Opened,
    Sent,
    Resolved,
    Message,
    RoomEvent,
    RoomInfo,
    RoomCreated,
    RoomPosted,
    RoomReplay,
    RoomMembers,
    RoomList,
    RoomOpResult,
    Introspected,
    HandleList,
    HandleMatches,
    SessionList,
    Error,
]


# ── Serialization / Parsing ────────────────────────────────────────


def serialize_command(cmd: dict[str, Any]) -> bytes:
    """Encode a command dict as a JSON line (bytes with trailing newline)."""
    return json.dumps(cmd, separators=(",", ":")).encode("utf-8") + b"\n"


_PARSERS: dict[str, Any] = {}


def _register_parser(type_name: str):  # type: ignore[no-untyped-def]
    def decorator(fn):  # type: ignore[no-untyped-def]
        _PARSERS[type_name] = fn
        return fn
    return decorator


def _room_info_from_dict(d: dict[str, Any]) -> RoomInfo:
    return RoomInfo(
        room_id=d["room_id"],
        title=d.get("title", ""),
        created_by=d.get("created_by", ""),
        home_node_id=d.get("home_node_id", ""),
        members=tuple(d.get("members", [])),
        status=d.get("status", ""),
        next_seq=d.get("next_seq", 0),
        created_at=d.get("created_at", 0),
        updated_at=d.get("updated_at", 0),
    )


def _room_event_from_dict(d: dict[str, Any]) -> RoomEvent:
    return RoomEvent(
        room_id=d["room_id"],
        room_seq=d.get("room_seq", 0),
        sender_handle=d.get("sender", ""),
        subject_handle=d.get("subject", ""),
        payload=d.get("payload", ""),
        content_type=d.get("content_type", "text/plain"),
        event_type=d.get("event_type", ""),
        trace_id=d.get("trace_id", ""),
        event_id=d.get("event_id", ""),
        sent_at=d.get("sent_at", 0),
        members=tuple(d.get("members", [])),
    )


@_register_parser("registered")
def _parse_registered(d: dict[str, Any]) -> Registered:
    return Registered(handle=d["handle"])


@_register_parser("opened")
def _parse_opened(d: dict[str, Any]) -> Opened:
    return Opened(
        session=d["session"],
        message_id=d["message_id"],
        trace_id=d.get("trace_id", ""),
    )


@_register_parser("sent")
def _parse_sent(d: dict[str, Any]) -> Sent:
    return Sent(message_id=d["message_id"])


@_register_parser("resolved")
def _parse_resolved(d: dict[str, Any]) -> Resolved:
    return Resolved(message_id=d["message_id"])


@_register_parser("message")
def _parse_message(d: dict[str, Any]) -> Message:
    return Message(
        session=d["session"],
        from_handle=d["from"],
        to_handle=d["to"],
        payload=d.get("payload", ""),
        content_type=d.get("content_type", "text/plain"),
        message_type=d.get("message_type", "message"),
        trace_id=d.get("trace_id", ""),
        message_id=d.get("message_id", ""),
        sent_at=d.get("sent_at", 0),
    )


@_register_parser("room_event")
def _parse_room_event(d: dict[str, Any]) -> RoomEvent:
    return _room_event_from_dict(d)


@_register_parser("room_created")
def _parse_room_created(d: dict[str, Any]) -> RoomCreated:
    return RoomCreated(room_id=d["room_id"])


@_register_parser("room_posted")
def _parse_room_posted(d: dict[str, Any]) -> RoomPosted:
    return RoomPosted(event_id=d["event_id"], room_seq=d.get("room_seq", 0))


@_register_parser("room_joined")
@_register_parser("room_left")
@_register_parser("room_closed")
def _parse_room_op(d: dict[str, Any]) -> RoomOpResult:
    return RoomOpResult(ok=d.get("ok", False))


@_register_parser("rooms")
def _parse_rooms(d: dict[str, Any]) -> RoomList:
    return RoomList(rooms=tuple(_room_info_from_dict(room) for room in d.get("rooms", [])))


@_register_parser("room_members")
def _parse_room_members(d: dict[str, Any]) -> RoomMembers:
    return RoomMembers(members=tuple(d.get("members", [])))


@_register_parser("room_replay")
def _parse_room_replay(d: dict[str, Any]) -> RoomReplay:
    return RoomReplay(events=tuple(_room_event_from_dict(evt) for evt in d.get("events", [])))


@_register_parser("introspected")
def _parse_introspected(d: dict[str, Any]) -> Introspected:
    return Introspected(
        handle=d["handle"],
        found=d.get("found", False),
        manifest=Manifest.from_dict(d.get("manifest")),
    )


@_register_parser("handles")
def _parse_handles(d: dict[str, Any]) -> HandleList:
    entries = tuple(
        HandleEntry(
            handle=e["handle"],
            manifest=Manifest.from_dict(e.get("manifest")),
        )
        for e in d.get("entries", [])
    )
    return HandleList(entries=entries)


@_register_parser("handle_matches")
def _parse_handle_matches(d: dict[str, Any]) -> HandleMatches:
    matches = tuple(
        HandleMatch(
            handle=e["handle"],
            manifest=Manifest.from_dict(e.get("manifest")),
            score=e.get("score", 0),
            match_reasons=tuple(e.get("match_reasons", [])),
        )
        for e in d.get("matches", [])
    )
    return HandleMatches(matches=matches)


@_register_parser("sessions")
def _parse_sessions(d: dict[str, Any]) -> SessionList:
    sessions = tuple(
        SessionInfo(
            session=s["session"],
            from_handle=s["from"],
            to_handle=s["to"],
            state=s.get("state", ""),
        )
        for s in d.get("sessions", [])
    )
    return SessionList(sessions=sessions)


@_register_parser("error")
def _parse_error(d: dict[str, Any]) -> Error:
    return Error(error=d["error"], request_type=d.get("request_type", "unknown"))


def parse_response(line: str) -> Response:
    """Parse a JSON line from the bridge into a typed response dataclass."""
    d = json.loads(line)
    resp_type = d.get("type", "")
    parser = _PARSERS.get(resp_type)
    if parser is None:
        raise ValueError(f"unknown response type: {resp_type!r}")
    return parser(d)  # type: ignore[no-any-return]
