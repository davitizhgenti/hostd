"""Carries out a Navigator's effects against an API, one after another.
It is the reference for what each effect means; the Qt layer does the same
asynchronously. Used by the tests."""

from hostd_shell import views
from hostd_shell.core import nav, sources


class Driver:
    def __init__(self, navigator, api, look=None, tokens=None, has_icon=lambda name: True):
        self.nav, self.api, self.look, self.tokens, self.has_icon = navigator, api, look, tokens, has_icon
        self.messages = []  # (text, error) said, in order
        self.saved_backgrounds = []

    def run(self, effects):
        queue = list(effects)
        while queue:
            e = queue.pop(0)
            if isinstance(e, nav.Refresh):
                self._refresh(e)
            elif isinstance(e, nav.Send):
                queue += self._send(e)
            elif isinstance(e, nav.SaveBackground):
                self.saved_backgrounds.append(e.body)
            elif isinstance(e, nav.ClearMessage) and self._said_something_else():
                self.messages.append(("", False))

    def _said_something_else(self):
        """The last message is about what was left: not the one that says hostd is out of reach."""
        return bool(self.messages and self.messages[-1][0] and not self.messages[-1][0].startswith("Cannot reach hostd"))

    def _refresh(self, e):
        n = self.nav
        if e.key not in n.rows:
            return
        try:
            rows, summary = sources.fetch(self.api, n.source(e.key), self.look, self.tokens)
        except Exception as ex:  # noqa: BLE001 - said on the screen
            self.messages.append((f"Cannot reach hostd: {ex}", True))
            return
        if e.key == n.category and self.messages and self.messages[-1][0].startswith("Cannot reach hostd"):
            self.messages.append(("", False))
        for r in rows:
            for x in [r, *r.sheet]:
                if x.app_icon and not self.has_icon(x.app_icon):
                    x.app_icon = ""
        n.set_rows(e.key, rows, summary, e.select)

    def _send(self, e):
        try:
            res = self.api.call(e.call.method, e.call.path, e.call.body)
        except Exception as ex:  # noqa: BLE001 - said on the screen
            self.messages.append((str(ex), True))
            return []
        text = views.result_text(res)
        self.messages.append((text, bool(text)))
        return [nav.Refresh(self.nav.category)] if e.refresh else []
