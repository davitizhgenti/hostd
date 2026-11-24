"""What each category lists, from hostd: which endpoints a page is made of,
and how the rows are built from their answers. Pure, so the same code serves
a blocking fetch (tests) and the asynchronous store."""

from dataclasses import dataclass

from hostd_shell import api as hapi
from hostd_shell import views


@dataclass(frozen=True)
class Answer:
    """What one endpoint said. ok: it answered with success; an error answer
    has status and message (hostd's)."""
    ok: bool
    payload: object = None
    status: int = 0
    message: str = ""

    def need(self):
        """The payload, or the error an answer that failed is."""
        if not self.ok:
            raise hapi.APIError(self.message or f"HTTP {self.status}", status=self.status)
        return self.payload


def tolerated(answer, default):
    """The payload of an optional endpoint, or default when hostd answered with
    an error (an older hostd has no such page). When hostd could not be
    reached at all (no status) the page fails, as for any other endpoint."""
    if answer.ok:
        return answer.payload
    if not answer.status:
        answer.need()  # raises
    return default


@dataclass(frozen=True)
class Dep:
    """An endpoint a page is made of."""
    path: str
    tolerant: bool = False  # the page copes with an error answer (an older hostd)


INSTANCES, APPS = "/v1/instances?all=true", "/v1/apps"
SEAT, AUDIO, DISPLAY = "/v1/seat", "/v1/audio", "/v1/display"
DEVICES, VERSION, ACTIONS = "/v1/devices", "/v1/version", "/v1/actions?limit=12"


def page_path(app):
    return f"/v1/catalog/{app}/status"


def deps(source):
    """The endpoints source is made of."""
    if source.page_app:
        return [Dep(page_path(source.page_app))]
    kind = source.category.kind
    if kind == "apps":
        return [Dep(INSTANCES), Dep(APPS)]
    if kind == "users":
        return [Dep(SEAT, tolerant=True)]
    if kind == "settings":
        return [Dep(AUDIO, tolerant=True), Dep(DISPLAY)]
    return [Dep(DEVICES, tolerant=True), Dep(VERSION), Dep(DISPLAY), Dep(ACTIONS)]


def build(source, answers, look, tokens):
    """A category's rows and its summary line, from the answers of its deps
    (a dict by path). An answer that failed raises hapi.APIError unless the
    page copes with it."""
    if source.page_app:
        return views.page_rows(answers[page_path(source.page_app)].need())
    category = source.category
    if category.kind == "apps":
        instances = answers[INSTANCES].need() or []
        apps = [it["spec"] for it in (answers[APPS].need() or []) if it.get("spec")]
        return views.apps_page(instances, apps, source.path, category.root, source.tabs, flat=category.view == "tiles")
    if category.kind == "users":
        seat = answers[SEAT]
        if not seat.ok and seat.status == 404:
            return [views.Row("Switching users is not set up", icon="info",
                              subtitle="the seats add-on: sudo hostd-admin add seats")], ""
        return views.users_page(seat.need()), ""
    if category.kind == "settings":
        audio = tolerated(answers[AUDIO], None)
        return views.settings_page(audio, answers[DISPLAY].need(), look, tokens), ""
    devices = tolerated(answers[DEVICES], []) or []
    return views.status_page(answers[VERSION].need(), answers[DISPLAY].need(), answers[ACTIONS].need(), devices), ""


def ask(api, path):
    """One endpoint, asked and waited for: an Answer. What is not an API
    answer (hostd cannot be reached) is raised."""
    try:
        return Answer(True, api.get(path))
    except hapi.APIError as e:
        return Answer(False, status=e.status, message=str(e))


def fetch(api, source, look, tokens):
    """A category's rows and its summary line, asking hostd and waiting.
    source: nav.Source."""
    answers = {}
    for dep in deps(source):
        answers[dep.path] = ask(api, dep.path)
        if not answers[dep.path].ok and not dep.tolerant:
            answers[dep.path].need()  # raises
    return build(source, answers, look, tokens)
