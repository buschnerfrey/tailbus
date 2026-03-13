from __future__ import annotations

import os
import random
import sys
import unittest
from collections import deque

sys.path.insert(0, os.path.dirname(os.path.dirname(__file__)))

from snake_game import SnakeGame


class SnakeGameTests(unittest.TestCase):
    def setUp(self) -> None:
        self.random = random.Random(0)
        self.game = SnakeGame(width=12, height=10, random_instance=self.random)

    def test_initial_state(self) -> None:
        self.assertEqual(self.game.score, 0)
        self.assertTrue(self.game.alive)
        self.assertEqual(self.game.direction, (0, 1))
        self.assertEqual(len(self.game.snake), 3)
        self.assertNotIn(self.game.food, self.game.snake)

    def test_step_moves_forward(self) -> None:
        head = self.game.snake[0]
        self.game.food = (0, 0)
        self.game.step()
        expected = (head[0], head[1] + 1)
        self.assertEqual(self.game.snake[0], expected)
        self.assertEqual(len(self.game.snake), 3)

    def test_eating_food_increments_score(self) -> None:
        head = self.game.snake[0]
        self.game.food = (head[0], head[1] + 1)
        self.game.step()
        self.assertEqual(self.game.score, 1)
        self.assertEqual(len(self.game.snake), 4)
        self.assertNotIn(self.game.food, self.game.snake)

    def test_cannot_reverse_direction(self) -> None:
        self.game.set_direction(-self.game.direction[0], -self.game.direction[1])
        self.assertEqual(self.game.direction, (0, 1))

    def test_wall_collision_ends_game(self) -> None:
        self.game.food = (0, 0)
        self.game.direction = (-1, 0)
        while self.game.alive:
            self.game.step()
        self.assertFalse(self.game.alive)

    def test_self_collision_ends_game(self) -> None:
        self.game.snake = deque([(5, 5), (5, 4), (4, 4), (4, 5)])
        self.game.direction = (0, -1)
        self.game.food = (0, 0)
        self.game.step()
        self.assertFalse(self.game.alive)

    def test_restart_resets_state(self) -> None:
        self.game.score = 5
        self.game.alive = False
        self.game.direction = (1, 0)
        self.game.restart()
        self.assertEqual(self.game.score, 0)
        self.assertTrue(self.game.alive)
        self.assertEqual(self.game.direction, (0, 1))
        self.assertEqual(len(self.game.snake), 3)
        self.assertNotIn(self.game.food, self.game.snake)

    def test_food_never_spawns_on_snake(self) -> None:
        for _ in range(5):
            self.game.restart()
            self.assertNotIn(self.game.food, self.game.snake)


if __name__ == "__main__":
    unittest.main()
