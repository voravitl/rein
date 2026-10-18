"""Offline tests using a temporary fake executable, never native Rein/providers."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import signal
import stat
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

SCRIPT = Path(__file__).resolve().parents[1] / "scripts" / "supervisor-process.py"
spec = importlib.util.spec_from_file_location("supervisor_process", SCRIPT)
process = importlib.util.module_from_spec(spec)
spec.loader.exec_module(process)


@unittest.skipUnless(os.name == "posix", "POSIX terminal process trampoline")
class SupervisorProcessTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        self.repo = self.root / "repo with $literal"
        self.repo.mkdir()
        self.plan = self.root / "plan with spaces.json"
        self.plan.write_text(json.dumps({"schema": 2, "repo": str(self.repo), "run": "offline-run"}))
        self.profile = self.root / "profile.json"
        self.profile.write_text('{"schema":1}')
        self.receipt = self.root / "process.json"
        self.invocation = "a" * 64
        self.trace = self.root / "fake-child.json"
        self.ready = self.root / "ready"
        self.reaped = self.root / "finished"
        self.rein = self.root / "fake rein $literal"
        self.rein.write_text("#!" + str(Path(sys.executable).resolve()) + "\n" + '''
# Explicit offline fake: records exact argv/env/cwd and optionally awaits signal.
import json, os, signal, sys, time
from pathlib import Path
Path(os.environ["OFFLINE_TRACE"]).write_text(json.dumps({"argv":sys.argv,"cwd":os.getcwd(),"nonce":os.environ.get("REIN_SUPERVISOR_INVOCATION"),"profile":os.environ.get("REIN_PROFILE"),"pid":os.getpid()}))
if os.environ.get("OFFLINE_WAIT_SIGNAL"):
    def finish(sig, frame):
        Path(os.environ["OFFLINE_FINISHED"]).write_text(str(sig))
        sys.exit(23)
    signal.signal(signal.SIGINT, finish)
    signal.signal(signal.SIGTERM, finish)
    Path(os.environ["OFFLINE_READY"]).write_text("ready")
    while True: time.sleep(.02)
if os.environ.get("OFFLINE_SELF_SIGNAL"):
    os.kill(os.getpid(), signal.SIGTERM)
time.sleep(.03)
Path(os.environ["OFFLINE_FINISHED"]).write_text("finished")
sys.exit(7)
''')
        self.rein.chmod(0o700)
        self.environment = os.environ.copy()
        self.environment.update(REIN_PROFILE=str(self.profile), REIN_SUPERVISOR_INVOCATION=self.invocation,
                                OFFLINE_TRACE=str(self.trace), OFFLINE_READY=str(self.ready),
                                OFFLINE_FINISHED=str(self.reaped))
        self.args = argparse.Namespace(rein=str(self.rein), plan=str(self.plan), receipt=str(self.receipt),
                                       invocation=self.invocation, plan_hash=process.digest(self.plan.read_bytes()),
                                       profile_hash=process.digest(self.profile.read_bytes()))

    def execute(self):
        with patch.dict(os.environ, self.environment, clear=True):
            return process.execute(self.args)

    def test_actual_wait_exit_and_literal_argv(self):
        self.assertEqual(self.execute(), 7)
        receipt = json.loads(self.receipt.read_text())
        trace = json.loads(self.trace.read_text())
        self.assertEqual(receipt["phase"], "exited")
        self.assertEqual(receipt["process_exit_code"], 7)
        self.assertEqual(receipt["child_pid"], trace["pid"])
        self.assertEqual(receipt["parent_pid"], os.getpid())
        self.assertEqual(trace["cwd"], str(self.repo))
        self.assertEqual(trace["argv"], [str(self.rein), "supervisor", "run", "--plan", str(self.plan)])
        self.assertEqual(trace["nonce"], self.invocation)
        self.assertEqual(trace["profile"], str(self.profile))
        self.assertEqual(stat.S_IMODE(self.receipt.stat().st_mode), 0o600)
        self.assertTrue(self.reaped.exists())
        with self.assertRaises(ChildProcessError):
            os.waitpid(trace["pid"], os.WNOHANG)

    def test_actual_signal_exit_is_not_normalized_in_receipt(self):
        self.environment["OFFLINE_SELF_SIGNAL"] = "1"
        self.assertEqual(self.execute(), 128 + int(signal.SIGTERM))
        receipt = json.loads(self.receipt.read_text())
        self.assertEqual(receipt["phase"], "exited")
        self.assertEqual(receipt["process_exit_code"], -int(signal.SIGTERM))

    def test_existing_receipt_never_respawns(self):
        self.receipt.write_text('{"phase":"spawn_pending"}')
        with patch.object(process.subprocess, "Popen", side_effect=AssertionError("must not spawn")):
            with self.assertRaises(FileExistsError):
                self.execute()
        self.assertFalse(self.trace.exists())

    def test_invalid_identity_never_spawns(self):
        for fault in ("plan", "profile", "nonce", "environment", "inside repo"):
            with self.subTest(fault=fault):
                args = argparse.Namespace(**vars(self.args))
                env = dict(self.environment)
                if fault == "plan": args.plan_hash = "b" * 64
                elif fault == "profile": args.profile_hash = "b" * 64
                elif fault == "nonce": args.invocation = "A" * 64
                elif fault == "environment": env["REIN_SUPERVISOR_INVOCATION"] = "b" * 64
                else: args.receipt = str(self.repo / "receipt.json")
                with patch.dict(os.environ, env, clear=True), patch.object(process.subprocess, "Popen", side_effect=AssertionError("must not spawn")):
                    with self.assertRaises(ValueError): process.execute(args)
                self.assertFalse(self.receipt.exists())

    def test_toctou_after_reservation_never_spawns_or_retries(self):
        reserve = process.reserve
        def mutate(path, state):
            reserve(path, state)
            self.profile.write_text("changed policy")
        with patch.object(process, "reserve", side_effect=mutate), patch.object(process.subprocess, "Popen", side_effect=AssertionError("must not spawn")):
            with self.assertRaisesRegex(ValueError, "changed before spawn"):
                self.execute()
        self.assertEqual(json.loads(self.receipt.read_text())["phase"], "spawn_pending")

    def test_reservation_failure_does_not_spawn(self):
        with patch.object(process, "reserve", side_effect=OSError("offline fsync failure")), patch.object(process.subprocess, "Popen", side_effect=AssertionError("must not spawn")):
            with self.assertRaises(OSError): self.execute()

    def test_running_receipt_failure_still_reaps_exact_child(self):
        persist = process.persist
        calls = []
        def fail_running(path, state):
            calls.append(state["phase"])
            if state["phase"] == "running": raise OSError("offline persist failure")
            persist(path, state)
        with patch.object(process, "persist", side_effect=fail_running):
            with self.assertRaisesRegex(RuntimeError, "child was waited"):
                self.execute()
        self.assertEqual(calls, ["running", "exited"])
        self.assertTrue(self.reaped.exists())
        receipt = json.loads(self.receipt.read_text())
        self.assertEqual(receipt["process_exit_code"], 7)
        with self.assertRaises(ChildProcessError): os.waitpid(receipt["child_pid"], os.WNOHANG)

    def test_signal_before_spawn_does_not_start_child(self):
        def install(sig, handler):
            if sig == signal.SIGTERM and callable(handler):
                handler(int(sig), None)
            return signal.getsignal(sig)
        with patch.object(process.signal, "signal", side_effect=install), patch.object(process.subprocess, "Popen", side_effect=AssertionError("must not spawn")):
            with self.assertRaisesRegex(RuntimeError, "interrupted before spawn"):
                self.execute()
        self.assertEqual(json.loads(self.receipt.read_text())["phase"], "spawn_pending")

    def test_unexpected_forward_failure_still_waits_actual_child(self):
        spawn = subprocess.Popen
        children = []
        def create(*args, **kwargs):
            child = spawn(*args, **kwargs)
            children.append(child)
            # Simulate a signal during Popen, before assignment to the child.
            signal.getsignal(signal.SIGTERM)(int(signal.SIGTERM), None)
            return child
        with patch.object(process.subprocess, "Popen", side_effect=create), patch.object(spawn, "send_signal", side_effect=OSError("offline forward failure")):
            with self.assertRaisesRegex(OSError, "offline forward failure"):
                self.execute()
        self.assertEqual(len(children), 1)
        self.assertEqual(children[0].returncode, 7)
        self.assertTrue(self.reaped.exists())
        with self.assertRaises(ChildProcessError): os.waitpid(children[0].pid, os.WNOHANG)

    def test_spawn_error_keeps_ambiguous_receipt_without_retry(self):
        with patch.object(process.subprocess, "Popen", side_effect=OSError("offline spawn failure")) as spawn:
            with self.assertRaises(OSError): self.execute()
        self.assertEqual(spawn.call_count, 1)
        self.assertEqual(json.loads(self.receipt.read_text())["phase"], "spawn_pending")

    def test_sigint_and_sigterm_forward_to_actual_child_then_wait(self):
        for sig in (signal.SIGINT, signal.SIGTERM):
            with self.subTest(signal=sig):
                for path in (self.receipt, self.ready, self.reaped):
                    path.unlink(missing_ok=True)
                env = dict(self.environment, OFFLINE_WAIT_SIGNAL="1")
                argv = [sys.executable, str(SCRIPT)]
                for key, value in vars(self.args).items(): argv += ["--" + key.replace("_", "-"), value]
                parent = subprocess.Popen(argv, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
                try:
                    deadline = time.monotonic() + 5
                    while not self.ready.exists() and time.monotonic() < deadline:
                        if parent.poll() is not None: break
                        time.sleep(.02)
                    self.assertTrue(self.ready.exists(), "offline child did not become ready")
                    parent.send_signal(sig)
                    stdout, stderr = parent.communicate(timeout=5)
                    self.assertEqual(parent.returncode, 23, (stdout, stderr))
                    receipt = json.loads(self.receipt.read_text())
                    self.assertEqual(receipt["phase"], "exited")
                    self.assertEqual(receipt["process_exit_code"], 23)
                    self.assertEqual(self.reaped.read_text(), str(int(sig)))
                finally:
                    if parent.poll() is None:
                        parent.send_signal(signal.SIGTERM)
                        parent.communicate(timeout=5)


if __name__ == "__main__":
    unittest.main()
