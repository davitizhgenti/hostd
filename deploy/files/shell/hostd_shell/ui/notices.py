"""The notices on screen: the queue (notices.Notices, which has no Qt), the
rows QML lists, and the clock that takes one away when its time is up."""

from PySide6.QtCore import Property, QObject, QTimer, Signal
from PySide6.QtQml import QmlElement, QmlUncreatable

from hostd_shell import notices, views
from hostd_shell.models import RowsModel
from hostd_shell.ui import QML_IMPORT_MAJOR_VERSION, QML_IMPORT_NAME  # noqa: F401 - read by @QmlElement/


class NoticesModel(RowsModel):
    """The notices on screen, as rows: title the summary, subtitle the body,
    value the app, tone the urgency."""

    def show(self, shown):
        self.set_rows([views.Row(n.summary, subtitle=n.body, value=n.app, app=str(n.id),
                                 tone={notices.CRITICAL: "danger", notices.LOW: "low"}.get(n.urgency, ""))
                       for n in shown])


@QmlElement
@QmlUncreatable("The shell makes it.")
class Notices(QObject):
    closed = Signal(int, int)  # id, reason (the notification spec's)

    def __init__(self, parent=None, clock=None):
        super().__init__(parent)
        self.queue = notices.Notices(clock) if clock else notices.Notices()
        self._model = NoticesModel(self)
        self._timer = QTimer(self, singleShot=True, timeout=self._expire)

    @Property(QObject, constant=True)
    def model(self):
        return self._model

    def notify(self, app, summary, body, replaces_id, urgency, expire_ms):
        nid = self.queue.notify(app, summary, body, replaces_id, urgency, expire_ms)
        self.show()
        return nid

    def close_notice(self, nid):
        if self.queue.close(nid):
            self.closed.emit(nid, notices.CLOSED_BY_CALL)
            self.show()

    def expire(self):
        """The menu opened: what has run out goes."""
        for nid in self.queue.expire():
            self.closed.emit(nid, notices.EXPIRED)
        self.show()

    def show(self):
        self._model.show(self.queue.shown())
        wait = self.queue.next_expiry()
        self._timer.stop()
        if wait is not None:
            self._timer.start(int(wait * 1000) + 50)

    def _expire(self):
        self.expire()
