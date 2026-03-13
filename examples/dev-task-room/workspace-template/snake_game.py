from __future__ import annotations

import random
from collections import deque
from dataclasses import dataclass, field
from typing import Deque

Position = tuple[int, int]


@dataclass
class SnakeGame:
    width: int = 20
    height: int = 15
    random_instance: random.Random | None = None

    snake: Deque[Position] = field(init=False)
    direction: Position = field(init=False)
    food: Position = field(init=False)
    score: int = field(init=False)
    alive: bool = field(init=False)
    _random: random.Random = field(init=False, repr=False)

    def __post_init__(self) -> None:
        if self.width < 3 or self.height < 3:
            raise ValueError("width and height must be at least 3")
        self._random = self.random_instance or random.Random()
        self.restart()

    def restart(self) -> None:
        self.score = 0
        self.direction = (0, 1)
        self.alive = True
        center_row = self.height // 2
        center_col = self.width // 2
        seed_positions: list[Position] = []
        for offset in range(3):
            col = center_col - offset
            if col < 0:
                raise ValueError("width must be at least 3 to center the snake")
            seed_positions.append((center_row, col))
        self.snake = deque(seed_positions)
        self._spawn_food()

    def _spawn_food(self) -> None:
        max_attempts = self.width * self.height
        attempts = 0
        while attempts < max_attempts:
            candidate = (self._random.randrange(self.height), self._random.randrange(self.width))
            if candidate not in self.snake:
                self.food = candidate
                return
            attempts += 1
        raise RuntimeError("unable to place food")

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
        tail = self.snake[-1]
        will_eat = new_head == self.food
        if new_head in self.snake and (will_eat or new_head != tail):
            self.alive = False
            return
        self.snake.appendleft(new_head)
        if will_eat:
            self.score += 1
            self._spawn_food()
        else:
            self.snake.pop()

    def _hits_wall(self, position: Position) -> bool:
        row, col = position
        return row < 0 or row >= self.height or col < 0 or col >= self.width

    @property
    def board_size(self) -> tuple[int, int]:
        return self.height, self.width
