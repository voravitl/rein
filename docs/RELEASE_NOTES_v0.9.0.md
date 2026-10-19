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
  skill was recognized. ✅
- **Kiro (kiro-cli 2.31.0):** Crux skills symlinked into `~/.kiro/skills/`. Live prompt in
  Thai returned a correct answer and the `crux-mode` skill was recognized. ✅
- **OpenCode (opencode 2.0.32):** Crux skills symlinked into `~/.config/opencode/skills/`.
  Live prompt in Thai returned a correct answer and the `crux-mode` skill was recognized. ✅
- **3 of 4 harnesses have live evidence.** Claude Code (just verified), Antigravity, and
  OpenCode2 all return correct Thai responses and recognize crux-mode. Codex remains
  unverified (credit-depleted).
- **ADLC workflow end-to-end (full run):** Created contract (allow README.md), launched agy
  worker on `run_c64540eb1c2a`, worker added a comment to README.md, guard denied write to
  main.go with clear message, drift detected uncommitted changes, worker closed cleanly
  (terminal closed, worktree removed, worker status → failed). Full guard+drift+contract+launch+
  close lifecycle verified. ✅
- **Orca recovery safety check:** `rein orca close --check-only` reports `safe: clean tree,
  HEAD reachable in another ref`. `rein orca tidy` audits terminals correctly.
  `rein orca run-create` creates runs successfully.
- **Orca worker launch + close (recovery test):** Launched agy worker on `run_288f3daca5da`,
  then closed it via `rein orca close` (terminal closed, worktree removed). Worker status
  changed to `failed`. `rein orca tidy` confirmed no orphaned terminals. ✅
- **npm publish dry-run:** `npm publish --dry-run` succeeded for `rein@0.9.0` (89 files,
  326 kB). The package is publishable; only authentication is missing.
- **install.ps1:** PowerShell parser reports zero syntax errors. Runtime on real Windows
  remains unverified (no Windows machine / PowerShell available on macOS).
- **Docker:** available (v29.4.0) but cannot run Windows containers on macOS, so the
  PowerShell installer cannot be exercised end-to-end.

## Not verified in this release

- Live activation on **Codex** (credit-depleted; Claude Code, Antigravity, OpenCode2 verified live).
- Real two-provider handoff (needs two providers with credits; `rein route auto` requires task+run+profile).
- Remote Orca recovery with actual worker kill (not exercised; `close --check-only` and `tidy` verified).
- Real Windows install (no Windows machine; PowerShell syntax verified only).
- npm registry publication (`npm whoami` = ENEEDAUTH; dry-run succeeds).

## Assets

`rein-darwin-amd64`, `rein-darwin-arm64`, `rein-linux-amd64`, `rein-linux-arm64`,
`rein-windows-amd64.exe`, `rein-windows-arm64.exe`, `crux-0.9.0.tgz`, and `SHA256SUMS`.
