from __future__ import annotations

import curses
import time

from snake_game import SnakeGame

FRAME_DELAY = 0.12
_QUIT_KEYS = {ord("q"), ord("Q"), 27}
_RESTART_KEYS = {ord("r"), ord("R")}
_DIRECTION_KEYS = {
    curses.KEY_UP: (-1, 0),
    curses.KEY_DOWN: (1, 0),
    curses.KEY_LEFT: (0, -1),
    curses.KEY_RIGHT: (0, 1),
    ord("w"): (-1, 0),
    ord("s"): (1, 0),
    ord("a"): (0, -1),
    ord("d"): (0, 1),
}


def _safe_addstr(window: curses.window, y: int, x: int, text: str) -> None:
    try:
        window.addstr(y, x, text)
    except curses.error:
        pass


def _direction_from_key(key: int) -> tuple[int, int] | None:
    return _DIRECTION_KEYS.get(key)


def _draw(stdscr: curses.window, game: SnakeGame) -> None:
    stdscr.erase()
    max_y, max_x = stdscr.getmaxyx()
    board_height = game.height + 2
    board_width = game.width + 2
    min_height = board_height + 4
    min_width = board_width + 4
    if max_y < min_height or max_x < min_width:
        resize_msg = f"Resize terminal to at least {min_width}x{min_height}."
        size_msg = f"Current size: {max_x}x{max_y}."
        _safe_addstr(
            stdscr,
            max(0, max_y // 2 - 1),
            max(0, (max_x - len(resize_msg)) // 2),
            resize_msg,
        )
        _safe_addstr(
            stdscr,
            max(1, max_y // 2),
            max(0, (max_x - len(size_msg)) // 2),
            size_msg,
        )
        stdscr.refresh()
        return
    board_y = (max_y - board_height) // 2
    board_x = (max_x - board_width) // 2
    scoreboard_y = max(0, board_y - 2)
    instructions_y = max(0, board_y - 1)
    board_win = curses.newwin(board_height, board_width, board_y, board_x)
    board_win.box()
    snake_positions = set(game.snake)
    for row in range(game.height):
        for col in range(game.width):
            position = (row, col)
            ch = " "
            if position == game.food:
                ch = "*"
            elif position == game.snake[0]:
                ch = "@"
            elif position in snake_positions:
                ch = "#"
            try:
                board_win.addch(row + 1, col + 1, ch)
            except curses.error:
                pass
    board_win.refresh()
    _safe_addstr(stdscr, scoreboard_y, board_x, f"Score: {game.score}")
    _safe_addstr(
        stdscr,
        instructions_y,
        board_x,
        "Arrows/WASD to move. R to restart, Q to quit.",
    )
    if not game.alive:
        prompt = "Game over! Press R to restart or Q to quit."
        _safe_addstr(stdscr, board_y + board_height + 1, board_x, prompt)
    stdscr.refresh()


def _handle_input(game: SnakeGame, key: int) -> bool:
    if key in _QUIT_KEYS:
        return False
    if key in _RESTART_KEYS:
        game.restart()
        return True
    direction = _direction_from_key(key)
    if direction:
        game.set_direction(*direction)
    return True


def _loop(stdscr: curses.window) -> None:
    try:
        curses.curs_set(0)
    except curses.error:
        pass
    stdscr.nodelay(True)
    stdscr.keypad(True)
    game = SnakeGame()
    last_tick = time.monotonic()
    running = True
    while running:
        key = stdscr.getch()
        if key != -1:
            running = _handle_input(game, key)
            if not running:
                break
            if key in _RESTART_KEYS:
                last_tick = time.monotonic()
        current = time.monotonic()
        if game.alive and current - last_tick >= FRAME_DELAY:
            game.step()
            last_tick = current
        _draw(stdscr, game)
        time.sleep(0.01)


def main() -> None:
    curses.wrapper(_loop)


if __name__ == "__main__":
    main()
