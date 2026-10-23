import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

SCRIPT = Path(__file__).resolve().parents[1] / "scripts" / "live-routing-smoke.py"
spec = importlib.util.spec_from_file_location("live_routing_smoke", SCRIPT)
smoke = importlib.util.module_from_spec(spec)
spec.loader.exec_module(smoke)


class LiveRoutingSmokeTests(unittest.TestCase):
    def test_default_does_not_start_runtime_or_models(self):
        p = subprocess.run([sys.executable, str(SCRIPT)], capture_output=True, text=True, timeout=5)
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(json.loads(p.stdout)["model_calls"], 0)

    def test_execution_requires_exact_quota_mappings(self):
        p = subprocess.run([sys.executable, str(SCRIPT), "--execute"], capture_output=True, text=True, timeout=5)
        self.assertEqual(p.returncode, 2)
        self.assertIn("explicit exact", p.stderr)

    def test_bound_cannot_exceed_six_requests_or_ninety_seconds(self):
        for argv in (("--max-model-calls", "7"), ("--timeout", "91")):
            p = subprocess.run([sys.executable, str(SCRIPT), *argv], capture_output=True, text=True, timeout=5)
            self.assertEqual(p.returncode, 2)

    def test_measured_usage_uses_input_and_output_without_double_counting_thinking(self):
        events = [{"event": "result", "result": {"status": "SUCCESS", "usage": {
            "input_tokens": 100, "output_tokens": 10, "thinking_tokens": 5, "total_tokens": 110}}}]
        self.assertEqual(smoke.measured_usage(events, "antigravity")[1], 110)
        codex = [{"type": "turn.completed", "usage": {"input_tokens": 100, "output_tokens": 10, "reasoning_output_tokens": 5}}]
        self.assertEqual(smoke.measured_usage(codex, "codex")[1], 110)

    def test_unknown_zero_negative_or_duplicate_usage_is_refused(self):
        for events in ([], [{"type": "turn.completed", "usage": {}}],
                       [{"type": "turn.completed", "usage": {"input_tokens": 0, "output_tokens": 0}}],
                       [{"type": "turn.completed", "usage": {"input_tokens": -1, "output_tokens": 2}}],
                       [{"type": "turn.completed", "usage": {"input_tokens": 1, "output_tokens": 2}}] * 2):
            with self.assertRaises(ValueError):
                smoke.measured_usage(events, "codex")

    def test_untrusted_source_rejects_io_side_effects_and_unbounded_execution(self):
        for source in ("import os\ndef remaining_percent(x): return 100-x",
                       "def remaining_percent(x):\n while True: pass",
                       "def remaining_percent(x): return x.__class__",
                       "def remaining_percent(x): return open('/tmp/a','w')",
                       "print('side effect')\ndef remaining_percent(x): return x",
                       "def remaining_percent(x=1): return x",
                       "@isfinite\ndef remaining_percent(x): return x"):
            with self.assertRaises(ValueError, msg=source):
                smoke.validate_source(source)
        self.assertEqual(smoke.validate_source(smoke.GOOD), smoke.GOOD)

    def test_frozen_fixtures_have_real_distinct_defects_and_clean_cases(self):
        cases = smoke.fixtures(smoke.GOOD)
        self.assertEqual(len({c["id"] for c in cases}), 9)
        triggers = {"type": True, "finite": float("nan"), "range": 101, "arithmetic": 25}
        for case in cases:
            namespace = {}
            exec(compile(smoke.validate_source(case["source"]), "fixture", "exec"), namespace)
            f = namespace["remaining_percent"]
            if case["rule"]:
                x = triggers[case["rule"]]
                if case["rule"] == "arithmetic":
                    self.assertNotEqual(f(x), 100-x)
                else:
                    f(x)  # Each mutation actually accepts a forbidden input.
            else:
                self.assertEqual(f(25), 75)
                for x in (True, float("nan"), 101, 10**1000, -10**1000):
                    with self.assertRaises(ValueError):
                        f(x)

    def test_request_count_retains_zero_inference_preflight_record(self):
        runner = object.__new__(smoke.Runner)
        runner.state = {"calls": [{"name": "worker"}, {"name": "review"},
                                 {"name": "refused-budget-preflight", "actual_model_calls": 0}]}
        self.assertEqual(runner.request_count(), 2)
        self.assertEqual(len(runner.state["calls"]), 3)

    def run_mock_full_auto(self, corrupt_nonce=False, reject_review=False):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        runner = object.__new__(smoke.Runner)
        runner.root = Path(temp.name)
        runner.wt = runner.root / "worktree"
        (runner.wt / "src").mkdir(parents=True)
        runner.task, runner.binary = "isolated-task", "/fixture/rein"
        runner.args = SimpleNamespace(max_model_calls=6, timeout=90, agy_quota_window=["gemini"], codex_quota_window=["weekly"])
        runner.state = {"calls": [{"name": "worker-cal"}, {"name": "review-cal"},
                                  {"name": "refused-budget-preflight", "actual_model_calls": 0}, {"name": "worker-probe"}],
                        "reviewed_sha": "calibration-head"}
        smoke.write_json(runner.root / "task-profile.json", {"phase": "worker"})
        commands, charges = [], []
        runner.quota_check = lambda *args: None
        runner.rein = lambda *args: commands.append(args) or ""
        runner.charge = lambda *args: charges.append(args)

        def local(argv, cwd=None, timeout=30):
            if argv[:2] == ["python3", "-c"]:
                return "17 objective cases PASS"
            if argv[:2] == ["git", "rev-parse"]:
                return "seed-head" if argv[2].endswith("^") else "automatic-head"
            return ""
        runner.run = local

        def prepare(profile, name, base):
            self.assertEqual(base, "seed-head")
            self.assertEqual(json.loads(profile.read_text())["phase"], "review")
            runner.state["calls"].append({"name": "review-probe"})
            return {"attempt": "review-attempt", "receipt": {"agent": "codex", "model": "gpt-6.1-sol", "review_revision": "revision-digest"}}
        runner.prepare_auto = prepare

        def call(name, argv, prompt=""):
            runner.state["calls"].append({"name": name})
            if name == "automatic-worker":
                nonce = argv[-1].split("Copy nonce: ", 1)[1]
                response = {"source": smoke.GOOD, "nonce": nonce, "summary_th": "ผ่าน"}
                return [{"event": "result", "result": {"status": "SUCCESS", "response": json.dumps(response),
                         "usage": {"input_tokens": 10, "output_tokens": 5}}}]
            handoff = json.loads(argv[-1].split("Handoff: ", 1)[1])
            smoke.write_json(runner.root / "automatic-verdict.json", {
                "verdict": "FAIL" if reject_review else "PASS", "violated_rules": ["reproduced defect"] if reject_review else [], "source_sha256": handoff["source_sha256"],
                "review_sha": handoff["review_sha"], "nonce": "incorrect" if corrupt_nonce else handoff["nonce"]})
            return [{"type": "turn.completed", "usage": {"input_tokens": 20, "output_tokens": 5}}]
        runner.call = call
        return runner, commands, charges

    def test_full_auto_records_real_attempts_and_settles_after_independent_review(self):
        runner, commands, charges = self.run_mock_full_auto()
        runner.full_auto({"attempt": "worker-attempt"})
        self.assertEqual(runner.state["actual_model_calls"], 6)
        self.assertEqual(len(runner.state["calls"]), 7)  # Preserves zero-inference failure.
        self.assertEqual(runner.state["selection_mode"], "baseline_insufficient_evidence")
        self.assertEqual([charge[0] for charge in charges], ["worker-attempt", "review-attempt"])
        self.assertEqual(commands[-2:], [("route", "settle", "--attempt", "review-attempt"),
                                         ("route", "settle", "--attempt", "worker-attempt")])
        self.assertTrue(any(c[:2] == ("route", "check") and "review" in c for c in commands))

    def test_full_auto_preserves_source_bytes_with_windows_text_newlines(self):
        runner, _, _ = self.run_mock_full_auto()
        original = Path.write_text

        def windows_write(path, text, *args, **kwargs):
            if path.name == "quota_math.py":
                with path.open("w", encoding="utf-8", newline="\r\n") as output:
                    return output.write(text)
            return original(path, text, *args, **kwargs)

        with patch.object(Path, "write_text", windows_write):
            runner.full_auto({"attempt": "worker-attempt"})
        self.assertEqual((runner.wt / "src/quota_math.py").read_bytes(), smoke.GOOD.encode())

    def test_corrupt_review_handoff_does_not_record_success_or_release_holds(self):
        runner, commands, charges = self.run_mock_full_auto(corrupt_nonce=True)
        with self.assertRaisesRegex(ValueError, "handoff changed"):
            runner.full_auto({"attempt": "worker-attempt"})
        self.assertEqual(len(charges), 2)  # Both actual requests are charged despite failed acceptance.
        self.assertFalse(any(c[:2] == ("route", "settle") for c in commands))
        self.assertFalse(any(c[:2] == ("ledger", "add") for c in commands))

    def test_full_auto_refuses_request_cap_before_launch(self):
        runner, commands, charges = self.run_mock_full_auto()
        runner.args.max_model_calls = 5
        with self.assertRaisesRegex(ValueError, "six calls"):
            runner.full_auto({"attempt": "worker-attempt"})
        self.assertEqual(commands, [])
        self.assertEqual(charges, [])

    def review_resume_fixture(self):
        runner, _, _ = self.run_mock_full_auto()
        runner.task = "isolated-task"
        runner.state["calls"] += [{"name": "automatic-worker", "exit_code": 0, "argv": ["agy", "Copy nonce: nonce"]},
                                   {"name": "automatic-review-availability-probe", "actual_model_calls": 1, "exit_code": 1}]
        runner.state["reviewed_sha"] = "calibration-head"
        (runner.wt / "src/quota_math.py").write_text(smoke.GOOD)
        auto = {"attempt": "worker-attempt", "receipt": {"task": runner.task, "run": runner.task,
                "agent": "antigravity", "hold": "worker-hold", "config_fingerprint": "cfg"}}
        smoke.write_json(runner.root / "automatic-baseline.json", auto)
        used = runner.root / "contracts/routes/used"
        used.mkdir(parents=True)
        (used / "worker-attempt").touch()
        result = {"event": "result", "result": {"status": "SUCCESS", "response": json.dumps({"source": smoke.GOOD, "nonce": "nonce"}),
                                                "usage": {"input_tokens": 10, "output_tokens": 5}}}
        (runner.root / "automatic-worker.jsonl").write_text(json.dumps(result))
        (runner.root / "automatic-worker.stderr").write_text("")
        rows = [{"kind": "routing_launch", "provider": "agy-worker", "config": "cfg",
                 "worker": {"agent": "antigravity", "model": "gemini-3.8-flash-low"}},
                {"kind": "routing_exit", "ok": True},
                {"kind": "cost", "component": "worker", "amount": 15, "unit": "tokens", "pool": "agy/subscription/tokens"}]
        for row in rows:
            row.update(attempt="worker-attempt", task=runner.task, run=runner.task)
        (runner.root / "ledger.jsonl").write_text("\n".join(json.dumps(r) for r in rows))
        failed = {"rounds": [{"inventories": [{"harness": "codex", "version": "0.163.0"}],
                              "probe": {"name": "codex-reviewer", "state": "down", "detail": smoke.TRUST_REFUSAL, "seconds": 0.18}}]}
        smoke.write_json(runner.root / "automatic-review-prepare.stdout", failed)
        (runner.root / "automatic-review-prepare.stderr").write_text("refused before model")
        hold = {"id": "worker-hold", "task": runner.task, "attempt": "worker-attempt", "state": "reserved"}
        runner.rein = lambda *argv: json.dumps([hold])
        def local(argv, cwd=None, timeout=30):
            if argv[:2] == ["python3", "-c"]:
                return "17 objective cases PASS"
            return "calibration-head" if argv[-1] == "HEAD^" else "automatic-head"
        runner.run = local
        runner.call = lambda *args: self.fail("resume validation must never start a model or relaunch the worker")
        return runner, hold

    def test_review_resume_recovers_exact_worker_without_model_request(self):
        runner, _ = self.review_resume_fixture()
        recovered = runner.validate_review_resume()
        self.assertEqual(recovered[0]["attempt"], "worker-attempt")
        self.assertEqual(recovered[4], "automatic-head")
        self.assertEqual(runner.request_count(), 4)
        self.assertEqual(runner.state["automatic_worker_usage"], {"input_tokens": 10, "output_tokens": 5})
        failed = runner.state["calls"][-1]
        self.assertEqual(failed["actual_model_calls"], 0)
        self.assertEqual(failed["actual_native_probe_processes"], 1)
        self.assertTrue((runner.root / "automatic-review-first-refusal.stdout").is_file())
        self.assertIn('"amount": 15', (runner.root / "ledger.jsonl").read_text())  # Ledger untouched.

    def test_review_resume_fails_closed_for_changed_source_or_uncertain_hold(self):
        for corrupt in ("source", "hold", "prior-review", "unknown-failure"):
            runner, hold = self.review_resume_fixture()
            if corrupt == "source":
                (runner.wt / "src/quota_math.py").write_text(smoke.GOOD + "\n")
            elif corrupt == "hold":
                hold["state"] = "settled"
            elif corrupt == "prior-review":
                (runner.root / "automatic-review.json").write_text("{}")
            else:
                smoke.write_json(runner.root / "automatic-review-prepare.stdout", {"rounds": [{"probe": {"state": "down", "detail": "unknown"}}]})
            with self.assertRaises(ValueError, msg=corrupt):
                runner.validate_review_resume()

    def test_only_pinned_exact_pre_model_refusal_avoids_inference_debit(self):
        report = {"inventories": [{"harness": "codex", "version": "0.163.0"}],
                  "probe": {"name": "codex-reviewer", "state": "down", "detail": smoke.TRUST_REFUSAL, "seconds": 0.18}}
        self.assertTrue(smoke.confirmed_pre_model_refusal(report))
        for key, value in (("detail", "quota exhausted"), ("state", "up"), ("seconds", 3), ("name", "other")):
            altered = json.loads(json.dumps(report))
            altered["probe"][key] = value
            self.assertFalse(smoke.confirmed_pre_model_refusal(altered))
        report["inventories"][0]["version"] = "future-version"
        self.assertFalse(smoke.confirmed_pre_model_refusal(report))

    def test_v1_clean_fixture_reproduces_huge_integer_overflow_and_v2_rejects_with_value_error(self):
        old_source = smoke.GOOD.replace("if isinstance(used, float) and not math.isfinite(used):", "if not math.isfinite(used):")
        old_ns, new_ns = {}, {}
        exec(old_source, old_ns)
        exec(smoke.GOOD, new_ns)
        for value in (10**1000, -10**1000):
            with self.assertRaises(OverflowError):
                old_ns["remaining_percent"](value)
            with self.assertRaises(ValueError):
                new_ns["remaining_percent"](value)
        self.assertEqual(smoke.SUITE, "crux-remaining-percent-v2")

    def test_valid_failed_review_records_nonapproval_and_settles_both_reservations(self):
        runner, commands, charges = self.run_mock_full_auto(reject_review=True)
        with self.assertRaisesRegex(ValueError, "failure recorded and both reservations settled"):
            runner.full_auto({"attempt": "worker-attempt"})
        self.assertEqual(runner.state["outcome"], "FAIL_INDEPENDENT_REVIEW")
        self.assertEqual(runner.state["automatic_review_verdict"]["verdict"], "FAIL")
        self.assertTrue(runner.state["automatic_review_handoff_valid"])
        self.assertEqual(runner.state["automatic_reviewer_usage"], {"input_tokens": 20, "output_tokens": 5})
        outcomes = [command for command in commands if command[:2] == ("ledger", "add")]
        self.assertEqual(len(outcomes), 1)
        self.assertNotIn("--approved", outcomes[0])
        self.assertNotIn("--highs", outcomes[0])  # Review schema supplies no severity counts.
        self.assertEqual(commands[-2:], [("route", "settle", "--attempt", "review-attempt"),
                                         ("route", "settle", "--attempt", "worker-attempt")])
        self.assertEqual(len(charges), 2)


if __name__ == "__main__":
    unittest.main()
