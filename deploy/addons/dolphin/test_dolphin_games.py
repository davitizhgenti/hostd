"""Tests for hostd-dolphin-games: python3 -m unittest (make lint-scripts)."""

import importlib.machinery
import importlib.util
import os
import struct
import tempfile
import unittest

_loader = importlib.machinery.SourceFileLoader(
    "hostd_dolphin_games", os.path.join(os.path.dirname(os.path.abspath(__file__)), "hostd-dolphin-games"))
_spec = importlib.util.spec_from_loader("hostd_dolphin_games", _loader)
dg = importlib.util.module_from_spec(_spec)
_loader.exec_module(dg)


def disc_header(game_id, title, wii=True):
    """The first 0x80 bytes of a disc: its ID, magic and title."""
    h = bytearray(0x80)
    h[0:6] = game_id.encode()
    h[0x18:0x1C] = struct.pack(">I", dg.WII_MAGIC if wii else 0)
    h[0x1C:0x20] = struct.pack(">I", 0 if wii else dg.GC_MAGIC)
    h[0x20:0x20 + len(title)] = title.encode()
    return bytes(h)


def image(ext, game_id, title, wii=True, size=4096):
    """A file in the format of ext holding that disc's header where it is read."""
    body = bytearray(size)
    head = disc_header(game_id, title, wii)
    if ext in (".iso", ".gcm"):
        body[0:len(head)] = head
    elif ext in (".rvz", ".wia"):
        body[0:4] = b"RVZ\x01" if ext == ".rvz" else b"WIA\x01"
        body[0x58:0x58 + len(head)] = head
    elif ext == ".wbfs":
        body[0:4] = b"WBFS"
        body[0x200:0x200 + len(head)] = head
    return bytes(body)


