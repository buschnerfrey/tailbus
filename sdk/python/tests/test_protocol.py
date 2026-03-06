"""Tests for tailbus._protocol — serialization and deserialization."""

import json
import unittest

from tailbus._protocol import (
    CommandSpec,
    Error,
    HandleEntry,
    HandleList,
    HandleMatches,
    Introspected,
    Manifest,
    Message,
    Opened,
    Registered,
    Resolved,
    RoomCreated,
    RoomEvent,
    RoomList,
    RoomMembers,
    RoomPosted,
    RoomReplay,
    Sent,
    SessionInfo,
    SessionList,
    parse_response,
    serialize_command,
)


class TestSerializeCommand(unittest.TestCase):
    def test_basic_command(self) -> None:
        result = serialize_command({"type": "register", "handle": "test"})
        self.assertIsInstance(result, bytes)
        self.assertTrue(result.endswith(b"\n"))
        parsed = json.loads(result)
        self.assertEqual(parsed["type"], "register")
        self.assertEqual(parsed["handle"], "test")

    def test_command_with_manifest(self) -> None:
        cmd = {
            "type": "register",
            "handle": "test",
            "manifest": {
                "description": "Test agent",
                "tags": ["a", "b"],
                "capabilities": ["research.company"],
                "domains": ["finance"],
            },
        }
        result = serialize_command(cmd)
        parsed = json.loads(result)
        self.assertEqual(parsed["manifest"]["description"], "Test agent")
        self.assertEqual(parsed["manifest"]["tags"], ["a", "b"])
        self.assertEqual(parsed["manifest"]["capabilities"], ["research.company"])
        self.assertEqual(parsed["manifest"]["domains"], ["finance"])


class TestParseRegistered(unittest.TestCase):
    def test_parse(self) -> None:
        resp = parse_response('{"type":"registered","handle":"my-agent"}')
        self.assertIsInstance(resp, Registered)
        assert isinstance(resp, Registered)
        self.assertEqual(resp.handle, "my-agent")


class TestParseOpened(unittest.TestCase):
    def test_parse(self) -> None:
        resp = parse_response(
            '{"type":"opened","session":"s1","message_id":"m1","trace_id":"t1"}'
        )
        self.assertIsInstance(resp, Opened)
        assert isinstance(resp, Opened)
        self.assertEqual(resp.session, "s1")
        self.assertEqual(resp.message_id, "m1")
        self.assertEqual(resp.trace_id, "t1")

    def test_missing_trace_id(self) -> None:
        resp = parse_response('{"type":"opened","session":"s1","message_id":"m1"}')
        assert isinstance(resp, Opened)
        self.assertEqual(resp.trace_id, "")


class TestParseSent(unittest.TestCase):
    def test_parse(self) -> None:
        resp = parse_response('{"type":"sent","message_id":"m1"}')
        self.assertIsInstance(resp, Sent)
        assert isinstance(resp, Sent)
        self.assertEqual(resp.message_id, "m1")


class TestParseResolved(unittest.TestCase):
    def test_parse(self) -> None:
        resp = parse_response('{"type":"resolved","message_id":"m1"}')
        self.assertIsInstance(resp, Resolved)
        assert isinstance(resp, Resolved)
        self.assertEqual(resp.message_id, "m1")


class TestParseMessage(unittest.TestCase):
    def test_parse_full(self) -> None:
        line = json.dumps(
            {
                "type": "message",
                "session": "s1",
                "from": "alice",
                "to": "bob",
                "payload": "hello",
                "content_type": "text/plain",
                "message_type": "session_open",
                "trace_id": "t1",
                "message_id": "m1",
                "sent_at": 1700000000,
            }
        )
        resp = parse_response(line)
        self.assertIsInstance(resp, Message)
        assert isinstance(resp, Message)
        self.assertEqual(resp.session, "s1")
        self.assertEqual(resp.from_handle, "alice")
        self.assertEqual(resp.to_handle, "bob")
        self.assertEqual(resp.payload, "hello")
        self.assertEqual(resp.content_type, "text/plain")
        self.assertEqual(resp.message_type, "session_open")
        self.assertEqual(resp.trace_id, "t1")
        self.assertEqual(resp.message_id, "m1")
        self.assertEqual(resp.sent_at, 1700000000)

    def test_parse_minimal(self) -> None:
        line = json.dumps(
            {"type": "message", "session": "s1", "from": "a", "to": "b"}
        )
        resp = parse_response(line)
        assert isinstance(resp, Message)
        self.assertEqual(resp.payload, "")
        self.assertEqual(resp.content_type, "text/plain")
        self.assertEqual(resp.message_type, "message")
        self.assertEqual(resp.sent_at, 0)


