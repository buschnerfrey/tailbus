#!/usr/bin/env python3
"""Focused regression tests for critic helpers."""

from __future__ import annotations

import asyncio
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import critic


class CriticTests(unittest.TestCase):
    def test_repair_review_json_recovers_valid_json(self) -> None:
        original = critic.llm_call
        critic.llm_call = lambda *args, **kwargs: '{"summary":"ok","decision":"approve","findings":[],"required_changes":[],"test_gaps":[]}'
        try:
            result = asyncio.run(critic.repair_review_json("not json", "chunk"))
        finally:
            critic.llm_call = original
        self.assertIsNotNone(result)
        assert result is not None
        self.assertEqual(result["decision"], "approve")


if __name__ == "__main__":
    unittest.main()
