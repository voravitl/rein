# Changelog

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
  enforced across waits and client calls.
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