class TestParseRoomResponses(unittest.TestCase):
    def test_parse_room_event(self) -> None:
        resp = parse_response(
            json.dumps(
                {
                    "type": "room_event",
                    "room_id": "room-1",
                    "room_seq": 2,
                    "sender": "alice",
                    "subject": "bob",
                    "payload": "joined",
                    "content_type": "text/plain",
                    "event_type": "member_joined",
                    "trace_id": "trace-1",
                    "event_id": "evt-1",
                    "sent_at": 1700000000,
                    "members": ["alice", "bob"],
                }
            )
        )
        self.assertIsInstance(resp, RoomEvent)
        assert isinstance(resp, RoomEvent)
        self.assertEqual(resp.room_id, "room-1")
        self.assertEqual(resp.room_seq, 2)
        self.assertEqual(resp.members, ("alice", "bob"))

    def test_parse_room_created_and_posted(self) -> None:
        created = parse_response('{"type":"room_created","room_id":"room-1"}')
        self.assertIsInstance(created, RoomCreated)
        posted = parse_response('{"type":"room_posted","event_id":"evt-1","room_seq":3}')
        self.assertIsInstance(posted, RoomPosted)

    def test_parse_room_lists(self) -> None:
        rooms = parse_response(
            json.dumps(
                {
                    "type": "rooms",
                    "rooms": [
                        {
                            "room_id": "room-1",
                            "title": "design-review",
                            "created_by": "alice",
                            "home_node_id": "node-1",
                            "members": ["alice", "bob"],
                            "status": "open",
                            "next_seq": 4,
                            "created_at": 1,
                            "updated_at": 2,
                        }
                    ],
                }
            )
        )
        self.assertIsInstance(rooms, RoomList)
        assert isinstance(rooms, RoomList)
        self.assertEqual(rooms.rooms[0].title, "design-review")

        members = parse_response('{"type":"room_members","members":["alice","bob"]}')
        self.assertIsInstance(members, RoomMembers)

        replay = parse_response(
            json.dumps(
                {
                    "type": "room_replay",
                    "events": [
                        {
                            "room_id": "room-1",
                            "room_seq": 1,
                            "sender": "alice",
                            "payload": "hello",
                            "content_type": "text/plain",
                            "event_type": "message_posted",
                            "event_id": "evt-1",
                            "sent_at": 3,
                        }
                    ],
                }
            )
        )
        self.assertIsInstance(replay, RoomReplay)


class TestParseIntrospected(unittest.TestCase):
    def test_found_with_manifest(self) -> None:
        line = json.dumps(
            {
                "type": "introspected",
                "handle": "sales",
                "found": True,
                "manifest": {
                    "description": "Sales agent",
                    "tags": ["sales"],
                    "version": "1.0",
                    "capabilities": ["pricing.quote"],
                    "domains": ["sales"],
                    "input_types": ["application/json"],
                    "output_types": ["application/json"],
                    "commands": [
                        {"name": "quote", "description": "Get a quote"}
                    ],
                },
            }
        )
        resp = parse_response(line)
        self.assertIsInstance(resp, Introspected)
        assert isinstance(resp, Introspected)
        self.assertEqual(resp.handle, "sales")
        self.assertTrue(resp.found)
        self.assertIsNotNone(resp.manifest)
        assert resp.manifest is not None
        self.assertEqual(resp.manifest.description, "Sales agent")
        self.assertEqual(resp.manifest.tags, ("sales",))
        self.assertEqual(resp.manifest.version, "1.0")
        self.assertEqual(resp.manifest.capabilities, ("pricing.quote",))
        self.assertEqual(resp.manifest.domains, ("sales",))
        self.assertEqual(resp.manifest.input_types, ("application/json",))
        self.assertEqual(resp.manifest.output_types, ("application/json",))
        self.assertEqual(len(resp.manifest.commands), 1)
        self.assertEqual(resp.manifest.commands[0].name, "quote")

    def test_not_found(self) -> None:
        resp = parse_response(
            '{"type":"introspected","handle":"nope","found":false}'
        )
        assert isinstance(resp, Introspected)
        self.assertFalse(resp.found)
        self.assertIsNone(resp.manifest)


class TestParseHandles(unittest.TestCase):
    def test_parse(self) -> None:
        line = json.dumps(
            {
                "type": "handles",
                "entries": [
                    {"handle": "a", "manifest": {"description": "Agent A"}},
                    {"handle": "b"},
                ],
            }
        )
        resp = parse_response(line)
        self.assertIsInstance(resp, HandleList)
        assert isinstance(resp, HandleList)
        self.assertEqual(len(resp.entries), 2)
        self.assertEqual(resp.entries[0].handle, "a")
        self.assertIsNotNone(resp.entries[0].manifest)
        self.assertEqual(resp.entries[1].handle, "b")
        self.assertIsNone(resp.entries[1].manifest)

    def test_empty_entries(self) -> None:
        resp = parse_response('{"type":"handles","entries":[]}')
        assert isinstance(resp, HandleList)
        self.assertEqual(len(resp.entries), 0)


