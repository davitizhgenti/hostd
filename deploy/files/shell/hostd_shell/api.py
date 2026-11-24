"""hostd's error answers, as the shell knows them. (Its API itself is
daemon.py: asynchronous, over the same unix socket.)"""


class APIError(Exception):
    """hostd answered with an error; code is its error code (forbidden, ...)."""

    def __init__(self, message, code="", status=0):
        super().__init__(message)
        self.code, self.status = code, status
