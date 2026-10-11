#!/usr/bin/env python3
"""Release settled supervised sessions and verify resource closure for one Run.

usage: orca_cleanup.py <run_id> [--stop ctx_a,ctx_b] [--check-worktree <exact-selector>] [--dry-run]
  --stop requires positive exited liveness and an exact worker-stop nextAction.
"""
import argparse
import json
import os
import subprocess
import sys
from pathlib import Path

# Match Orca's recovery executable resolution, once for the whole invocation.
CLI = (os.environ.get('ORCA_CLI_COMMAND', '').strip() or
       ('orca-dev' if os.environ.get('ORCA_DEV_REPO_ROOT') else
        'orca-ide' if sys.platform == 'linux' else 'orca'))


RUNTIME = None


def orca(*args):
    global RUNTIME
    try:
        response = subprocess.run([CLI, *args, '--json'], capture_output=True, text=True)
        if response.returncode != 0:
            raise ValueError(f'exit {response.returncode}: {response.stderr[-300:]} {response.stdout[-300:]}')
        result = json.loads(response.stdout)
        if not isinstance(result, dict) or result.get('ok') is not True or result.get('error'):
            raise ValueError(f'failed CLI response: {result}')
        meta = result.get('_meta')
        runtime = meta.get('runtimeId') if isinstance(meta, dict) else None
        if not isinstance(runtime, str) or not runtime or (RUNTIME is not None and runtime != RUNTIME):
            raise ValueError('missing or changed Orca runtime')
        RUNTIME = runtime
        return result
    except (OSError, ValueError) as error:
        raise SystemExit(f'[orca_cleanup] {args[1]} failed: {error}')


ap = argparse.ArgumentParser()
ap.add_argument('run')
ap.add_argument('--stop', default='')
ap.add_argument('--dry-run', action='store_true')
ap.add_argument('--close-operator', nargs=3, action='append', default=[],
                metavar=('LAUNCH', 'COMPLETED_SHOW', 'REPORT'),
                help='coordinator authorizes completed operator session using archived evidence')
ap.add_argument('--check-worktree', action='append', default=[],
                help='finished run worktree: require a complete empty terminal inventory')
a = ap.parse_args()
stop = {s for s in a.stop.split(',') if s}

def list_workers(run_id=None, runtime=None):
    workers = []
    seen = set()
    cursors = set()
    cursor = None
    # Complete enumeration before making any cleanup decision or mutation.
    while True:
        args = ['orchestration', 'worker-list', '--run', run_id or a.run, '--include-remote']
        if cursor is not None:
            args += ['--cursor', cursor]
        receipt = orca(*args)
        result = receipt.get('result')
        scope = result.get('scope') if isinstance(result, dict) else None
        if (not isinstance(scope, dict) or scope.get('source') != 'flag' or
                scope.get('run') != (run_id or a.run) or
                (runtime is not None and receipt['_meta']['runtimeId'] != runtime)):
            raise SystemExit('[orca_cleanup] FAILED: worker inventory scope/runtime unproven')
        if not isinstance(result, dict) or not isinstance(result.get('workers'), list):
            raise SystemExit('[orca_cleanup] malformed worker-list result')
        for row in result['workers']:
            if (not isinstance(row, dict) or not isinstance(row.get('dispatchId'), str) or
                    not row['dispatchId'] or row['dispatchId'] in seen or
                    not isinstance(row.get('projection'), dict)):
                raise SystemExit('[orca_cleanup] malformed or duplicate worker row')
            seen.add(row['dispatchId'])
            workers.append(row)
        page = result.get('page')
        if not isinstance(page, dict) or type(page.get('hasMore')) is not bool:
            raise SystemExit('[orca_cleanup] missing or malformed pagination')
        if not page['hasMore']:
            break
        cursor = page.get('nextCursor')
        if not isinstance(cursor, str) or not cursor or cursor in cursors:
            raise SystemExit('[orca_cleanup] missing or repeated page cursor')
        cursors.add(cursor)
    return workers


workers = list_workers()
seen = {w['dispatchId'] for w in workers}