class DolphinGames(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.home = self.tmp.name
        self.games = os.path.join(self.home, "Games")
        self.apps = os.path.join(self.home, ".config/hostd/apps")
        os.makedirs(os.path.join(self.games, "Wii"))

    def put(self, rel, data):
        path = os.path.join(self.games, rel)
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "wb") as f:
            f.write(data)
        return path

    def test_the_title_is_read_from_the_disc_whatever_its_format(self):
        for ext in (".iso", ".gcm", ".rvz", ".wia", ".wbfs"):
            path = self.put(f"x{ext}", image(ext, "RMCE01", "Mario Kart Wii", wii=ext != ".gcm"))
            self.assertEqual(dg.header(path), ("RMCE01", "Mario Kart Wii", "GameCube" if ext == ".gcm" else "Wii"), ext)

    def test_what_is_no_disc_is_named_by_its_file(self):
        self.assertIsNone(dg.header(self.put("junk.iso", b"\0" * 4096)))
        self.assertIsNone(dg.header(self.put("short.iso", b"abc")))
        self.assertIsNone(dg.header(self.put("fake.rvz", image(".iso", "AAAAAA", "X"))))  # no RVZ magic
        self.assertIsNone(dg.header(self.put("c.ciso", b"CISO" + b"\0" * 100)))  # a format it does not read into
        self.assertEqual(dg.title_of("/g/Super_Smash.Bros.rvz", None), "Super Smash Bros")

    def test_each_game_gets_an_app_listed_under_dolphin(self):
        self.put("Wii/Mario Kart Wii.rvz", image(".rvz", "RMCE01", "Mario Kart Wii", size=2_000_000_000 // 1000))
        self.put("Zelda.iso", image(".iso", "GZLE01", "Zelda: Wind Waker", wii=False))
        self.put("readme.txt", b"not a game")
        written, removed = dg.sync(self.home, self.apps)
        self.assertEqual((written, removed), (2, 0))
        wii = open(os.path.join(self.apps, "dolphin-mario-kart-wii.toml")).read()
        self.assertTrue(wii.startswith(dg.MARKER + "\n"))
        for line in ('name = "Mario Kart Wii"', 'under = "dolphin-emu"', 'folder = "Games/Dolphin/Wii"',
                     "[window]", "fullscreen = true", 'details = "Wii · RVZ · 0.0 GB"'):
            self.assertIn(line, wii)
        game = self.games + "/Wii/Mario Kart Wii.rvz"
        self.assertIn('"flatpak", "run", "org.DolphinEmu.dolphin-emu", "-b", "-e", "' + game + '"', wii)
        gc = open(os.path.join(self.apps, "dolphin-zelda.toml")).read()
        self.assertIn('folder = "Games/Dolphin/GameCube"', gc)
        self.assertIn('name = "Zelda: Wind Waker"', gc)

    def test_it_is_idempotent_and_removes_what_is_gone_but_not_other_files(self):
        path = self.put("A.iso", image(".iso", "AAAAAA", "A Game"))
        os.makedirs(self.apps)
        mine = os.path.join(self.apps, "dolphin-mine.toml")
        with open(mine, "w") as f:
            f.write('name = "Mine"\n')  # a file of the person's: no marker
        self.assertEqual(dg.sync(self.home, self.apps), (1, 0))
        self.assertEqual(dg.sync(self.home, self.apps), (0, 0))
        os.remove(path)
        self.assertEqual(dg.sync(self.home, self.apps), (0, 1))
        self.assertFalse(os.path.exists(os.path.join(self.apps, "dolphin-a.toml")))
        self.assertTrue(os.path.exists(mine))

    def test_a_file_of_the_persons_for_a_game_wins(self):
        self.put("A.iso", image(".iso", "AAAAAA", "A Game"))
        os.makedirs(self.apps)
        own = os.path.join(self.apps, "dolphin-a.toml")
        with open(own, "w") as f:
            f.write('name = "My own A"\n')
        self.assertEqual(dg.sync(self.home, self.apps), (0, 0))
        self.assertEqual(open(own).read(), 'name = "My own A"\n')

    def test_two_files_of_one_name_are_two_games(self):
        self.put("one/Game.iso", image(".iso", "AAAAAA", "Game"))
        self.put("two/Game.iso", image(".iso", "BBBBBB", "Game"))
        self.assertEqual(dg.sync(self.home, self.apps), (2, 0))
        self.assertEqual(sorted(os.listdir(self.apps)), ["dolphin-game-2.toml", "dolphin-game.toml"])

    def test_the_folders_dolphin_looks_in_are_searched_too(self):
        other = os.path.join(self.home, "drive")
        os.makedirs(other)
        with open(os.path.join(other, "B.iso"), "wb") as f:
            f.write(image(".iso", "BBBBBB", "From Elsewhere"))
        ini = os.path.join(self.home, ".var/app/org.DolphinEmu.dolphin-emu/config/dolphin-emu/Dolphin.ini")
        os.makedirs(os.path.dirname(ini))
        with open(ini, "w") as f:
            f.write(f"[General]\nISOPaths = 1\nISOPath0 = {other}\nISOPath1 = /does/not/exist\n")
        self.assertEqual(dg.sync(self.home, self.apps), (1, 0))
        self.assertIn('name = "From Elsewhere"', open(os.path.join(self.apps, "dolphin-b.toml")).read())

    def test_a_downloaded_cover_is_the_banner(self):
        self.put("A.iso", image(".iso", "GZLE01", "Zelda"))
        covers = os.path.join(self.home, ".var/app/org.DolphinEmu.dolphin-emu/cache/dolphin-emu/GameCovers")
        os.makedirs(covers)
        png = os.path.join(covers, "GZLE01.png")
        open(png, "wb").close()
        dg.sync(self.home, self.apps)
        self.assertIn(f'banner = "{png}"', open(os.path.join(self.apps, "dolphin-a.toml")).read())

    def test_remove_deletes_what_it_made(self):
        self.put("A.iso", image(".iso", "AAAAAA", "A"))
        dg.sync(self.home, self.apps)
        self.assertEqual(dg.sync(self.home, self.apps, remove_all=True), (0, 1))
        self.assertEqual(os.listdir(self.apps), [])


if __name__ == "__main__":
    unittest.main()
