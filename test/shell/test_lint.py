"""qmllint over the shell's QML: no warnings. With a typed Theme and the
shell passed as a property (no context properties), qmllint checks every
token and member use, which catches what the screen would only show as a
missing element (a name shadowed by a property, a token misspelt)."""

import glob
import os
import shutil
import subprocess
import unittest

from harness import SHELL

QMLLINT = shutil.which("qmllint") or "/usr/lib/qt6/bin/qmllint"


class Lint(unittest.TestCase):
    @unittest.skipUnless(os.path.exists(QMLLINT), "qmllint is not installed (qt6-declarative-dev-tools)")
    def test_no_warnings(self):
        qml = os.path.join(SHELL, "qml")
        files = sorted(glob.glob(os.path.join(qml, "Hostd", "*.qml")))
        out = subprocess.run([QMLLINT, "--max-warnings", "0", "-I", qml, *files], capture_output=True, text=True,
                             env={**os.environ, "LANG": "C.UTF-8"})
        warnings = [line for line in (out.stdout + out.stderr).splitlines() if line.startswith("Warning")]
        self.assertEqual((out.returncode, warnings), (0, []), out.stdout + out.stderr)


if __name__ == "__main__":
    unittest.main()
