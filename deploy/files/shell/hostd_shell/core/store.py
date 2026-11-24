"""What the menu knows of hostd: the last answer of each endpoint, and which
of them an event made stale. No Qt, no sockets: the caller asks what to
fetch, fetches it however it likes (asynchronously), and hands the answers
back.

Freshness is by endpoint, not by category: an event refreshes only what it
could have changed, and an endpoint two pages share (the display) is asked
once for both. Events do not carry what ended instances were dropped or in
which order the read lists them, so the cache is refetched, never patched
from an event; what an event says is only *which* endpoints to ask again.
"""

from hostd_shell.core import sources

# Events after which an endpoint is asked again, by endpoint.
REFRESH_ON = {
    sources.INSTANCES: ("instance.", "hostd.", "bus."),
    sources.APPS: ("app.", "hostd."),
    sources.SEAT: ("seat.", "hostd."),
    sources.AUDIO: ("audio.", "hostd."),
    sources.DISPLAY: ("display.output", "display.mode", "display.power", "hostd."),
    sources.VERSION: ("hostd.",),
    sources.ACTIONS: ("action.", "hostd."),
    sources.DEVICES: ("device.", "controller.", "hostd."),
}


def stale_after(event_type):
    """The endpoints an event of this type makes stale. A service's page is
    asked again by its own clock, not by events."""
    return {path for path, prefixes in REFRESH_ON.items() if event_type.startswith(prefixes)}


class Store:
    def __init__(self):
        self.answers = {}  # path -> sources.Answer, the last one
        self.stale = set()  # paths an event made stale since they were answered

    def on_event(self, event_type):
        """An event happened. Returns the stale endpoints that had been
        answered before (the others are asked for when a page needs them)."""
        paths = stale_after(event_type) & set(self.answers)
        self.stale |= paths
        return paths

    def needs(self, source, force=False):
        """The deps of source that must be asked: never answered, stale, or
        all of them when force."""
        return [d for d in sources.deps(source) if force or d.path not in self.answers or d.path in self.stale]

    def answered(self, path, answer):
        self.answers[path] = answer
        self.stale.discard(path)

    def ready(self, source):
        """Whether every dep of source has an answer to build from."""
        return all(d.path in self.answers for d in sources.deps(source))

    def build(self, source, look, tokens):
        """The rows of source from what is known (raises hapi.APIError when a
        dep it cannot do without failed)."""
        return sources.build(source, {d.path: self.answers[d.path] for d in sources.deps(source)}, look, tokens)

    def uses(self, source, paths):
        """Whether source is made of any of paths."""
        return any(d.path in paths for d in sources.deps(source))

    def forget(self, path_prefix):
        """Drops what is known of a family of endpoints (a service's page
        closed)."""
        for p in [p for p in self.answers if p.startswith(path_prefix)]:
            del self.answers[p]
            self.stale.discard(p)
