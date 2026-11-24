"""Sources, the store and the loader with no Qt and no hostd (make lint-scripts)."""

import os
import random
import sys
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
sys.path.insert(0, os.path.join(HERE, "..", "..", "..", "test", "shell"))

from test_nav import TOKENS, make  # noqa: E402

import fixtures  # noqa: E402
from hostd_shell import api as hapi  # noqa: E402
from hostd_shell import background, menu, views  # noqa: E402
from hostd_shell import input as inp  # noqa: E402
from hostd_shell.core import loader, nav, sources, store  # noqa: E402
from hostd_shell.core.driver import Driver  # noqa: E402

LOOK = background.load("/nonexistent/ui.toml")


def legacy_fetch(api, category, look, tokens, path=(), tabs=()):
    """What the Shell fetched before the endpoints were separated from the rows."""
    if category.kind == "apps":
        instances = api.get("/v1/instances?all=true") or []
        apps = [it["spec"] for it in (api.get("/v1/apps") or []) if it.get("spec")]
        return views.apps_page(instances, apps, path, category.root, tabs, flat=category.view == "tiles")
    if category.kind == "users":
        try:
            seats = api.get("/v1/seat")
        except hapi.APIError as e:
            if e.status != 404:
                raise
            return [views.Row("Switching users is not set up", icon="info",
                              subtitle="the seats add-on: sudo hostd-admin add seats")], ""
        return views.users_page(seats), ""
    if category.kind == "settings":
        try:
            audio = api.get("/v1/audio")
        except hapi.APIError:
            audio = None
        return views.settings_page(audio, api.get("/v1/display"), look, tokens), ""
    try:
        devices = api.get("/v1/devices")
    except hapi.APIError:
        devices = []
    return views.status_page(api.get("/v1/version"), api.get("/v1/display"), api.get("/v1/actions?limit=12"), devices), ""


def tabs_and_cats(menu_text=fixtures.MENU):
    import tempfile
    path = os.path.join(tempfile.mkdtemp(), "menu.toml")
    with open(path, "w") as f:
        f.write(menu_text)
    tabs, _ = menu.load(path, lambda name: True)
    return tabs, views.categories(tabs)


def outcome(fn):
    try:
        return ("rows", fn())
    except hapi.APIError as e:
        return ("error", str(e), e.status)


class SourcesParity(unittest.TestCase):
    """The rows built from separate answers are the rows the blocking fetch built."""

    def test_every_page_under_every_failure(self):
        tabs, cats = tabs_and_cats()
        missing_sets = [(), ("/v1/seat",), ("/v1/audio",), ("/v1/devices",), ("/v1/version",), ("/v1/display",),
                        ("/v1/actions?limit=12",), ("/v1/instances?all=true",), ("/v1/apps",),
                        ("/v1/audio", "/v1/devices", "/v1/seat")]
        for missing in missing_sets:
            api = fixtures.FakeAPI()
            for path in missing:
                api.reads.pop(path, None)
            for c in cats:
                source = nav.Source(c, c.root, "", tuple(t.folder for t in tabs))
                want = outcome(lambda c=c, api=api: legacy_fetch(api, c, LOOK, TOKENS, c.root, [t.folder for t in tabs]))
                got = outcome(lambda source=source, api=api: sources.fetch(api, source, LOOK, TOKENS))
                self.assertEqual(got, want, f"{c.key} without {missing}")

    def test_a_service_page(self):
        api = fixtures.FakeAPI()
        api.reads["/v1/catalog/homeassistant/status"] = fixtures.STATUS
        cat = views.APPS_CATEGORY
        rows, summary = sources.fetch(api, nav.Source(cat, (), "homeassistant", ()), LOOK, TOKENS)
        self.assertEqual((rows, summary), views.page_rows(fixtures.STATUS))
        deps = sources.deps(nav.Source(cat, (), "homeassistant", ()))
        self.assertEqual([d.path for d in deps], ["/v1/catalog/homeassistant/status"])

    def test_deps_are_what_each_page_is_made_of(self):
        _tabs, cats = tabs_and_cats()
        by_key = {c.key: [d.path for d in sources.deps(nav.Source(c))] for c in cats}
        self.assertEqual(by_key["apps"], ["/v1/instances?all=true", "/v1/apps"])
        self.assertEqual(by_key["users"], ["/v1/seat"])
        self.assertEqual(by_key["settings"], ["/v1/audio", "/v1/display"])
        self.assertEqual(by_key["status"], ["/v1/devices", "/v1/version", "/v1/display", "/v1/actions?limit=12"])


