import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

SCRIPT = Path(__file__).with_name('cleanup_worktrees.py')

class CleanupTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.main = self.root / 'main'
        self.main.mkdir()
        self.git('init', '-q', cwd=self.main)
        self.git('config', 'user.email', 'test@example.com')
        self.git('config', 'user.name', 'Test')
        (self.main / 'file').write_text('base')
        self.git('add', '.')
        self.git('commit', '-qm', 'base')
        self.git('remote', 'add', 'origin', str(self.main))
        self.git('fetch', 'origin', '-q')
        self.git('update-ref', 'refs/remotes/origin/main', self.git('rev-parse', 'HEAD').stdout.strip())
        self.worker = self.root / 'workers' / 'done'
        self.git('worktree', 'add', '-qb', 'done', str(self.worker))
        self.row = dict(id='repo::worker', identity=dict(key='wt2:local:instance', instanceId='instance', executionHostId='local'), instanceId='instance', hostId='local', repoId='repo', path=str(self.worker), isMainWorktree=False, childWorktreeIds=[], workspaceStatus='completed', linkedPR=None, linkedGitLabMR=None, head=self.git('rev-parse', 'HEAD').stdout.strip())
        self.calls = []
        spec = importlib.util.spec_from_file_location('cleanup', SCRIPT)
        self.module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.module)
    def tearDown(self): self.tmp.cleanup()
    def git(self, *args, cwd=None):
        return subprocess.run(['git', *args], cwd=cwd or self.main, capture_output=True, text=True, check=True, env={**os.environ, 'GIT_CONFIG_COUNT':'1', 'GIT_CONFIG_KEY_0':'core.hooksPath', 'GIT_CONFIG_VALUE_0':'/dev/null'})
    def orca(self, *args):
        self.calls.append(args)
        if args[:2] == ('orchestration', 'run-list'): return dict(runs=[], nextCursor=None)
        if args[:2] == ('worktree', 'list'): return dict(worktrees=[] if not self.worker.exists() else [self.row], totalCount=0 if not self.worker.exists() else 1, truncated=False, hostScope=dict(hostIds=['local'], omittedHostIds=[]))
        if args[:2] == ('worktree', 'ps'):
            return dict(worktrees=[{**self.row, 'worktreeId':self.row['id'], 'worktreeInstanceId':self.row['instanceId'], 'isActive':False, 'hasAttachedPty':False, 'liveTerminalCount':0, 'agents':[]}], totalCount=1, truncated=False, hostScope=dict(hostIds=['local'], omittedHostIds=[]))
        if args[:2] == ('worktree', 'show'): return dict(worktree=self.row)
        if args[:2] == ('terminal', 'list'): return dict(terminals=[], totalCount=0, truncated=False, hostScope=dict(hostIds=['local'], omittedHostIds=[]))
        if args[:2] == ('worktree', 'rm'):
            self.git('worktree', 'remove', str(self.worker))
            return dict(removed=True)
        raise AssertionError(args)
    def run_cleanup(self, *extra, fake=None):
        with patch.object(self.module, 'orca', side_effect=fake or self.orca):
            return self.module.main([str(self.main), '--root', str(self.worker.parent), '--keep', 'none', *extra])
    def test_failure_never_falls_back(self):
        def fake(*args):
            if args[:2] == ('worktree', 'rm'): raise RuntimeError('Orca unavailable')
            return self.orca(*args)
        self.assertEqual(self.run_cleanup('--archive-dir', str(self.root/'archive'), fake=fake), 1)
        self.assertTrue(self.worker.exists())
    def test_missing_archive_fails(self):
        self.assertEqual(self.run_cleanup(), 1)
        self.assertTrue(self.worker.exists())
    def test_live_session_blocks(self):
        def fake(*args):
            if args[:2] == ('terminal', 'list'): return dict(terminals=[dict(handle='live')], totalCount=1, truncated=False, hostScope=dict(hostIds=['local'], omittedHostIds=[]))
            return self.orca(*args)
        self.assertEqual(self.run_cleanup('--archive-dir', str(self.root/'archive'), fake=fake), 1)
        self.assertTrue(self.worker.exists())
    def test_false_receipt_fails(self):
        def fake(*args):
            if args[:2] == ('worktree', 'rm'): return dict(removed=True)
            return self.orca(*args)
        self.assertEqual(self.run_cleanup('--archive-dir', str(self.root/'archive'), fake=fake), 1)
    def test_success_archive_and_no_force(self):
        archive = self.root / 'archive'
        self.assertEqual(self.run_cleanup('--archive-dir', str(archive)), 0)
        self.assertFalse(self.worker.exists())
        self.assertTrue(list(archive.glob('*/history.bundle')))
        self.assertNotIn('--force', [a for call in self.calls for a in call])
        self.git('show-ref', '--verify', 'refs/heads/done')
    def test_dryrun_no_archive_or_removal(self):
        self.assertEqual(self.run_cleanup('--dry-run'), 0)
        self.assertTrue(self.worker.exists())
        self.assertFalse(any(c[:2] == ('worktree','rm') for c in self.calls))
    def test_active_and_linked_work_keep(self):
        for changes in [dict(workspaceStatus='in-review'), dict(linkedPR=dict(number=4)), dict(childWorktreeIds=['child'])]:
            with self.subTest(changes=changes):
                original = self.row.copy(); self.row.update(changes)
                self.assertEqual(self.run_cleanup(), 0)
                self.assertTrue(self.worker.exists()); self.row = original
    def test_ignored_files_archived(self):
        self.git('config', 'core.excludesfile', str(self.root/'exclude'))
        (self.root/'exclude').write_text('cache/\n')
        (self.worker/'cache').mkdir(); (self.worker/'cache'/'data').write_text('retain me')
        archive = self.root/'archive'
        self.assertEqual(self.run_cleanup('--archive-dir', str(archive)), 0)
        import tarfile
        with tarfile.open(next(archive.glob('*/local-files.tar'))) as tar:
            self.assertEqual(tar.extractfile('cache/data').read(), b'retain me')

    def test_ps_discovered_pr_retained(self):
        def fake(*args):
            result = self.orca(*args)
            if args[:2] == ('worktree', 'ps'): result['worktrees'][0]['linkedPR'] = dict(number=4, state='draft')
            return result
        self.assertEqual(self.run_cleanup(fake=fake), 0)
        self.assertTrue(self.worker.exists())
    def test_missing_or_incomplete_runtime_evidence_fails(self):
        for operation in [('worktree', 'list'), ('worktree', 'ps'), ('terminal', 'list')]:
            with self.subTest(operation=operation):
                def fake(*args):
                    result = self.orca(*args)
                    if args[:2] == operation: result['truncated'] = True
                    return result
                self.assertEqual(self.run_cleanup('--dry-run', fake=fake), 1)
                self.assertTrue(self.worker.exists())
    def test_identity_replaced_during_archive_fails(self):
        shows = 0
        def fake(*args):
            nonlocal shows
            result = self.orca(*args)
            if args[:2] == ('worktree', 'show'):
                shows += 1
                if shows == 2: result['worktree'] = {**self.row, 'instanceId':'replacement'}
            return result
        self.assertEqual(self.run_cleanup('--archive-dir', str(self.root/'archive'), fake=fake), 1)
        self.assertTrue(self.worker.exists())
    def test_main_checkout_protected_even_if_mispointed(self):
        self.row['isMainWorktree'] = True
        self.assertEqual(self.run_cleanup(), 0)
        self.assertTrue(self.worker.exists())
    def test_archive_inside_worktree_fails(self):
        self.assertEqual(self.run_cleanup('--archive-dir', str(self.worker/'archive')), 1)
        self.assertTrue(self.worker.exists())
    def test_keep_and_unintegrated_branches_retained(self):
        self.assertEqual(self.run_cleanup('--keep', 'done'), 0)
        (self.worker/'file').write_text('new work')
        self.git('commit', '-qam', 'unmerged', cwd=self.worker)
        self.assertEqual(self.run_cleanup(), 0)
        self.assertTrue(self.worker.exists())
    def test_integrated_exact_sha_removes_preserving_branch(self):
        (self.worker/'file').write_text('integrated work')
        self.git('commit', '-qam', 'feature', cwd=self.worker)
        head = self.git('rev-parse', 'HEAD', cwd=self.worker).stdout.strip()
        self.row['head'] = head
        self.assertEqual(self.run_cleanup('--integrated-into', head, '--archive-dir', str(self.root/'archive')), 0)
        self.git('show-ref', '--verify', 'refs/heads/done')
    def test_drifting_integration_reference_rejected(self):
        self.assertEqual(self.run_cleanup('--integrated-into', 'HEAD'), 1)
        self.assertTrue(self.worker.exists())
    def test_failed_git_probe_is_failed_gate(self):
        original = self.module.git
        def failing(*args, **kwargs):
            if args[0] == 'status': raise RuntimeError('status unavailable')
            return original(*args, **kwargs)
        with patch.object(self.module, 'git', side_effect=failing):
            self.assertEqual(self.run_cleanup(), 1)
        self.assertTrue(self.worker.exists())
    def test_strict_cli_receipts(self):
        for response in [dict(ok=False, result={}), dict(ok=True, result={}), dict(ok=True, result={}, _meta=dict(runtimeId='other'))]:
            with self.subTest(response=response):
                self.module.RUNTIME = 'expected'
                with patch.object(self.module.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, json.dumps(response), '')):
                    with self.assertRaises(RuntimeError): self.module.orca('worktree', 'list')
    def test_local_content_changed_after_archive_fails(self):
        (self.root/'exclude').write_text('cache\n')
        self.git('config', 'core.excludesfile', str(self.root/'exclude'))
        (self.worker/'cache').write_text('old')
        shows = 0
        def fake(*args):
            nonlocal shows
            result = self.orca(*args)
            if args[:2] == ('worktree', 'show'):
                shows += 1
                if shows == 2: (self.worker/'cache').write_text('new')
            return result
        self.assertEqual(self.run_cleanup('--archive-dir', str(self.root/'archive'), fake=fake), 1)
        self.assertTrue(self.worker.exists())

    def test_allowed_untracked_archive_without_force_remains_safe(self):
        (self.worker/'artifact').write_text('user data')
        archive = self.root/'archive'
        def fake(*args):
            if args[:2] == ('worktree', 'rm'):
                result = subprocess.run(['git', 'worktree', 'remove', str(self.worker)], cwd=self.main, capture_output=True, text=True)
                if result.returncode: raise RuntimeError('Git refuses untracked files without force')
            return self.orca(*args)
        self.assertEqual(self.run_cleanup('--ignore', 'artifact', '--archive-dir', str(archive), fake=fake), 1)
        self.assertEqual((self.worker/'artifact').read_text(), 'user data')
        self.assertTrue(list(archive.glob('*/local-files.tar')))
    def test_ps_active_worktree_blocks(self):
        def fake(*args):
            result = self.orca(*args)
            if args[:2] == ('worktree', 'ps'): result['worktrees'][0]['isActive'] = True
            return result
        self.assertEqual(self.run_cleanup('--dry-run', fake=fake), 1)
        self.assertTrue(self.worker.exists())

    def dispatch(self, workspace=None, settled=False, released=False):
        return dict(dispatchId='ctx_one', runId='run_two', dispatchStatus='succeeded' if settled else 'running', workerState='ready', terminalState='released' if released else 'retained', resource=None, projection=dict(workspace=dict(id=workspace) if workspace else None, outcome='succeeded' if settled else None))
    def dispatch_fake(self, worker):
        def fake(*args):
            if args[:2] == ('orchestration', 'run-list'):
                if '--cursor' not in args: return dict(runs=[dict(id='run_one')], nextCursor='page_two')
                return dict(runs=[dict(id='run_two')], nextCursor=None)
            if args[:2] == ('orchestration', 'worker-list'):
                run = args[args.index('--run') + 1]
                workers = [worker] if run == 'run_two' else []
                return dict(workers=workers, page=dict(hasMore=False, nextCursor=None), scope=dict(source='flag', run=run))
            return self.orca(*args)
        return fake
    def test_cross_run_active_dispatch_without_pty_blocks(self):
        worker = self.dispatch(self.row['id'])
        self.assertEqual(self.run_cleanup('--archive-dir', str(self.root/'archive'), fake=self.dispatch_fake(worker)), 1)
        self.assertTrue(self.worker.exists())
    def test_unknown_active_workspace_fails_closed(self):
        self.assertEqual(self.run_cleanup('--dry-run', fake=self.dispatch_fake(self.dispatch())), 1)
    def test_settled_supervised_unreleased_blocks(self):
        self.assertEqual(self.run_cleanup('--dry-run', fake=self.dispatch_fake(self.dispatch(self.row['id'], settled=True))), 1)
    def test_released_cross_run_worker_allows_cleanup(self):
        self.assertEqual(self.run_cleanup('--dry-run', fake=self.dispatch_fake(self.dispatch(self.row['id'], settled=True, released=True))), 0)
    def test_unrelated_known_active_workspace_preserved(self):
        self.assertEqual(self.run_cleanup('--dry-run', fake=self.dispatch_fake(self.dispatch('other-worktree'))), 0)
    def test_run_and_worker_pagination_errors_fail(self):
        for bad in ('run-page', 'worker-scope', 'worker-page'):
            with self.subTest(bad=bad):
                good = self.dispatch_fake(self.dispatch(self.row['id'], settled=True, released=True))
                def fake(*args):
                    result = good(*args)
                    if bad == 'run-page' and args[:2] == ('orchestration','run-list'): result.pop('nextCursor')
                    if bad == 'worker-scope' and args[:2] == ('orchestration','worker-list'): result['scope']['source'] = 'caller'
                    if bad == 'worker-page' and args[:2] == ('orchestration','worker-list'): result['page'] = dict(hasMore=True, nextCursor=None)
                    return result
                self.assertEqual(self.run_cleanup('--dry-run', fake=fake), 1)

    def parent_child(self, active=False):
        child = self.worker.parent/'zchild'
        self.git('worktree', 'add', '-qb', 'child', str(child))
        childrow = {**self.row, 'id':'repo::child', 'path':str(child), 'identity':dict(key='wt2:local:child', instanceId='child', executionHostId='local'), 'instanceId':'child', 'childWorktreeIds':[]}
        self.row['childWorktreeIds'] = [childrow['id']]
        if active: childrow['workspaceStatus'] = 'in-progress'
        def fake(*args):
            rows = [r for r in [self.row, childrow] if Path(r['path']).exists()]
            if args[:2] == ('worktree','list'): return dict(worktrees=json.loads(json.dumps(rows)), totalCount=len(rows), truncated=False, hostScope=dict(hostIds=['local'], omittedHostIds=[]))
            if args[:2] == ('worktree','ps'):
                return dict(worktrees=[{**r, 'worktreeId':r['id'], 'worktreeInstanceId':r['instanceId'], 'isActive':False, 'hasAttachedPty':False, 'liveTerminalCount':0, 'agents':[]} for r in rows], totalCount=len(rows), truncated=False, hostScope=dict(hostIds=['local'], omittedHostIds=[]))
            if args[:2] in [('worktree','show'), ('worktree','rm')]:
                selector = args[args.index('--worktree')+1]
                row = next(r for r in rows if selector == 'identity:' + r['identity']['key'])
                if args[1] == 'show': return dict(worktree=row)
                self.git('worktree','remove',row['path'])
                if row is childrow: self.row['childWorktreeIds'] = []
                return dict(removed=True)
            return self.orca(*args)
        self.assertEqual(self.run_cleanup('--archive-dir', str(self.root/'archive'), fake=fake), 0)
        self.assertEqual(child.exists(), active)
        self.assertEqual(self.worker.exists(), active)
    def test_completed_parent_child_removed_bottom_up(self): self.parent_child()
    def test_active_child_preserves_parent(self): self.parent_child(active=True)

    def test_settled_historical_unsupervised_without_workspace_allows(self):
        worker = self.dispatch()
        worker.update(dispatchStatus='completed', workerState='unsupervised')
        worker['projection']['outcome'] = 'succeeded'
        self.assertEqual(self.run_cleanup('--dry-run', fake=self.dispatch_fake(worker)), 0)
    def test_prior_cleanup_failure_skips_docker(self):
        with patch.object(self.module.subprocess, 'run', wraps=subprocess.run) as run:
            self.assertEqual(self.run_cleanup('--prune-images', 'app-tests'), 1)
            self.assertFalse(any(call.args[0][0] == 'docker' for call in run.call_args_list))

    def test_zero_host_inventory_not_proof(self):
        def fake(*args):
            result = self.orca(*args)
            if args[:2] == ('terminal','list'): result['hostScope']['hostIds'] = []
            return result
        self.assertEqual(self.run_cleanup('--dry-run', fake=fake), 1)
    def test_completed_merged_review_can_remove(self):
        self.row['linkedPR'] = dict(number=4, state='merged')
        self.assertEqual(self.run_cleanup('--archive-dir', str(self.root/'archive')), 0)
        self.assertFalse(self.worker.exists())
    def test_worker_second_page_active_dispatch_blocks(self):
        worker = self.dispatch(self.row['id'])
        good = self.dispatch_fake(worker)
        def fake(*args):
            if args[:2] == ('orchestration','worker-list') and args[args.index('--run')+1] == 'run_two' and '--cursor' not in args:
                return dict(workers=[], page=dict(hasMore=True, nextCursor='active_page'), scope=dict(source='flag', run='run_two'))
            return good(*args)
        self.assertEqual(self.run_cleanup('--dry-run', fake=fake), 1)

if __name__ == '__main__': unittest.main()
