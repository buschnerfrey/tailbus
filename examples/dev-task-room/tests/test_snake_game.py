from __future__ import annotations

import os
import random
import sys
import unittest
from collections import deque
from types import MethodType

sys.path.insert(0, os.path.dirname(os.path.dirname(__file__)))

from snake_game import SnakeGame


class SnakeGameTests(unittest.TestCase):
    def setUp(self) -> None:
        self.game = SnakeGame(width=6, height=6, random_instance=random.Random(0))

    def test_restart_resets_state(self) -> None:
        self.game.score = 5
        self.game.alive = False
        self.game.restart()

        self.assertEqual(self.game.score, 0)
        self.assertTrue(self.game.alive)
        self.assertEqual(len(self.game.snake), 3)
        self.assertNotIn(self.game.food, self.game.snake)
        self.assertEqual(self.game.occupied_positions, frozenset(self.game.snake))

    def test_occupied_positions_tracks_body(self) -> None:
        self.assertEqual(self.game.occupied_positions, frozenset(self.game.snake))
        head_row, head_col = self.game.snake[0]
        self.game.food = (head_row, head_col + 1)
        self.game.step()
        self.assertEqual(self.game.occupied_positions, frozenset(self.game.snake))

    def test_step_eats_food_and_grows(self) -> None:
        head_row, head_col = self.game.snake[0]
        self.game.food = (head_row, head_col + 1)
        next_food = (0, 0)

        def fixed_spawn(game: SnakeGame) -> None:
            game.food = next_food

        self.game._spawn_food = MethodType(fixed_spawn, self.game)
        self.game.step()

        self.assertEqual(self.game.score, 1)
        self.assertEqual(len(self.game.snake), 4)
        self.assertEqual(self.game.food, next_food)
        self.assertNotIn(next_food, self.game.snake)

    def test_step_detects_wall_collision(self) -> None:
        self.game.snake = deque([(0, 2), (0, 1), (0, 0)])
        self.game._occupied = set(self.game.snake)
        self.game.direction = (-1, 0)
        self.game.food = (5, 5)

        self.game.step()

        self.assertFalse(self.game.alive)

    def test_step_detects_self_collision(self) -> None:
        self.game.snake = deque(
            [
                (2, 2),
                (2, 3),
                (3, 3),
                (3, 2),
                (3, 1),
                (2, 1),
            ]
        )
        self.game._occupied = set(self.game.snake)
        self.game.direction = (0, 1)
        self.game.food = (0, 0)

        self.game.step()

        self.assertFalse(self.game.alive)

    def test_set_direction_ignores_reverse(self) -> None:
        self.game.set_direction(0, -1)
        self.assertEqual(self.game.direction, (0, 1))

        self.game.set_direction(0, 0)
        self.assertEqual(self.game.direction, (0, 1))


if __name__ == "__main__":
    unittest.main()
