from __future__ import annotations

import unittest

from focus_timer import Phase, TimerState


class TimerStateTests(unittest.TestCase):
    def test_tick_advances_phase_and_cycles(self) -> None:
        state = TimerState(work_seconds=2, break_seconds=1)
        state.tick()
        self.assertEqual(state.remaining_seconds, 1)
        state.tick()
        self.assertEqual(state.current_phase, Phase.BREAK)
        self.assertEqual(state.remaining_seconds, 1)
        state.tick()
        self.assertEqual(state.current_phase, Phase.WORK)
        self.assertEqual(state.cycles_completed, 1)

    def test_formatted_remaining_edge_cases(self) -> None:
        state = TimerState(work_seconds=60, break_seconds=5)
        self.assertEqual(state.formatted_remaining(), "01:00")
        state.remaining_seconds = 9
        self.assertEqual(state.formatted_remaining(), "00:09")
        state.remaining_seconds = 0
        self.assertEqual(state.formatted_remaining(), "00:00")

    def test_negative_durations_raise(self) -> None:
        with self.assertRaises(ValueError):
            TimerState(work_seconds=-1, break_seconds=5)
        with self.assertRaises(ValueError):
            TimerState(work_seconds=5, break_seconds=-5)


if __name__ == "__main__":
    unittest.main()