failed = []
actions = []
for w in workers:
    dispatch = w['dispatchId']
    projection = w['projection']
    liveness = projection.get('liveness')
    exited = isinstance(liveness, dict) and liveness.get('verdict') == 'exited'
    recommendation = projection.get('nextAction')
    argv = recommendation.get('argv') if isinstance(recommendation, dict) else None
    command = 'worker-stop' if dispatch in stop else 'worker-release'
    expected = ['orchestration', command, '--dispatch', dispatch]
    settled = (w.get('dispatchStatus') in ('succeeded', 'failed') or
               projection.get('outcome') in ('succeeded', 'failed'))
    if (exited if dispatch in stop else settled) and argv == expected:
        actions.append(expected)
    elif dispatch in stop:
        failed.append(f'refused stop {dispatch}: requires exited liveness and exact nextAction {expected}; inspect worker-list/worker-show')
    elif (not a.dry_run and settled and w.get('workerState') != 'unsupervised' and
          w.get('terminalState') != 'released'):
        failed.append(f'session closure unconfirmed for {dispatch}: inspect receipts and follow recovery')
    else:
        print('keep   ', dispatch, '(no proven cleanup recommendation)')
for dispatch in sorted(stop - seen):
    failed.append(f'refused stop {dispatch}: absent from complete worker-list')
if failed:
    raise SystemExit(f"[orca_cleanup] FAILED: {'; '.join(failed)}")
for action in actions:
    print(action[1], action[-1], '(dry-run)' if a.dry_run else '')
    if not a.dry_run:
        receipt = orca(*action)
        print('  ->', json.dumps(receipt))
        # Recovery receipts remain authoritative; never replace them with terminal close.

if not a.dry_run:
    releases = {action[-1] for action in actions}
    if releases:
        current = {w['dispatchId']: w for w in list_workers()}
        pending = sorted(dispatch for dispatch in releases
                         if current.get(dispatch, {}).get('terminalState') != 'released')
        if pending:
            raise SystemExit('[orca_cleanup] FAILED: session closure unconfirmed for ' +
                             ', '.join(pending) + '; inspect receipts and follow recovery; never force-close a supervised terminal')

def terminal_inventory(selector):
    result = orca('terminal', 'list', '--worktree', selector).get('result')
    scope = result.get('hostScope') if isinstance(result, dict) else None
    if (not isinstance(result, dict) or not isinstance(result.get('terminals'), list) or
            result.get('truncated') is not False or type(result.get('totalCount')) is not int or
            result['totalCount'] != len(result['terminals']) or not isinstance(scope, dict) or
            not isinstance(scope.get('hostIds'), list) or 'local' not in scope['hostIds'] or
            any(not isinstance(host, str) for host in scope['hostIds']) or
            scope.get('omittedHostIds') != [] or
            any(not isinstance(t, dict) or not isinstance(t.get('handle'), str)
                for t in result['terminals'])):
        raise SystemExit('[orca_cleanup] FAILED: incomplete terminal inventory for ' + selector)
    return result['terminals']


def operator_ownership(runtime):
    coordinators = set()
    run_ids = set()
    cursors = set()
    cursor = None
    while True:
        args = ['orchestration', 'run-list']
        if cursor is not None:
            args += ['--cursor', cursor]
        receipt = orca(*args)
        result = receipt.get('result')
        if (receipt.get('_meta', {}).get('runtimeId') != runtime or not isinstance(result, dict) or
                not isinstance(result.get('runs'), list) or 'nextCursor' not in result):
            raise ValueError('incomplete cross-run ownership inventory')
        for row in result['runs']:
            if (not isinstance(row, dict) or not isinstance(row.get('id'), str) or not row['id'] or
                    row['id'] in run_ids or 'coordinator_handle' not in row or
                    (row['coordinator_handle'] is not None and not isinstance(row['coordinator_handle'], str))):
                raise ValueError('malformed/duplicate run ownership')
            run_ids.add(row['id'])
            if row['coordinator_handle']:
                coordinators.add(row['coordinator_handle'])
        cursor = result['nextCursor']
        if cursor is None:
            break
        if not isinstance(cursor, str) or not cursor or cursor in cursors:
            raise ValueError('invalid/repeated run cursor')
        cursors.add(cursor)
    if a.run not in run_ids:
        raise ValueError('requested run missing from complete ownership inventory')
    rows = [row for run_id in sorted(run_ids) for row in list_workers(run_id, runtime)]
    return coordinators, rows


