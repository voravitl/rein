"""Exercise the real merge helper with local fake processes; never contacts GitLab."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

SCRIPT = Path(__file__).resolve().parents[1] / 'skills/worktree-pipeline/scripts/push_merge_pinned.sh'


class MergeGates(unittest.TestCase):
    def run_helper(self, gate='', no_merge=False):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            log = root / 'calls'
            scripts = {
                # All children are local fixtures; subprocess.run bounds the whole test.
                'timeout': 'shift; exec "$@"',
                'git': '''case "$1" in
rev-parse) echo aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa;;
remote) echo https://example.test/repo;;
ls-remote) echo aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa;;
esac''',
                'curl': 'exit 0',
                'rein': '''printf 'rein %s\\n' "$*" >> "$CALL_LOG"
case "$*" in
verdict*) [ "$FAIL_GATE" != verdict ];;
run*) [ "$FAIL_GATE" != audit ];;
esac''',
                'glab': '''printf 'glab %s\\n' "$*" >> "$CALL_LOG"
if [ "$1" = mr ]; then touch "$MERGED"; exit 0; fi
if [ -f "$MERGED" ]; then
 echo '{"state":"merged","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","merge_commit_sha":"bbbb","target_branch":"main"}'
else
 echo '{"state":"opened","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","detailed_merge_status":"mergeable","target_branch":"main"}'
fi''',
            }
            for name, body in scripts.items():
                path = root / name
                path.write_text('#!/bin/sh\n' + body + '\n')
                path.chmod(0o755)
            env = {**os.environ, 'PATH': str(root) + os.pathsep + os.environ['PATH'],
                   'CALL_LOG': str(log), 'FAIL_GATE': gate, 'MERGED': str(root / 'merged')}
            args = ['bash', str(SCRIPT), tmp, 'feature', '42']
            if no_merge:
                args.append('--no-merge')
            result = subprocess.run(args, env=env, capture_output=True, text=True, timeout=10)
            return result, log.read_text() if log.exists() else ''

    def test_failing_gates_never_merge(self):
        for gate in ['verdict', 'audit']:
            with self.subTest(gate=gate):
                result, calls = self.run_helper(gate)
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn('glab mr merge', calls)
                self.assertIn('rein verdict check', calls)

    def test_success_rechecks_gates_and_pins_sha(self):
        result, calls = self.run_helper()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(calls.count('rein verdict check'), 2)
        self.assertEqual(calls.count('rein run audit'), 2)
        self.assertIn('--tier T3', calls)
        self.assertIn('glab mr merge 42 --sha aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', calls)
        self.assertNotIn('--pinned', calls)

    def test_push_only_never_merges(self):
        result, calls = self.run_helper('verdict', no_merge=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn('glab mr merge', calls)
