"""Gets the navigator's rows from hostd without waiting for it.

A Refresh effect becomes requests for the endpoints that page is made of,
sent through ask(path, callback), which answers whenever it can (the Qt
Daemon in the shell, a fake in the tests). Rows are built when the answers
are in, and handed to the navigator. What this adds to asking and waiting:

- what is known already is shown at once, and asked again behind it
  (stale while revalidating), so moving between categories never waits;
- an endpoint two pages share, or two refreshes want, is asked once;
- of two refreshes of one category, the newest wins, whatever the order
  the answers come in;
- an event makes stale only the endpoints it could have changed, and only
  the categories made of those are refreshed.
"""

from dataclasses import dataclass, field

from hostd_shell.core import nav, sources
from hostd_shell.core.store import Store, stale_after


@dataclass
class _Job:
    key: str
    select: nav.Select
    source: nav.Source
    cached: bool  # rows from what was known are on the screen already
    waiting: set = field(default_factory=set)
    gen: int = 0


class Loader:
    def __init__(self, navigator, ask, look=lambda: None, tokens=None, say=lambda text, error=False: None,
                 has_icon=lambda name: True):
        """ask(path, callback): asks hostd, calls callback(sources.Answer)
        later. look() is the background setting now. say(text, error) is for
        what the person should read."""
        self.nav, self.ask, self.look, self.tokens, self.say, self.has_icon = navigator, ask, look, tokens, say, has_icon
        self.store = Store()
        self._jobs = {}  # key -> the newest job of that category
        self._inflight = {}  # path -> the jobs waiting for it, in the order they asked
        self._watchers = {}  # path -> [callback(payload)] called when it is answered ok
        self._gen = 0
        self.unreachable = False  # the last thing asked could not be had

    def watch(self, path, callback):
        """callback(payload) whenever path is answered with success (and at
        once if it already was)."""
        self._watchers.setdefault(path, []).append(callback)
        known = self.store.answers.get(path)
        if known is not None and known.ok:
            callback(known.payload)

    def watching(self):
        return set(self._watchers)

    # --- refreshing ---

    def refresh(self, effect):
        """Carries out a nav.Refresh."""
        key = effect.key
        if key not in self.nav.rows:
            return
        source = self.nav.source(key)
        self._gen += 1
        job = _Job(key, effect.select, source, cached=False, gen=self._gen)
        old = self._jobs.get(key)
        if old is not None:
            self._forget(old)
        self._jobs[key] = job
        if self.store.ready(source):  # what is known, now
            try:
                rows, summary = self.store.build(source, self.look(), self.tokens)
            except Exception:  # noqa: BLE001 - the answer behind it will say
                pass
            else:
                self._apply(job, rows, summary)
                job.cached = True
        job.waiting = {d.path for d in sources.deps(source)}
        for path in sorted(job.waiting):
            self._want(path, job)

    def run(self, effects):
        """Carries out the Refresh effects among effects; returns the others."""
        rest = []
        for e in effects:
            if isinstance(e, nav.Refresh):
                self.refresh(e)
            else:
                rest.append(e)
        return rest

    def _want(self, path, job):
        waiters = self._inflight.get(path)
        if waiters is not None:  # asked already: this one waits for the same answer
            waiters.append(job)
            return
        self._inflight[path] = [job]
        self.ask(path, lambda answer, path=path: self._answered(path, answer))

    def _forget(self, job):
        for path in job.waiting:
            if path in self._inflight and job in self._inflight[path]:
                self._inflight[path].remove(job)
        job.waiting = set()

    def _answered(self, path, answer):
        self.store.answered(path, answer)
        waiters = self._inflight.pop(path, [])
        if answer.ok:
            for callback in self._watchers.get(path, ()):
                callback(answer.payload)
        for job in waiters:
            if self._jobs.get(job.key) is not job:
                continue  # a newer refresh of this category took over
            job.waiting.discard(path)
            if not job.waiting:
                self._finish(job)

    def _finish(self, job):
        try:
            rows, summary = self.store.build(job.source, self.look(), self.tokens)
        except Exception as e:  # noqa: BLE001 - said on the screen
            self.unreachable = True
            self.say(f"Cannot reach hostd: {e}", True)
            return
        if job.key == self.nav.category and self.unreachable:
            self.say("", False)
        self.unreachable = False
        self._apply(job, rows, summary)

    def _apply(self, job, rows, summary):
        for r in rows:
            for x in [r, *r.sheet]:
                if x.app_icon and not self.has_icon(x.app_icon):
                    x.app_icon = ""
        # Behind rows already shown, the cursor stays where it is.
        self.nav.set_rows(job.key, rows, summary, nav.KEEP if job.cached else job.select)

    # --- events ---

    def on_event(self, event_type):
        """An event: the categories to refresh because of it (the caller
        waits a moment for more events first, then calls refresh_categories).
        Watched endpoints it made stale are asked again at once. The current
        category is left alone while an options panel or a question is open,
        as it was before."""
        paths = stale_after(event_type)
        self.store.on_event(event_type)
        self.refresh_watched(paths)
        busy = isinstance(self.nav.overlay, (nav.Options, nav.Question))
        out = []
        for c in self.nav.cats:
            source = self.nav.source(c.key)
            if source.page_app or not self.store.uses(source, paths) or (busy and c.key == self.nav.category):
                continue  # a page has its own clock
            out.append(c.key)
        return out

    def refresh_categories(self, keys):
        for key in keys:
            self.refresh(nav.Refresh(key))

    def refresh_watched(self, paths):
        """Asks again the watched endpoints that are among paths."""
        for path in sorted(set(paths) & set(self._watchers)):
            if path not in self._inflight:
                self._inflight[path] = []
                self.ask(path, lambda answer, path=path: self._answered(path, answer))
