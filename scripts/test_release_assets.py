"""Release packaging guards: one version across manifests, and the Windows installer must use the
same checksum-verified release assets as the POSIX installer. These are static/contract tests; the
Windows installer itself is not executed on macOS/Linux.
"""
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parent.parent


class VersionConsistencyTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        (self.root / "scripts").mkdir()
        shutil.copyfile(ROOT / "scripts/check-version-consistency.sh",
                        self.root / "scripts/check-version-consistency.sh")
        for manifest in (
            "package.json",
            ".claude-plugin/plugin.json",
            "plugin.json",
            "plugins/rein/plugin.json",
            "plugins/crux/plugin.json",
            "plugins/crux/.claude-plugin/plugin.json",
            "plugins/crux/.codex-plugin/plugin.json",
        ):
            self.write(manifest, "0.9.0")

    def write(self, relative, version):
        path = self.root / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text('{\n  "name": "rein",\n  "version": "%s"\n}\n' % version)

    def run_script(self, *args):
        return subprocess.run(["sh", str(self.root / "scripts/check-version-consistency.sh"), *args],
                              capture_output=True, text=True)

    def test_equal_manifests_pass_with_and_without_tag(self):
        self.assertEqual(self.run_script().returncode, 0)
        self.assertEqual(self.run_script("v0.9.0").returncode, 0)

    def test_drift_between_manifests_fails(self):
        self.write("plugins/rein/plugin.json", "0.8.0")
        result = self.run_script()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("version drift", result.stderr)

    def test_tag_mismatch_fails(self):
        result = self.run_script("v0.8.0")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("does not match", result.stderr)


class InstallerContractTests(unittest.TestCase):
    def test_windows_installer_declares_verified_release_download(self):
        script = (ROOT / "scripts/install.ps1").read_text()
        for required in (
            "REIN_RELEASE_URL",
            "REIN_RELEASE_REPO",
            "releases/download/v$version",
            "SHA256SUMS",
            "Get-FileHash -Algorithm SHA256",
            "file://",
            "checksum mismatch",
            "dist\\$asset",
        ):
            self.assertIn(required, script)
        self.assertNotIn("no Go toolchain and no dist", script)

    def test_posix_and_windows_installers_share_the_asset_name(self):
        posix = (ROOT / "scripts/install.sh").read_text()
        windows = (ROOT / "scripts/install.ps1").read_text()
        self.assertIn("rein-$os-$arch$exe", posix)
        self.assertIn("rein-windows-$arch.exe", windows)


if __name__ == "__main__":
    unittest.main()
