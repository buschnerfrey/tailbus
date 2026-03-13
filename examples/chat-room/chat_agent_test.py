#!/usr/bin/env python3
"""Focused tests for chat-room response cleanup."""

from __future__ import annotations

import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(__file__))

from chat_agent import extract_final_chat_text


class ExtractFinalChatTextTest(unittest.TestCase):
    def test_prefers_text_after_closed_think_block(self) -> None:
        raw = "<think>internal reasoning</think>\nHello there. How are you?"
        self.assertEqual(extract_final_chat_text(raw), "Hello there. How are you?")

    def test_recovers_sentence_labels_from_truncated_reasoning(self) -> None:
        raw = """<think>Thinking Process:

1. Analyze the request.
Sentence 1: Hello!
Sentence 2: How are you doing today?
"""
        self.assertEqual(
            extract_final_chat_text(raw, "length"),
            "Hello! How are you doing today?",
        )

    def test_recovers_marker_candidate_from_reasoning(self) -> None:
        raw = """Thinking Process:

Let's go with:
Hello there. How are you today?
"""
        self.assertEqual(
            extract_final_chat_text(raw, "length"),
            "Hello there. How are you today?",
        )


if __name__ == "__main__":
    unittest.main()
