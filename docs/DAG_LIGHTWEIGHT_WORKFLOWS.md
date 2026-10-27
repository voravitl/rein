# Bounded DAGs and lightweight actions

Schema 3 explicitly opts into parallel execution with one Rein controller and
one durable journal. Schema 1 remains a monitor; schema 2 retains serial graph
execution and its existing journal format. A new plan needs a new authorization.

```json
"execution": {"max_parallel": 2, "failure_policy": "isolate_known"}
```

`max_parallel` must be 1..32 and cannot exceed task count. Each active attempt
occupies a slot through preparation, execution and finalization. Only external
execution overlaps. The controller serializes admission, transitions, settlement
and artifact publication. `max_checks` bounds total admitted attempts, including
retries, across restarts. Schema 3 does not use polling to infer process death.

## Worker and action tasks

Every task declares `kind`, an exact `contract_hash`, a full 40-character source
commit SHA in `base`, and `max_attempts` from 1..3. The initial checkout HEAD must
equal that commit. A controller snapshot digest additionally binds dirty content
and every copied source byte. Source snapshots reject links and unsupported Git
states and are limited to 10,000 files and 64 MiB. Contract worktrees must belong
to the controller's repository and have distinct, non-overlapping ownership;
report paths must also be distinct.

Workers retain schema-2 requirements: task profile, exact provider configuration
digest, approved launch commands, `process_policy: "non_detaching"`, verification
commands, installed guard hooks and reservation before launch. The adapter uses
real routing receipts and leases. Worker outputs are worktree-relative files;
declared untracked files also need `allowed_untracked`. A verified input directory
and its digest are included in the worker's literal task prompt. Host workers
remain cooperative harness processes with their existing OS sandbox requirements;
file permissions are not protection against a hostile process with the same UID.

Actions forbid worker profile/configuration/launch fields. An action has:

```json
"action": {
  "argv": ["/usr/bin/printf", "literal argument"],
  "timeout_ms": 5000,
  "max_output_bytes": 4096,
  "max_output_files": 8,
  "max_output_total_bytes": 65536,
  "idempotent": false,
  "sandbox": {"backend": "docker", "image": "sha256:<local-image-id>"}
}
```

The executable must be absolute. Arguments are passed literally. An explicitly
approved shell or interpreter command runs inside the same container policy.
Timeout is 1..plan timeout; captured output is 1 byte..1 MiB; output entries are
1..1000; total captured file bytes are 1 byte..64 MiB. At most 32 verification
commands are allowed per task. Resolved input metadata is limited to 256 KiB.
Directory entries count toward the file bound. Actions use their own single-use receipts and never call
provider discovery, model routing or paid reservation APIs.

The supported backend requires an existing local Unix Docker socket, a Linux
container daemon and an already installed image ID. Tags, pulls, remote contexts,
Docker environment overrides and host-exec fallback are refused. Rein fixes:

- Network `none`, a read-only root, nonroot UID/GID 65532, dropped capabilities,
  `no-new-privileges`, private process namespace and IPC `none`.
- One CPU, 256 MiB memory, no extra swap, 64 processes, bounded file sizes and
  descriptors, plus a 16 MiB non-executable scratch tmpfs.
- Read-only source snapshot at `/src`, read-only verified inputs at `/inputs`,
  and a fresh writable output directory at `/out`. No repository metadata,
  controller state, Docker socket, host home or host credential environment is
  mounted or passed to the container.

The action and each verifier run in separate owned containers. Verification
mounts `/out` read-only. Runtime inspection must match the approved image, command,
mounts and policy. Cleanup checks exact container ID, name and ownership labels,
proves termination, removes only that container and checks absence. Missing or
uncertain proof blocks the entire run. No remote or Windows backend is claimed.

File count and aggregate file bytes are checked after container termination.
`fsize` limits individual files during execution; the bind-mounted output
directory does not provide a hard aggregate disk quota. Operators requiring an
OS-enforced aggregate disk quota need a backend with that capability. No speed,
RAM reduction, exactly-once execution or live containment result is claimed here.

