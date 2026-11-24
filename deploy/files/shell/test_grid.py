import random
import unittest

from hostd_shell import views
from hostd_shell.core import grid


def row(kind, title=""):
    return views.Row(title or kind, kind=kind)


def wall():
    # DOLPHIN (launcher) 2 games, STEAM (launcher) 5 games, OTHER (header) 1 game
    return ([row(views.LAUNCHER, "Dolphin")] + [row(views.ITEM, f"d{i}") for i in range(2)]
            + [row(views.LAUNCHER, "Steam")] + [row(views.ITEM, f"s{i}") for i in range(5)]
            + [row(views.HEADER, "Other")] + [row(views.ITEM, "o0")])


class Grid(unittest.TestCase):
    def test_a_heading_is_a_line_and_games_fill_lines_of_four(self):
        shape = [(h, len(m)) for h, m in grid.lines(wall(), 4)]
        self.assertEqual(shape, [(True, 1), (False, 2), (True, 1), (False, 4), (False, 1), (True, 1), (False, 1)])

    def test_down_keeps_the_column_and_lands_on_the_last_tile_of_a_short_line(self):
        rows = wall()
        self.assertEqual(grid.vertical(rows, 0, 1, 4), 1)  # the launcher to the first tile
        self.assertEqual(grid.vertical(rows, 2, 1, 4), 3)  # d1 (column 1) down: Steam, a launcher
        self.assertEqual(grid.vertical(rows, 4, 1, 4), 8)  # s0 down: s4 is the next line's only tile
        self.assertEqual(grid.vertical(rows, 7, 1, 4), 8)  # s3 (column 3) down: the short line's last

    def test_a_section_label_is_passed_over(self):
        rows = wall()
        self.assertEqual(grid.vertical(rows, 8, 1, 4), 10)  # s4 down: past "Other", to its game
        self.assertEqual(grid.vertical(rows, 10, -1, 4), 8)

    def test_the_ends_stay(self):
        rows = wall()
        self.assertEqual(grid.vertical(rows, 0, -1, 4), 0)
        self.assertEqual(grid.vertical(rows, 10, 1, 4), 10)

    def test_sideways_stops_at_the_line_end_and_a_heading_has_no_sides(self):
        rows = wall()
        self.assertEqual(grid.horizontal(rows, 1, 1, 4), 2)
        self.assertIsNone(grid.horizontal(rows, 2, 1, 4))  # d1 is the last: leave
        self.assertIsNone(grid.horizontal(rows, 1, -1, 4))
        self.assertIsNone(grid.horizontal(rows, 0, 1, 4))  # a launcher

    def test_offset_counts_headings_and_tile_lines_above(self):
        rows = wall()
        self.assertEqual(grid.offset(rows, 0, 4), (0, 0))
        self.assertEqual(grid.offset(rows, 1, 4), (1, 0))
        self.assertEqual(grid.offset(rows, 4, 4), (2, 1))
        self.assertEqual(grid.offset(rows, 8, 4), (2, 2))
        self.assertEqual(grid.offset(rows, 10, 4), (3, 3))

    def test_random_walks_stay_on_a_tile_or_a_launcher(self):
        rng = random.Random(7)
        for columns in (1, 2, 3, 4, 6):
            rows = wall()
            i = 1
            for _ in range(400):
                direction = rng.choice((1, -1))
                j = grid.vertical(rows, i, direction, columns) if rng.random() < 0.5 else grid.horizontal(rows, i, direction, columns)
                i = i if j is None else j
                self.assertNotEqual(rows[i].kind, views.HEADER)


if __name__ == "__main__":
    unittest.main()
