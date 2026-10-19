import asyncio
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import textwrap
import unittest
from unittest.mock import patch

SCRIPT = Path(__file__).resolve().parents[1] / 'scripts' / 'opencode-readiness.py'
spec = importlib.util.spec_from_file_location('opencode_readiness', SCRIPT)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
ADAPTER = SCRIPT.parents[1] / 'adapters' / 'opencode2'


def inventory(path=None, state='active'):
    return {'data': [{'id': 'crux', 'source': {'type': 'local', 'path': str(path or ADAPTER / 'index.js')},
                      'state': {'status': state}}]}


class ReadinessTests(unittest.TestCase):
    def test_exact_source_required_and_duplicate_rejected(self):
        ready = module.summarize(inventory(), ADAPTER)
        self.assertEqual(ready['status'], 'METADATA_READY')
        self.assertEqual(ready['live_acceptance'], 'NOT_VERIFIED')
        self.assertEqual(ready['sessions_created'], 0)
        self.assertEqual(module.summarize(inventory('/foreign/index.js'), ADAPTER)['status'], 'NOT_READY')
        self.assertEqual(module.summarize(inventory(state='error'), ADAPTER)['status'], 'NOT_READY')
        duplicate = inventory()
        duplicate['data'] *= 2
        self.assertEqual(module.summarize(duplicate, ADAPTER)['status'], 'NOT_READY')
        with self.assertRaises(module.InspectionError):
            module.summarize({'data': 'not-list'}, ADAPTER)

    def test_isolation_rejects_missing_duplicate_and_escaped_paths(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            valid = '\n'.join(key + '  ' + str(root / key) for key in module.PATH_KEYS)
            self.assertEqual(module.check_paths(valid, root), 10)
            for invalid in ['', valid + '\nhome /outside', valid.replace(str(root / 'db'), '/outside/db')]:
                with self.assertRaises(module.InspectionError):
                    module.check_paths(invalid, root)

    def test_no_credentials_or_runtime_injection_in_environment(self):
        with tempfile.TemporaryDirectory() as td, patch.dict(os.environ, {
                'ANTHROPIC_API_KEY': 'secret', 'NODE_OPTIONS': '--import /foreign',
                'OPENCODE_SERVER': 'http://foreign', 'HTTP_PROXY': 'http://foreign'}):
            env = module.isolated_environment(Path(td))
            self.assertNotIn('ANTHROPIC_API_KEY', env)
            self.assertNotIn('NODE_OPTIONS', env)
            self.assertNotIn('OPENCODE_SERVER', env)
            self.assertNotIn('HTTP_PROXY', env)
            self.assertTrue(Path(env['HOME']).is_relative_to(td))

    def test_schema_requires_unique_supported_read_only_operation(self):
        operation = {'get': {'operationId': 'plugin.list', 'parameters': [
            {'name': 'location', 'in': 'query', 'style': 'deepObject', 'explode': True}]}}
        self.assertEqual(module.plugin_operation({'paths': {'/api/plugin': operation}}), '/api/plugin')
        for paths in [{}, {'/api/plugin': operation, '/api/other': operation},
                      {'https://foreign': operation}, {'/api/plugin': {'get': {'operationId': 'plugin.list'}}}]:
            with self.assertRaises(module.InspectionError):
                module.plugin_operation({'paths': paths})

    def fake_cli(self, root, mode):
        program = root / 'fake-opencode2'
        program.write_text('#!/usr/bin/env python3\n' + textwrap.dedent('''\
            import http.server,json,os,pathlib,socket,socketserver,sys,threading
            MODE = MODE_VALUE
            # http.server.HTTPServer.server_bind() calls socket.getfqdn(), a reverse DNS lookup of
            # 127.0.0.1 that stalls for many seconds on GitHub macOS 15+ runners (actions/runner-images#12162,
            # actions/setup-python#1223), so the ready line arrived after the probe timeout. Bind without
            # it, and make any reintroduced lookup fail fast on every OS instead of stalling only on macOS.
            def no_reverse_dns(*args):raise AssertionError('reverse DNS lookup in fixture')
            socket.getfqdn=socket.gethostbyaddr=no_reverse_dns
            class LoopbackServer(http.server.HTTPServer):
                def server_bind(self):
                    socketserver.TCPServer.server_bind(self)
                    self.server_name,self.server_port=self.server_address[:2]
            if sys.argv[1:] == ['--version']:
                print('opencode fixture');sys.exit()
            if sys.argv[1:] == ['debug','paths']:
                root=pathlib.Path(os.environ['HOME']).parent
                for key in ['home','data','cache','config','state','tmp','bin','log','repos','db']:
                    print(key, '/foreign' if MODE=='escape' and key=='db' else str(root/key))
                sys.exit()
            assert sys.argv[1:] == ['serve','--stdio','--hostname','127.0.0.1','--port','0']
            pathlib.Path(__file__+'.pid').write_text(str(os.getpid()))
            if MODE=='hang':
                sys.stdin.read();sys.exit()
            adapter=pathlib.Path('.opencode/plugins/crux').resolve()
            class Handler(http.server.BaseHTTPRequestHandler):
                def log_message(self,*args):pass
                def do_POST(self):raise AssertionError('model/session request forbidden')
                def do_GET(self):
                    assert self.headers.get('Authorization')
                    if self.path=='/openapi.json':
                        value={'paths':{'/api/plugin':{'get':{'operationId':'plugin.list','parameters':[
                            {'name':'location','in':'query','style':'deepObject','explode':True}]}}}}
                        if MODE=='schema':value={'paths':{}}
                    else:
                        assert self.path.startswith('/api/plugin?location%5Bdirectory%5D=')
                        value={'data':[{'id':'crux','source':{'type':'local','path':str(adapter/'index.js')},
                                       'state':{'status':'error' if MODE=='inactive' else 'active'}}]}
                    body=json.dumps(value).encode();self.send_response(200);self.end_headers();self.wfile.write(body)
            server=LoopbackServer(('127.0.0.1',0),Handler)
            threading.Thread(target=server.serve_forever,daemon=True).start()
            print(json.dumps({'url':'http://127.0.0.1:'+str(server.server_port)}),flush=True)
            sys.stdin.read();server.shutdown();server.server_close()
        ''').replace('MODE_VALUE', repr(mode)))
        program.chmod(0o700)
        return str(program)

    def run_fake(self, mode, timeout=5):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            executable = self.fake_cli(root, mode)
            try:
                with patch.object(module.shutil, 'which', return_value=executable):
                    return asyncio.run(module.inspect(ADAPTER, timeout))
            finally:
                marker = Path(executable + '.pid')
                if marker.exists():
                    with self.assertRaises(ProcessLookupError):
                        os.kill(int(marker.read_text()), 0)

    def test_private_server_metadata_success_and_eof_cleanup(self):
        report = self.run_fake('ready')
        self.assertEqual(report['status'], 'METADATA_READY')
        self.assertEqual(report['isolated_paths'], 10)
        self.assertEqual(report['model_requests'], 0)
        self.assertEqual(report['cleanup'], {'method': 'stdin_eof', 'exit_code': 0, 'reaped': True})
        self.assertTrue(report['temporary_root_removed'])

    def test_private_server_inactive_is_not_ready(self):
        self.assertEqual(self.run_fake('inactive')['status'], 'NOT_READY')

    def test_private_server_schema_and_isolation_fail_closed(self):
        for mode in ['schema', 'escape']:
            with self.subTest(mode=mode), self.assertRaises(module.InspectionError):
                self.run_fake(mode)

    def test_private_server_startup_timeout_is_bounded_and_reaped(self):
        with self.assertRaises(asyncio.TimeoutError):
            self.run_fake('hang', timeout=1)


if __name__ == '__main__':
    unittest.main()