## Artifact edges

```json
"outputs": [{"name": "result", "path": "result.json", "max_bytes": 4096}]
```

```json
"depends_on": ["producer"],
"inputs": [{"producer": "producer", "name": "result", "as": "data"}]
```

Inputs must name an output of a direct dependency. Aliases are unique. Output
paths are clean relative slash paths; absolute paths, `..`, backslashes, symlinks,
hardlinks, special files and directory escapes are refused. Declared outputs
have a combined maximum of 64 MiB. Action output paths are relative to `/out`.

After termination, accounting and objective verification succeed, the controller
publishes copied bytes and an immutable manifest under its private state root.
The journal pins the manifest digest. The manifest binds run, exact plan hash,
producer task/attempt/receipt, contract, source commit/snapshot, input digest,
execution, verification, checkpoint and SHA256 of each exact output byte stream.
Consumers receive fresh copies of those verified bytes, not producer worktree
paths. Hashes and bindings are checked again before execution and on replay.
No Git diff transfer, merge or review approval is implicit in an artifact edge.

## Failure and recovery

A known unsuccessful task or parked task blocks only its dependents. Independent
branches continue. The result includes `status`, `completed`, `failed`, `blocked`
and `cancelled`; `WAITING_HUMAN` alone does not mean all tasks succeeded. A fully
verified graph still requires independent review and human approval.

Broken authority, corrupted evidence, unknown worker liveness, missing accounting
or unresolved durable intent stop the whole run. Schema 3 uses a versioned
`.graph3-<plan-hash>.jsonl` journal with monotonic sequence and operation identities.
An interrupted attempt is never automatically relaunched. Successful replay
revalidates evidence and does not re-execute successful tasks. Ordinary branch
waits cannot override uncertain process or accounting state.

Retries use fresh receipts, attempts and output directories. Worker retries
retain verified quota continuation. Actions require `idempotent: true` for more
than one attempt and retry only after proven termination. Cancellation cancels
active execution, keeps controller ownership during a bounded drain using a fresh
15-second cleanup context, and records a persistent cancellation fence. Docker
cleanup has its own fresh 8-second bound. Objective verification and finalization
share a 15-second per-attempt controller budget. Worker discovery/probe operations
use a per-operation timeout capped at 30 seconds; cancellation may wait for the
current bounded probe before the drain begins. A cancelled authorization requires
explicit reconciliation/new authorization; no blind retry occurs.

## Local example

`examples/dag-parallel-action.json` describes A -> (B,C) -> D. It is a binding
template: zero hashes and `/absolute/repository` deliberately cannot authorize a
real run. Use an isolated repository with a small, clean committed source tree,
an already installed pinned image, and four distinct governed worktrees.
Run the commands from the Rein source checkout, using a built `rein` on `PATH`,
in your own terminal. `run start --supervisor-plan` and `run allow` are owner
operations; the CLI refuses them from a Claude Code tool session. Use fresh,
absolute disposable paths outside this checkout for both directories below.
The recipe creates a small committed fixture repository, a fixture-only profile,
four worktrees and their contracts; it does not install hooks or images. Supply
the exact ID of an already installed Linux image containing `/bin/sh`, `cat`,
`head`, `tail` and `wc`. Python is only a demonstration binding tool, not a Rein
runtime dependency. Review the generated plan before starting its run.