# the table the Shell used: events after which a category listed again, by its kind
OLD_REFRESH_ON = {
    "apps": ("instance.", "app.", "hostd.", "bus."),
    "users": ("seat.", "hostd."),
    "settings": ("audio.", "display.output", "display.mode", "display.power", "hostd."),
    "status": ("hostd.", "action.", "device.", "controller."),
}
EVENTS = ["instance.started", "instance.exited", "app.catalog.changed", "bus.lagged", "seat.switched", "audio.volume.changed",
          "audio.outputs.changed", "display.mode", "display.power", "display.output.changed", "display.focused",
          "display.pointer",
          "window.focus", "device.connected", "controller.connected", "action.applied", "action.failed", "hostd.connected",
          "resource.applied", "deploy.done", "media.changed", "token.created", "scene.applied"]


class Async:
    """hostd answering when the test says, in the order it says."""

    def __init__(self, api):
        self.api, self.queue, self.asked = api, [], []

    def ask(self, path, callback):
        self.asked.append(path)
        self.queue.append((path, callback))

    def deliver(self, rnd=None, only=None):
        queue = [q for q in self.queue if only is None or q[0] in only]
        self.queue = [q for q in self.queue if q not in queue]
        if rnd:
            rnd.shuffle(queue)
        for path, callback in queue:
            callback(sources.ask(self.api, path))

    def count(self, path):
        return self.asked.count(path)


def make_loader(menu_text=fixtures.MENU, api=None):
    tabs, cats = tabs_and_cats(menu_text) if menu_text else ([], views.categories(()))
    n = nav.Navigator(cats, tabs)
    hostd = Async(api or fixtures.FakeAPI())
    said = []
    ld = loader.Loader(n, hostd.ask, look=lambda: LOOK, tokens=TOKENS, say=lambda text, error=False: said.append((text, error)))
    return n, ld, hostd, said


def first_load(menu_text=fixtures.MENU, api=None):
    n, ld, hostd, said = make_loader(menu_text, api)
    ld.run([nav.Refresh(c.key) for c in n.cats])
    hostd.deliver()
    return n, ld, hostd, said


