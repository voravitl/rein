#!/usr/bin/env python3
"""Inspect source-selected Claude/Antigravity metadata without sending a user turn."""
import argparse
import asyncio
import json
import os
from pathlib import Path
import shutil
import signal
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]
LIMIT = 2 * 1024 * 1024
REQUEST_ID = 'crux-discovery'


class InspectionError(Exception):
    pass


async def command(argv, cwd, payload=b''):
    process = await asyncio.create_subprocess_exec(
        *argv, cwd=cwd, stdin=asyncio.subprocess.PIPE, stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.DEVNULL, start_new_session=True)
    try:
        process.stdin.write(payload)
        await process.stdin.drain()
        process.stdin.close()
        chunks, size = [], 0
        while True:
            chunk = await process.stdout.read(65536)
            if not chunk:
                break
            size += len(chunk)
            if size > LIMIT:
                raise InspectionError('output_limit')
            chunks.append(chunk)
        code = await process.wait()
        if code:
            raise InspectionError('native_exit_nonzero')
        return b''.join(chunks)
    finally:
        if process.returncode is None:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            await process.wait()


def messages(raw):
    try:
        rows = [json.loads(line) for line in raw.decode('utf-8').splitlines() if line.strip()]
    except (ValueError, UnicodeError):
        raise InspectionError('invalid_json') from None
    if not rows or any(not isinstance(row, dict) for row in rows):
        raise InspectionError('invalid_schema')
    return rows


def named(rows):
    if not isinstance(rows, list) or any(not isinstance(row, dict) or
                                       not isinstance(row.get('name'), str) for row in rows):
        raise InspectionError('invalid_inventory')
    names = [row['name'] for row in rows]
    if len(names) != len(set(names)):
        raise InspectionError('duplicate_inventory')
    return set(names)


def summarize(harness, raw, expected):
    rows = messages(raw)
    result = {'schema': 1, 'harness': harness, 'scope': 'temporary_source_selection_metadata_only',
              'default_installation': 'NOT_VERIFIED', 'native_source_binding': 'NOT_VERIFIED',
              'live_acceptance': 'NOT_VERIFIED',
              'model_requests': 0, 'trust_modified': False}
    # Any result/user/assistant/tool event contradicts this metadata-only protocol.
    if harness == 'claude':
        if any(row.get('type') != 'control_response' for row in rows) or len(rows) != 1:
            raise InspectionError('unexpected_protocol_event')
        envelope = rows[0].get('response')
        if not isinstance(envelope, dict) or envelope.get('request_id') != REQUEST_ID or envelope.get('subtype') != 'success':
            raise InspectionError('initialize_failed')
        if envelope.get('pending_permission_requests') != [] or envelope.get('pending_user_dialog_requests') != []:
            raise InspectionError('unexpected_permission_request')
        data = envelope.get('response')
        if not isinstance(data, dict):
            raise InspectionError('invalid_schema')
        skills, agents = named(data.get('commands')), named(data.get('agents'))
        required_agents = {'crux:crux-agent', 'crux:crux-comment-reviewer'}
        missing_skills = sorted(expected - skills)
        missing_agents = sorted(required_agents - agents)
        unexpected_skills = sorted(name for name in skills if name.startswith('crux:') and name not in expected)
        result.update(hooks_disabled=True, mcp_disabled=True,
                      native_skills_found=len(expected & skills), native_agents_found=len(required_agents & agents),
                      missing_required_skills=missing_skills, missing_required_agents=missing_agents,
                      unexpected_skill_count=len(unexpected_skills))
        ready = not missing_skills and not missing_agents and not unexpected_skills
    else:
        if len(rows) != 1 or rows[0].get('event') != 'init' or not isinstance(rows[0].get('init'), dict):
            raise InspectionError('unexpected_protocol_event')
        init = rows[0]['init']
        if not isinstance(init.get('agent'), str) or not isinstance(init.get('tools'), list):
            raise InspectionError('invalid_schema')
        ready = init['agent'] == 'crux'
        result.update(selected_agent='crux' if ready else 'OTHER',
                      user_data_isolation='NOT_VERIFIED', hooks_disabled='NOT_VERIFIED',
                      mcp_disabled='NOT_VERIFIED', normal_cli_initialization_side_effects_possible=True,
                      bundle_skills=len(expected),
                      bundle_agents=3, native_skill_inventory='NOT_VERIFIED', native_child_agent_inventory='NOT_VERIFIED')
    result['status'] = 'METADATA_READY' if ready else 'NOT_READY'
    return result