class TestParseHandleMatches(unittest.TestCase):
    def test_parse(self) -> None:
        line = json.dumps(
            {
                "type": "handle_matches",
                "matches": [
                    {
                        "handle": "solver",
                        "score": 160,
                        "match_reasons": ["capability:code.solve", "command:solve"],
                        "manifest": {"description": "Code solver"},
                    }
                ],
            }
        )
        resp = parse_response(line)
        self.assertIsInstance(resp, HandleMatches)
        assert isinstance(resp, HandleMatches)
        self.assertEqual(len(resp.matches), 1)
        self.assertEqual(resp.matches[0].handle, "solver")
        self.assertEqual(resp.matches[0].score, 160)
        self.assertEqual(
            resp.matches[0].match_reasons,
            ("capability:code.solve", "command:solve"),
        )


class TestParseSessions(unittest.TestCase):
    def test_parse(self) -> None:
        line = json.dumps(
            {
                "type": "sessions",
                "sessions": [
                    {"session": "s1", "from": "a", "to": "b", "state": "open"},
                ],
            }
        )
        resp = parse_response(line)
        self.assertIsInstance(resp, SessionList)
        assert isinstance(resp, SessionList)
        self.assertEqual(len(resp.sessions), 1)
        self.assertEqual(resp.sessions[0].session, "s1")
        self.assertEqual(resp.sessions[0].from_handle, "a")
        self.assertEqual(resp.sessions[0].to_handle, "b")
        self.assertEqual(resp.sessions[0].state, "open")


class TestParseError(unittest.TestCase):
    def test_parse(self) -> None:
        resp = parse_response(
            '{"type":"error","error":"not registered","request_type":"open"}'
        )
        self.assertIsInstance(resp, Error)
        assert isinstance(resp, Error)
        self.assertEqual(resp.error, "not registered")
        self.assertEqual(resp.request_type, "open")

    def test_missing_request_type(self) -> None:
        resp = parse_response('{"type":"error","error":"bad json"}')
        assert isinstance(resp, Error)
        self.assertEqual(resp.request_type, "unknown")


class TestUnknownType(unittest.TestCase):
    def test_raises(self) -> None:
        with self.assertRaises(ValueError) as ctx:
            parse_response('{"type":"bogus"}')
        self.assertIn("bogus", str(ctx.exception))

    def test_missing_type(self) -> None:
        with self.assertRaises(ValueError):
            parse_response('{"handle":"test"}')


class TestManifestSerialization(unittest.TestCase):
    def test_to_dict_full(self) -> None:
        m = Manifest(
            description="Test",
            commands=(
                CommandSpec("cmd1", "Desc1", '{"type":"object"}'),
                CommandSpec("cmd2", "Desc2"),
            ),
            tags=("tag1", "tag2"),
            version="1.0",
            capabilities=("research.company", "document.summarize"),
            domains=("finance",),
            input_types=("application/json",),
            output_types=("text/plain",),
        )
        d = m.to_dict()
        self.assertEqual(d["description"], "Test")
        self.assertEqual(d["version"], "1.0")
        self.assertEqual(d["tags"], ["tag1", "tag2"])
        self.assertEqual(d["capabilities"], ["research.company", "document.summarize"])
        self.assertEqual(d["domains"], ["finance"])
        self.assertEqual(d["input_types"], ["application/json"])
        self.assertEqual(d["output_types"], ["text/plain"])
        self.assertEqual(len(d["commands"]), 2)
        self.assertEqual(d["commands"][0]["parameters_schema"], '{"type":"object"}')
        self.assertNotIn("parameters_schema", d["commands"][1])

    def test_to_dict_empty(self) -> None:
        m = Manifest()
        d = m.to_dict()
        self.assertEqual(d, {})

    def test_from_dict_none(self) -> None:
        self.assertIsNone(Manifest.from_dict(None))

    def test_from_dict_empty(self) -> None:
        m = Manifest.from_dict({})
        self.assertIsNotNone(m)
        assert m is not None
        self.assertEqual(m.description, "")
        self.assertEqual(m.commands, ())
        self.assertEqual(m.tags, ())
        self.assertEqual(m.capabilities, ())
        self.assertEqual(m.domains, ())

    def test_roundtrip(self) -> None:
        original = Manifest(
            description="Test",
            commands=(CommandSpec("cmd", "A command"),),
            tags=("t1",),
            version="2.0",
            capabilities=("sql.query",),
            domains=("analytics",),
            input_types=("application/sql",),
            output_types=("application/json",),
        )
        restored = Manifest.from_dict(original.to_dict())
        assert restored is not None
        self.assertEqual(original.description, restored.description)
        self.assertEqual(original.version, restored.version)
        self.assertEqual(original.tags, restored.tags)
        self.assertEqual(original.capabilities, restored.capabilities)
        self.assertEqual(original.domains, restored.domains)
        self.assertEqual(original.input_types, restored.input_types)
        self.assertEqual(original.output_types, restored.output_types)
        self.assertEqual(len(original.commands), len(restored.commands))
        self.assertEqual(original.commands[0].name, restored.commands[0].name)


if __name__ == "__main__":
    unittest.main()
