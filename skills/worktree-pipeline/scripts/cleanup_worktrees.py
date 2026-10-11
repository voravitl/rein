#!/usr/bin/env python3
"""Archive and remove settled, integrated Orca worktrees; never force Git deletion."""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tarfile
import tempfile

RUNTIME = None

CLI = (os.environ.get('ORCA_CLI_COMMAND', '').strip() or
       ('orca-dev' if os.environ.get('ORCA_DEV_REPO_ROOT') else
        'orca-ide' if sys.platform == 'linux' else 'orca'))


def git(*args, cwd, allowed=(0,)):
    result = subprocess.run(['git', *args], cwd=cwd, capture_output=True, text=True)
    if result.returncode not in allowed:
        raise RuntimeError(f'git {args[0]} failed: {result.stderr.strip()}')
    return result


def orca(*args):
    global RUNTIME
    response = subprocess.run([CLI, *args, '--json'], capture_output=True, text=True)
    if response.returncode != 0:
        raise RuntimeError(f'Orca {args[1]} failed: {response.stderr} {response.stdout}')
    result = json.loads(response.stdout)
    if not isinstance(result, dict) or result.get('ok') is not True or result.get('error') or not isinstance(result.get('result'), dict):
        raise RuntimeError('invalid Orca receipt')
    metadata = result.get('_meta')
    runtime = metadata.get('runtimeId') if isinstance(metadata, dict) else None
    if not isinstance(runtime, str) or not runtime or (RUNTIME is not None and runtime != RUNTIME):
        raise RuntimeError('missing or changed Orca runtime')
    RUNTIME = runtime
    return result['result']


def complete_rows(result, key):
    rows = result.get(key)
    scope = result.get('hostScope')
    if (not isinstance(rows, list) or not all(isinstance(row, dict) for row in rows) or
            result.get('truncated') is not False or type(result.get('totalCount')) is not int or
            result['totalCount'] != len(rows) or not isinstance(scope, dict) or scope.get('omittedHostIds') != [] or
            not isinstance(scope.get('hostIds'), list) or not scope['hostIds'] or
            not all(isinstance(host, str) and host for host in scope['hostIds']) or 'local' not in scope['hostIds']):
        raise RuntimeError(f'incomplete Orca {key} inventory')
    return rows


def worktrees(main):
    result = git('worktree', 'list', '--porcelain', '-z', cwd=main).stdout
    rows = []
    for block in result.split('\0\0'):
        fields = dict(line.split(' ', 1) if ' ' in line else (line, '') for line in block.split('\0') if line)
        if fields:
            if not fields.get('worktree') or not fields.get('HEAD'):
                raise RuntimeError('malformed Git worktree inventory')
            rows.append(fields)
    return rows


def local_files(path):
    # Include ignored files even when --ignore was not needed for the status gate.
    result = git('ls-files', '--others', '-z', cwd=path)
    return sorted(set(p for p in result.stdout.split('\0') if p))


def status(path, ignore):
    result = git('status', '--porcelain', '-z', '--untracked-files=all', cwd=path)
    def ignorable(entry):
        if not entry.startswith('?? '): return False
        name = entry[3:]
        for item in ignore:
            if '/' not in item:
                if name.split('/')[-1] == item: return True
            else:
                item = item.removeprefix('./')
                if name == item or name.startswith(item + '/'): return True
        return False
    return [e for e in result.stdout.split('\0') if e and not ignorable(e)]


def identity(row, path, head):
    ident = row.get('identity')
    if (not isinstance(ident, dict) or not all(isinstance(ident.get(k), str) and ident[k] for k in ('key', 'instanceId', 'executionHostId')) or
            ident['instanceId'] != row.get('instanceId') or ident['executionHostId'] != row.get('hostId') or
            row.get('hostId') != 'local' or os.path.realpath(row.get('path', '')) != path or row.get('head') != head or
            not row.get('id') or not row.get('repoId') or type(row.get('isMainWorktree')) is not bool or
            not isinstance(row.get('childWorktreeIds'), list)):
        raise RuntimeError('unproven local worktree identity')
    return row['id'], ident['key'], row['repoId'], row['instanceId'], row['hostId']


