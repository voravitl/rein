"""Exercise the real merge helper with local fake processes; never contacts GitLab."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

SCRIPT = Path(__file__).resolve().parents[1] / 'skills/worktree-pipeline/scripts/push_merge_pinned.sh'


class MergeGates(unittest.TestCase):
    def run_helper(self, gate='', no_merge=False, merge_exit=0, api_failure='',
                   merged_sha='a' * 40, ancestor_exit=0, merge_state='merged'):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            log = root / 'calls'
            scripts = {
                # All children are local fixtures; subprocess.run bounds the whole test.
                'timeout': 'shift; exec "$@"',
                'git': '''printf 'git %s\\n' "$*" >> "$CALL_LOG"
case "$1" in
rev-parse) echo aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa;;
remote) echo https://example.test/repo;;
merge-base) exit "$ANCESTOR_EXIT";;
ls-remote) echo aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa;;
esac''',
                'curl': 'exit 0',
                'sleep': 'exit 0',
                'rein': '''printf 'rein %s\\n' "$*" >> "$CALL_LOG"
case "$*" in
verdict*) [ "$FAIL_GATE" != verdict ];;
run*) [ "$FAIL_GATE" != audit ];;
esac''',
                'glab': '''printf 'glab %s\\n' "$*" >> "$CALL_LOG"
if [ "$1" = mr ]; then touch "$MERGED"; exit "$MERGE_EXIT"; fi
phase=before; [ ! -f "$MERGED" ] || phase=after
if [ "$API_FAILURE" = "$phase-error" ] && [ ! -f "$MERGED.$phase" ]; then
 touch "$MERGED.$phase"; exit 1
fi
if [ "$API_FAILURE" = "$phase-json" ] && [ ! -f "$MERGED.$phase" ]; then
 touch "$MERGED.$phase"; echo '{}'; exit 0
fi
if [ -f "$MERGED" ]; then
 printf '{"state":"%s","sha":"%s","merge_commit_sha":"bbbb","target_branch":"main"}\\n' "$MERGE_STATE" "$MERGED_SHA"
else
 echo '{"state":"opened","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","detailed_merge_status":"mergeable","target_branch":"main"}'
fi''',
            }
            for name, body in scripts.items():
                path = root / name
                path.write_text('#!/bin/sh\n' + body + '\n')
                path.chmod(0o755)
            env = {**os.environ, 'PATH': str(root) + os.pathsep + os.environ['PATH'],
                   'CALL_LOG': str(log), 'FAIL_GATE': gate, 'MERGED': str(root / 'merged'),
                   'MERGE_EXIT': str(merge_exit), 'API_FAILURE': api_failure,
                   'MERGED_SHA': merged_sha, 'ANCESTOR_EXIT': str(ancestor_exit),
                   'MERGE_STATE': merge_state}
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

    def test_nonzero_merge_still_verifies_remote_state(self):
        result, calls = self.run_helper(merge_exit=1)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('git merge-base --is-ancestor bbbb origin/main', calls)

    def test_transient_api_and_missing_json_fields_are_retried(self):
        for failure in ['before-error', 'after-error', 'before-json', 'after-json']:
            with self.subTest(failure=failure):
                result, calls = self.run_helper(api_failure=failure)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertEqual(calls.count('glab api'), 3)
                self.assertIn('git merge-base --is-ancestor bbbb origin/main', calls)

    def test_nonzero_merge_does_not_bypass_head_or_ancestor_checks(self):
        for options in [{'merged_sha': 'c' * 40}, {'ancestor_exit': 1}]:
            with self.subTest(options=options):
                result, _ = self.run_helper(merge_exit=1, **options)
                self.assertEqual(result.returncode, 8, result.stdout + result.stderr)

    def test_nonzero_merge_without_remote_confirmation_stops_train(self):
        result, calls = self.run_helper(merge_exit=1, merge_state='opened')
        self.assertEqual(result.returncode, 7, result.stdout + result.stderr)
        self.assertIn('stop the train', result.stdout)
        self.assertNotIn('git merge-base', calls)
