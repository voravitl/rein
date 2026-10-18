#!/usr/bin/env python3
"""Wait for one exact Rein child. This receipt proves only that child's OS exit.

This is not a controller, ledger, process-group terminator or retry mechanism.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import tempfile

LIMIT = 1 << 20


def canonical(value, label):
    if not isinstance(value, str) or any(c in value for c in "\0\n\r"):
        raise ValueError(f"{label} requires a canonical absolute path")
    path = Path(value)
    if not path.is_absolute() or str(path) != value or path.resolve() != path:
        raise ValueError(f"{label} requires a canonical absolute path")
    return path


def read_bounded(path):
    with path.open("rb") as source:
        raw = source.read(LIMIT + 1)
    if len(raw) > LIMIT:
        raise ValueError("input exceeds 1 MiB")
    return raw


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def unique_object(pairs):
    value = {}
    for key, item in pairs:
        if key in value:
            raise ValueError("duplicate JSON field")
        value[key] = item
    return value


def sync_directory(path):
    fd = os.open(path, os.O_RDONLY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def encode(state):
    return (json.dumps(state, sort_keys=True, indent=2) + "\n").encode()


def reserve(path, state):
    # Never reuse an existing receipt, even if its last phase is ambiguous.
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "wb") as target:
        target.write(encode(state))
        target.flush()
        os.fsync(target.fileno())
    sync_directory(path.parent)


def persist(path, state):
    fd, temporary = tempfile.mkstemp(prefix=".supervisor-process-", dir=path.parent)
    try:
        with os.fdopen(fd, "wb") as target:
            target.write(encode(state))
            target.flush()
            os.fsync(target.fileno())
        os.replace(temporary, path)
        sync_directory(path.parent)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def execute(args):
    if os.name != "posix":
        raise ValueError("supervisor process waiting currently requires POSIX")
    for label, value in (("invocation", args.invocation), ("plan hash", args.plan_hash), ("profile hash", args.profile_hash)):
        if not re.fullmatch(r"[0-9a-f]{64}", value):
            raise ValueError(f"{label} requires 64 lowercase hex characters")
    rein = canonical(args.rein, "rein")
    plan = canonical(args.plan, "plan")
    receipt = canonical(args.receipt, "receipt")
    if not rein.is_file() or not os.access(rein, os.X_OK):
        raise ValueError("rein is not executable")
    raw = read_bounded(plan)
    if digest(raw) != args.plan_hash:
        raise ValueError("plan hash changed")
    parsed = json.loads(raw, object_pairs_hook=unique_object)
    if not isinstance(parsed, dict) or type(parsed.get("schema")) is not int or parsed["schema"] != 2:
        raise ValueError("requires a schema-2 plan")
    repo = canonical(parsed.get("repo"), "plan repo")
    if not repo.is_dir():
        raise ValueError("plan repo is not a directory")
    run = parsed.get("run")
    if not isinstance(run, str) or not run or len(run) > 128 or any(ord(c) < 32 for c in run):
        raise ValueError("plan run identity missing or invalid")
    if receipt.is_relative_to(repo) or not receipt.parent.is_dir():
        raise ValueError("receipt requires an existing directory outside the repo")
    environment = os.environ.copy()
    profile = canonical(environment.get("REIN_PROFILE"), "REIN_PROFILE")
    profile_raw = read_bounded(profile)
    if digest(profile_raw) != args.profile_hash:
        raise ValueError("profile hash changed")
    if environment.get("REIN_SUPERVISOR_INVOCATION") != args.invocation:
        raise ValueError("invocation environment mismatch")
    argv = [str(rein), "supervisor", "run", "--plan", str(plan)]
    state = {"schema": 1, "phase": "spawn_pending", "invocation_id": args.invocation,
             "run": run, "plan_hash": args.plan_hash, "profile_hash": args.profile_hash,
             "repo": str(repo), "rein": str(rein), "plan": str(plan),
             "parent_pid": os.getpid(), "child_pid": None, "process_exit_code": None,
             "argv": argv}
    reserve(receipt, state)
    # Recheck bytes immediately before spawn. Reservation failure never spawns.
    if read_bounded(plan) != raw or read_bounded(profile) != profile_raw:
        raise ValueError("plan or profile changed before spawn; receipt must not be retried")
    child = None
    received = []

    def forward(signum, _frame):
        received.append(signum)
        if child is not None:
            try:
                child.send_signal(signum)
            except ProcessLookupError:
                pass

    previous = {sig: signal.signal(sig, forward) for sig in (signal.SIGINT, signal.SIGTERM)}
    failure = None
    waited = False
    try:
        if received:
            raise RuntimeError("interrupted before spawn; receipt must not be retried")
        # No shell and no output capture. The child receives the terminal streams.
        child = subprocess.Popen(argv, cwd=repo, env=environment)
        for signum in received:
            try:
                child.send_signal(signum)
            except ProcessLookupError:
                pass
        state.update(phase="running", child_pid=child.pid)
        try:
            persist(receipt, state)
        except Exception as exc:
            failure = exc  # Still wait for this exact child; never retry or detach.
        code = child.wait()
        waited = True
        state.update(phase="exited", process_exit_code=code)
        try:
            persist(receipt, state)
        except Exception as exc:
            failure = failure or exc
        if failure is not None:
            raise RuntimeError("process receipt persistence failed; child was waited; do not retry") from failure
        return code if code >= 0 else 128 - code
    finally:
        try:
            if child is not None and not waited:
                # Any post-spawn failure still owns a wait obligation. Never
                # detach or resolve a PID from a receipt as a substitute.
                child.wait()
        finally:
            for sig, handler in previous.items():
                signal.signal(sig, handler)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("rein", "plan", "receipt", "invocation", "plan-hash", "profile-hash"):
        parser.add_argument("--" + name, required=True)
    args = parser.parse_args(argv)
    try:
        return execute(args)
    except (OSError, ValueError, RuntimeError) as exc:
        print(f"supervisor-process: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
