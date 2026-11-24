"""What each category lists, fetched from hostd: the one place that knows
which endpoints a page is made of. Pure: takes any object with get()."""

from hostd_shell import api as hapi
from hostd_shell import views


def fetch(api, source, look, tokens):
    """A category's rows and its summary line. source: nav.Source."""
    if source.page_app:  # a service's own page
        return views.page_rows(api.get(f"/v1/catalog/{source.page_app}/status"))
    category = source.category
    if category.kind == "apps":
        instances = api.get("/v1/instances?all=true") or []
        apps = [it["spec"] for it in (api.get("/v1/apps") or []) if it.get("spec")]
        return views.apps_page(instances, apps, source.path, category.root, source.tabs, flat=category.view == "tiles")
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
        devices = []  # an older hostd
    return views.status_page(api.get("/v1/version"), api.get("/v1/display"), api.get("/v1/actions?limit=12"), devices), ""