async def inspect(harness):
    executable = shutil.which('claude' if harness == 'claude' else 'agy')
    if not executable:
        raise InspectionError('cli_not_found')
    expected = {'crux:' + path.parent.name for path in (ROOT / 'skills').glob('*/SKILL.md')}
    if not {'crux:crux-mode', 'crux:thai-communication'} <= expected:
        raise InspectionError('source_missing_required_skills')
    with tempfile.TemporaryDirectory(prefix='crux-sdk-readiness-') as temporary:
        if harness == 'claude':
            argv = [executable, '--setting-sources', '', '--settings', '{"disableAllHooks":true}',
                    '--plugin-dir', str(ROOT), '--strict-mcp-config', '--mcp-config', '{"mcpServers":{}}',
                    '--no-session-persistence', '--input-format', 'stream-json', '--output-format', 'stream-json',
                    '--verbose', '--print']
            payload = (json.dumps({'type': 'control_request', 'request_id': REQUEST_ID,
                                  'request': {'subtype': 'initialize'}}) + '\n').encode()
        else:
            node = shutil.which('node')
            if not node:
                raise InspectionError('node_not_found')
            destination = Path(temporary) / '.agents/agents/crux'
            await command([node, str(ROOT / 'adapters/antigravity/build.mjs'), str(destination)], temporary)
            expected_files = ['agent.md', 'agents/crux.md', 'agents/crux-agent.md', 'agents/comment-reviewer.md']
            if any(not (destination / item).is_file() for item in expected_files):
                raise InspectionError('incomplete_bundle')
            bundle_skills = {'crux:' + path.parent.name for path in (destination / 'skills').glob('*/SKILL.md')}
            if bundle_skills != expected:
                raise InspectionError('incomplete_bundle')
            argv = [executable, '--agent', 'crux', '--input-format', 'stream-json', '--output-format', 'stream-json', '--print', '']
            payload = b''  # EOF only: no user message and no agent loop.
        return summarize(harness, await command(argv, temporary, payload), expected)


def render(report):
    label = 'Claude Code' if report['harness'] == 'claude' else 'Antigravity'
    if report['status'] == 'ERROR':
        first = f"{label}: ตรวจไม่สำเร็จ ({report['error']})"
    elif report['status'] == 'NOT_READY':
        first = f'{label}: ยังไม่พบ metadata ของ Crux ครบตามเกณฑ์'
    else:
        first = f'{label}: เลือก Crux จาก source ชั่วคราวสำเร็จตาม metadata'
    lines = [first]
    if 'native_skills_found' in report:
        lines.append(f"พบ skills {report['native_skills_found']} และ agents {report['native_agents_found']} ที่จำเป็น")
    if report['harness'] == 'antigravity' and report['status'] != 'ERROR':
        lines.extend(['ยืนยันเฉพาะ main agent; ยังไม่ยืนยันว่า runtime โหลด skills และ child agents ครบ',
                      'ใช้ profile ปกติของ AGY; CLI อาจเก็บข้อมูลเริ่มต้นตามปกติ ไม่แก้ config/auth/trust',
                      'ยังไม่ยืนยันการแยก userdata หรือการปิด hooks/MCP ส่วนกลาง'])
    lines.extend(['ยังไม่ยืนยันการติดตั้งถาวร การตอบภาษาไทย หรือการส่งงานจริง',
                  'ไม่ส่ง prompt ไม่แก้ trust; CLI อาจบันทึก metadata ของการเริ่ม session'])
    return '\n'.join(lines)


def main(argv=None):
    parser = argparse.ArgumentParser(description='ตรวจ metadata ของ Crux โดยไม่ส่ง prompt ให้โมเดล')
    parser.add_argument('--harness', choices=('claude', 'antigravity'), required=True)
    parser.add_argument('--timeout', type=int, default=30)
    parser.add_argument('--format', choices=('text', 'json'), default='text')
    args = parser.parse_args(argv)
    if not 1 <= args.timeout <= 60:
        parser.error('timeout ต้องอยู่ระหว่าง 1–60 วินาที')
    try:
        async def run():
            return await asyncio.wait_for(inspect(args.harness), args.timeout)
        report = asyncio.run(run())
    except (InspectionError, OSError, ValueError, asyncio.TimeoutError) as error:
        report = {'schema': 1, 'harness': args.harness, 'status': 'ERROR',
                  'scope': 'temporary_source_selection_metadata_only', 'default_installation': 'NOT_VERIFIED',
                  'error': str(error) if isinstance(error, InspectionError) else type(error).__name__,
                  'live_acceptance': 'NOT_VERIFIED', 'model_requests': 0, 'trust_modified': False}
    print(json.dumps(report, ensure_ascii=False, indent=2) if args.format == 'json' else render(report))
    return {'METADATA_READY': 0, 'NOT_READY': 2, 'ERROR': 1}[report['status']]


if __name__ == '__main__':
    sys.exit(main())
