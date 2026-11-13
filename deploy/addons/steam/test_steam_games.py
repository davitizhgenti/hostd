"""Tests for hostd-steam-games: python3 -m unittest (make lint-scripts)."""

import importlib.machinery
import importlib.util
import os
import tempfile
import unittest

_loader = importlib.machinery.SourceFileLoader(
    "hostd_steam_games", os.path.join(os.path.dirname(os.path.abspath(__file__)), "hostd-steam-games"))
_spec = importlib.util.spec_from_loader("hostd_steam_games", _loader)
sg = importlib.util.module_from_spec(_spec)
_loader.exec_module(sg)


def manifest(appid, name, flags="4"):
    return f'"AppState"\n{{\n\t"appid"\t\t"{appid}"\n\t"name"\t\t"{name}"\n\t"StateFlags"\t\t"{flags}"\n}}\n'


class SteamGames(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.home = self.tmp.name
        self.root = os.path.join(self.home, ".var/app/com.valvesoftware.Steam/.local/share/Steam")
        self.second = os.path.join(self.home, "games-drive")
        for lib in (self.root, self.second):
            os.makedirs(os.path.join(lib, "steamapps"))
        with open(os.path.join(self.root, "steamapps/libraryfolders.vdf"), "w") as f:
            f.write(f'''"libraryfolders"
{{
\t"0" {{ "path" "{self.root}" "apps" {{ "620" "1" }} }}
\t"1" {{ "path" "{self.second}" }}  // a second drive
\t"contentstatsid" "123"
}}
''')
        self.write(self.root, "620", 'Portal "2"')
        self.write(self.root, "1493710", "Proton Experimental")
        self.write(self.root, "1628350", "Steam Linux Runtime 3.0 (sniper)")
        self.write(self.second, "440", "Team Fortress 2")
        self.write(self.second, "70", "Half-Life", flags="1026")  # not fully installed
        self.apps = os.path.join(self.home, "apps")

    def tearDown(self):
        self.tmp.cleanup()

    def write(self, lib, appid, name, flags="4"):
        with open(os.path.join(lib, "steamapps", f"appmanifest_{appid}.acf"), "w") as f:
            f.write(manifest(appid, name.replace('"', '\\"'), flags))

    def test_games_from_every_library_without_tools(self):
        self.assertEqual(sg.games(self.home), {"620": 'Portal "2"', "440": "Team Fortress 2"})

    def test_sync_writes_updates_and_removes_only_its_own(self):
        self.assertEqual(sg.sync(self.home, self.apps), (2, 0))
        with open(os.path.join(self.apps, "steam-620.toml")) as f:
            body = f.read()
        self.assertIn('name = "Portal \\"2\\""', body)
        self.assertIn('handoff = "SteamAppId=620"', body)
        self.assertIn('class = "steam_app_620"', body)
        self.assertIn('"flatpak", "run", "com.valvesoftware.Steam", "steam://rungameid/620"', body)
        self.assertIn('under = "steam"', body)
        self.assertIn('folder = "Games/Steam"', body)
        self.assertIn('requires = ["steam"]', body)
        self.assertEqual(sg.sync(self.home, self.apps), (0, 0))  # nothing changed

        # The person's own file for a game is left alone, even when stale.
        with open(os.path.join(self.apps, "steam-999.toml"), "w") as f:
            f.write('name = "Mine"\n')
        os.remove(os.path.join(self.second, "steamapps/appmanifest_440.acf"))
        self.assertEqual(sg.sync(self.home, self.apps), (0, 1))
        self.assertEqual(sorted(os.listdir(self.apps)), ["steam-620.toml", "steam-999.toml"])

        self.assertEqual(sg.sync(self.home, self.apps, remove_all=True), (0, 1))
        self.assertEqual(os.listdir(self.apps), ["steam-999.toml"])

    def test_a_game_with_a_shortcut_extends_it_and_has_its_pictures(self):
        share = os.path.join(self.home, ".var/app/com.valvesoftware.Steam/.local/share")
        os.makedirs(os.path.join(share, "applications"))
        with open(os.path.join(share, "applications", "Portal 2.desktop"), "w") as f:
            f.write("[Desktop Entry]\nName=Portal 2\nExec=steam steam://rungameid/620\nIcon=steam_icon_620\n")
        with open(os.path.join(share, "applications", "Other.desktop"), "w") as f:
            f.write("[Desktop Entry]\nName=Other\nExec=other\n")
        cache = os.path.join(self.root, "appcache", "librarycache")
        os.makedirs(os.path.join(cache, "620"))
        for n in ("620/library_600x900.jpg", "620/library_hero.jpg", "440_header.jpg"):  # new and old layouts
            open(os.path.join(cache, n), "w").close()
        self.assertEqual(sg.shortcuts(self.home), {"620": "steam-portal-2"})
        sg.sync(self.home, self.apps)
        with open(os.path.join(self.apps, "steam-620.toml")) as f:
            portal = f.read()
        self.assertIn('extends = "steam-portal-2"', portal)
        self.assertIn(f'cover = "{cache}/620/library_600x900.jpg"', portal)
        self.assertIn(f'backdrop = "{cache}/620/library_hero.jpg"', portal)
        with open(os.path.join(self.apps, "steam-440.toml")) as f:
            tf2 = f.read()
        self.assertNotIn("extends", tf2)  # no shortcut: a file of its own
        self.assertNotIn("cover", tf2)
        self.assertIn(f'backdrop = "{cache}/440_header.jpg"', tf2)

    def test_id_part_is_hostds(self):
        self.assertEqual(sg.id_part("steam-JoJo's Bizarre Adventure: All-Star"), "steam-jojo-s-bizarre-adventure--all-star")
        self.assertEqual(sg.id_part("..."), "app")

    def test_no_steam_is_no_games(self):
        with tempfile.TemporaryDirectory() as empty:
            self.assertEqual(sg.games(empty), {})

    def test_parse_vdf(self):
        self.assertEqual(sg.parse_vdf('"A" { "b" "1" "C" { "d" "x\\"y" } }'), {"a": {"b": "1", "c": {"d": 'x"y'}}})
        with self.assertRaises(ValueError):
            sg.parse_vdf('"a" { "b" "1"')


if __name__ == "__main__":
    unittest.main()
