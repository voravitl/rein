# Rein v0.9.0 — 2026-10-19

## What shipped

- **Crux plugin (first bundled release).** 55 shared skills, 23 playbooks, native adapters for Claude
  Code, Codex, Antigravity and OpenCode2, session-scoped `crux-mode`, and the shared Thai
  communication policy. Crux is a workflow plugin, not a second execution engine: Rein remains the
  single authority for contracts, routing, checkpoints, accounting and approvals.
- **Audit remediation F-01–F-12.** Revision-body proof after a rebase, effective-latest verdicts per
  reviewer, coordinator approval validation, the full hunter controller → owner → worker lifecycle,
  checksum-verified npm bootstrap, E2E evidence confinement and deadlines, reconciled active docs,
  and fail-closed preflight. Revision identity is computed from the named SHA, not the checkout
  `HEAD`.
- **Crux + Rein integration.** `rein config init/check` produces private, disabled-by-default
  provider configuration; native launch-input validation covers Crux agents for Claude Code, Codex,
  Antigravity and OpenCode2; graph lifecycle and routing receipts preserve the approved provider and
  model.
- **Release hardening.** One version across seven package/plugin manifests, a version/tag guard in CI
  and the release workflow, a checksum-verified Windows release download, and a Crux plugin tarball
  published beside the six platform binaries and `SHA256SUMS`.
- **Thai communication skill.** Added the report/evidence rules and the `thai-scan.py` scanner; the
  repo copy and the global skill are in sync.

## Verified

- `go test -count=1 ./...`, `go test -race -count=1 ./...`, `go vet ./...`,
  `GOOS=windows go vet ./cmd/... ./internal/...`, and `gofmt -l .` clean.
- Node Crux suite 18/18, Python Crux suite 86/86, scripts suite 23/23, npm tarball/wrapper tests 3/3,
  and offline Crux harness acceptance.
- `scripts/preflight-push.sh` on the release commit; `scripts/check-version-consistency.sh v0.9.0`.
- `dist/crux-0.9.0.tgz` has no `__pycache__` or test-only files and carries manifest version 0.9.0.

## Not verified in this release

- Live activation and Thai output on all four harnesses, a real two-provider handoff, remote Orca
  recovery/cancellation, and a real Windows install. These remain explicit gates; local fixtures are
  not live proof.
- npm registry publication. `npm whoami` is unauthenticated; this release is published on GitHub.
  The npm package version in the repo is 0.9.0, but the registry still serves an older version.

## Assets

`rein-darwin-amd64`, `rein-darwin-arm64`, `rein-linux-amd64`, `rein-linux-arm64`,
`rein-windows-amd64.exe`, `rein-windows-arm64.exe`, `crux-0.9.0.tgz`, and `SHA256SUMS`.
