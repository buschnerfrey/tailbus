from __future__ import annotations

import argparse
import enum
import sys
import time
from dataclasses import dataclass, field
from typing import Optional


class Phase(enum.Enum):
    WORK = "Work"
    BREAK = "Break"


@dataclass
class TimerState:
    """Tracks the remaining seconds and completed work+break pairs."""

    work_seconds: int
    break_seconds: int
    current_phase: Phase = field(default=Phase.WORK)
    remaining_seconds: int = field(init=False)
    cycles_completed: int = 0
    """Counts completed work+break pairs (incremented when returning to work)."""

    def __post_init__(self) -> None:
        self._validate_durations()
        self.remaining_seconds = self.work_seconds

    def _validate_durations(self) -> None:
        if self.work_seconds <= 0 or self.break_seconds <= 0:
            raise ValueError("Work and break durations must be positive seconds")

    def tick(self) -> None:
        if self.remaining_seconds <= 0:
            return
        self.remaining_seconds -= 1
        if self.remaining_seconds == 0:
            self._advance_phase()

    def _advance_phase(self) -> None:
        if self.current_phase == Phase.WORK:
            self.current_phase = Phase.BREAK
            self.remaining_seconds = self.break_seconds
            return
        self.current_phase = Phase.WORK
        self.remaining_seconds = self.work_seconds
        self.cycles_completed += 1

    def formatted_remaining(self) -> str:
        minutes, seconds = divmod(self.remaining_seconds, 60)
        return f"{minutes:02d}:{seconds:02d}"


def _parse_args(argv: Optional[list[str]] = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Simple terminal focus timer using work/break cycles.",
        formatter_class=argparse.ArgumentDefaultsHelpFormatter,
    )
    parser.add_argument("--work", type=int, default=25, help="Work duration in minutes")
    parser.add_argument(
        "--break",
        dest="break_minutes",
        type=int,
        default=5,
        help="Break duration in minutes",
    )
    parser.add_argument(
        "--cycles",
        type=int,
        default=0,
        help="Number of work/break cycles to run (0 for infinite)",
    )
    return parser.parse_args(argv or [])


def _render(state: TimerState, target_cycles: int) -> None:
    label = f"Cycle {state.cycles_completed + 1}"
    if target_cycles > 0:
        label += f"/{target_cycles}"
    text = f"{state.current_phase.value}: {state.formatted_remaining()} ({label})"
    width = max(len(text), 60)
    print(f"\r{text.ljust(width)}", end="", flush=True)


def run_timer(work_minutes: int, break_minutes: int, target_cycles: int) -> None:
    state = TimerState(work_minutes * 60, break_minutes * 60)
    _render(state, target_cycles)
    try:
        while target_cycles == 0 or state.cycles_completed < target_cycles:
            time.sleep(1)
            state.tick()
            _render(state, target_cycles)
    except KeyboardInterrupt:
        print("\nTimer interrupted.")
        return
    print("\r" + " " * 80, end="\r")
    completed = state.cycles_completed
    if target_cycles > 0 and completed >= target_cycles:
        print(f"Completed {target_cycles} cycle(s). Great job!")
    else:
        print(f"Completed {completed} cycle(s). Keep going!")


def main(argv: Optional[list[str]] = None) -> int:
    args = _parse_args(argv or sys.argv[1:])
    try:
        run_timer(args.work, args.break_minutes, args.cycles)
    except ValueError as exc:
        print(f"Invalid durations: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
