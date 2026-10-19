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

## Partially verified in this session

- **Antigravity (agy 1.4.2):** Crux plugin installed via `agy plugin install plugins/crux`
  (55 skills processed). Live prompt in Thai returned a correct answer and the `crux-mode`
  skill was recognized. **One of four harnesses has live evidence.**
- **npm publish dry-run:** `npm publish --dry-run` succeeded for `rein@0.9.0` (89 files,
  326 kB). The package is publishable; only authentication is missing.
- **install.ps1:** PowerShell parser reports zero syntax errors. Runtime on real Windows
  remains unverified (no Windows machine / PowerShell available on macOS).
- **Docker:** available (v29.4.0) but cannot run Windows containers on macOS, so the
  PowerShell installer cannot be exercised end-to-end. A Linux container with `pwsh`
  parses the script but cannot execute Windows paths.

## Not verified in this release

- Live activation and Thai output on Claude Code and Codex (both rate-limited this session);
  agy has live evidence, OpenCode2 not yet tested end-to-end with Crux.
- Real two-provider handoff (needs two providers with credits).
- Remote Orca recovery/cancellation (Orca CLI available; not exercised).
- Real Windows install (no Windows machine; PowerShell syntax verified only).
- npm registry publication (`npm whoami` = ENEEDAUTH; dry-run succeeds).

## Assets

`rein-darwin-amd64`, `rein-darwin-arm64`, `rein-linux-amd64`, `rein-linux-arm64`,
`rein-windows-amd64.exe`, `rein-windows-arm64.exe`, `crux-0.9.0.tgz`, and `SHA256SUMS`.
