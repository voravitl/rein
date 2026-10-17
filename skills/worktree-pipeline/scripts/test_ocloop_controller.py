import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


class WaiterControllerTest(unittest.TestCase):
    def test_controller_refusal_never_falls_back_to_raw_orca(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            rein = root / "rein"
            rein.write_text('#!/bin/sh\nprintf "%s\\n" "$@" > "$CAPTURE"\necho "supervised run refused" >&2\nexit 1\n')
            rein.chmod(0o700)
            orca = root / "orca"
            orca.write_text('#!/bin/sh\ntouch "$RAW_CALLED"\n')
            orca.chmod(0o700)
            env = dict(os.environ, REIN=str(rein), ORCA_REPO_DIR=tmp,
                       PIPELINE_LOGDIR=tmp, CAPTURE=str(root / "args"),
                       RAW_CALLED=str(root / "raw"), PATH=tmp + os.pathsep + os.environ["PATH"])
            script = Path(__file__).with_name("ocloop2.py")
            result = subprocess.run([sys.executable, str(script), "r", "2"],
                                    env=env, text=True, capture_output=True, timeout=5)
            self.assertEqual(result.returncode, 1)
            self.assertIn("supervised run refused", result.stderr)
            self.assertEqual((root / "args").read_text().splitlines()[:6],
                             ["run", "controller-exec", tmp, "--run", "r", "--"])
            self.assertFalse((root / "raw").exists())


if __name__ == "__main__":
    unittest.main()