def retention(row):
    if type(row.get('isMainWorktree')) is not bool or not isinstance(row.get('childWorktreeIds'), list):
        raise RuntimeError('unproven main/child worktree state')
    if row['isMainWorktree']: return 'main checkout'
    if row['childWorktreeIds']: return 'child worktree still registered'
    if row.get('workspaceStatus') not in ('completed', 'done', 'archived'): return 'work not proven settled'
    for key in ('linkedPR', 'linkedGitLabMR', 'linkedBitbucketPR', 'linkedAzureDevOpsPR', 'linkedGiteaPR', 'linkedWorkItem'):
        linked = row.get(key)
        if linked and (not isinstance(linked, dict) or str(linked.get('state', '')).lower() not in ('merged', 'closed')):
            return 'linked review/work item; preserve until separately resolved'
    return None


def dispatch_gate(worktree_id):
    runs = []
    seen = set()
    cursors = set()
    cursor = None
    while True:
        args = ['orchestration', 'run-list']
        if cursor is not None: args += ['--cursor', cursor]
        result = orca(*args)
        rows = result.get('runs')
        if not isinstance(rows, list) or 'nextCursor' not in result:
            raise RuntimeError('incomplete Run inventory')
        for row in rows:
            run = row.get('id') if isinstance(row, dict) else None
            if not isinstance(run, str) or not run or run in seen:
                raise RuntimeError('malformed/duplicate Run')
            seen.add(run); runs.append(run)
        cursor = result['nextCursor']
        if cursor is None: break
        if not isinstance(cursor, str) or not cursor or cursor in cursors:
            raise RuntimeError('invalid/repeated Run cursor')
        cursors.add(cursor)
    seen_dispatches = set()
    for run in runs:
        cursor = None
        cursors = set()
        while True:
            args = ['orchestration', 'worker-list', '--run', run, '--include-remote']
            if cursor is not None: args += ['--cursor', cursor]
            result = orca(*args)
            if result.get('scope') != {'source': 'flag', 'run': run} or not isinstance(result.get('workers'), list):
                raise RuntimeError('unproven worker-list Run scope')
            for row in result['workers']:
                if (not isinstance(row, dict) or not isinstance(row.get('dispatchId'), str) or
                        not row['dispatchId'] or row['dispatchId'] in seen_dispatches or
                        row.get('runId') != run or not isinstance(row.get('projection'), dict)):
                    raise RuntimeError('malformed/duplicate cross-run worker')
                seen_dispatches.add(row['dispatchId'])
                workspace = row['projection'].get('workspace')
                resource = row.get('resource')
                ids = set()
                for value, key in ((workspace, 'id'), (resource, 'worktreeId')):
                    if value is not None and not isinstance(value, dict):
                        raise RuntimeError('unproven dispatch workspace')
                    if isinstance(value, dict) and value.get(key) is not None:
                        if not isinstance(value[key], str) or not value[key]:
                            raise RuntimeError('invalid dispatch workspace identity')
                        ids.add(value[key])
                settled = (row.get('dispatchStatus') in ('succeeded', 'failed') or
                           row['projection'].get('outcome') in ('succeeded', 'failed'))
                closed = row.get('workerState') == 'unsupervised' or row.get('terminalState') == 'released'
                if (worktree_id in ids or not ids) and not (settled and closed):
                    raise RuntimeError('active/unreleased dispatch ' + row['dispatchId'] + '; workspace cleanup unproven')
            page = result.get('page')
            if not isinstance(page, dict) or type(page.get('hasMore')) is not bool or 'nextCursor' not in page:
                raise RuntimeError('incomplete worker pagination')
            cursor = page['nextCursor']
            if not page['hasMore']:
                if cursor is not None: raise RuntimeError('invalid terminal worker cursor')
                break
            if not isinstance(cursor, str) or not cursor or cursor in cursors:
                raise RuntimeError('invalid/repeated worker cursor')
            cursors.add(cursor)


def session_gate(selector, frozen):
    dispatch_gate(frozen[0])
    rows = complete_rows(orca('worktree', 'ps'), 'worktrees')
    matches = [r for r in rows if r.get('worktreeId') == frozen[0]]
    if len(matches) != 1:
        raise RuntimeError('missing/ambiguous worktree process evidence')
    row = matches[0]
    if (row.get('worktreeInstanceId') != frozen[3] or row.get('hostId') != frozen[4] or
            type(row.get('isActive')) is not bool or type(row.get('hasAttachedPty')) is not bool or
            type(row.get('liveTerminalCount')) is not int or not isinstance(row.get('agents'), list)):
        raise RuntimeError('unproven worktree process identity/session state')
    reason = retention(row)
    if reason: return reason
    if row['isActive'] or row['hasAttachedPty'] or row['liveTerminalCount'] != 0 or row['agents']:
        raise RuntimeError('active worktree/session; finish and close before cleanup')
    if complete_rows(orca('terminal', 'list', '--worktree', selector), 'terminals'):
        raise RuntimeError('live sessions; finish and close before cleanup')
    return None


