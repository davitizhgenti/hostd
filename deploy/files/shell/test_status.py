import unittest

from hostd_shell.core import status

APPS = [{"id": "jellyfin", "name": "Jellyfin", "surface": "background"},
        {"id": "samba", "name": "Samba", "surface": "background"},
        {"id": "nextcloud", "name": "Nextcloud", "surface": "background"},
        {"id": "dolphin", "name": "Dolphin", "surface": "window"}]


class Services(unittest.TestCase):
    def test_up_starting_and_failed_services_have_a_square_and_a_stopped_one_none(self):
        instances = [{"app": "jellyfin", "state": "running"}, {"app": "nextcloud", "state": "starting"},
                     {"app": "samba", "state": "failed"}, {"app": "dolphin", "state": "running"}]
        got = status.services(instances, APPS)
        self.assertEqual([(s["id"], s["state"], s["tone"]) for s in got],
                         [("samba", "failed", "danger"), ("jellyfin", "running", "positive"), ("nextcloud", "starting", "warning")])
        self.assertEqual(status.services([{"app": "jellyfin", "state": "stopped"}], APPS), [])

    def test_a_line_that_overflows_gives_its_last_place_to_the_count(self):
        self.assertEqual(status.capped(range(6)), (list(range(6)), 0))
        self.assertEqual(status.capped(range(7)), (list(range(5)), 2))
        self.assertEqual(status.capped(range(40)), (list(range(5)), 35))
        self.assertEqual(status.capped([]), ([], 0))


if __name__ == "__main__":
    unittest.main()
