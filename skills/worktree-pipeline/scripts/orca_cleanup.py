#!/usr/bin/env python3
"""Release settled supervised sessions and verify resource closure for one Run.

usage: orca_cleanup.py <run_id> [--stop ctx_a,ctx_b] [--dry-run]
  --stop requires positive exited liveness and an exact worker-stop nextAction.
"""
import argparse
import json
import os
import subprocess
import sys

# Match Orca's recovery executable resolution, once for the whole invocation.
CLI = (os.environ.get('ORCA_CLI_COMMAND', '').strip() or
       ('orca-dev' if os.environ.get('ORCA_DEV_REPO_ROOT') else
        'orca-ide' if sys.platform == 'linux' else 'orca'))


def orca(*args):
    try:
        response = subprocess.run([CLI, *args, '--json'], capture_output=True, text=True)
        if response.returncode != 0:
            raise ValueError(f'exit {response.returncode}: {response.stderr[-300:]} {response.stdout[-300:]}')
        result = json.loads(response.stdout)
        if not isinstance(result, dict) or result.get('ok') is not True or result.get('error'):
            raise ValueError(f'failed CLI response: {result}')
        return result
    except (OSError, ValueError) as error:
        raise SystemExit(f'[orca_cleanup] {args[1]} failed: {error}')


ap = argparse.ArgumentParser()
ap.add_argument('run')
ap.add_argument('--stop', default='')
ap.add_argument('--dry-run', action='store_true')
a = ap.parse_args()
stop = {s for s in a.stop.split(',') if s}

def list_workers():
    workers = []
    seen = set()
    cursors = set()
    cursor = None
    # Complete enumeration before making any cleanup decision or mutation.
    while True:
        args = ['orchestration', 'worker-list', '--run', a.run, '--include-remote']
        if cursor is not None:
            args += ['--cursor', cursor]
        result = orca(*args).get('result')
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
    releases = {action[-1] for action in actions if action[1] == 'worker-release'}
    if releases:
        current = {w['dispatchId']: w for w in list_workers()}
        pending = sorted(dispatch for dispatch in releases
                         if current.get(dispatch, {}).get('terminalState') != 'released')
        if pending:
            raise SystemExit('[orca_cleanup] FAILED: session closure unconfirmed for ' +
                             ', '.join(pending) + '; inspect receipts and follow recovery; never force-close a supervised terminal')