```sh
set -eu
export DAG_DEMO_DIR=/absolute/disposable/dag-demo
export DAG_DEMO_REPO=/absolute/disposable/repository
export PIPELINE_CONTRACTS="$DAG_DEMO_DIR/contracts"
export DAG_DEMO_WORKTREES="$DAG_DEMO_DIR/worktrees"
export DAG_DEMO_IMAGE='sha256:<already-installed-image-id>'
test ! -e "$DAG_DEMO_DIR"
test ! -e "$DAG_DEMO_REPO"
mkdir -p "$DAG_DEMO_DIR" "$DAG_DEMO_WORKTREES"
mkdir -p "$DAG_DEMO_REPO"
git -C "$DAG_DEMO_REPO" init -q
printf 'committed action source\n' > "$DAG_DEMO_REPO/source.txt"
git -C "$DAG_DEMO_REPO" add source.txt
git -C "$DAG_DEMO_REPO" -c user.name='Rein demo' \
  -c user.email='rein-demo@example.invalid' commit -qm 'Local DAG fixture'
printf '{}\n' > "$DAG_DEMO_DIR/profile.json"
for task in dag-A dag-B dag-C dag-D; do
  git -C "$DAG_DEMO_REPO" worktree add --detach "$DAG_DEMO_WORKTREES/$task" HEAD
  rein contract new --name "$task" --run-dir "$DAG_DEMO_DIR" \
    --profile "$DAG_DEMO_DIR/profile.json" --worktree-root "$DAG_DEMO_WORKTREES" \
    --allow 'workflow/**' --scope ACTION
done

python3 - examples/dag-parallel-action.json <<'PY'
import json, os, pathlib, subprocess, sys
plan = json.loads(pathlib.Path(sys.argv[1]).read_text())
plan['repo'] = str(pathlib.Path(os.environ['DAG_DEMO_REPO']).resolve())
for task in plan['tasks']:
    task['contract_hash'] = subprocess.check_output(
        ['rein', 'contract', 'hash', task['task']], text=True).strip()
    worktree = pathlib.Path(os.environ['DAG_DEMO_WORKTREES']) / task['task']
    task['base'] = subprocess.check_output(
        ['git', '-C', str(worktree), 'rev-parse', 'HEAD'], text=True).strip()
    task['action']['sandbox']['image'] = os.environ['DAG_DEMO_IMAGE']
dest = pathlib.Path(os.environ['DAG_DEMO_DIR']) / 'approved-plan.json'
dest.write_text(json.dumps(plan, indent=2) + '\n')
print(dest)
PY

rein supervisor validate --plan "$DAG_DEMO_DIR/approved-plan.json"
rein run start "$DAG_DEMO_REPO" --run dag-demo --session local-demo \
  --pid $$ --profile "$DAG_DEMO_DIR/profile.json" \
  --supervisor-plan "$DAG_DEMO_DIR/approved-plan.json"
for task in dag-A dag-B dag-C dag-D; do
  rein run allow "$DAG_DEMO_REPO" --task "$task" --reason 'approved local action fixture'
done
rein supervisor run --plan "$DAG_DEMO_DIR/approved-plan.json"
```

This example demonstrates artifact fan-out/fan-in; it has no rendezvous and does
not establish measured overlap. Shell commands use `set -eu` so an intermediate
copy or verification failure cannot be hidden by a later successful command.
JSON `\\n` decodes to a single backslash before `n`, which `printf` interprets as
a newline. The unbound template cannot be run unchanged. After an interrupted
run, preserve its evidence and follow reconciliation rules rather than repeating
the setup or deleting its journal.

The CLI exits 0 for an inspectable verified/known-wait result, 1 for errors or
reconciliation, and 2 for invalid arguments or preflight refusal. Optional fresh
`REIN_SUPERVISOR_INVOCATION` correlation uses the same read-only
`rein supervisor invocation --plan FILE --invocation ID` interface as schema 2.
Invocation return telemetry does not itself prove cleanup or authorize recovery.

## Verification boundary

This implementation's acceptance requires container receipts: full tests, vet,
format inspection, focused race tests, compile-preserving mutation controls and
a real external CLI/Docker fixture with observed independent execution and
cleanup. Fake backend tests exercise controller decisions and zero-model routing;
they do not establish container containment. Run tests in a disposable container
copy with read-only host source mounts. Do not expose a host Docker socket to the
ordinary test container to bypass an unavailable backend.
The parent-owned live Docker fixture must be rerun after the Docker start-flag
repair; historical unit/race results and fake CLI tests do not prove that live
execution or this binding recipe succeeds.
