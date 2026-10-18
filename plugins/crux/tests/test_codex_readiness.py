"""Native metadata diagnostics: ownership, RPC boundary, privacy and cleanup."""
import asyncio
import copy
import importlib.util
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

SCRIPT = Path(__file__).resolve().parents[1] / "scripts/codex-readiness.py"
spec = importlib.util.spec_from_file_location("codex_readiness", SCRIPT)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


def fixtures(cwd):
    hooks = {"data": [{"cwd": cwd, "errors": [], "warnings": [], "hooks": [
        {"eventName": event, "pluginId": "crux@rein", "enabled": True, "trustStatus": "trusted"}
        for event in module.EVENTS]}]}
    skills = {"data": [{"cwd": cwd, "errors": [], "skills": [
        {"name": name, "pluginId": "crux@rein", "enabled": True}
        for name in module.REQUIRED_SKILLS]}]}
    return hooks, skills


class CodexReadinessTests(unittest.TestCase):
    def test_ready_metadata_never_becomes_live_acceptance(self):
        report = module.summarize(*fixtures("/project"), "/project")
        self.assertEqual(report["status"], "READY")
        self.assertEqual(report["live_acceptance"], "NOT_VERIFIED")
        self.assertEqual(report["model_requests"], 0)
        self.assertFalse(report["trust_modified"])
        self.assertIn("ยังไม่ยืนยัน", module.render(report))

    def test_untrusted_modified_disabled_missing_and_duplicate_hooks_refuse_ready(self):
        for state in ("untrusted", "modified", "unknown", None):
            hooks, skills = fixtures("/project")
            hooks["data"][0]["hooks"][0]["trustStatus"] = state
            self.assertEqual(module.summarize(hooks, skills, "/project")["status"], "NOT_READY")
        for operation in ("disabled", "missing", "duplicate"):
            hooks, skills = fixtures("/project")
            rows = hooks["data"][0]["hooks"]
            if operation == "disabled":
                rows[0]["enabled"] = False
            elif operation == "missing":
                rows.pop(0)
            else:
                rows.append(dict(rows[0], trustStatus="untrusted"))
            self.assertEqual(module.summarize(hooks, skills, "/project")["status"], "NOT_READY")

    def test_same_named_repo_skill_or_foreign_plugin_is_not_owned_evidence(self):
        for owner in (None, "pstack@pstack-local"):
            hooks, skills = fixtures("/project")
            for row in skills["data"][0]["skills"]:
                row["pluginId"] = owner
            report = module.summarize(hooks, skills, "/project")
            self.assertEqual(report["status"], "NOT_READY")
            self.assertEqual(report["enabled_skills"], 0)
            self.assertEqual(len(report["missing_required_skills"]), 2)

    def test_wrong_workspace_errors_warnings_and_schema_cannot_be_ready(self):
        original = fixtures("/project")
        variants = []
        for key, value in (("cwd", "/other"), ("errors", [{"message": "private"}]),
                           ("warnings", ["private"]), ("hooks", None)):
            hooks, skills = copy.deepcopy(original)
            hooks["data"][0][key] = value
            variants.append((hooks, skills))
        hooks, skills = copy.deepcopy(original)
        hooks["data"].append(hooks["data"][0])
        variants.append((hooks, skills))
        for values in variants:
            with self.assertRaises(module.InspectionError):
                module.summarize(*values, "/project")

    def test_report_excludes_private_metadata(self):
        hooks, skills = fixtures("/project")
        hooks["data"][0]["hooks"][0].update(command="secret token", sourcePath="private path")
        skills["data"][0]["skills"][0].update(description="private description", path="private path")
        text = json.dumps(module.summarize(hooks, skills, "/project"))
        self.assertNotIn("private", text)
        self.assertNotIn("secret", text)

    def test_server_request_is_not_approved(self):
        class Writer:
            def write(self, _):
                pass
            async def drain(self):
                pass
        async def check():
            reader = asyncio.StreamReader()
            reader.feed_data(b'{"id":91,"method":"item/commandExecution/requestApproval"}\n')
            await module.exchange(reader, Writer(), "/project")
        with self.assertRaisesRegex(module.InspectionError, "unexpected_server_request"):
            asyncio.run(check())

    def test_actual_process_exact_metadata_methods_large_response_and_reaping(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            hooks, skills = fixtures(directory)
            skills["data"][0]["skills"].append({"name": "foreign", "description": "x" * 100000})
            executable = root / "codex"
            executable.write_text("#!" + sys.executable + "\n" +
                "import json,sys,os\nfrom pathlib import Path\n" +
                f"root=Path({directory!r})\n" +
                "root.joinpath('pid').write_text(str(os.getpid()))\n" +
                f"results={{'initialize': {{}}, 'hooks/list': {hooks!r}, 'skills/list': {skills!r}}}\n" +
                "with root.joinpath('requests').open('w') as log:\n" +
                " for line in sys.stdin:\n" +
                "  row=json.loads(line); log.write(line); log.flush()\n" +
                "  if 'id' in row: print(json.dumps({'id':row['id'],'result':results[row['method']]}),flush=True)\n")
            executable.chmod(0o700)
            with patch.dict(os.environ, {"PATH": directory}):
                report = asyncio.run(module.inspect(directory, 5))
            self.assertEqual(report["status"], "READY")
            requests = [json.loads(line) for line in (root / "requests").read_text().splitlines()]
            self.assertEqual([row["method"] for row in requests],
                             ["initialize", "initialized", "hooks/list", "skills/list"])
            self.assertEqual(requests[-1]["params"], {"cwds": [directory], "forceReload": True})
            with self.assertRaises(ProcessLookupError):
                os.kill(int((root / "pid").read_text()), 0)

    def test_timeout_kills_and_reaps_only_owned_process(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            executable = root / "codex"
            executable.write_text("#!" + sys.executable + "\nimport os,time\n" +
                f"open({str(root / 'pid')!r},'w').write(str(os.getpid()))\n" + "time.sleep(30)\n")
            executable.chmod(0o700)
            with patch.dict(os.environ, {"PATH": directory}):
                with self.assertRaises(asyncio.TimeoutError):
                    asyncio.run(module.inspect(directory, 0.2))
            with self.assertRaises(ProcessLookupError):
                os.kill(int((root / "pid").read_text()), 0)


if __name__ == "__main__":
    unittest.main()
