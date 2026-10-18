import asyncio
import importlib.util
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location('sdk_readiness', Path(__file__).resolve().parents[1] / 'scripts/sdk-readiness.py')
SDK = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(SDK)
SKILLS = {'crux:crux-mode', 'crux:thai-communication'}


def claude(skills=SKILLS, agents=('crux:crux-agent', 'crux:crux-comment-reviewer')):
    return {'type': 'control_response', 'response': {'subtype': 'success', 'request_id': 'crux-discovery',
            'pending_permission_requests': [], 'pending_user_dialog_requests': [], 'response': {
                'commands': [{'name': name} for name in skills], 'agents': [{'name': name} for name in agents],
                'account': {'token': 'PRIVATE_SECRET'}, 'pid': 12345}}}


def agy(agent='crux'):
    return {'event': 'init', 'conversation_id': 'PRIVATE_SECRET',
            'init': {'agent': agent, 'tools': [], 'cwd': '/private/SECRET'}}


def raw(value):
    return json.dumps(value).encode()


class SummaryTests(unittest.TestCase):
    def test_claude_ready_and_private_fields_removed(self):
        result = SDK.summarize('claude', raw(claude()), SKILLS)
        self.assertEqual(result['status'], 'METADATA_READY')
        self.assertEqual(result['native_agents_found'], 2)
        self.assertNotIn('PRIVATE', json.dumps(result))
        self.assertNotIn('12345', json.dumps(result))
        self.assertEqual(result['live_acceptance'], 'NOT_VERIFIED')

    def test_missing_exact_skill_or_agent_not_ready(self):
        for row in [claude({'crux:crux-mode'}), claude(agents=('crux-agent',)), claude(SKILLS | {'crux:stale'})]:
            self.assertEqual(SDK.summarize('claude', raw(row), SKILLS)['status'], 'NOT_READY')

    def test_errors_permissions_duplicate_invalid_schema(self):
        invalid = [b'garbage', b'[]', b'{}', b'']
        for change in ['subtype', 'request_id', 'pending_permission_requests']:
            row = claude()
            row['response'][change] = 'bad'
            invalid.append(raw(row))
        row = claude()
        row['response']['response']['commands'] = [{'name': 'same'}, {'name': 'same'}]
        invalid.append(raw(row))
        row = claude()
        row['response']['response']['agents'] = ['bad']
        invalid.append(raw(row))
        for value in invalid:
            with self.assertRaises(SDK.InspectionError):
                SDK.summarize('claude', value, SKILLS)

    def test_agy_selection_and_inventory_scope(self):
        result = SDK.summarize('antigravity', raw(agy()), SKILLS)
        self.assertEqual(result['status'], 'METADATA_READY')
        self.assertEqual(result['native_skill_inventory'], 'NOT_VERIFIED')
        self.assertEqual(result['user_data_isolation'], 'NOT_VERIFIED')
        self.assertEqual(result['hooks_disabled'], 'NOT_VERIFIED')
        self.assertTrue(result['normal_cli_initialization_side_effects_possible'])
        self.assertEqual(result['native_child_agent_inventory'], 'NOT_VERIFIED')
        self.assertNotIn('PRIVATE', json.dumps(result))
        self.assertEqual(SDK.summarize('antigravity', raw(agy('other')), SKILLS)['status'], 'NOT_READY')

    def test_unexpected_model_or_multiple_events_rejected(self):
        for harness, value in [('claude', claude()), ('antigravity', agy())]:
            with self.assertRaises(SDK.InspectionError):
                SDK.summarize(harness, raw(value) + b'\n' + raw({'type': 'assistant'}), SKILLS)
        with self.assertRaises(SDK.InspectionError):
            SDK.summarize('antigravity', raw({'event': 'result'}), SKILLS)

    def test_thai_render_has_evidence_limits(self):
        for harness, value in [('claude', claude()), ('antigravity', agy())]:
            text = SDK.render(SDK.summarize(harness, raw(value), SKILLS))
            self.assertIn('ชั่วคราว', text)
            self.assertIn('ยังไม่ยืนยัน', text)
            self.assertNotIn('PRIVATE', text)


class ProcessTests(unittest.IsolatedAsyncioTestCase):
    async def test_fake_process_input_and_stderr_privacy(self):
        script = 'import sys; data=sys.stdin.buffer.read(); sys.stderr.write("PRIVATE_SECRET"); sys.stdout.buffer.write(data)'
        result = await SDK.command([sys.executable, '-c', script], tempfile.gettempdir(), b'control only')
        self.assertEqual(result, b'control only')

    async def test_nonzero_and_output_limit(self):
        for script, expected in [('raise SystemExit(2)', 'native_exit_nonzero'),
                                 ('import sys; sys.stdout.buffer.write(b"x" * 2100000)', 'output_limit')]:
            with self.assertRaisesRegex(SDK.InspectionError, expected):
                await SDK.command([sys.executable, '-c', script], tempfile.gettempdir())

    async def test_timeout_kills_owned_process(self):
        with tempfile.TemporaryDirectory() as directory:
            marker = Path(directory) / 'pid'
            script = 'import os,time,pathlib; pathlib.Path("pid").write_text(str(os.getpid())); time.sleep(20)'
            with self.assertRaises(asyncio.TimeoutError):
                await asyncio.wait_for(SDK.command([sys.executable, '-c', script], directory), .3)
            self.assertTrue(marker.exists())
            with self.assertRaises(ProcessLookupError):
                os.kill(int(marker.read_text()), 0)

    async def test_missing_cli_is_bounded_error(self):
        with patch.object(SDK.shutil, 'which', return_value=None):
            with self.assertRaisesRegex(SDK.InspectionError, 'cli_not_found'):
                await SDK.inspect('antigravity')

    async def test_claude_argv_only_initialize(self):
        captured = []
        async def fake(argv, cwd, payload=b''):
            captured.append((argv, cwd, payload))
            expected = {'crux:' + path.parent.name for path in (SDK.ROOT / 'skills').glob('*/SKILL.md')}
            return raw(claude(expected))
        with patch.object(SDK.shutil, 'which', return_value='/fake/claude'), patch.object(SDK, 'command', fake):
            report = await SDK.inspect('claude')
        argv, cwd, payload = captured[0]
        self.assertEqual(json.loads(payload)['request']['subtype'], 'initialize')
        self.assertNotIn('user', json.loads(payload))
        self.assertIn('{"disableAllHooks":true}', argv)
        self.assertIn('{"mcpServers":{}}', argv)
        self.assertIn('--no-session-persistence', argv)
        self.assertFalse(Path(cwd).exists())
        self.assertEqual(report['model_requests'], 0)


if __name__ == '__main__':
    unittest.main()
