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
import tempfile
from unittest.mock import patch

SCRIPT = Path(__file__).with_name('orca_cleanup.py')


def worker(dispatch='ctx_one', verdict='exited', action='worker-release', **fields):
    return dict(dispatchId=dispatch, dispatchStatus=fields.pop('dispatchStatus', 'succeeded'),
                agentTerminalHandle='term_reused', terminalState='reclaimable',
                projection={'liveness': {'verdict': verdict}, 'nextAction': {
                    'argv': ['orchestration', action, '--dispatch', dispatch]}}, **fields)


def page(workers, more=False, cursor=None):
    return {'ok': True, 'result': {'workers': workers, 'scope': {'source': 'flag', 'run': 'run_one'},
                                  'page': {'hasMore': more, 'nextCursor': cursor}}}


def released_page(dispatch="ctx_one", state="released"):
    row = worker(dispatch)
    row["terminalState"] = state
    return page([row])


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
            if isinstance(value, dict):
                value.setdefault('_meta', {'runtimeId': 'runtime_one'})
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
        if '--close-operator' not in args:
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
                code, calls, _ = self.execute([page([worker()]), receipt, released_page(state=state)])
                self.assertEqual(code, 0 if state == "released" else 1)
                self.assertEqual(len(calls), 3)

    def test_release_requires_fresh_positive_resource_confirmation(self):
        for final in (page([]), page([worker()]), {'ok': False}, {'ok': True, 'result': {}}):
            with self.subTest(final=final):
                code, calls, output = self.execute([page([worker()]), {'ok': True}, final])
                self.assertEqual(code, 1)
                self.assertEqual(len(calls), 3)

    def test_retry_cannot_clear_unresolved_finished_session(self):
        for state in ('retained', 'release_pending', 'release_unknown', None):
            with self.subTest(state=state):
                row = worker(action='worker-show')
                row['terminalState'] = state
                code, calls, _ = self.execute([page([row])])
                self.assertEqual(code, 1)
                self.assertEqual(len(calls), 1)

    def test_retry_released_and_operator_owned_sessions_are_not_force_closed(self):
        for state, mode in [('released', 'succeeded'), ('retained', 'unsupervised')]:
            row = worker(action='worker-show')
            row.update(terminalState=state, workerState=mode)
            code, calls, _ = self.execute([page([row])])
            self.assertEqual(code, 0)
            self.assertEqual(len(calls), 1)

    def test_unresolved_retry_dry_run_does_not_mutate(self):
        row = worker(action='worker-show')
        row['terminalState'] = 'release_pending'
        code, calls, _ = self.execute([page([row])], '--dry-run')
        self.assertEqual(code, 0)
        self.assertEqual(len(calls), 1)

    def test_release_postcheck_completes_pagination(self):
        code, calls, _ = self.execute([page([worker()]), {'ok': True},
                                      page([], True, 'after'), released_page()])
        self.assertEqual(code, 0)
        self.assertEqual(len(calls), 4)
        self.assertIn('after', calls[-1])

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

    def test_missing_projection_evidence_fails_without_mutation(self):
        row = worker()
        row['projection'] = {}
        code, calls, _ = self.execute([page([row])])
        self.assertEqual(code, 1)
        self.assertEqual(len(calls), 1)

    def test_all_pages_before_mutation(self):
        code, calls, _ = self.execute([page([worker()], True, 'opaque'),
                                      page([worker('ctx_two', 'live', 'worker-show', dispatchStatus='dispatched')]),
                                      {'ok': True}, released_page()])
        self.assertEqual(code, 0)
        self.assertEqual([c[2] for c in calls], ['worker-list', 'worker-list', 'worker-release', 'worker-list'])
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
                code, calls, _ = self.execute([page([row]), {'ok': True}, released_page()])
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
                self.assertEqual(code, 1)
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
        code, calls, _ = self.execute([page([worker(action='worker-stop')]), {'ok': True},
                                      released_page()], '--stop', 'ctx_one')
        self.assertEqual(code, 0)
        self.assertEqual(calls[1][1:], ['orchestration', 'worker-stop', '--dispatch', 'ctx_one', '--json'])
        self.assertEqual(len(calls), 3)

    def test_stopped_session_must_be_released_before_success(self):
        for post in (page([]), page([worker(action='worker-show')]),
                     page([worker(dispatchStatus='dispatched')]), {'ok': False}):
            with self.subTest(post=post):
                code, calls, _ = self.execute([page([worker(action='worker-stop')]), {'ok': True}, post],
                                              '--stop', 'ctx_one')
                self.assertEqual(code, 1)
                self.assertEqual(len(calls), 3)

    def test_stop_unknown_state_fails(self):
        code, _, _ = self.execute([page([worker(action='worker-stop')]), {'ok': True},
                                  released_page(state='release_unknown')], '--stop', 'ctx_one')
        self.assertEqual(code, 1)

    def test_finished_worktree_session_gate(self):
        for terminals in ([], [{'handle': 'term_operator'}]):
            code, calls, _ = self.execute([page([]), {'ok': True, 'result': {
                'terminals': terminals, 'truncated': False, 'totalCount': len(terminals),
                'hostScope': {'hostIds': ['local'], 'omittedHostIds': []}}}],
                '--check-worktree', 'id:repo::/finished')
            self.assertEqual(code, 0 if not terminals else 1)
            self.assertEqual(calls[-1][1:], ['terminal', 'list', '--worktree', 'id:repo::/finished', '--json'])

    def test_unverifiable_worktree_session_gate_fails(self):
        for result in ({}, {'terminals': []}, {'terminals': [], 'truncated': True},
                       {'terminals': [], 'truncated': False, 'totalCount': 0,
                        'hostScope': {'hostIds': [], 'omittedHostIds': ['remote']}},
                       {'terminals': [], 'truncated': False, 'totalCount': 0,
                        'hostScope': {'hostIds': ['remote'], 'omittedHostIds': []}}):
            code, _, _ = self.execute([page([]), {'ok': True, 'result': result}],
                                      '--check-worktree', 'id:repo::/finished')
            self.assertEqual(code, 1)

    def operator_case(self, change=None, close=True, remaining=False, main=False,
                      adopted=False, coordinator=False, activity=False, bad_close=False, dry_run=False, bad_runs=None, wrong_scope=False):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            identity = dict(handle='term_operator', incarnationId='inc_one', executionHostId='local',
                            worktreeId='repo::/finished', ptyId='repo::/finished@@pty')
            launch = {'ok': True, '_meta': {'runtimeId': 'runtime_one'},
                      'result': {'terminal': identity}}
            snapshot = json.loads(json.dumps(launch))
            snapshot['result']['terminal']['lastOutputAt'] = 42
            current = json.loads(json.dumps(snapshot))
            if change:
                current['result']['terminal'].update(change)
            for name, value in [('launch.json', launch), ('snapshot.json', snapshot)]:
                (root/name).write_text(json.dumps(value))
            (root/'report.md').write_text('Final report archived and verified by coordinator.')
            runs = {'ok': True, '_meta': {'runtimeId': 'runtime_one'}, 'result': {
                'runs': [{'id': 'run_one', 'coordinator_handle': 'term_operator' if coordinator else None},
                         {'id': 'run_other', 'coordinator_handle': None}], 'nextCursor': None}}
            owners = page([])
            owners['_meta'] = {'runtimeId': 'runtime_one'}
            owners['result']['scope'] = {'source': 'flag', 'run': 'run_one'}
            if bad_runs is not None:
                runs['result'] = bad_runs
            if wrong_scope:
                owners['result']['scope']['source'] = 'bound'
            other = page([dict(worker(), agentTerminalHandle='term_operator')] if adopted else [])
            other['_meta'] = {'runtimeId': 'runtime_one'}
            other['result']['scope'] = {'source': 'flag', 'run': 'run_other'}
            responses = [page([]), runs, owners, other, {'ok': True, 'result': {'worktree': {
                'id': identity['worktreeId'], 'isMainWorktree': main}}}, current]
            if close:
                recheck = json.loads(json.dumps(current))
                if activity:
                    recheck['result']['terminal']['lastOutputAt'] = 43
                responses += [runs, owners, other, recheck, {'ok': True, '_meta': {'runtimeId': 'runtime_one'}, 'result': {'close': {
                    'handle': identity['handle'], 'ptyKilled': not bad_close}}}, {'ok': True, 'result': {
                        'terminals': [{'handle': 'term_operator'}] if remaining else [],
                        'truncated': False, 'totalCount': 1 if remaining else 0,
                        'hostScope': {'hostIds': ['local'], 'omittedHostIds': []}}}]
            return self.execute(responses, '--close-operator', str(root/'launch.json'),
                                str(root/'snapshot.json'), str(root/'report.md'), *(['--dry-run'] if dry_run else []))

    def test_operator_completed_exact_identity_closes(self):
        code, calls, _ = self.operator_case()
        self.assertEqual(code, 0)
        self.assertEqual(calls[-2][1:], ['terminal', 'close', '--terminal', 'term_operator', '--json'])

    def test_operator_reused_identity_or_new_activity_preserved(self):
        for change in ({'incarnationId': 'new'}, {'executionHostId': 'remote'},
                       {'worktreeId': 'another'}, {'ptyId': 'new'}, {'lastOutputAt': 43}):
            code, calls, _ = self.operator_case(change, close=False)
            self.assertEqual(code, 1)
            self.assertFalse(any(c[1:3] == ['terminal', 'close'] for c in calls))

    def test_main_operator_session_cannot_close(self):
        code, calls, _ = self.operator_case(close=False, main=True)
        self.assertEqual(code, 1)
        self.assertFalse(any(c[1:3] == ['terminal', 'close'] for c in calls))

    def test_operator_close_requires_fresh_absence(self):
        code, _, _ = self.operator_case(remaining=True)
        self.assertEqual(code, 1)

    def test_operator_adopted_by_other_run_is_preserved(self):
        code, calls, _ = self.operator_case(adopted=True, close=False)
        self.assertEqual(code, 1)
        self.assertFalse(any(c[1:3] == ['terminal', 'close'] for c in calls))
        self.assertTrue(any('run_other' in c for c in calls))

    def test_operator_coordinator_in_feature_worktree_is_preserved(self):
        code, calls, _ = self.operator_case(coordinator=True, close=False)
        self.assertEqual(code, 1)
        self.assertFalse(any(c[1:3] == ['terminal', 'close'] for c in calls))

    def test_operator_recheck_detects_new_activity(self):
        code, calls, _ = self.operator_case(activity=True)
        self.assertEqual(code, 1)
        self.assertFalse(any(c[1:3] == ['terminal', 'close'] for c in calls))

    def test_operator_no_positive_pty_receipt_fails(self):
        code, _, _ = self.operator_case(bad_close=True)
        self.assertEqual(code, 1)

    def test_operator_dry_run_does_not_close(self):
        code, calls, _ = self.operator_case(dry_run=True)
        self.assertEqual(code, 0)
        self.assertFalse(any(c[1:3] == ['terminal', 'close'] for c in calls))

    def test_operator_bound_scope_is_not_global_ownership_proof(self):
        code, calls, _ = self.operator_case(wrong_scope=True, close=False)
        self.assertEqual(code, 1)
        self.assertFalse(any(c[1:3] == ['terminal', 'close'] for c in calls))

    def test_operator_incomplete_run_inventory_is_preserved(self):
        for result in ({}, {'runs': []}, {'runs': [], 'nextCursor': None},
                       {'runs': [{'id': 'run_one'}], 'nextCursor': None},
                       {'runs': [], 'nextCursor': True}):
            with self.subTest(result=result):
                code, calls, _ = self.operator_case(bad_runs=result, close=False)
                self.assertEqual(code, 1)
                self.assertFalse(any(c[1:3] == ['terminal', 'close'] for c in calls))

    def test_runtime_missing_or_changed_never_confirms_closure(self):
        for meta in ({}, {'runtimeId': 'new_runtime'}, None):
            result = {'ok': True, '_meta': meta, 'result': {
                'terminals': [], 'totalCount': 0, 'truncated': False,
                'hostScope': {'hostIds': ['local'], 'omittedHostIds': []}}}
            code, calls, _ = self.execute([page([]), result], '--check-worktree', 'id:repo::/finished')
            self.assertEqual(code, 1)
            self.assertEqual(len(calls), 2)

    def test_initial_missing_runtime_fails_before_mutation(self):
        value = page([worker()])
        value['_meta'] = {}
        code, calls, _ = self.execute([value])
        self.assertEqual(code, 1)
        self.assertEqual(len(calls), 1)

    def test_wrong_initial_run_scope_never_mutates(self):
        value = page([worker()])
        value['result']['scope'] = {'source': 'bound', 'run': 'run_other'}
        code, calls, _ = self.execute([value])
        self.assertEqual(code, 1)
        self.assertEqual(len(calls), 1)

    def test_wrong_post_cleanup_run_scope_fails(self):
        value = released_page()
        value['result']['scope'] = {'source': 'flag', 'run': 'run_other'}
        code, _, _ = self.execute([page([worker()]), {'ok': True}, value])
        self.assertEqual(code, 1)

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
        self.assertEqual(code, 1)
        self.assertEqual(len(calls), 1)

    def test_resolves_one_binary_for_whole_session(self):
        for env, platform, binary in [({'ORCA_CLI_COMMAND': '/custom/orca', 'ORCA_DEV_REPO_ROOT': '/dev'}, 'linux', '/custom/orca'),
                                      ({'ORCA_DEV_REPO_ROOT': '/dev'}, 'darwin', 'orca-dev'),
                                      ({}, 'linux', 'orca-ide'), ({}, 'darwin', 'orca')]:
            with self.subTest(binary=binary):
                code, calls, _ = self.execute([page([worker()]), {'ok': True}, released_page()], env=env, platform=platform)
                self.assertEqual(code, 0)
                self.assertTrue(all(c[0] == binary for c in calls))


if __name__ == '__main__':
    unittest.main()
