import importlib.machinery
import importlib.util
import os
import unittest

here = os.path.dirname(os.path.abspath(__file__))
loader = importlib.machinery.SourceFileLoader("helper", os.path.join(here, "hostd-seat-helper"))
spec = importlib.util.spec_from_loader("helper", loader)
helper = importlib.util.module_from_spec(spec)
loader.exec_module(helper)

SEATS = {"serve": 7, "alice": 8}
LIVE = [
    {"id": "1", "user": "serve", "vt": 7, "active": True},
    {"id": "14", "user": "alice", "vt": 8, "active": False},
]


class HelperTest(unittest.TestCase):
    def ask(self, req, caller="serve", live=LIVE):
        calls = []
        out = helper.handle(req, caller, SEATS, lambda: live, calls.append)
        return out, calls

    def test_only_listed_users(self):
        out, calls = self.ask({"verb": "switch", "user": "alice"}, caller="mallory")
        self.assertIn("may not use", out["error"])
        self.assertEqual(calls, [])

    def test_list(self):
        out, _ = self.ask({"verb": "list"})
        self.assertEqual([(r["user"], r["holds"]) for r in out["seats"]], [("serve", True), ("alice", False)])

    def test_switch_activates_the_session(self):
        out, calls = self.ask({"verb": "switch", "user": "alice"})
        self.assertTrue(out["ok"])
        self.assertEqual(calls, ["14"])

    def test_switch_to_the_holder_does_nothing(self):
        out, calls = self.ask({"verb": "switch", "user": "serve"})
        self.assertTrue(out["ok"])
        self.assertEqual(calls, [])

    def test_unknown_user_session_and_verb(self):
        self.assertIn("not a seat user", self.ask({"verb": "switch", "user": "bob"})[0]["error"])
        self.assertIn("no session", self.ask({"verb": "switch", "user": "alice"}, live=[LIVE[0]])[0]["error"])
        self.assertIn("no verb", self.ask({"verb": "reboot"})[0]["error"])

    def test_read_seats(self):
        import tempfile

        with tempfile.NamedTemporaryFile("w", suffix=".seats") as f:
            f.write("# comment\nserve 7\nalice 8 # second\nbad x\n")
            f.flush()
            self.assertEqual(helper.read_seats(f.name), SEATS)
        self.assertEqual(helper.read_seats("/nonexistent/seats"), {})


if __name__ == "__main__":
    unittest.main()
