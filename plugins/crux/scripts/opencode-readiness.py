#!/usr/bin/env python3
"""Inspect a fresh isolated OpenCode2 Crux source load without model turns."""
import argparse
import asyncio
import base64
import json
import os
from pathlib import Path
import secrets
import shutil
import sys
import tempfile
import urllib.parse
import urllib.request

LIMIT = 8 * 1024 * 1024
PATH_KEYS = {'home', 'data', 'cache', 'config', 'state', 'tmp', 'bin', 'log', 'repos', 'db'}


class InspectionError(Exception):
    pass


def isolated_environment(root):
    # Do not inherit API keys, proxy settings, NODE_OPTIONS, shell configuration,
    # server URLs or CLI overrides. HOME is a child-process setting only.
    env = {'PATH': os.environ.get('PATH', '/usr/bin:/bin'), 'LANG': 'en_US.UTF-8'}
    for key, leaf in [('HOME', 'home'), ('OPENCODE_TEST_HOME', 'home'),
                      ('XDG_CONFIG_HOME', 'config'), ('XDG_DATA_HOME', 'data'),
                      ('XDG_STATE_HOME', 'state'), ('XDG_CACHE_HOME', 'cache'), ('TMPDIR', 'tmp')]:
        directory = root / leaf
        directory.mkdir(exist_ok=True)
        env[key] = str(directory)
    return env


def check_paths(output, root):
    paths = {}
    for line in output.splitlines():
        parts = line.split(None, 1)
        if len(parts) != 2 or parts[0] not in PATH_KEYS or parts[0] in paths:
            raise InspectionError('unexpected_paths_schema')
        value = Path(parts[1])
        if not value.is_absolute() or not value.resolve().is_relative_to(root.resolve()):
            raise InspectionError('isolation_not_proven')
        paths[parts[0]] = value
    if set(paths) != PATH_KEYS:
        raise InspectionError('isolation_not_proven')
    return len(paths)


def plugin_operation(spec):
    if not isinstance(spec, dict) or not isinstance(spec.get('paths'), dict):
        raise InspectionError('invalid_openapi')
    matches = []
    for path, methods in spec['paths'].items():
        if isinstance(methods, dict) and isinstance(methods.get('get'), dict):
            operation = methods['get']
            if operation.get('operationId') == 'plugin.list':
                matches.append((path, operation))
    if len(matches) != 1:
        raise InspectionError('plugin_list_not_unique')
    path, operation = matches[0]
    if not isinstance(path, str) or not path.startswith('/api/') or '?' in path or '{' in path:
        raise InspectionError('invalid_plugin_path')
    parameters = operation.get('parameters', [])
    if not any(isinstance(p, dict) and p.get('name') == 'location' and p.get('in') == 'query'
               and p.get('style') == 'deepObject' and p.get('explode') is True for p in parameters):
        raise InspectionError('unsupported_location_schema')
    return path


def summarize(payload, adapter):
    entries = payload.get('data') if isinstance(payload, dict) else None
    if not isinstance(entries, list) or any(not isinstance(row, dict) for row in entries):
        raise InspectionError('invalid_plugin_inventory')
    selected = [row for row in entries if row.get('id') == 'crux']
    ready = False
    if len(selected) == 1:
        row = selected[0]
        source, state = row.get('source'), row.get('state')
        if isinstance(source, dict) and isinstance(state, dict):
            source_path = source.get('path')
            ready = (source.get('type') == 'local' and isinstance(source_path, str)
                     and Path(source_path).is_absolute()
                     and Path(source_path).resolve() == (adapter / 'index.js').resolve()
                     and state.get('status') == 'active')
    return {'schema': 1, 'harness': 'opencode2', 'scope': 'temporary_isolated_source_load',
            'status': 'METADATA_READY' if ready else 'NOT_READY', 'matching_plugins': len(selected),
            'exact_source_active': ready, 'live_acceptance': 'NOT_VERIFIED',
            'prompt_activation': 'NOT_VERIFIED', 'context_hook': 'NOT_VERIFIED',
            'user_installation': 'NOT_VERIFIED', 'model_requests': 0, 'sessions_created': 0,
            'global_daemon_used': False}


async def stop(process):
    if process.stdin:
        process.stdin.close()
    method = 'stdin_eof'
    try:
        await asyncio.wait_for(process.wait(), 2)
    except asyncio.TimeoutError:
        method = 'terminate'
        process.terminate()
        try:
            await asyncio.wait_for(process.wait(), 1)
        except asyncio.TimeoutError:
            method = 'kill'
            process.kill()
            await asyncio.wait_for(process.wait(), 1)
    return {'method': method, 'exit_code': process.returncode, 'reaped': True}


async def small_command(executable, args, project, env):
    process = await asyncio.create_subprocess_exec(executable, *args, cwd=project, env=env,
                                                  stdin=asyncio.subprocess.DEVNULL,
                                                  stdout=asyncio.subprocess.PIPE,
                                                  stderr=asyncio.subprocess.DEVNULL)
    try:
        output = await asyncio.wait_for(process.stdout.read(65537), 10)
        if len(output) > 65536:
            raise InspectionError('command_output_limit')
        code = await asyncio.wait_for(process.wait(), 2)
        if code:
            raise InspectionError('command_failed')
        return output.decode('utf-8')
    finally:
        await stop(process)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise InspectionError('unexpected_redirect')


