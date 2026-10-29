"""Offline CLI contract tests. No Orca runtime, provider or model is invoked."""
import importlib.util
import contextlib
import io
import json
import os
from pathlib import Path
import shlex
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

SCRIPT = Path(__file__).resolve().parents[1] / "scripts" / "orca-local-supervisor.py"
spec = importlib.util.spec_from_file_location("orca_local_supervisor", SCRIPT)
bridge = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bridge)


class OfflineOrca:
    def __init__(self, repo, plan, fault=None):
        self.repo, self.plan, self.fault, self.calls = str(repo), plan, fault, []

    def __call__(self, argv, **kwargs):
        self.calls.append(argv)
        if argv[1:3] == ["supervisor", "validate"]:
            self.validator_environment = kwargs.get("env")
        runtime = "foreign" if self.fault == "runtime" and argv[1:3] == ["terminal", "show"] else "runtime-1"
        meta = {"runtimeId": runtime}
        if argv[1:3] == ["supervisor", "validate"]:
            value = {"valid": self.fault != "invalid-plan", "plan_hash": bridge.digest(self.plan.read_bytes()), "run": "r1"}
        elif argv[1] == "status":
            value = {"_meta": meta, "result": {"target": {"kind": "environment" if self.fault == "remote-target" else "local"}, "runtime": {"runtimeId": "runtime-1", "reachable": True, "capabilities": [bridge.CAPABILITY]}}}
        elif argv[1:3] == ["worktree", "show"]:
            worktree = {"path": self.repo, "hostId": "local", "identity": {"executionHostId": "local"}}
            if self.fault == "registered-path":
                worktree["path"] += "-foreign"
            if self.fault == "registered-host":
                worktree["identity"]["executionHostId"] = "foreign"
            if self.fault == "unregistered":
                worktree = {}
            value = {"_meta": meta, "result": {"worktree": worktree}}
        elif argv[1:3] == ["terminal", "create"]:
            if self.fault == "create-timeout":
                raise subprocess.TimeoutExpired(argv, 10)
            value = {"_meta": meta, "result": {"terminal": {"handle": "owned-terminal"}}}
        elif argv[1:3] == ["terminal", "list"]:
            row = {"handle": "owned-terminal", "executionHostId": "local", "worktreePath": self.repo, "connected": True}
            if self.fault == "host":
                row["executionHostId"] = "paired-remote"
            if self.fault == "missing-host":
                del row["executionHostId"]
            if self.fault == "worktree":
                row["worktreePath"] += "-foreign"
            if self.fault == "handle":
                row["handle"] = "foreign-terminal"
            value = {"_meta": meta, "result": {"terminals": [row], "truncated": self.fault == "truncated"}}
        elif argv[1:3] == ["terminal", "show"]:
            if self.fault == "changed-profile":
                Path(self.validator_environment["REIN_PROFILE"]).write_text("changed owner policy")
            terminal = {"handle": "owned-terminal", "worktreePath": self.repo, "ptyId": "pty-1", "incarnationId": "process-1", "executionHostId": "local", "connected": True, "writable": True}
            if self.fault == "show-host":
                terminal["executionHostId"] = "foreign"
            if self.fault == "missing-incarnation":
                del terminal["incarnationId"]
            value = {"_meta": meta, "result": {"terminal": terminal}}
        elif argv[1:3] == ["terminal", "send"]:
            send = {"handle": "owned-terminal", "accepted": True, "prompt": {"requestId": "request-1", "processIncarnation": "process-1", "generation": 1, "observation": "observed", "stages": ["input_accepted"]}}
            if self.fault == "missing-receipt":
                del send["prompt"]
            if self.fault == "changed-incarnation":
                send["prompt"]["processIncarnation"] = "replacement-process"
            if self.fault == "send-timeout":
                raise subprocess.TimeoutExpired(argv, 10)
            value = {"_meta": meta, "result": {"send": send}}
        else:
            raise AssertionError(f"Unexpected offline command: {argv}")
        if self.fault == "missing-runtime-meta" and argv[1:3] == ["terminal", "create"]:
            del value["_meta"]
        return SimpleNamespace(returncode=0, stdout=json.dumps(value), stderr="")


