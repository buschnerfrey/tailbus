from __future__ import annotations

import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(__file__)))

from todo import filter_by_status


ITEMS = [
    {"title": "write docs", "status": "open"},
    {"title": "ship build", "status": "done"},
    {"title": "fix flaky test", "status": "open"},
]

MIXED_CASE_ITEMS = [
    {"title": "write docs", "status": "Open"},
    {"title": "ship build", "status": "DONE"},
    {"title": "fix flaky test", "status": "oPEn"},
]


class TodoTests(unittest.TestCase):
    def test_filter_by_status_returns_all_for_all(self) -> None:
        self.assertEqual(filter_by_status(ITEMS, "All"), ITEMS)

    def test_filter_by_status_matches_exact_value(self) -> None:
        self.assertEqual(
            filter_by_status(ITEMS, "open"),
            [
                {"title": "write docs", "status": "open"},
                {"title": "fix flaky test", "status": "open"},
            ],
        )

    def test_filter_by_status_handles_mixed_case_query(self) -> None:
        self.assertEqual(
            filter_by_status(ITEMS, "oPeN"),
            [
                {"title": "write docs", "status": "open"},
                {"title": "fix flaky test", "status": "open"},
            ],
        )

    def test_filter_by_status_handles_mixed_case_items(self) -> None:
        self.assertEqual(
            filter_by_status(MIXED_CASE_ITEMS, "oPeN"),
            [
                {"title": "write docs", "status": "Open"},
                {"title": "fix flaky test", "status": "oPEn"},
            ],
        )

    def test_filter_by_status_matches_done_mixed_case(self) -> None:
        self.assertEqual(
            filter_by_status(ITEMS, "Done"),
            [{"title": "ship build", "status": "done"}],
        )


if __name__ == "__main__":
    unittest.main()
