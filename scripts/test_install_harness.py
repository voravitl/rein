"""Installer ownership and error propagation checks; never touches the user's HOME."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


class HarnessInstallerTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        (self.root / 'scripts').mkdir()
        shutil.copyfile(Path(__file__).with_name('install-harness.sh'), self.root / 'scripts/install-harness.sh')
        self.bin = self.root / 'bin'
        self.bin.mkdir()
        self.home = self.root / 'home'
        self.home.mkdir()
        self.log = self.root / 'calls'
        self.env = dict(os.environ, HOME=str(self.home), XDG_CONFIG_HOME=str(self.home / 'xdg'),
                        PATH=f'{self.bin}:/usr/bin:/bin', CALLS=str(self.log))
        self.env.pop('CODEX_HOME', None)

    def cli(self, name, exit_code=0):
        path = self.bin / name
        path.write_text(f'#!/bin/sh\nprintf "%s\\n" "$*" >> "$CALLS"\nexit {exit_code}\n')
        path.chmod(0o755)

    def run_script(self, *args):
        return subprocess.run(['sh', str(self.root / 'scripts/install-harness.sh'), *args],
                              env=self.env, capture_output=True, text=True)

    def test_claude_executes_install_and_uninstall(self):
        self.cli('claude')
        self.assertEqual(self.run_script('claude').returncode, 0)
        self.assertIn('plugin install rein@rein', self.log.read_text())
        self.assertEqual(self.run_script('uninstall', 'claude').returncode, 0)
        self.assertIn('plugin uninstall rein@rein', self.log.read_text())

    def test_native_cli_failures_propagate(self):
        for harness in ('claude', 'agy', 'codex'):
            with self.subTest(harness=harness):
                self.cli(harness, 23)
                for args in ((harness,), ('uninstall', harness)):
                    self.assertNotEqual(self.run_script(*args).returncode, 0)

    def test_missing_clis_fail_without_fallback_install(self):
        for harness in ('claude', 'agy', 'codex'):
            with self.subTest(harness=harness):
                self.assertNotEqual(self.run_script(harness).returncode, 0)

    def test_missing_artifacts_fail(self):
        for harness in ('opencode', 'kiro', 'codex', 'hermes'):
            self.assertNotEqual(self.run_script(harness).returncode, 0)

    def test_task_bound_hooks_cannot_be_registered_globally(self):
        source = self.root / '.opencode/plugins/rein'
        source.mkdir(parents=True)
        (source / 'server.js').write_text('// bound to another task\n')
        result = self.run_script('opencode')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('rein hooks install <task> --vendors opencode', result.stderr)
        self.assertFalse((self.home / 'xdg/opencode/plugins/rein').exists())

    def test_opencode_uninstall_removes_only_owned_legacy_links(self):
        source = self.root / '.opencode/plugins/rein'
        for relative in ('xdg/opencode/plugins/rein', 'xdg/plugins/rein'):
            destination = self.home / relative
            destination.parent.mkdir(parents=True, exist_ok=True)
            destination.symlink_to(source)
        self.assertEqual(self.run_script('uninstall', 'opencode').returncode, 0)
        self.assertFalse((self.home / 'xdg/opencode/plugins/rein').is_symlink())
        self.assertFalse((self.home / 'xdg/plugins/rein').is_symlink())

    def test_foreign_opencode_directory_and_symlink_are_preserved(self):
        source = self.root / '.opencode/plugins/rein'
        source.mkdir(parents=True)
        (source / 'server.js').write_text('// fixture\n')
        destination = self.home / 'xdg/opencode/plugins/rein'
        destination.mkdir(parents=True)
        sentinel = destination / 'foreign'
        sentinel.write_text('keep')
        for args in (('opencode',), ('uninstall', 'opencode')):
            self.assertNotEqual(self.run_script(*args).returncode, 0)
            self.assertEqual(sentinel.read_text(), 'keep')
        sentinel.unlink()
        destination.rmdir()
        destination.symlink_to(self.home)
        self.assertNotEqual(self.run_script('uninstall', 'opencode').returncode, 0)
        self.assertTrue(destination.is_symlink())

    def test_foreign_kiro_definition_is_preserved(self):
        source = self.root / '.kiro/agents/rein.json'
        source.parent.mkdir(parents=True)
        source.write_text('{"name":"rein","hooks":{}}')
        destination = self.home / '.kiro/agents/rein.json'
        destination.parent.mkdir(parents=True)
        destination.write_text('foreign')
        for args in (('kiro',), ('uninstall', 'kiro')):
            self.assertNotEqual(self.run_script(*args).returncode, 0)
            self.assertEqual(destination.read_text(), 'foreign')

    def test_all_reports_failure_and_stops_dependent_plugin_install(self):
        for harness in ('claude', 'agy', 'codex'):
            self.cli(harness, 23)
        self.assertNotEqual(self.run_script('all').returncode, 0)
        calls = self.log.read_text()
        self.assertNotIn('plugin install rein@rein', calls)
        self.assertNotIn('plugin add rein@rein', calls)

    def test_agy_checks_usable_binary_before_registering(self):
        self.cli('agy')
        self.assertNotEqual(self.run_script('agy').returncode, 0)
        self.assertFalse(self.log.exists())
        self.cli('rein', 19)
        self.assertNotEqual(self.run_script('agy').returncode, 0)
        self.assertEqual(self.log.read_text(), 'version\n')
        self.cli('rein')
        self.log.unlink()
        self.assertEqual(self.run_script('agy').returncode, 0)
        self.assertEqual(self.log.read_text().splitlines(), [
            'version', f'plugin install {self.root}/plugins/rein'])

    def test_codex_global_install_refused_but_legacy_uninstall_executes(self):
        self.cli('codex')
        result = self.run_script('codex')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('rein hooks install <task> --vendors codex', result.stderr)
        self.assertFalse(self.log.exists())
        self.assertEqual(self.run_script('uninstall', 'codex').returncode, 0)
        self.assertEqual(self.log.read_text(), 'plugin remove rein@rein\n')

    def test_hermes_is_task_bound_and_preserves_global_home(self):
        foreign = self.home / '.hermes/config.yaml'
        foreign.parent.mkdir()
        foreign.write_text('foreign config\n')
        self.cli('hermes')
        for args, action in ((('hermes',), 'install'), (('uninstall', 'hermes'), 'uninstall')):
            result = self.run_script(*args)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn(f'rein hooks {action} <task> --vendors hermes', result.stderr)
            self.assertEqual(foreign.read_text(), 'foreign config\n')
        self.assertFalse(self.log.exists(), 'no global Hermes CLI operation is supported')

    def test_all_attempts_hermes_task_bound_guidance(self):
        result = self.run_script('all')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('rein hooks install <task> --vendors hermes', result.stderr)

    def test_invalid_target_fails(self):
        self.assertNotEqual(self.run_script('invalid').returncode, 0)


if __name__ == '__main__':
    unittest.main()