def fetch(base, path, authorization):
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    request = urllib.request.Request(base + path, headers={'Authorization': authorization})
    with opener.open(request, timeout=3) as response:
        body = response.read(LIMIT + 1)
    if len(body) > LIMIT:
        raise InspectionError('http_output_limit')
    return json.loads(body)


async def probe(executable, adapter, root):
    project = root / 'project'
    project.mkdir()
    env = isolated_environment(root)
    link = project / '.opencode' / 'plugins' / 'crux'
    link.parent.mkdir(parents=True)
    link.symlink_to(adapter, target_is_directory=True)
    # Explicit native source registration; the symlink preserves the full Crux
    # tree and shared imports. No user config is read or written.
    (project / 'opencode.jsonc').write_text(json.dumps({'plugins': [str(link)]}))
    await small_command(executable, ['--version'], project, env)
    path_count = check_paths(await small_command(executable, ['debug', 'paths'], project, env), root)
    password = secrets.token_urlsafe(32)
    env['OPENCODE_PASSWORD'] = password
    process = await asyncio.create_subprocess_exec(
        executable, 'serve', '--stdio', '--hostname', '127.0.0.1', '--port', '0', cwd=project, env=env,
        stdin=asyncio.subprocess.PIPE, stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.DEVNULL, limit=65536)
    try:
        ready = json.loads(await process.stdout.readline())
        base = ready.get('url') if isinstance(ready, dict) else None
        if not isinstance(base, str):
            raise InspectionError('invalid_server_ready')
        url = urllib.parse.urlsplit(base)
        if (url.scheme != 'http' or url.hostname != '127.0.0.1' or not url.port
                or url.username or url.password or url.path not in ('', '/') or url.query or url.fragment):
            raise InspectionError('unexpected_server_url')
        base = base.rstrip('/')
        authorization = 'Basic ' + base64.b64encode(('opencode:' + password).encode()).decode()
        spec = await asyncio.to_thread(fetch, base, '/openapi.json', authorization)
        path = plugin_operation(spec) + '?' + urllib.parse.urlencode({'location[directory]': str(project)})
        for _ in range(60):
            if process.returncode is not None:
                raise InspectionError('server_exited')
            report = summarize(await asyncio.to_thread(fetch, base, path, authorization), adapter)
            if report['status'] == 'METADATA_READY' or report['matching_plugins']:
                break
            await asyncio.sleep(.25)
        report['isolated_paths'] = path_count
    finally:
        cleanup = await stop(process)
    report['cleanup'] = cleanup
    if cleanup['exit_code'] != 0 or cleanup['method'] != 'stdin_eof':
        raise InspectionError('server_shutdown_not_clean')
    return report


async def inspect(adapter, timeout):
    executable = shutil.which('opencode2')
    if not executable:
        raise InspectionError('opencode2_not_found')
    with tempfile.TemporaryDirectory(prefix='crux-opencode-readiness-') as directory:
        report = await asyncio.wait_for(probe(executable, adapter, Path(directory)), timeout)
    report['temporary_root_removed'] = True
    return report


def render(report):
    if report['status'] == 'ERROR':
        return 'ตรวจ OpenCode2 ไม่สำเร็จ (' + report['error'] + ')\nยังไม่ยืนยันความพร้อม'
    lines = ['OpenCode2: ' + ('โหลด source ของ Crux สำเร็จในพื้นที่ทดสอบ' if report['status'] == 'METADATA_READY'
                              else 'ยังไม่พบ source ของ Crux ที่ active ตรงตามกำหนด'),
             'แยกข้อมูลออกจากบัญชีและ config ผู้ใช้ ไม่ใช้ daemon หรือส่ง prompt ให้โมเดล',
             'ยังไม่ยืนยันการติดตั้งของผู้ใช้ การเปิดโหมด การตอบภาษาไทย หรือการส่งงานข้าม provider']
    return '\n'.join(lines)


def main(argv=None):
    parser = argparse.ArgumentParser(description='ตรวจ Crux ผ่าน private OpenCode2 โดยไม่เรียกโมเดล')
    parser.add_argument('--crux-root', type=Path, default=Path(__file__).resolve().parents[1])
    parser.add_argument('--timeout', type=int, default=45)
    parser.add_argument('--format', choices=('text', 'json'), default='text')
    args = parser.parse_args(argv)
    adapter = args.crux_root.resolve() / 'adapters' / 'opencode2'
    if not (adapter / 'index.js').is_file() or not 1 <= args.timeout <= 60:
        parser.error('ต้องมี source ของ Crux และ timeout 1–60 วินาที')
    try:
        report = asyncio.run(inspect(adapter, args.timeout))
    except (InspectionError, OSError, ValueError, asyncio.TimeoutError) as error:
        report = {'schema': 1, 'harness': 'opencode2', 'scope': 'temporary_isolated_source_load',
                  'status': 'ERROR', 'error': str(error) if isinstance(error, InspectionError) else type(error).__name__,
                  'live_acceptance': 'NOT_VERIFIED', 'model_requests': 0}
    print(json.dumps(report, ensure_ascii=False, indent=2) if args.format == 'json' else render(report))
    return {'METADATA_READY': 0, 'NOT_READY': 2, 'ERROR': 1}[report['status']]


if __name__ == '__main__':
    sys.exit(main())
