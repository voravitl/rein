"""Offline acceptance report tests; no native provider or model requests."""
from contextlib import redirect_stderr, redirect_stdout
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch

SCRIPT = Path(__file__).resolve().parents[1] / "scripts/harness-acceptance.py"
spec = importlib.util.spec_from_file_location("harness_acceptance", SCRIPT)
acceptance = importlib.util.module_from_spec(spec)
spec.loader.exec_module(acceptance)


class HarnessAcceptanceTests(unittest.TestCase):
    def collect(self, statuses=None):
        seen = []
        outcomes = iter(statuses or [0] * 5)

        def execute(command, timeout):
            seen.append((command, timeout))
            outcome = next(outcomes)
            if isinstance(outcome, BaseException):
                raise outcome
            return subprocess.CompletedProcess(command, outcome, "local output\n", "diagnostic\n")

        return acceptance.collect_report(9, _runner=execute, _node="/offline/node"), seen

    def test_local_success_keeps_every_live_requirement_unverified(self):
        report, seen = self.collect()
        self.assertEqual(report["local_status"], "PASS")
        self.assertEqual(report["acceptance_status"], "NOT_VERIFIED")
        self.assertEqual(report["live_status"], "NOT_VERIFIED")
        self.assertFalse(report["native_dispatch_enabled"])
        self.assertEqual([row["harness"] for row in report["harnesses"]],
                         ["codex", "claude", "antigravity", "opencode2"])
        for row in report["harnesses"]:
            self.assertEqual(row["live_checks"], {key: "NOT_VERIFIED" for key in acceptance.LIVE_CHECKS})
        self.assertEqual(len(seen), 5)
        for (name, paths), (command, timeout) in zip(acceptance.SUITES, seen):
            self.assertEqual(command, ["/offline/node", "--test", *paths])
            self.assertEqual(timeout, 9)
        self.assertEqual(report["shared_package_status"], "PASS")
        self.assertEqual(report["suites"][0]["stdout"], "local output\n")
        self.assertEqual(report["suites"][0]["stderr"], "diagnostic\n")

    def test_failure_timeout_and_missing_binary_are_distinct_and_do_not_skip_other_suites(self):
        report, seen = self.collect([0, 7, subprocess.TimeoutExpired(["node"], 9, b"partial", b"waiting"),
                                    FileNotFoundError("missing node"), 0])
        self.assertEqual(len(seen), 5)
        self.assertEqual(report["local_status"], "FAIL")
        self.assertEqual([row["status"] for row in report["suites"]],
                         ["PASS", "FAIL", "TIMEOUT", "ERROR", "PASS"])
        self.assertEqual(report["suites"][1]["exit_code"], 7)
        self.assertIsNone(report["suites"][2]["exit_code"])
        self.assertEqual(report["suites"][2]["stdout"], "partial")
        self.assertEqual(report["suites"][2]["stderr"], "waiting")

    def test_require_live_does_not_accept_green_local_checks(self):
        report, _ = self.collect()
        for arguments, status in [([], 0), (["--require-live"], 2), (["--format", "json", "--require-live"], 2)]:
            with self.subTest(arguments=arguments), redirect_stdout(io.StringIO()) as output:
                self.assertEqual(acceptance.main(arguments, _collector=lambda _: report), status)
                if "json" in arguments:
                    self.assertEqual(json.loads(output.getvalue()), report)
                else:
                    self.assertIn("ยังไม่ยืนยันการทำงานจริงครบทุก harness", output.getvalue())
        failed, _ = self.collect([1, 0, 0, 0, 0])
        with redirect_stdout(io.StringIO()):
            self.assertEqual(acceptance.main(["--require-live"], _collector=lambda _: failed), 1)

    def test_default_thai_is_concise_and_details_preserves_diagnostics(self):
        report, _ = self.collect()
        with redirect_stdout(io.StringIO()) as output:
            self.assertEqual(acceptance.main([], _collector=lambda _: report), 0)
        self.assertNotIn("local output", output.getvalue())
        self.assertNotIn("diagnostic", output.getvalue())
        self.assertNotIn("argv:", output.getvalue())
        self.assertIn("การปฏิเสธนอกขอบเขต", output.getvalue())
        with redirect_stdout(io.StringIO()) as output:
            self.assertEqual(acceptance.main(["--details"], _collector=lambda _: report), 0)
        self.assertIn("local output", output.getvalue())
        self.assertIn("diagnostic", output.getvalue())
        self.assertIn("argv:", output.getvalue())
        failed, _ = self.collect([0, 1, 0, 0, 0])
        text = acceptance.render_text(failed)
        self.assertEqual(text.count("argv:"), 1)
        self.assertIn("Claude Code: FAIL", text)
        self.assertIn("diagnostic", text)

    def test_no_cli_command_injection_or_timeout_bypass(self):
        for args in [["--command", "claude"], ["--timeout", "0"], ["--timeout", "301"], ["--format", "other"]]:
            with self.subTest(args=args), redirect_stdout(io.StringIO()), redirect_stderr(io.StringIO()):
                with self.assertRaises(SystemExit) as error:
                    acceptance.main(args, _collector=lambda _: self.fail("invalid CLI must not run suites"))
                self.assertEqual(error.exception.code, 2)

    def test_actual_subprocess_contract_filters_credentials_and_native_optins(self):
        # Observe the real launch boundary without replacing any Node suite or
        # permitting arbitrary commands through production CLI.
        with patch.dict(os.environ, {"CRUX_ALLOW_MODEL_CALLS": "1", "ANTHROPIC_API_KEY": "secret",
                                     "NODE_OPTIONS": "--require=untrusted", "NODE_PATH": "untrusted"}), \
             patch.object(acceptance.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, "", "")) as run:
            acceptance._run_suite(["/offline/node", "--test", "tests/claude.test.mjs"], 3)
        args, kwargs = run.call_args
        self.assertEqual(args[0], ["/offline/node", "--test", "tests/claude.test.mjs"])
        self.assertEqual(kwargs["timeout"], 3)
        self.assertEqual(kwargs["stdin"], subprocess.DEVNULL)
        self.assertNotIn("shell", kwargs)
        for key in ["CRUX_ALLOW_MODEL_CALLS", "ANTHROPIC_API_KEY", "NODE_OPTIONS", "NODE_PATH"]:
            self.assertNotIn(key, kwargs["env"])
        self.assertEqual(kwargs["cwd"], acceptance.PLUGIN)

    def test_native_codex_is_explicit_and_does_not_upgrade_live_acceptance(self):
        for native_status, exit_code in [("READY", 0), ("NOT_READY", 2), ("ERROR", 1)]:
            report, _ = self.collect()
            with redirect_stdout(io.StringIO()) as output:
                status = acceptance.main(["--native-codex", "--format", "json"],
                    _collector=lambda _: report,
                    _native_collector=lambda: {"status": native_status})
            self.assertEqual(status, exit_code)
            returned = json.loads(output.getvalue())
            self.assertEqual(returned["live_status"], "NOT_VERIFIED")
            self.assertEqual(returned["native_codex_metadata"]["status"], native_status)
        report, _ = self.collect()
        with redirect_stdout(io.StringIO()):
            acceptance.main([], _collector=lambda _: report,
                _native_collector=lambda: self.fail("native inspection must be explicit"))

    def test_all_native_checks_preserve_live_gaps_and_report_source_scope(self):
        seen = []
        def native(name):
            seen.append(name)
            return {"harness": name, "status": "NOT_READY" if name == "codex" else "METADATA_READY",
                    "live_acceptance": "NOT_VERIFIED", "model_requests": 0,
                    "hooks": [{"event": "sessionStart", "trust": ["untrusted"]}] if name == "codex" else []}
        report, _ = self.collect()
        with redirect_stdout(io.StringIO()) as output:
            result = acceptance.main(["--native-all"], _collector=lambda _: report, _harness_collector=native)
        self.assertEqual(result, 2)
        self.assertEqual(seen, ["codex", "claude", "antigravity", "opencode2"])
        self.assertIn("อีกสามระบบโหลด source ในพื้นที่ทดสอบชั่วคราว", output.getvalue())
        self.assertIn("untrusted", output.getvalue())
        self.assertEqual(report["live_status"], "NOT_VERIFIED")
        for row in report["harnesses"]:
            self.assertTrue(all(status == "NOT_VERIFIED" for status in row["live_checks"].values()))
        for statuses, arguments, expected in [
            (["METADATA_READY"] * 4, ["--native-all"], 0),
            (["METADATA_READY"] * 4, ["--native-all", "--require-live"], 2),
            (["METADATA_READY", "ERROR", "NOT_READY", "METADATA_READY"], ["--native-all"], 1)]:
            report, _ = self.collect()
            outcomes = iter(statuses)
            with redirect_stdout(io.StringIO()):
                actual = acceptance.main(arguments, _collector=lambda _: report,
                    _harness_collector=lambda name: {"status": next(outcomes)})
            self.assertEqual(actual, expected)

    def test_native_modes_are_explicit_and_mutually_exclusive(self):
        report, _ = self.collect()
        with redirect_stdout(io.StringIO()):
            self.assertEqual(acceptance.main([], _collector=lambda _: report,
                _harness_collector=lambda _: self.fail("must stay offline by default")), 0)
        with redirect_stderr(io.StringIO()), self.assertRaises(SystemExit) as error:
            acceptance.main(["--native-all", "--native-codex"])
        self.assertEqual(error.exception.code, 2)

    def test_native_collectors_pin_commands_and_reject_false_or_mixed_reports(self):
        for harness, script in [("claude", "sdk-readiness.py"), ("antigravity", "sdk-readiness.py"),
                                 ("opencode2", "opencode-readiness.py")]:
            report = {"harness": harness, "status": "METADATA_READY", "model_requests": 0,
                      "live_acceptance": "NOT_VERIFIED"}
            result = subprocess.CompletedProcess([], 0, json.dumps(report), "")
            with patch.object(acceptance.subprocess, "run", return_value=result) as run:
                self.assertEqual(acceptance.collect_native_harness(harness), report)
            command = run.call_args.args[0]
            self.assertEqual(Path(command[2]).name, script)
            self.assertEqual(command[-2:], ["--format", "json"])
            self.assertEqual(run.call_args.kwargs["stdin"], subprocess.DEVNULL)
            for changes in ({"harness": "other"}, {"model_requests": 1}, {"live_acceptance": "PASS"}):
                changed = dict(report, **changes)
                with patch.object(acceptance.subprocess, "run", return_value=
                        subprocess.CompletedProcess([], 0, json.dumps(changed), "")):
                    self.assertEqual(acceptance.collect_native_harness(harness)["status"], "ERROR")
            with patch.object(acceptance.subprocess, "run", return_value=
                    subprocess.CompletedProcess([], 1, json.dumps(report), "private diagnostic")):
                returned = acceptance.collect_native_harness(harness)
                self.assertEqual(returned["status"], "ERROR")
                self.assertNotIn("private", json.dumps(returned))
        with self.assertRaises(ValueError):
            acceptance.collect_native_harness("arbitrary-command")

    def test_real_local_node_suites_and_report(self):
        # This calls only the pinned production Node tests. No provider binaries.
        report = acceptance.collect_report(60)
        self.assertEqual(report["local_status"], "PASS", report["suites"])
        self.assertEqual(report["acceptance_status"], "NOT_VERIFIED")
        for suite in report["suites"]:
            self.assertEqual(suite["exit_code"], 0, suite)
            self.assertTrue(suite["stdout"], suite)


if __name__ == "__main__":
    unittest.main()