def operator_veto(handle, runtime):
    coordinators, rows = operator_ownership(runtime)
    if handle in coordinators or any(
            (w.get('agentTerminalHandle') == handle or
             (isinstance(w.get('resource'), dict) and w['resource'].get('terminalHandle') == handle)) and
            (w.get('workerState') != 'unsupervised' or
             not (w.get('dispatchStatus') in ('succeeded', 'failed') or
                  w['projection'].get('outcome') in ('succeeded', 'failed'))) for w in rows):
        raise ValueError('coordinator, supervised or active dispatch owns handle')


# Completion is explicitly declared by the coordinator, never inferred from terminal idle.
operators = []
handles = set()
for launch_file, completed_file, report_file in a.close_operator:
    try:
        launch = json.loads(Path(launch_file).read_text())
        completed = json.loads(Path(completed_file).read_text())
        if not Path(report_file).read_text().strip():
            raise ValueError('empty archived report')
        original = launch['result']['terminal']
        snapshot = completed['result']['terminal']
        keys = ('handle', 'incarnationId', 'executionHostId', 'worktreeId', 'ptyId')
        runtime = launch['_meta']['runtimeId']
        if (launch.get('ok') is not True or completed.get('ok') is not True or
                not isinstance(runtime, str) or not runtime or completed['_meta']['runtimeId'] != runtime or
                any(not isinstance(original.get(k), str) or not original[k] or
                    snapshot.get(k) != original[k] for k in keys) or
                type(snapshot.get('lastOutputAt')) not in (int, float) or
                original['executionHostId'] != 'local' or original['handle'] in handles):
            raise ValueError('unproven/duplicate local launch and completion identity')
        handles.add(original['handle'])
        operator_veto(original['handle'], runtime)
        selector = 'id:' + original['worktreeId']
        worktree = orca('worktree', 'show', '--worktree', selector)['result']['worktree']
        if worktree.get('id') != original['worktreeId'] or worktree.get('isMainWorktree') is not False:
            raise ValueError('main or unverifiable worktree')
        current = orca('terminal', 'show', '--terminal', original['handle'])
        now = current['result']['terminal']
        if (current['_meta']['runtimeId'] != runtime or
                any(now.get(k) != snapshot[k] for k in keys) or
                now.get('lastOutputAt') != snapshot['lastOutputAt']):
            raise ValueError('identity changed or new output since completion verification')
        operators.append((original['handle'], selector, runtime, snapshot))
    except (OSError, ValueError, KeyError, TypeError) as error:
        raise SystemExit('[orca_cleanup] FAILED: operator completion evidence: ' + str(error))
for handle, selector, runtime, snapshot in operators:
    print('close operator', handle, '(dry-run)' if a.dry_run else '')
    if not a.dry_run:
        try:
            operator_veto(handle, runtime)
            current = orca('terminal', 'show', '--terminal', handle)
            now = current['result']['terminal']
            if (current['_meta']['runtimeId'] != runtime or
                    any(now.get(k) != snapshot[k] for k in keys) or
                    now.get('lastOutputAt') != snapshot['lastOutputAt']):
                raise ValueError('operator changed immediately before close')
        except (OSError, ValueError, KeyError, TypeError) as error:
            raise SystemExit('[orca_cleanup] FAILED: operator recheck: ' + str(error))
        # shortcut: CLI has no conditional-close primitive; coordinator must exclude concurrent writers.
        receipt = orca('terminal', 'close', '--terminal', handle)
        result = receipt.get('result')
        close = result.get('close') if isinstance(result, dict) else None
        if (receipt.get('_meta', {}).get('runtimeId') != runtime or not isinstance(close, dict) or
                close.get('handle') != handle or close.get('ptyKilled') is not True):
            raise SystemExit('[orca_cleanup] FAILED: operator PTY closure unconfirmed for ' + handle)
        print('  ->', json.dumps(receipt))
        if any(t['handle'] == handle for t in terminal_inventory(selector)):
            raise SystemExit('[orca_cleanup] FAILED: operator still listed after close: ' + handle)

for selector in a.check_worktree:
    if terminal_inventory(selector):
        print('unfinished sessions', selector, '(dry-run)' if a.dry_run else '')
        if not a.dry_run:
            raise SystemExit('[orca_cleanup] FAILED: sessions still open in ' + selector +
                             '; close only completed run-owned sessions using original launch identity')
