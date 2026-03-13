from __future__ import annotations

import random
from collections import deque
from dataclasses import dataclass, field
from typing import Deque

Position = tuple[int, int]


@dataclass
class SnakeGame:
    """Core state machine for a standard Snake board."""

    width: int = 20
    height: int = 15
    random_instance: random.Random | None = None

    snake: Deque[Position] = field(init=False)
    direction: Position = field(init=False)
    food: Position = field(init=False)
    score: int = field(init=False)
    alive: bool = field(init=False)
    _random: random.Random = field(init=False, repr=False)
    _occupied: set[Position] = field(init=False, repr=False)

    def __post_init__(self) -> None:
        if self.width < 3 or self.height < 3:
            raise ValueError("width and height must be at least 3")
        self._random = self.random_instance or random.Random()
        self.restart()

    def restart(self) -> None:
        """Reset the board while preserving the arena size."""
        self.score = 0
        self.direction = (0, 1)
        self.alive = True
        self.snake = self._initial_snake()
        self._occupied = set(self.snake)
        self._spawn_food()

    def _initial_snake(self) -> Deque[Position]:
        center_row = self.height // 2
        center_col = self.width // 2
        start_col = max(0, min(center_col - 2, self.width - 3))
        body = deque()
        # Build three segments with the head leading and the tail trailing.
        for offset in range(2, -1, -1):
            body.append((center_row, start_col + offset))
        return body

    def _spawn_food(self) -> None:
        if len(self._occupied) >= self.width * self.height:
            raise RuntimeError("unable to place food: the board is full")
        available_cells = [
            (row, col)
            for row in range(self.height)
            for col in range(self.width)
            if (row, col) not in self._occupied
        ]
        self.food = self._random.choice(available_cells)

    def set_direction(self, row_delta: int, col_delta: int) -> None:
        new_direction = (row_delta, col_delta)
        if new_direction == (0, 0):
            return
        opposite = (-self.direction[0], -self.direction[1])
        if new_direction == opposite:
            return
        self.direction = new_direction

    def step(self) -> None:
        if not self.alive:
            return
        head_row, head_col = self.snake[0]
        delta_row, delta_col = self.direction
        new_head = (head_row + delta_row, head_col + delta_col)
        if self._hits_wall(new_head):
            self.alive = False
            return
        will_eat = new_head == self.food
        tail = self.snake[-1]
        if new_head in self._occupied and not (new_head == tail and not will_eat):
            self.alive = False
            return
        self.snake.appendleft(new_head)
        self._occupied.add(new_head)
        if will_eat:
            self.score += 1
            self._spawn_food()
        else:
            removed_tail = self.snake.pop()
            self._occupied.remove(removed_tail)

    def _hits_wall(self, position: Position) -> bool:
        row, col = position
        return row < 0 or row >= self.height or col < 0 or col >= self.width

    @property
    def board_size(self) -> tuple[int, int]:
        return self.height, self.width

    @property
    def occupied_positions(self) -> frozenset[Position]:
        return frozenset(self._occupied)
