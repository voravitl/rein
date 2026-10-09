"""Advisor attribution and route gates, using fake CLIs without model calls."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


SCRIPT = Path(__file__).with_name('advise.sh')


class AdvisorRoutingTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.calls = self.root / 'calls.jsonl'
        self.task = self.root / 'task.md'
        self.task.write_text('Review this task without writing files.\n')
        self.out = self.root / 'answer.md'
        self.env = dict(os.environ, PATH=str(self.root) + os.pathsep + os.environ['PATH'],
                        REIN=str(self.root / 'rein'), TEST_CALLS=str(self.calls),
                        PIPELINE_LOGDIR=str(self.root / 'logs'), ADVISE_NO_PACK='1',
                        ADVISE_RUN='run-r', ADVISE_TASK='contract-task',
                        ADVISE_PROVIDER='codex', ADVISE_MODEL='gpt-test')
        self.write_cli('rein', '''import json,os,sys
with open(os.environ['TEST_CALLS'],'a') as f: f.write(json.dumps(sys.argv[1:])+'\\n')
if sys.argv[1:3]==['route','check']: sys.exit(int(os.environ.get('ROUTE_EXIT','0')))
if sys.argv[1:3]==['ledger','call']: sys.exit(int(os.environ.get('LEDGER_EXIT','0')))
raise SystemExit(2)
''')
        self.write_cli('codex', '''import json,os,sys
with open(os.environ['TEST_CALLS'],'a') as f: f.write(json.dumps(['model-call']+sys.argv[1:])+'\\n')
from pathlib import Path
Path(sys.argv[sys.argv.index('-o')+1]).write_text('Verified answer\\n')
print('tokens used\\n123')
''')
        self.write_cli('claude', '''import json,os,sys
with open(os.environ['TEST_CALLS'],'a') as f: f.write(json.dumps(['claude-call',os.getcwd()]+sys.argv[1:])+'\\n')
print('Claude verified answer')
''')
        self.write_cli('timeout', '''import os,sys
os.execvp(sys.argv[2],sys.argv[2:])
''')

    def write_cli(self, name, code):
        import sys
        path = self.root / name
        path.write_text('#!' + sys.executable + '\n' + code)
        path.chmod(0o755)

    def invoke(self):
        result = subprocess.run(['bash', str(SCRIPT), 'claim-auditor', str(self.root),
                                 str(self.task), str(self.out)], env=self.env,
                                capture_output=True, text=True)
        calls = [json.loads(line) for line in self.calls.read_text().splitlines()] if self.calls.exists() else []
        return result, calls

    def test_missing_attribution_or_selection_blocks_model(self):
        for name in ('ADVISE_RUN', 'ADVISE_TASK', 'ADVISE_PROVIDER', 'ADVISE_MODEL'):
            with self.subTest(name=name):
                original = self.env.pop(name)
                result, calls = self.invoke()
                self.env[name] = original
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(any(call[0] == 'model-call' for call in calls))

    def test_route_mismatch_blocks_model_and_preserves_prior_answer(self):
        self.env['ROUTE_EXIT'] = '1'
        self.out.write_text('Previous report\n')
        result, calls = self.invoke()
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(any(call[0] == 'model-call' for call in calls))
        self.assertEqual(self.out.read_text(), 'Previous report\n')

    def test_explicit_route_is_checked_before_model_and_attributed_to_ledger(self):
        result, calls = self.invoke()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(calls[0], ['route', 'check', '--task', 'contract-task', '--run',
                                    'run-r', '--agent', 'codex', '--model', 'gpt-test',
                                    '--effort', 'high', '--phase', 'review', '--worktree', str(self.root)])
        self.assertEqual(calls[1][0], 'model-call')
        self.assertIn('model_reasoning_effort=high', calls[1])
        self.assertEqual(calls[2][:2], ['ledger', 'call'])
        for flag, value in (('--task', 'contract-task'), ('--run', 'run-r'),
                            ('--provider', 'codex'), ('--model', 'gpt-test'), ('--tokens', '123')):
            self.assertEqual(calls[2][calls[2].index(flag) + 1], value)

    def test_claude_review_has_fixed_read_only_flags_and_attribution(self):
        self.env['ADVISE_PROVIDER'] = 'claude'
        self.env['ADVISE_MODEL'] = 'claude-opus-5-1'
        result, calls = self.invoke()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(calls[0][calls[0].index('--agent') + 1], 'claude')
        self.assertEqual(calls[1][0], 'claude-call')
        self.assertEqual(Path(calls[1][1]).resolve(), self.root.resolve())
        self.assertEqual(calls[1][2:-1], ['-p', '--model', 'claude-opus-5-1',
                                       '--restricted', '--tools', 'Read,Grep,Glob',
                                       '--strict-mcp-config'])
        self.assertEqual(self.out.read_text(), 'Claude verified answer\n')
        ledger = calls[2]
        for flag, value in (('--task', 'contract-task'), ('--run', 'run-r'),
                            ('--provider', 'claude'), ('--model', 'claude-opus-5-1')):
            self.assertEqual(ledger[ledger.index(flag) + 1], value)
        self.assertNotIn('--tokens', ledger)
        self.assertNotIn('--credits', ledger)

    def test_effort_is_checked_against_the_receipt_and_applied_to_the_call(self):
        # the effort an automatic receipt was chosen on is the effort the review runs at: route check sees it, the CLI gets it
        self.env['ADVISE_EFFORT'] = 'xhigh'
        result, calls = self.invoke()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(calls[0][calls[0].index('--effort') + 1], 'xhigh')
        self.assertIn('model_reasoning_effort=xhigh', calls[1])
        self.assertNotIn('model_reasoning_effort=high', calls[1])
        self.calls.unlink()
        self.env.update(ADVISE_PROVIDER='claude', ADVISE_MODEL='claude-opus-5-1', ADVISE_EFFORT='max')
        result, calls = self.invoke()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(calls[0][calls[0].index('--effort') + 1], 'max')
        self.assertEqual(calls[1][calls[1].index('--effort') + 1], 'max')

    def test_kiro_has_no_effort_and_a_receipt_effort_blocks_the_call(self):
        self.env.update(ADVISE_PROVIDER='kiro', ADVISE_EFFORT='high')
        result, calls = self.invoke()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(calls, [], 'nothing may be checked or called when the effort cannot be applied')

    def test_post_call_ledger_failure_is_failure(self):
        self.env['LEDGER_EXIT'] = '1'
        result, calls = self.invoke()
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue(any(call[0] == 'model-call' for call in calls))
        self.assertIn('ledger', result.stdout + result.stderr)


if __name__ == '__main__':
    unittest.main()