class Loading(unittest.TestCase):
    def test_the_rows_are_those_the_blocking_driver_makes_whatever_the_order_of_the_answers(self):
        want, _ = make(fixtures.MENU)
        for seed in range(25):
            n, ld, hostd, said = make_loader()
            ld.run([nav.Refresh(c.key) for c in n.cats])
            hostd.deliver(random.Random(seed))
            self.assertEqual({k: [r.key() for r in v] for k, v in n.rows.items()},
                             {k: [r.key() for r in v] for k, v in want.rows.items()}, seed)
            self.assertEqual(n.currents, want.currents, seed)
            self.assertEqual(said, [])

    def test_an_endpoint_two_pages_share_is_asked_once(self):
        n, ld, hostd, _ = make_loader()
        ld.run([nav.Refresh("settings"), nav.Refresh("status")])
        self.assertEqual(hostd.count("/v1/display"), 1)
        hostd.deliver()
        self.assertTrue(n.rows["settings"] and n.rows["status"])

    def test_what_is_known_is_shown_at_once_and_asked_again_behind_it(self):
        n, ld, hostd, _ = first_load()
        n.rows["users"] = []  # as if the page was never shown
        shown = []
        original = n.set_rows
        n.set_rows = lambda *a, **k: (shown.append(a[0]), original(*a, **k))[1]
        ld.run([nav.Refresh("users")])
        self.assertEqual(shown, ["users"], "shown from what is known, before any answer")
        self.assertTrue(n.rows["users"])
        self.assertEqual(hostd.queue[-1][0], "/v1/seat", "and asked again")
        hostd.deliver()
        self.assertEqual(shown, ["users", "users"])

    def test_behind_cached_rows_the_cursor_stays_where_the_person_put_it(self):
        n, ld, hostd, _ = first_load()
        ld.run(n.set_category("users"))
        hostd.deliver()
        ld.run([nav.Refresh("users")])
        n.currents["users"] = len(n.rows["users"]) - 1  # moved while the answer was on its way
        want = n.rows["users"][n.currents["users"]].key()
        hostd.deliver()
        self.assertEqual(n.current_row().key(), want)

    def test_the_newest_refresh_of_a_category_wins(self):
        n, ld, hostd, _ = make_loader()
        ld.run([nav.Refresh("apps", nav.FIRST)])
        ld.run([nav.Refresh("apps", nav.Select(app="jellyfin"))])
        hostd.deliver()
        self.assertEqual(n.current_row().app, "jellyfin")
        self.assertEqual(hostd.count("/v1/instances?all=true"), 1, "both wanted the same answer")

    def test_a_refresh_replaced_before_its_answers_come_never_applies(self):
        n, ld, hostd, _ = make_loader()
        calls = []
        original = n.set_rows
        n.set_rows = lambda *a, **k: (calls.append(a[0]), original(*a, **k))[1]
        ld.run([nav.Refresh("apps"), nav.Refresh("apps")])
        hostd.deliver()
        self.assertEqual(calls, ["apps"])

    def test_hostd_not_answering_stops_nothing_the_cursor_does(self):
        n, ld, _hostd, said = first_load()
        ld.run([nav.Refresh("apps")])  # never delivered
        for verb in (inp.DOWN, inp.RIGHT, inp.RIGHT, inp.LEFT, inp.ACTIONS, inp.BACK, inp.HOME):
            ld.run(n.handle(verb))
        self.assertEqual(said, [])
        self.assertEqual(n.category, "apps")


class Failing(unittest.TestCase):
    def test_a_page_that_cannot_do_without_an_endpoint_says_so_and_keeps_its_rows(self):
        api = fixtures.FakeAPI()
        n, ld, hostd, said = first_load(api=api)
        rows = [r.key() for r in n.rows["apps"]]
        del api.reads["/v1/instances?all=true"]
        ld.run([nav.Refresh("apps")])
        hostd.deliver()
        self.assertEqual(len(said), 1)
        self.assertTrue(said[0][0].startswith("Cannot reach hostd:") and said[0][1])
        self.assertEqual([r.key() for r in n.rows["apps"]], rows)
        api.reads["/v1/instances?all=true"] = []
        ld.run([nav.Refresh("apps")])
        hostd.deliver()
        self.assertEqual(said[-1], ("", False), "said no more once it can be reached")

    def test_a_page_that_copes_is_built_without_it(self):
        api = fixtures.FakeAPI()
        del api.reads["/v1/audio"]
        n, _ld, _hostd, said = first_load(api=api)
        self.assertTrue(n.rows["settings"])
        self.assertEqual(said, [])


