# Changelog

## Unreleased

### Added

- `rein dag lint`, `rein dag compile` and `rein dag integrate`: validate an agent-written `rein_dag_spec/v1` graph,
  compile it into an owner-approved schema-3 supervisor plan (worktrees, contracts, hooks), and after the run merge
  the finished worker commits into one new branch with checkpoint, clean-worktree and ancestry checks.
- Crux `dag` skill (56 skills); Crux supervisor scripts accept schema-3 plans.

### Fixed

- `rein dag compile` launched print-mode workers with no edit permission, so with no one to answer a prompt
  Claude Code could not edit and Codex fell back to its read-only sandbox. Compiled launches now carry the
  worktree-scoped edit mode (`claude --permission-mode acceptEdits`, `codex exec --sandbox workspace-write`,
  `agy --mode accept-edits`); the Rein hook still judges every tool call. The approved-argv parser accepts exactly
  that one flag and value per harness and keeps refusing bypass, auto, dontAsk and full-access modes.
- A schema-3 DAG worker that edited files without committing left a dirty worktree, so `rein dag integrate`
  refused it. After a successful worker exits, the supervisor now commits its edits itself (fixed author, repository
  hooks bypassed, literal pathspecs) before the checkpoint, so every agent leaves the same integrable shape. Any changed
  path outside the contract's allow globs or inside its deny globs refuses the commit, leaves the tree untouched and
  ends the attempt for a human with the exact path. A worker that committed its own work and left the tree clean is
  not committed again.
- `plan.md` from `rein dag compile` pointed the owner commands at `<repo>/plan.json`, which compile never writes, and
  omitted the per-task `rein run allow` the supervisor needs. It now prints commands against `<out>/plan.json`,
  shell-quoted, with one `run allow` per task and the `dag integrate` step.
- The guard refuses a command name that expands at run time (`$ORCA`, `$(which rm)`). Such a name could be any
  command, so the rules keyed by name (worker spawn, route admission, write targets) no longer apply to it, and a
  worker could write or launch outside the contract. A literal basename with a dynamic directory is still judged by
  its name, and an in-command assignment still resolves.

## 0.9.1 — 2026-10-20

### Changed

- Dropped Windows from the supported scope; removed the `GOOS=windows` vet step and the Windows installer follow-up.
- Closed remaining audit follow-ups: historical docs no longer claim the hunter registry is test-only; hunter usage already documented in `CLI_REFERENCE.md` and `main.go` help; version/tag consistency already enforced in CI and release.

## 0.9.0 — 2026-10-19

### Added

- Crux plugin: 55 shared skills (including 24 principle skills), 23 playbooks, native adapters for
  Claude Code, Codex, Antigravity and OpenCode2, session-scoped `crux-mode`, and shared Thai
  communication policy.
  Crux is a workflow plugin and does not create a second execution engine; Rein remains the single
  authority for contracts, routing, checkpoints, accounting and approvals.
- `rein config init` / `rein config check` for Crux provider configuration. The generated config
  disables model calls by default and requires a quota veto before probes or launches.
- `rein hunter triage`, `authorize-fix` and `start-fix` complete the controller → owner → worker
  lifecycle through the production CLI.
- Version consistency guard across all seven package and plugin manifests, including the Crux
  manifests; wired into CI and the release workflow.
- Crux plugin tarball is published as a release asset alongside the six platform binaries and
  `SHA256SUMS`.

### Fixed

- F-01/F-02: revision-body proof after a rebase and effective-latest verdict per reviewer. A newer
  `REQUEST_CHANGES` retracts an older `APPROVE`; there is no file-intersection fallback. Revision
  identity is computed from the named SHA, never the checkout `HEAD`.
- F-03: coordinator approvals are validated and recorded through the same hook entrypoint as worker
  approvals, with owner binding.
- F-04: hunter lifecycle reaches owner closure through the production CLI.
- F-05/F-06/F-07: npm tarball bootstraps from checksum-verified release assets, the wrapper cannot
  recurse into itself, and a failed native launch exits nonzero.
- F-08/F-09: E2E screenshots are confined to the evidence directory and scenario deadlines are
  enforced across waits and client calls. Client cancellation now kills the whole process group so
  a child process cannot keep a deadline from firing.
- F-10: active docs describe the shipped capability and separate historical receipts from live
  verification.
- F-12: preflight distinguishes ignored, untracked and Git-failure states; untracked source is fatal.
- Windows installer now downloads `rein-windows-<arch>.exe` and verifies `SHA256SUMS`, matching the
  POSIX installer. The PowerShell path is still unverified on a real Windows machine.

### Verified

- `go test -count=1 ./...`, `go test -race -count=1 ./...`, `go vet ./...`,
  `GOOS=windows go vet ./cmd/... ./internal/...`, and `gofmt -l .` clean.
- Node Crux suite 18/18, Python Crux suite 86/86, script suite, npm tarball/wrapper tests, and the
  offline Crux harness acceptance.
- `scripts/preflight-push.sh` passes on the release commit.

### Not verified in this release

- Live activation and Thai output on all four harnesses, a real two-provider handoff, remote Orca
  recovery/cancellation, and a real Windows install. These remain explicit gates; local fixtures are
  not live proof.
- npm registry publication. The release is a GitHub release; `npm whoami` was unauthenticated.

## 0.8.0 — 2026-10-18

- Automatic task and model selection (`rein route auto`), enforced routing and guarded Orca launches,
  canonical quota cooldowns, completion cleanup, and the release pipeline with checksum-verified
  platform assets.
