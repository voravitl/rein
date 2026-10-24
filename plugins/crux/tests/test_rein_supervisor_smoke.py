"""Smoke the real rein binary at the seam crux depends on.

Every other crux test drives a fake executable; this one builds the native
rein CLI and runs `supervisor validate` against a crux-shaped plan, so a
schema or flag drift on rein's side fails here instead of in production.
It never starts a worker, a model, or Orca.
"""
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]  # plugins/crux/tests -> repository root


@unittest.skipUnless(shutil.which("go") is not None, "Go toolchain not installed")
@unittest.skipUnless(os.name == "posix", "POSIX shell fixture")
class ReinSupervisorSmokeTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        self.repo = self.root / "repo"
        self.repo.mkdir()
        subprocess.run(["git", "init", "-q", "-b", "main", str(self.repo)], check=True)
        self.rein = self.root / "rein"
        build = subprocess.run(
            ["go", "build", "-o", str(self.rein), "./cmd/rein"],
            cwd=ROOT,
            capture_output=True,
            text=True,
        )
        self.assertEqual(build.returncode, 0, build.stderr)

    def plan(self, **overrides):
        plan = {
            "schema": 1,
            "run": "crux-smoke",
            "repo": str(self.repo),
            "terminal": "orca-check",
            "source": "crux-local-plan",
            "version": "1",
            "max_checks": 2,
            "timeout_ms": 30000,
            "poll_interval_ms": 1000,
            "pools": [],
            "tasks": [
                {
                    "task": "task_smoke1",
                    "attempt": "attempt_smoke1",
                    "current_attempt": "attempt_smoke1",
                    "orca_task": "task_smoke1",
                    "dispatch": "dispatch_smoke1",
                }
            ],
        }
        plan.update(overrides)
        path = self.root / "plan.json"
        path.write_text(json.dumps(plan))
        return path

    def test_validates_a_crux_shaped_plan(self):
        done = subprocess.run(
            [str(self.rein), "supervisor", "validate", "--plan", str(self.plan())],
            capture_output=True,
            text=True,
        )
        self.assertEqual(done.returncode, 0, done.stderr + done.stdout)
        verdict = json.loads(done.stdout)
        self.assertTrue(verdict.get("valid"))
        self.assertTrue(verdict.get("plan_hash"))

    def test_rejects_a_drifted_plan(self):
        done = subprocess.run(
            [str(self.rein), "supervisor", "validate", "--plan", str(self.plan(schema=2))],
            capture_output=True,
            text=True,
        )
        self.assertNotEqual(done.returncode, 0)
        self.assertFalse(done.stdout.strip().startswith("{"))


if __name__ == "__main__":
    unittest.main()