class Events(unittest.TestCase):
    def categories(self, event, setup=None):
        n, ld, _hostd, _ = first_load()
        if setup:
            setup(n)
        return set(ld.on_event(event))

    def test_an_event_refreshes_only_the_categories_made_of_what_it_changed(self):
        apps = {"apps", "tab:Games", "tab:Services"}
        self.assertEqual(self.categories("instance.started"), apps)
        self.assertEqual(self.categories("app.catalog.changed"), apps)
        self.assertEqual(self.categories("seat.switched"), {"users"})
        self.assertEqual(self.categories("audio.volume.changed"), {"settings"})
        self.assertEqual(self.categories("display.mode"), {"settings", "status"})
        self.assertEqual(self.categories("device.connected"), {"status"})
        self.assertEqual(self.categories("action.applied"), {"status"})
        self.assertEqual(self.categories("window.focus"), set())
        everything = {"apps", "tab:Games", "tab:Services", "users", "settings", "status"}
        self.assertEqual(self.categories("hostd.connected"), everything)

    def test_no_category_that_refreshed_before_is_left_out(self):
        _tabs, cats = tabs_and_cats()
        for event in EVENTS:
            _n, ld, _hostd, _ = first_load()
            got = set(ld.on_event(event))
            want = {c.key for c in cats if event.startswith(OLD_REFRESH_ON[c.kind])}
            self.assertTrue(want <= got, f"{event}: left out {want - got}")

    def test_the_stale_endpoints_are_asked_again_not_the_rest(self):
        _n, ld, hostd, _ = first_load()
        ld.refresh_categories(ld.on_event("audio.volume.changed"))
        self.assertEqual(sorted(p for p, _ in hostd.queue), ["/v1/audio", "/v1/display"])

    def test_the_current_category_waits_while_an_overlay_is_open(self):
        n, ld, _hostd, _ = first_load()
        n.overlay = nav.Options(next(r for r in n.all_rows["apps"] if r.sheet))
        self.assertNotIn("apps", ld.on_event("instance.started"))
        n.overlay = None
        self.assertIn("apps", ld.on_event("instance.started"))

    def test_a_service_page_has_its_own_clock(self):
        n, ld, _hostd, _ = first_load()
        n.svc_page["apps"] = {"app": "homeassistant", "title": "Home Assistant"}
        self.assertNotIn("apps", ld.on_event("instance.started"))

    def test_the_stale_set_is_what_the_store_says(self):
        s = store.Store()
        self.assertEqual(s.on_event("instance.started"), set(), "nothing was asked yet")
        s.answered("/v1/instances?all=true", sources.Answer(True, []))
        self.assertEqual(s.on_event("instance.started"), {"/v1/instances?all=true"})
        self.assertEqual([d.path for d in s.needs(nav.Source(views.APPS_CATEGORY))], ["/v1/instances?all=true", "/v1/apps"])
        s.answered("/v1/instances?all=true", sources.Answer(True, []))
        self.assertEqual([d.path for d in s.needs(nav.Source(views.APPS_CATEGORY))], ["/v1/apps"])


class Watching(unittest.TestCase):
    def test_the_device_list_reaches_the_watcher_when_answered_and_after_device_events(self):
        api = fixtures.FakeAPI()
        api.reads["/v1/devices"] = fixtures.DEVICES
        _n, ld, hostd, _ = make_loader(api=api)
        got = []
        ld.watch("/v1/devices", got.append)
        ld.refresh_watched({"/v1/devices"})
        hostd.deliver()
        self.assertEqual(len(got), 1)
        self.assertEqual(got[0], fixtures.DEVICES)
        ld.on_event("device.connected")
        self.assertEqual(hostd.count("/v1/devices"), 2)
        hostd.deliver()
        self.assertEqual(len(got), 2)
        late = []
        ld.watch("/v1/devices", late.append)
        self.assertEqual(len(late), 1, "told at once of what is known")

    def test_an_older_hostd_without_devices_tells_the_watcher_nothing(self):
        api = fixtures.FakeAPI()
        api.reads.pop("/v1/devices", None)
        _n, ld, hostd, _ = make_loader(api=api)
        got = []
        ld.watch("/v1/devices", got.append)
        ld.refresh_watched({"/v1/devices"})
        hostd.deliver()
        self.assertEqual(got, [])


class Driven(unittest.TestCase):
    def test_the_loader_and_the_blocking_driver_agree_through_a_session(self):
        for seed in range(20):
            n1, d1 = make(fixtures.MENU)
            n2, ld, hostd, _ = first_load()
            rnd = random.Random(seed)
            for _ in range(40):
                verb = rnd.choice(inp.VERBS)
                d1.run(n1.handle(verb))
                rest = ld.run(n2.handle(verb))
                hostd.deliver(rnd)
                for e in rest:  # sends are the driver's business: apply it to both through the driver
                    if isinstance(e, nav.Send):
                        Driver(n2, d1.api).run([e])
                        hostd.api = d1.api
                        ld.run([nav.Refresh(n2.category)])
                        hostd.deliver(rnd)
                self.assertEqual(n1.category, n2.category, f"seed {seed}")
                self.assertEqual({k: [r.key() for r in v] for k, v in n1.rows.items() if k == n1.category},
                                 {k: [r.key() for r in v] for k, v in n2.rows.items() if k == n2.category}, f"seed {seed}")


if __name__ == "__main__":
    unittest.main()
