"""qmllint over the shell's QML: no warnings. With a typed Theme and the
shell passed as a property (no context properties), qmllint checks every
token and member use, which catches what the screen would only show as a
missing element (a name shadowed by a property, a token misspelt)."""

import glob
import os
import shutil
import subprocess
import tempfile
import unittest

from harness import SHELL

QMLLINT = shutil.which("qmllint") or "/usr/lib/qt6/bin/qmllint"
DUMP, REGISTRAR = shutil.which("pyside6-metaobjectdump"), shutil.which("pyside6-qmltyperegistrar")


def typed_imports():
    """A QML import path that knows the shell's Python objects (module
    Hostd.Shell): their type information, made from their source."""
    root = tempfile.mkdtemp()
    sources = sorted(glob.glob(os.path.join(SHELL, "hostd_shell", "ui", "*.py")))
    dump = os.path.join(root, "types.json")
    subprocess.run([DUMP, *sources, "-o", dump], check=True, capture_output=True, cwd=SHELL)
    module = os.path.join(root, "Hostd", "Shell")
    os.makedirs(module)
    subprocess.run([REGISTRAR, "--generate-qmltypes", os.path.join(module, "plugin.qmltypes"), "--import-name", "Hostd.Shell",
                    "--major-version", "1", "--minor-version", "0", dump], check=True, capture_output=True)
    with open(os.path.join(module, "qmldir"), "w") as f:
        f.write("module Hostd.Shell\ntypeinfo plugin.qmltypes\n")
    return root


class Lint(unittest.TestCase):
    @unittest.skipUnless(os.path.exists(QMLLINT), "qmllint is not installed (qt6-declarative-dev-tools)")
    def test_no_warnings(self):
        qml = os.path.join(SHELL, "qml")
        files = sorted(glob.glob(os.path.join(qml, "Hostd", "*.qml")))
        out = subprocess.run([QMLLINT, "--max-warnings", "0", "-I", qml, *files], capture_output=True, text=True,
                             env={**os.environ, "LANG": "C.UTF-8"})
        warnings = [line for line in (out.stdout + out.stderr).splitlines() if line.startswith("Warning")]
        self.assertEqual((out.returncode, warnings), (0, []), out.stdout + out.stderr)


QML_USING_POINTERS = """import QtQuick
import Hostd.Shell

Item {
    id: root
    required property Pointers pointers
    property var rows: root.pointers.%s
    Connections {
        target: root.pointers
        function onPressed(device) { console.log(device) }
    }
}
"""


@unittest.skipUnless(DUMP and REGISTRAR and os.path.exists(QMLLINT), "pyside6 tools are not installed (pyside6-tools)")
class TypedObjects(unittest.TestCase):
    """qmllint knows the Python objects QML binds to, so a misspelt member
    is a warning here and not a blank on the screen."""

    @classmethod
    def setUpClass(cls):
        cls.imports = typed_imports()

    def lint(self, member):
        qml = tempfile.mkdtemp()
        path = os.path.join(qml, "Probe.qml")
        with open(path, "w") as f:
            f.write(QML_USING_POINTERS % member)
        out = subprocess.run([QMLLINT, "-I", self.imports, path], capture_output=True, text=True,
                             env={**os.environ, "LANG": "C.UTF-8"})
        return [line for line in (out.stdout + out.stderr).splitlines() if line.startswith(("Warning", "Error"))]

    def test_a_real_member_is_known(self):
        for member in ("hovers", "strip", "pointer", "calibrating", "pointerIds"):
            self.assertEqual(self.lint(member), [], member)

    def test_a_misspelt_member_is_a_warning(self):
        warnings = self.lint("hovrs")
        self.assertEqual(len(warnings), 1, warnings)
        self.assertIn('Member "hovrs" not found on type "Pointers"', warnings[0])


if __name__ == "__main__":
    unittest.main()
