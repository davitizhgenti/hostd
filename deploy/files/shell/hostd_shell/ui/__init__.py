"""The menu's Qt objects, one per concern, that QML binds to. They wrap the
Qt-free core (hostd_shell.core): the logic and its tests live there, these
only own timers, signals and what QML can see.

Registered as the QML module Hostd.Shell (typed: qmllint checks QML against
them). Properties are written with @Property(type, notify=signal) on named
methods: pyside6-metaobjectdump, which reads the source, cannot read lambdas.
"""

QML_IMPORT_NAME = "Hostd.Shell"
QML_IMPORT_MAJOR_VERSION = 1
