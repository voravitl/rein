"""Isolated cleanup regressions: subprocess is always replaced by fake Orca."""
import contextlib
import io
import json
import os
from pathlib import Path
import runpy
import subprocess
import sys
import unittest
from unittest.mock import patch

SCRIPT = Path(__file__).with_name('orca_cleanup.py')


def worker(dispatch='ctx_one', verdict='exited', action='worker-release', **fields):
    return dict(dispatchId=dispatch, dispatchStatus=fields.pop('dispatchStatus', 'succeeded'),
                agentTerminalHandle='term_reused', terminalState='reclaimable',
                projection={'liveness': {'verdict': verdict}, 'nextAction': {
                    'argv': ['orchestration', action, '--dispatch', dispatch]}}, **fields)


def page(workers, more=False, cursor=None):
    return {'ok': True, 'result': {'workers': workers,
                                  'page': {'hasMore': more, 'nextCursor': cursor}}}


class CleanupTests(unittest.TestCase):
    def execute(self, responses, *args, env=None, platform='darwin'):
        calls = []
        responses = iter(responses)

        def fake(command, **kwargs):
            calls.append(command)
            value = next(responses)
            if isinstance(value, Exception):
                raise value
            if isinstance(value, subprocess.CompletedProcess):
                return value
            return subprocess.CompletedProcess(command, 0, json.dumps(value), '')

        output = io.StringIO()
        with patch.object(sys, 'argv', [str(SCRIPT), 'run_one', *args]), \
             patch.object(sys, 'platform', platform), \
             patch.dict(os.environ, env or {}, clear=True), \
             patch('subprocess.run', side_effect=fake), \
             contextlib.redirect_stdout(output), contextlib.redirect_stderr(output):
            try:
                runpy.run_path(str(SCRIPT), run_name='__main__')
                code = 0
            except SystemExit as error:
                code = 0 if error.code in (None, 0) else 1
                output.write(str(error.code))
        self.assertFalse(any(c[1:3] == ['terminal', 'close'] for c in calls), calls)
        return code, calls, output.getvalue()

    def test_release_failure_never_closes(self):
        code, calls, _ = self.execute([page([worker()]), {'ok': False, 'error': {'message': 'no'}}])
        self.assertEqual(code, 1)
        self.assertEqual(len(calls), 2)

    def test_retained_reused_and_uncertain_receipts_never_close(self):
        for state in ('retained', 'released', 'release_pending', 'release_unknown'):
            with self.subTest(state=state):
                receipt = {'ok': True, 'result': {'releaseState': state,
                                                'nextAction': {'argv': ['orchestration', 'worker-show']}}}
                code, calls, _ = self.execute([page([worker()]), receipt])
                self.assertEqual(code, 0)
                self.assertEqual(len(calls), 2)

    def test_failed_mutation_response_stops_without_fallback(self):
        for response in [[], {'ok': True, 'error': {'message': 'bad'}},
                         subprocess.CompletedProcess([], 2, '{"ok": true}', 'denied'),
                         subprocess.CompletedProcess([], 0, 'broken', '')]:
            with self.subTest(response=response):
                code, calls, _ = self.execute([page([worker()]), response])
                self.assertEqual(code, 1)
                self.assertEqual(len(calls), 2)

    def test_refused_stop_prevents_other_mutations(self):
        code, calls, _ = self.execute([page([worker(), worker('ctx_active', 'live', 'worker-stop')])],
                                      '--stop', 'ctx_active')
        self.assertEqual(code, 1)
        self.assertEqual(len(calls), 1)

    def test_missing_projection_evidence_preserved(self):
        row = worker()
        row['projection'] = {}
        code, calls, _ = self.execute([page([row])])
        self.assertEqual(code, 0)
        self.assertEqual(len(calls), 1)

    def test_all_pages_before_mutation(self):
        code, calls, _ = self.execute([page([worker()], True, 'opaque'),
                                      page([worker('ctx_two', 'live', 'worker-show')]),
                                      {'ok': True}])
        self.assertEqual(code, 0)
        self.assertEqual([c[2] for c in calls], ['worker-list', 'worker-list', 'worker-release'])
        self.assertIn('--include-remote', calls[0])
        self.assertIn('opaque', calls[1])

    def test_active_and_unverifiable_preserved(self):
        for verdict in ('live', 'unverifiable', None):
            with self.subTest(verdict=verdict):
                code, calls, _ = self.execute([page([worker(verdict=verdict, dispatchStatus='dispatched')])])
                self.assertEqual(code, 0)
                self.assertEqual(len(calls), 1)

    def test_live_settled_release(self):
        for status, outcome in [('succeeded', None), ('failed', None),
                                ('completed', 'succeeded'), ('dispatched', 'failed')]:
            with self.subTest(status=status, outcome=outcome):
                row = worker(verdict='live', dispatchStatus=status)
                row['projection']['outcome'] = outcome
                code, calls, _ = self.execute([page([row]), {'ok': True}])
                self.assertEqual(code, 0)
                self.assertEqual(calls[1][1:], ['orchestration', 'worker-release', '--dispatch', 'ctx_one', '--json'])

    def test_exited_without_settlement_preserved(self):
        for status in ('dispatched', 'completed', None):
            with self.subTest(status=status):
                row = worker(dispatchStatus=status)
                row['projection']['outcome'] = 'in_progress'
                code, calls, _ = self.execute([page([row])])
                self.assertEqual(code, 0)
                self.assertEqual(len(calls), 1)

    def test_settled_without_exact_release_action_preserved(self):
        for action in ('worker-read', 'worker-stop'):
            with self.subTest(action=action):
                code, calls, _ = self.execute([page([worker(verdict='live', action=action)])])
                self.assertEqual(code, 0)
                self.assertEqual(len(calls), 1)

    def test_dry_run_has_no_mutations(self):
        code, calls, _ = self.execute([page([worker()])], '--dry-run')
        self.assertEqual(code, 0)
        self.assertEqual(len(calls), 1)

    def test_stop_requires_exit_and_exact_action(self):
        for verdict, action in [('live', 'worker-stop'), ('unverifiable', 'worker-stop'),
                                (None, 'worker-stop'), ('exited', 'worker-read'),
                                ('exited', 'worker-release')]:
            with self.subTest(verdict=verdict, action=action):
                code, calls, output = self.execute([page([worker(verdict=verdict, action=action)])], '--stop', 'ctx_one')
                self.assertEqual(code, 1)
                self.assertEqual(len(calls), 1)
                self.assertIn('refus', output.lower())

    def test_proven_stop_only(self):
        code, calls, _ = self.execute([page([worker(action='worker-stop')]), {'ok': True}], '--stop', 'ctx_one')
        self.assertEqual(code, 0)
        self.assertEqual(calls[1][1:], ['orchestration', 'worker-stop', '--dispatch', 'ctx_one', '--json'])

    def test_unknown_stop_refused(self):
        code, calls, _ = self.execute([page([])], '--stop', 'missing')
        self.assertEqual(code, 1)
        self.assertEqual(len(calls), 1)

    def test_failed_or_malformed_cli_fails_closed(self):
        for response in [[], None, {'ok': 'true'}, {'ok': True, 'error': {'message': 'bad'}},
                         {'ok': False}, {'ok': True, 'result': {}},
                         subprocess.CompletedProcess([], 1, json.dumps(page([worker()])), 'failed'),
                         subprocess.CompletedProcess([], 0, 'not json', ''), OSError('missing')]:
            with self.subTest(response=response):
                code, calls, _ = self.execute([response])
                self.assertEqual(code, 1)
                self.assertEqual(len(calls), 1)

    def test_bad_later_page_prevents_all_mutations(self):
        for tail in [page([], True, 'same'), page([], True, None),
                     {'ok': False}, page([{'dispatchId': 'ctx_two'}]),
                     page([worker('ctx_one')])]:
            with self.subTest(tail=tail):
                code, calls, _ = self.execute([page([worker()], True, 'same'), tail])
                self.assertEqual(code, 1)
                self.assertEqual(len(calls), 2)

    def test_wrong_dispatch_action_preserved(self):
        row = worker()
        row['projection']['nextAction']['argv'][-1] = 'ctx_other'
        code, calls, _ = self.execute([page([row])])
        self.assertEqual(code, 0)
        self.assertEqual(len(calls), 1)

    def test_resolves_one_binary_for_whole_session(self):
        for env, platform, binary in [({'ORCA_CLI_COMMAND': '/custom/orca', 'ORCA_DEV_REPO_ROOT': '/dev'}, 'linux', '/custom/orca'),
                                      ({'ORCA_DEV_REPO_ROOT': '/dev'}, 'darwin', 'orca-dev'),
                                      ({}, 'linux', 'orca-ide'), ({}, 'darwin', 'orca')]:
            with self.subTest(binary=binary):
                code, calls, _ = self.execute([page([worker()]), {'ok': True}], env=env, platform=platform)
                self.assertEqual(code, 0)
                self.assertTrue(all(c[0] == binary for c in calls))


if __name__ == '__main__':
    unittest.main()
