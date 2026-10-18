"""Failure modes of the push latch: hidden sources, untracked sources, git failure.

Each fixture is a disposable directory; git runs with GIT_CONFIG_GLOBAL=/dev/null
and GIT_CONFIG_NOSYSTEM=1 so host ignore files and hooks cannot interfere.
"""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

SCRIPT = Path(__file__).with_name('preflight-push.sh')


class PreflightPushTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.env = dict(os.environ, GIT_CONFIG_GLOBAL='/dev/null', GIT_CONFIG_NOSYSTEM='1')

    def fixture(self, name):
        """Copy the latch into a disposable fixture as scripts/preflight-push.sh."""
        root = self.root / name
        (root / 'scripts').mkdir(parents=True)
        shutil.copyfile(SCRIPT, root / 'scripts/preflight-push.sh')
        return root

    def git(self, root, *args):
        result = subprocess.run(['git', *args], cwd=root, env=self.env,
                                capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        return result

    def repo(self, name):
        """Fixture that is a git repository; the latch itself is a tracked source."""
        root = self.fixture(name)
        self.git(root, 'init', '-q')
        self.git(root, 'config', 'user.email', 'fixture@example.invalid')
        self.git(root, 'config', 'user.name', 'fixture')
        self.git(root, 'config', 'commit.gpgsign', 'false')
        return root

    def commit(self, root):
        self.git(root, 'add', '-A')
        self.git(root, 'commit', '-qm', 'fixture')

    def run_latch(self, root):
        return subprocess.run(['sh', 'scripts/preflight-push.sh'], cwd=root, env=self.env,
                              capture_output=True, text=True)

    def assert_no_success_claim(self, result):
        self.assertNotIn('preflight ok', result.stdout)

    def test_ignored_directory_hiding_a_source_file_fails(self):
        root = self.repo('ignored-dir')
        (root / '.gitignore').write_text('bin/\n')
        self.commit(root)
        (root / 'bin').mkdir()
        (root / 'bin/lost.go').write_text('package main\n')
        (root / 'bin/keep.bin').write_text('artifact\n')
        result = self.run_latch(root)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('hidden by ignore patterns', result.stderr)
        self.assertIn('bin/lost.go', result.stderr)
        self.assert_no_success_claim(result)

    def test_git_failure_outside_a_repository_is_not_masked(self):
        root = self.fixture('no-repo')
        (root / 'hosted.go').write_text('package main\n')
        self.assertNotEqual(subprocess.run(['git', 'rev-parse', '--git-dir'], cwd=root,
                                           env=self.env, capture_output=True).returncode, 0,
                            'fixture must not sit inside a git work tree')
        result = self.run_latch(root)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('git failed (exit', result.stderr)
        self.assert_no_success_claim(result)

    def test_untracked_source_file_never_gets_a_success_claim(self):
        root = self.repo('untracked')
        (root / 'main.go').write_text('package main\n')
        self.commit(root)
        (root / 'lost.go').write_text('package main\n')
        result = self.run_latch(root)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('untracked', result.stderr)
        self.assertIn('lost.go', result.stderr)
        self.assertNotIn('scripts/preflight-push.sh', result.stderr)
        self.assert_no_success_claim(result)

    def test_clean_fully_tracked_tree_passes_with_an_accurate_message(self):
        root = self.repo('clean')
        (root / 'main.go').write_text('package main\n')
        (root / 'go.mod').write_text('module fixture\n')
        self.commit(root)
        result = self.run_latch(root)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('no source file is ignored or untracked', result.stdout)
        self.assertIn('tracked source files: 2', result.stdout)
        self.assertIn('ignored paths: 0', result.stdout)

    def test_ignored_artifacts_and_vendored_trees_pass(self):
        root = self.repo('artifacts')
        (root / '.gitignore').write_text('bin/\n.opencode/\ngraphify-out/\n')
        self.commit(root)
        (root / 'bin').mkdir()
        (root / 'bin/rein').write_text('binary\n')
        (root / '.opencode/node_modules/pkg').mkdir(parents=True)
        (root / '.opencode/node_modules/pkg/README.md').write_text('vendored\n')
        (root / 'graphify-out').mkdir()
        (root / 'graphify-out/GRAPH_REPORT.md').write_text('generated\n')
        result = self.run_latch(root)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('no source file is ignored or untracked', result.stdout)
        self.assertIn('ignored paths: 3', result.stdout)

    def test_unexpected_ignored_path_fails_the_artifact_allowlist(self):
        root = self.repo('unexpected')
        (root / '.gitignore').write_text('mystery/\n')
        self.commit(root)
        (root / 'mystery').mkdir()
        (root / 'mystery/notes.txt').write_text('not a recognized artifact\n')
        result = self.run_latch(root)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('unexpected ignored paths', result.stderr)
        self.assertIn('mystery/notes.txt', result.stderr)
        self.assert_no_success_claim(result)


if __name__ == '__main__':
    unittest.main()
