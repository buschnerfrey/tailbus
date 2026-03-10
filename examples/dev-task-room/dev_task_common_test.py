#!/usr/bin/env python3
"""Focused regression tests for dev-task-room helpers."""

from __future__ import annotations

import io
import sys
import urllib.error
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from dev_task_common import (
    build_room_state,
    build_review_units,
    collect_streamed_llm_text,
    format_llm_http_error,
    is_context_limit_error,
    select_workspace_snapshot_files,
    split_review_unit,
    strip_code_fences,
    truncate_preserving_ends,
)


class DevTaskCommonTests(unittest.TestCase):
    def test_strip_code_fences_handles_single_line_fence(self) -> None:
        self.assertEqual(strip_code_fences("```json"), "")

    def test_strip_code_fences_unwraps_fenced_json(self) -> None:
        self.assertEqual(strip_code_fences("```json\n{\"ok\": true}\n```"), '{"ok": true}')

    def test_truncate_preserving_ends_keeps_head_and_tail(self) -> None:
        text = "head-" + ("x" * 40) + "-tail"
        truncated = truncate_preserving_ends(text, 20, marker="...")
        self.assertTrue(truncated.startswith("head-"))
        self.assertTrue(truncated.endswith("-tail"))
        self.assertIn("...", truncated)
        self.assertLessEqual(len(truncated), 20)

    def test_select_workspace_snapshot_files_includes_new_sources_and_skips_cache(self) -> None:
        files = (
            "README.md",
            "snake_game.py",
            "tests/test_snake_game.py",
            "__pycache__/snake_game.cpython-312.pyc",
            ".pytest_cache/README.md",
        )
        selected = select_workspace_snapshot_files(files)
        self.assertIn("snake_game.py", selected)
        self.assertIn("tests/test_snake_game.py", selected)
        self.assertNotIn("__pycache__/snake_game.cpython-312.pyc", selected)
        self.assertNotIn(".pytest_cache/README.md", selected)

    def test_collect_streamed_llm_text_joins_chunks_until_done(self) -> None:
        seen: list[str] = []
        result = collect_streamed_llm_text(
            [
                b'data: {"choices":[{"delta":{"content":"Hello"}}]}\n',
                b'data: {"choices":[{"delta":{"content":" world"}}]}\n',
                b"data: [DONE]\n",
                b'data: {"choices":[{"delta":{"content":" ignored"}}]}\n',
            ],
            on_chunk=seen.append,
        )
        self.assertEqual(result, "Hello world")
        self.assertEqual(seen, ["Hello", "Hello world"])

    def test_format_llm_http_error_includes_status_and_body(self) -> None:
        err = urllib.error.HTTPError(
            url="http://127.0.0.1:1234/v1/chat/completions",
            code=400,
            msg="Bad Request",
            hdrs=None,
            fp=io.BytesIO(b'{"error":{"message":"context length exceeded"}}'),
        )
        message = format_llm_http_error(err)
        self.assertIn("status=400", message)
        self.assertIn("context length exceeded", message)

    def test_build_review_units_splits_large_changed_file(self) -> None:
        original = "\n".join(f"old line {i}" for i in range(220)) + "\n"
        updated = "\n".join(f"new line {i}" for i in range(220)) + "\n"
        units = build_review_units(
            {"files": [{"path": "snake_game.py", "content": updated}], "deleted_paths": []},
            {"snake_game.py": original},
        )
        self.assertGreater(len(units), 1)
        self.assertTrue(all(unit["scope_paths"] == ["snake_game.py"] for unit in units))
        self.assertTrue(all(unit["sections"] for unit in units))

    def test_split_review_unit_creates_smaller_sections(self) -> None:
        original = "\n".join(f"old line {i}" for i in range(160)) + "\n"
        updated = "\n".join(f"new line {i}" for i in range(160)) + "\n"
        unit = build_review_units(
            {"files": [{"path": "snake_game.py", "content": updated}], "deleted_paths": []},
            {"snake_game.py": original},
        )[0]
        split_units = split_review_unit(unit)
        self.assertGreaterEqual(len(split_units), 1)
        self.assertTrue(all(item["scope_paths"] == ["snake_game.py"] for item in split_units))

    def test_is_context_limit_error_matches_llm_studio_message(self) -> None:
        self.assertTrue(is_context_limit_error("Cannot truncate prompt with n_keep (5717) >= n_ctx (4096)"))
        self.assertFalse(is_context_limit_error("Bad Request"))

    def test_build_room_state_groups_agentic_room_messages(self) -> None:
        class Event:
            def __init__(self, seq: int, payload: str) -> None:
                self.room_seq = seq
                self.payload = payload
                self.event_type = "message_posted"
                self.content_type = "application/json"
                self.sender_handle = "tester"

        events = [
            Event(1, '{"kind":"task_opened","task_id":"task-1","task":"do work"}'),
            Event(2, '{"kind":"plan_proposed","task_id":"task-1","summary":"plan"}'),
            Event(3, '{"kind":"implement_reply","task_id":"task-1","iteration":1,"status":"ok"}'),
            Event(4, '{"kind":"review_reply","task_id":"task-1","iteration":1,"decision":"approve"}'),
            Event(5, '{"kind":"final_outcome","task_id":"task-1","status":"complete","summary":"done"}'),
        ]
        state = build_room_state(events)
        self.assertEqual(state["task"]["task_id"], "task-1")
        self.assertEqual(len(state["plans"]), 1)
        self.assertEqual(len(state["implementations"]), 1)
        self.assertEqual(state["final_outcome"]["status"], "complete")


if __name__ == "__main__":
    unittest.main()
