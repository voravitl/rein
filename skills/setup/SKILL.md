---
name: setup
description: "Set up or repair the rein plugin on this machine: install or rebuild the guard binary, check every prerequisite (Go, git, Orca, OMC, codex/agy/opencode/kiro, glab, docker), remove duplicate rein hooks or old local copies of the worktree-pipeline skill and orca agents, and create a project profile, project pack and fallback chain from the examples. Use it after installing rein, when a session says the rein guard binary is not installed, when the user says 'setup rein', 'ติดตั้ง rein', 'rein doctor', or before the first worktree-pipeline run on a new project."
---

# rein setup

Plugin root: `${CLAUDE_PLUGIN_ROOT}`. Plugin data (kept across updates): `${CLAUDE_PLUGIN_DATA}`.
The guard binary lives at `${CLAUDE_PLUGIN_DATA}/bin/rein`; the plugin's hooks run it from there.

Work through the steps in order. Show the user what you found before changing anything outside the plugin's own
data dir, and ask (AskUserQuestion, recommended option first) before each change to their settings or files.

## 1. Guard binary
```sh
CLAUDE_PLUGIN_ROOT="${CLAUDE_PLUGIN_ROOT}" CLAUDE_PLUGIN_DATA="${CLAUDE_PLUGIN_DATA}" sh "${CLAUDE_PLUGIN_ROOT}/scripts/ensure-binary.sh" --force
"${CLAUDE_PLUGIN_DATA}/bin/rein" version
```
It builds with Go when Go is installed, otherwise downloads the release binary for the plugin version and checks it
against `SHA256SUMS`. When both fail, the user needs Go 1.26+ (https://go.dev/dl/; `go.mod` sets the minimum) or a published release.
The build log is `${CLAUDE_PLUGIN_DATA}/bin/build.log`.

Outside a worktree that has a contract the hook is silent by design. To see it deny, run the throwaway
contract check in `${CLAUDE_PLUGIN_ROOT}/docs/SETUP.md` ("Verify the guard").

**Optional, on PATH:** the coordinator and `advise.sh` call `rein` by name. Offer to link it:
`mkdir -p ~/.local/bin && ln -sf "${CLAUDE_PLUGIN_DATA}/bin/rein" ~/.local/bin/rein` (Windows: copy `rein.exe`
into a directory on `PATH`). The link survives plugin updates because the data dir does.

## 2. Prerequisites
```sh
CLAUDE_PLUGIN_DATA="${CLAUDE_PLUGIN_DATA}" sh "${CLAUDE_PLUGIN_ROOT}/scripts/doctor.sh"
```
Report the MISSING lines grouped as: required (rein will not work), pipeline (multi-agent runs), vendors (only the
ones the user routes work to), optional. Give the install command for each from `docs/SETUP.md`. Logins (`codex
login`, `kiro-cli login`, `glab auth login`) are interactive: tell the user to run them with `! <command>`.

## 3. Duplicates from a manual install
Check, and offer to fix each one found (back up first, `cp <file> <file>.bak-rein-plugin-<date>`):
- **Hooks:** `~/.claude/settings.json` (and the project's `.claude/settings*.json`) with a `PreToolUse` or `Stop`
  hook whose command is a `rein` binary and whose args are `["hook"]`. With the plugin enabled the guard would run
  twice. Remove only those hook entries; keep every other hook.
- **Skill and agents:** `~/.claude/skills/worktree-pipeline/`, `~/.claude/agents/orca-swarm.md`,
  `~/.claude/agents/orca-steward.md`. The plugin ships them as `rein:worktree-pipeline`, `rein:orca-swarm` and
  `rein:orca-steward`; two copies route the same trigger phrases to different files. Before archiving
  (`tar czf ~/.cache/worktree-pipeline/local-skill-<date>.tgz ...`), move anything project-specific in them into
  that project's pack (step 4). Never delete without the archive.

## 4. Project profile and pack (once per project)
Ask for the project name, repo path and what must never be touched (containers, ports, DB, owner-only scripts).
- Profile: copy `${CLAUDE_PLUGIN_ROOT}/examples/profile.example.json` to `~/.config/rein/profiles/<project>.json`,
  fill it in, and set `"pack": "~/.config/rein/packs/<project>"`.
- Pack: copy `${CLAUDE_PLUGIN_ROOT}/examples/pack/` to `~/.config/rein/packs/<project>/` and fill `PACK.md`,
  `worker-rules.md`, `review-rules.md`, `advisor-rules.md` and the gate scripts with the project's real commands
  (read the repo's `AGENTS.md`/`CLAUDE.md`/CI config for them; cite what you used).
- Gate scripts that drive docker go into the profile's `sandbox_excluded_commands` with their absolute path.
- Both stay local: never commit a profile or pack to a public repository.

## 5. Fallback chain (optional)
Copy `${CLAUDE_PLUGIN_ROOT}/examples/fallback-chain.example.json` to `~/.config/rein/fallback-chain.json`, keep only
the providers the user has, then run `rein providers` and show the UP/QUOTA/DOWN table.

## 6. Report
One line per step: done / skipped (why) / needs the user (exact command). End with the doctor's exit code.
