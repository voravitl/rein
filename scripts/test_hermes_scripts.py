"""Hermes doctor discovery uses only a version probe in a disposable home."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


class HermesDoctorTests(unittest.TestCase):
    def test_hermes_version_probe_is_optional_and_never_calls_a_model(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            log = root / 'hermes-calls'
            for name in ('claude', 'rein', 'npm'):
                cli = root / name
                cli.write_text('#!/bin/sh\necho test-version\n')
                cli.chmod(0o755)
            hermes = root / 'hermes'
            hermes.write_text('#!/bin/sh\nprintf "%s\\n" "$*" >> "$HERMES_TEST_CALLS"\n'
                              '[ "$*" = version ] || exit 23\necho Hermes-test\n')
            hermes.chmod(0o755)
            env = dict(os.environ, HOME=str(root), PATH=str(root) + ':/usr/bin:/bin',
                       HERMES_TEST_CALLS=str(log))
            env.pop('CLAUDE_PLUGIN_DATA', None)
            script = Path(__file__).with_name('doctor.sh')
            result = subprocess.run(['sh', str(script)], env=env, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertEqual(log.read_text(), 'version\n')
            self.assertRegex(result.stdout, r'ok\s+hermes\s+Hermes-test')
            hermes.unlink()
            result = subprocess.run(['sh', str(script)], env=env, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertRegex(result.stdout, r'MISSING\s+hermes\s+-')
            self.assertFalse((root / '.hermes').exists(), 'doctor must not create a global Hermes home')


if __name__ == '__main__':
    unittest.main()