class LocalSupervisorTests(unittest.TestCase):
    def test_thai_preview_has_no_process_or_model_calls(self):
        output = io.StringIO()
        with patch.object(bridge.subprocess, "run", side_effect=AssertionError("no process allowed")), contextlib.redirect_stdout(output):
            self.assertEqual(bridge.main(["--format", "text"]), 0)
        self.assertIn("ยังไม่ได้ส่งงาน", output.getvalue())
        self.assertIn("การเรียกโมเดล: 0 ครั้ง", output.getvalue())

    def test_thai_dispatch_does_not_claim_supervisor_completion(self):
        text = bridge.render_result({"status": "dispatched", "terminal": "owned-terminal", "receipt": "/tmp/receipt.json"}, "text")
        self.assertIn("Orca รับคำสั่งแล้ว", text)
        self.assertIn("ยังไม่ยืนยัน", text)
        self.assertIn("/tmp/receipt.json", text)

    def test_thai_failure_preserves_original_diagnostic(self):
        diagnostic = "terminal_handle_stale: exact original error"
        text = bridge.render_result({"status": "blocked", "error": diagnostic, "retry": False}, "text")
        self.assertIn(diagnostic, text)
        self.assertIn("ไม่ส่งซ้ำอัตโนมัติ", text)

    def fixture(self, fault=None, tricky=False):
        if os.name != "posix":
            self.skipTest("Orca shell dispatch is explicitly POSIX-only")
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        root = Path(temp.name).resolve()
        repo = root / "checkout"
        repo.mkdir()
        rein = root / ("rein $(touch INJECTED); '" if tricky else "rein")
        rein.write_text("offline executable fixture")
        rein.chmod(0o700)
        plan = root / ("plan '; $(touch PWNED).json" if tricky else "plan.json")
        plan.write_text(json.dumps({"schema": 2, "run": "r1", "repo": str(repo)}))
        profile = root / ("owner policy $(literal) '.json" if tricky else "owner.json")
        profile.write_text("{}")
        captured_environment = dict(os.environ)
        for key in (*bridge.PATH_SETTINGS, *bridge.ORCA_OVERRIDES):
            captured_environment.pop(key, None)
        captured_environment["REIN_PROFILE"] = str(profile)
        environment_patch = patch.dict(os.environ, captured_environment, clear=True)
        environment_patch.start()
        self.addCleanup(environment_patch.stop)
        args = SimpleNamespace(receipt=str(root / "receipt.json"), rein=str(rein), plan=str(plan), orca="/offline/orca", timeout=10)
        return bridge.Transport(args), OfflineOrca(repo, plan, fault)

    def test_schema_3_plan_accepted_and_schema_4_refused(self):
        transport, offline = self.fixture()
        # Test schema 3 accepted
        Path(transport.args.plan).write_text(json.dumps({"schema": 3, "run": "r1", "repo": str(offline.repo)}))
        with patch.object(bridge.subprocess, "run", offline):
            result = transport.dispatch()
        self.assertEqual(result["status"], "dispatched")

        # Test schema 4 refused
        transport2, offline2 = self.fixture()
        Path(transport2.args.plan).write_text(json.dumps({"schema": 4, "run": "r1", "repo": str(offline2.repo)}))
        with patch.object(bridge.subprocess, "run", offline2):
            with self.assertRaisesRegex(ValueError, "requires a schema-2 or schema-3 plan"):
                transport2.dispatch()

    def test_default_has_zero_subprocess_actions(self):
        with patch.object(bridge.subprocess, "run", side_effect=AssertionError("no process allowed")):
            self.assertEqual(bridge.main([]), 0)
        p = subprocess.run([sys.executable, str(SCRIPT)], capture_output=True, text=True, timeout=5)
        self.assertEqual(p.returncode, 0)
        self.assertEqual(json.loads(p.stdout)["actions"], 0)

    def test_windows_rejected_before_any_command(self):
        transport, offline = self.fixture()
        with patch.object(bridge.os, "name", "nt"), patch.object(bridge.subprocess, "run", offline):
            with self.assertRaisesRegex(ValueError, "requires POSIX"):
                transport.dispatch()
        self.assertEqual(offline.calls, [])

    def test_existing_validator_runs_before_empty_create(self):
        transport, offline = self.fixture()
        with patch.object(bridge.subprocess, "run", offline):
            result = transport.dispatch()
        self.assertEqual(result["status"], "dispatched")
        self.assertEqual(offline.calls[0][1:3], ["supervisor", "validate"])
        creates = [a for a in offline.calls if a[1:3] == ["terminal", "create"]]
        self.assertEqual(len(creates), 1)
        self.assertNotIn("--command", creates[0])
        receipt = json.loads(transport.receipt.read_text())
        self.assertEqual(receipt["send_receipt"]["prompt"]["requestId"], "request-1")
        self.assertEqual(receipt["runtime_id"], "runtime-1")
        self.assertEqual(receipt["pty_id"], "pty-1")
        self.assertEqual(receipt["create_response"]["_meta"]["runtimeId"], "runtime-1")
        self.assertEqual(receipt["create_response"]["result"]["terminal"]["handle"], "owned-terminal")

    def test_remote_target_prevents_terminal_creation(self):
        transport, offline = self.fixture("remote-target")
        with patch.object(bridge.subprocess, "run", offline), self.assertRaisesRegex(ValueError, "local IPC target"):
            transport.dispatch()
        self.assertFalse(any(a[1] == "terminal" for a in offline.calls))

    def test_remote_environment_or_metadata_override_prevents_any_commands(self):
        for key in bridge.ORCA_OVERRIDES:
            transport, offline = self.fixture()
            with patch.dict(os.environ, {key: "private-selector-must-not-print"}), patch.object(bridge.subprocess, "run", offline):
                with self.assertRaises(ValueError) as error:
                    transport.dispatch()
            self.assertNotIn("private-selector-must-not-print", str(error.exception))
            self.assertEqual(offline.calls, [])

    def test_missing_owner_profile_prevents_all_commands(self):
        transport, offline = self.fixture()
        with patch.dict(os.environ, {}, clear=True), patch.object(bridge.subprocess, "run", offline):
            with self.assertRaisesRegex(ValueError, "REIN_PROFILE is required"):
                transport.dispatch()
        self.assertEqual(offline.calls, [])

    def test_changed_owner_profile_prevents_send(self):
        transport, offline = self.fixture("changed-profile")
        with patch.object(bridge.subprocess, "run", offline), self.assertRaisesRegex(ValueError, "owner profile changed"):
            transport.dispatch()
        self.assertFalse(any(a[1:3] == ["terminal", "send"] for a in offline.calls))

    def test_profile_path_controls_are_rejected_before_commands(self):
        with self.assertRaises(ValueError):
            bridge.absolute("/tmp/owner\0policy", "REIN_PROFILE")
        for value in ("relative", "/tmp/owner\npolicy"):
            transport, offline = self.fixture()
            with patch.dict(os.environ, {"REIN_PROFILE": value}):
                with self.assertRaises(ValueError):
                    bridge.absolute(value, "REIN_PROFILE")
                with patch.object(bridge.subprocess, "run", offline), self.assertRaises(ValueError):
                    transport.dispatch()
            self.assertEqual(offline.calls, [])

    def test_invalid_plan_cannot_create_terminal(self):
        transport, offline = self.fixture("invalid-plan")
        with patch.object(bridge.subprocess, "run", offline), self.assertRaises(ValueError):
            transport.dispatch()
        self.assertEqual(len(offline.calls), 1)

    def test_unregistered_or_foreign_worktree_prevents_terminal_mutations(self):
        for fault in ("unregistered", "registered-path", "registered-host"):
            with self.subTest(fault=fault):
                transport, offline = self.fixture(fault)
                with patch.object(bridge.subprocess, "run", offline), self.assertRaisesRegex(ValueError, "registered local worktree"):
                    transport.dispatch()
                self.assertFalse(any(a[1:3] in (["terminal", "create"], ["terminal", "send"]) for a in offline.calls))

    def test_identity_failures_prevent_dispatch(self):
        for fault in ("host", "missing-host", "runtime", "worktree", "handle", "truncated", "show-host", "missing-incarnation"):
            with self.subTest(fault=fault):
                transport, offline = self.fixture(fault)
                with patch.object(bridge.subprocess, "run", offline), self.assertRaises(ValueError):
                    transport.dispatch()
                self.assertFalse(any(a[1:3] == ["terminal", "send"] for a in offline.calls))

    def test_missing_or_ambiguous_send_receipt_never_retries(self):
        for fault in ("missing-receipt", "send-timeout", "create-timeout", "missing-runtime-meta", "changed-incarnation"):
            with self.subTest(fault=fault):
                transport, offline = self.fixture(fault)
                with patch.object(bridge.subprocess, "run", offline):
                    with self.assertRaises((ValueError, subprocess.TimeoutExpired)):
                        transport.dispatch()
                    with self.assertRaises(FileExistsError):
                        transport.dispatch()
                self.assertEqual(sum(a[1:3] == ["terminal", "create"] for a in offline.calls), 1)
                self.assertEqual(sum(a[1:3] == ["terminal", "send"] for a in offline.calls), 0 if fault in ("create-timeout", "missing-runtime-meta") else 1)

    def test_shell_text_round_trips_literal_untrusted_paths(self):
        transport, offline = self.fixture(tricky=True)
        with patch.object(bridge.subprocess, "run", offline):
            transport.dispatch()
        send = next(a for a in offline.calls if a[1:3] == ["terminal", "send"])
        command = send[send.index("--text") + 1]
        argv = shlex.split(command)
        self.assertEqual(argv[:2], ["exec", "/usr/bin/env"])
        self.assertEqual(argv[argv.index("--rein") + 1], transport.args.rein)
        self.assertEqual(argv[argv.index("--plan") + 1], transport.args.plan)
        self.assertIn(str(SCRIPT.with_name("supervisor-process.py")), argv)
        self.assertIn(transport.state["process_receipt"], argv)
        self.assertIn("REIN_PROFILE=" + offline.validator_environment["REIN_PROFILE"], argv)
        self.assertEqual(transport.state["profile_hash"], bridge.digest(Path(os.environ["REIN_PROFILE"]).read_bytes()))
        invocation = transport.state["invocation_id"]
        self.assertEqual(len(invocation), 64)
        self.assertIn("REIN_SUPERVISOR_INVOCATION=" + invocation, argv)
        for key in bridge.PATH_SETTINGS[1:]:
            self.assertNotIn(key, offline.validator_environment)
            self.assertIn(["-u", key], [argv[i:i+2] for i in range(len(argv)-1)])

    def test_observe_is_read_only_and_never_calls_orca(self):
        for returned in (False, True):
            with self.subTest(returned=returned):
                transport, offline = self.fixture()
                with patch.object(bridge.subprocess, "run", offline):
                    transport.dispatch()
                before = transport.receipt.read_bytes()
                state = transport.state
                event = {key: state[key] for key in ("invocation_id", "run", "plan_hash", "repo")}
                observed = {"start": event}
                if returned:
                    observed["return"] = dict(event, outcome="graph_returned", result={"state": "BLOCKED_RECONCILIATION"}, error="original refusal")
                response = SimpleNamespace(returncode=0, stdout=json.dumps(observed), stderr="")
                with patch.object(bridge.subprocess, "run", return_value=response) as call:
                    result = transport.observe()
                self.assertEqual(call.call_count, 1)
                self.assertEqual(call.call_args.args[0][1:3], ["supervisor", "invocation"])
                self.assertEqual(result["status"], "graph_returned" if returned else "graph_started")
                self.assertEqual(result["process_termination"], "not_verified")
                self.assertEqual(transport.receipt.read_bytes(), before)
                self.assertIn("ยังไม่ยืนยัน", bridge.render_result(result, "text"))

    def test_observe_rejects_tampered_plan_before_any_command(self):
        transport, offline = self.fixture()
        with patch.object(bridge.subprocess, "run", offline):
            transport.dispatch()
        Path(transport.args.plan).write_text("changed")
        with patch.object(bridge.subprocess, "run", side_effect=AssertionError("no process allowed")), self.assertRaisesRegex(ValueError, "plan changed"):
            transport.observe()

    def test_observe_rejects_foreign_invocation_without_mutating_receipt(self):
        transport, offline = self.fixture()
        with patch.object(bridge.subprocess, "run", offline):
            transport.dispatch()
        before = transport.receipt.read_bytes()
        response = SimpleNamespace(returncode=0, stdout=json.dumps({"start": {"invocation_id": "0" * 64}}), stderr="")
        with patch.object(bridge.subprocess, "run", return_value=response), self.assertRaisesRegex(ValueError, "identity differs"):
            transport.observe()
        self.assertEqual(transport.receipt.read_bytes(), before)

    def test_parent_wait_receipt_is_correlated_with_exact_supervisor_pid(self):
        for fault in (None, "pid", "nonce"):
            with self.subTest(fault=fault):
                transport, offline = self.fixture()
                with patch.object(bridge.subprocess, "run", offline):
                    transport.dispatch()
                state = transport.state
                event = dict({key: state[key] for key in ("invocation_id", "run", "plan_hash", "repo")}, pid=4242)
                observed = {"start": event, "return": dict(event, outcome="graph_returned", result={"state": "BLOCKED_RECONCILIATION"})}
                process = dict(event, schema=1, phase="exited", parent_pid=4241, child_pid=4242,
                               process_exit_code=1, rein=state["rein"], plan=state["plan"],
                               profile_hash=state["profile_hash"], argv=[state["rein"], "supervisor", "run", "--plan", state["plan"]])
                if fault == "pid":
                    process["child_pid"] += 1
                if fault == "nonce":
                    process["invocation_id"] = "0" * 64
                Path(state["process_receipt"]).write_text(json.dumps(process))
                response = SimpleNamespace(returncode=0, stdout=json.dumps(observed), stderr="")
                with patch.object(bridge.subprocess, "run", return_value=response):
                    if fault:
                        with self.assertRaisesRegex(ValueError, "exact supervisor invocation"):
                            transport.observe()
                    else:
                        result = transport.observe()
                        self.assertEqual(result["supervisor_process_termination"], "verified_by_parent_wait")
                        self.assertEqual(result["supervisor_process_exit_code"], 1)
                        self.assertEqual(result["process_termination"], "not_verified")
                        self.assertIn("OS ยืนยันว่า supervisor จบแล้ว", bridge.render_result(result, "text"))

    def test_cancel_without_current_process_incarnation_cannot_interrupt_or_close(self):
        transport, offline = self.fixture()
        with patch.object(bridge.subprocess, "run", offline):
            transport.dispatch()
            with self.assertRaisesRegex(ValueError, "process incarnation and generation"):
                transport.cancel()
            with self.assertRaises(ValueError):
                transport.cancel()
        interrupts = [a for a in offline.calls if "--interrupt" in a]
        self.assertEqual(len(interrupts), 0)
        self.assertFalse(any(a[1:3] == ["terminal", "close"] for a in offline.calls))
        self.assertEqual(json.loads(transport.receipt.read_text())["phase"], "cancellation_blocked_unverified_process")


if __name__ == "__main__":
    unittest.main()