def verify_archive(path, directory, files):
    with tarfile.open(directory / 'local-files.tar') as saved:
        if sorted(saved.getnames()) != files:
            raise RuntimeError('local archive verification failed')
        for member in saved:
            if member.issym() and (not os.path.islink(os.path.join(path, member.name)) or os.readlink(os.path.join(path, member.name)) != member.linkname):
                raise RuntimeError('local archive symlink mismatch')
            if member.isfile():
                with saved.extractfile(member) as source, open(os.path.join(path, member.name), 'rb') as original:
                    while True:
                        chunk = source.read(1024 * 1024)
                        if chunk != original.read(len(chunk) or 1):
                            raise RuntimeError('local archive content mismatch')
                        if not chunk: break


def archive(path, head, destination, row):
    destination = Path(destination).resolve()
    if destination == Path(path) or Path(path) in destination.parents:
        raise RuntimeError('archive must be outside the removed worktree')
    destination.mkdir(parents=True, exist_ok=True)
    directory = Path(tempfile.mkdtemp(prefix=Path(path).name + '-', dir=destination))
    git('bundle', 'create', str(directory / 'history.bundle'), 'HEAD', cwd=path)
    git('bundle', 'verify', str(directory / 'history.bundle'), cwd=path)
    files = local_files(path)
    with tarfile.open(directory / 'local-files.tar', 'w') as output:
        for name in files:
            output.add(os.path.join(path, name), arcname=name, recursive=False)
    verify_archive(path, directory, files)
    (directory / 'receipt.json').write_text(json.dumps(dict(head=head, worktree=row, localFiles=files), indent=2) + '\n')
    print('archive', directory)
    return directory, files


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument('main'); ap.add_argument('--root', required=True)
    ap.add_argument('--keep', required=True)
    ap.add_argument('--dry-run', action='store_true'); ap.add_argument('--prune-images')
    ap.add_argument('--ignore', default=''); ap.add_argument('--archive-dir')
    ap.add_argument('--integrated-into', help='exact full commit SHA containing completed auxiliary work')
    a = ap.parse_args(argv)
    main_path = os.path.realpath(a.main); root = os.path.realpath(a.root)
    keep = {k for k in a.keep.split(',') if k and k != 'none'}
    ignore = {i.strip().rstrip('/') for i in a.ignore.split(',') if i.strip().rstrip('/') not in ('', '.', './')}
    destination = a.archive_dir or (os.path.join(os.environ['PIPELINE_LOGDIR'], 'worktree-archives') if os.environ.get('PIPELINE_LOGDIR') else None)
    failed = []
    try:
        if a.dry_run:
            print('dry-run: using cached target refs; no fetch or mutation')
        else:
            git('fetch', 'origin', '--quiet', cwd=main_path)
        target = a.integrated_into or 'origin/' + os.environ.get('PIPELINE_TARGET_BRANCH', 'main')
        if a.integrated_into and not re.fullmatch(r'[0-9a-f]{40}|[0-9a-f]{64}', target):
            raise RuntimeError('--integrated-into requires an exact full SHA')
        target = git('rev-parse', '--verify', target + '^{commit}', cwd=main_path).stdout.strip()
        candidates = worktrees(main_path)
        managed = complete_rows(orca('worktree', 'list'), 'worktrees')
        if any(not isinstance(row.get('id'), str) or not row['id'] or not isinstance(row.get('path'), str) or
               not isinstance(row.get('childWorktreeIds'), list) or
               not all(isinstance(child, str) and child for child in row['childWorktreeIds']) for row in managed):
            raise RuntimeError('malformed managed worktree lineage')
        by_id = {row['id']: row for row in managed}
        if len(by_id) != len(managed): raise RuntimeError('duplicate managed worktree identity')
        def child_depth(row, ancestors=()):
            key = row.get('id')
            if key in ancestors: raise RuntimeError('cyclic worktree lineage')
            children = row.get('childWorktreeIds')
            if not isinstance(children, list): raise RuntimeError('unproven child worktree state')
            return max([child_depth(by_id[child], (*ancestors, key)) + 1 for child in children if child in by_id] or [0])
        depths = {os.path.realpath(row.get('path', '')): child_depth(row) for row in managed}
        candidates.sort(key=lambda row: depths.get(os.path.realpath(row['worktree']), 0))
        for fields in candidates:
            path = os.path.realpath(fields['worktree']); name = os.path.basename(path)
            if path == main_path or os.path.commonpath([root, path]) != root or path == root: continue
            try:
                if name in keep: print('keep   ', name, '(--keep)'); continue
                if 'locked' in fields or 'prunable' in fields: raise RuntimeError('locked/prunable worktree')
                if status(path, ignore): print('keep   ', name, '(uncommitted changes)'); continue
                head = git('rev-parse', '--verify', 'HEAD', cwd=path).stdout.strip()
                merged = git('merge-base', '--is-ancestor', head, target, cwd=path, allowed=(0, 1)).returncode == 0
                if not merged:
                    if git('rev-list', '--merges', f'{target}..{head}', cwd=path).stdout.strip():
                        print('keep   ', name, '(unmerged merge commits)'); continue
                    if any(line.startswith('+ ') for line in git('cherry', target, head, cwd=path).stdout.splitlines()):
                        print('keep   ', name, '(unintegrated commits)'); continue
                matches = [r for r in managed if os.path.realpath(r.get('path', '')) == path]
                if len(matches) != 1: raise RuntimeError('missing/ambiguous Orca identity')
                row = matches[0]; frozen = identity(row, path, head)
                selector = 'identity:' + frozen[1]
                current = orca('worktree', 'show', '--worktree', selector).get('worktree')
                if not isinstance(current, dict) or identity(current, path, head) != frozen:
                    raise RuntimeError('worktree changed before cleanup')
                reason = retention(current)
                if reason: print('keep   ', name, '(' + reason + ')'); continue
                reason = session_gate(selector, frozen)
                if reason: print('keep   ', name, '(' + reason + ')'); continue
                print('remove ', name, '(integrated; branch deletion delegated safely to Orca)')
                if a.dry_run: continue
                if not destination: raise RuntimeError('external --archive-dir or PIPELINE_LOGDIR required')
                archive_path = os.path.realpath(destination)
                if any(archive_path == os.path.realpath(w['worktree']) or os.path.commonpath([archive_path, os.path.realpath(w['worktree'])]) == os.path.realpath(w['worktree']) for w in candidates):
                    raise RuntimeError('archive must be external to all repository worktrees')
                directory, archived = archive(path, head, destination, current)
                # Revalidate after archiving; no new work or sessions may enter the removal gate.
                if status(path, ignore) or local_files(path) != archived or git('rev-parse', 'HEAD', cwd=path).stdout.strip() != head:
                    raise RuntimeError('local work changed during archive')
                current = orca('worktree', 'show', '--worktree', selector).get('worktree')
                if not isinstance(current, dict) or identity(current, path, head) != frozen or retention(current):
                    raise RuntimeError('worktree changed during archive')
                if session_gate(selector, frozen):
                    raise RuntimeError('worktree became retained during archive')
                verify_archive(path, directory, archived)
                # No atomic CLI lease: coordinator must exclude concurrent writers/launches during cleanup.
                orca('worktree', 'rm', '--worktree', selector)
                after = complete_rows(orca('worktree', 'list'), 'worktrees')
                if any(r.get('id') == frozen[0] or os.path.realpath(r.get('path', '')) == path for r in after):
                    raise RuntimeError('Orca registration remains after removal')
                if any(os.path.realpath(r['worktree']) == path for r in worktrees(main_path)) or os.path.lexists(path):
                    raise RuntimeError('Git registration or directory remains after removal')
            except (OSError, ValueError, RuntimeError) as error:
                failed.append(f'{name}: {error}')
        if a.prune_images and not failed:
            images = subprocess.run(['docker', 'images', '--format', '{{.Repository}}:{{.Tag}}', a.prune_images], capture_output=True, text=True)
            processes = subprocess.run(['docker', 'ps', '--format', '{{.Image}}'], capture_output=True, text=True)
            if images.returncode or processes.returncode: raise RuntimeError('docker listing failed')
            busy = set(processes.stdout.split())
            old = [t for t in images.stdout.split() if not t.endswith(':latest') and t not in busy]
            print('images ', len(old), 'to remove', '(dry-run)' if a.dry_run else '')
            if old and not a.dry_run and subprocess.run(['docker', 'rmi', *old], capture_output=True).returncode:
                raise RuntimeError('docker rmi failed')
    except (OSError, ValueError, RuntimeError) as error:
        failed.append(str(error))
    if failed:
        print('[cleanup] FAILED: ' + '; '.join(failed), file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
