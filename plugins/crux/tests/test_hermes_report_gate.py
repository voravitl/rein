"""Exercise generated Rein report gating through an installed Hermes dispatcher.

Set HERMES_SOURCE_ROOT to the Hermes checkout; no model or provider is loaded.
"""
import ast
import json
import os
from pathlib import Path
import subprocess
import sys
import types
import unittest
from typing import Any, Dict, List, Optional
from unittest.mock import patch


class HermesReportGateTests(unittest.TestCase):
    @unittest.skipUnless(os.environ.get("HERMES_SOURCE_ROOT"), "set HERMES_SOURCE_ROOT for upstream integration")
    def test_subprocess_failures_keep_upstream_turn_open(self):
        source = Path(__file__).resolve().parents[3] / "internal/hooks/hooks.go"
        plugin = source.read_text().split("const hermesPluginInit = `", 1)[1].split("`", 1)[0]
        for value in ("/nonexistent-rein", "review", "/tmp"):
            plugin = plugin.replace("%s", json.dumps(value), 1)
        namespace = {}
        exec(plugin, namespace)
        upstream = Path(os.environ["HERMES_SOURCE_ROOT"]) / "hermes_cli"

        def function(filename, name):
            tree = ast.parse((upstream / filename).read_text())
            node = next(n for n in ast.walk(tree) if isinstance(n, ast.FunctionDef) and n.name == name)
            return ast.unparse(node)

        fake = types.ModuleType("hermes_cli.plugins")
        fake._resolve_hook_callback_timeout = lambda: 0
        scope = dict(globals(), OBSERVER_SCHEMA_VERSION=1,
                     _hook_uses_callback_timeout=lambda *_: False,
                     _HOOK_TIMEOUT_FAIL_CLOSED_HOOKS={"pre_tool_call"})
        with patch.dict(sys.modules, {"hermes_cli": types.ModuleType("hermes_cli"), "hermes_cli.plugins": fake}):
            exec(function("plugins_dispatch.py", "invoke_hook"), scope)

            class Manager:
                _hooks = {"pre_verify": [namespace["_report_gate"]]}

                def __init__(self):
                    self.errors = []

                def _invoke_hook_callback(self, callback, kwargs):
                    return callback(**kwargs)

                def _report_hook_failure(self, *args):
                    self.errors.append(args[-1])

            manager = Manager()
            dispatch = scope["invoke_hook"]
            scope["invoke_hook"] = lambda *args, **kwargs: dispatch(manager, *args, **kwargs)
            exec(function("plugins.py", "get_pre_verify_continue_message"), scope)
            for error in (FileNotFoundError("missing binary"), PermissionError("not executable"), subprocess.TimeoutExpired("rein", 10)):
                with self.subTest(error=type(error).__name__), patch.object(subprocess, "run", side_effect=error):
                    result = scope["get_pre_verify_continue_message"](session_id="s", final_response="done")
                    self.assertIn("guard did not run", result)
                    self.assertEqual(manager.errors, [], "dispatcher must receive a directive, not skip an exception")


if __name__ == "__main__":
    unittest.main()
