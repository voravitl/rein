# Setting up rein

rein ships as one Claude Code plugin with two layers:

| Layer | What you get | Needs |
|---|---|---|
| **Guard** (always on) | `rein` binary; PreToolUse + Stop hooks that enforce a task contract inside worker worktrees; `rein contract / sandbox / drift / ledger / providers` | Claude Code, git, Go *or* a release binary |
| **Pipeline** (on demand) | skill `rein:worktree-pipeline` (coordinator playbook, templates, scripts), agents `rein:orca-swarm` and `rein:orca-steward`, skill `rein:setup` | Orca, OMC, the worker CLIs you route to, a project profile and pack |

You can use the guard alone (with any orchestrator) and add the pipeline later.

## 1. Prerequisites

Run `sh scripts/doctor.sh` in a clone (or `/rein:setup` after installing) to check all of these at once.

### Required (guard)
| Tool | Why | Install | Tested |
|---|---|---|---|
| Claude Code | plugin, hooks (exec form), `CLAUDE_PLUGIN_DATA` | https://code.claude.com | 2.1.293 |
| git | worktrees, drift check (`diff`, `status -z`, `range-diff`) | system package | 2.50 |
| POSIX `sh` | the SessionStart installer | built in; on Windows, Git for Windows (Git Bash) | macOS sh |
| Go 1.26+ (`go.mod`) **or** curl | build the binary on first session, or download the release binary for the plugin version (checked against `SHA256SUMS`) | https://go.dev/dl/ or `brew install go` | go 1.27.1 |

### Pipeline (multi-agent runs)
| Tool | Why | Install | Tested |
|---|---|---|---|
| **Orca** app + `orca` CLI | runs, worktrees, workers in visible terminals, messages (`worker_done`, questions) | the Orca desktop app; its CLI must be on `PATH` | 1.4.219 |
| **OMC plugin** (oh-my-claudecode) | MCP tools the coordinator uses: `notepad_write_priority` (run pointer), `session_search` (resume), `wiki_add` (run log) | `/plugin marketplace add https://github.com/Yeachan-Heo/oh-my-claudecode.git`, then `/plugin install oh-my-claudecode@omc` | 5.6.2 |
| **OMC CLI** `omc` (npm `oh-my-claude-sisyphus`) | the agent prompts `advise.sh` uses as advisor roles (`critic`, `tracer`, `code-reviewer`, ...) from `$(npm root -g)/oh-my-claude-sisyphus/agents/` | `npm i -g oh-my-claude-sisyphus && omc setup` (Node.js + npm) | 5.6.2 |
| python3 | waiter (`ocloop2.py`), `orca_cleanup.py`, `cleanup_worktrees.py`, `advise.sh` | system package | 3.12 |
| bash, GNU `timeout` | `advise.sh`, `rebase_proof.sh`, `push_merge_pinned.sh` | macOS: `brew install coreutils`, and put `$(brew --prefix)/opt/coreutils/libexec/gnubin` on `PATH` if only `gtimeout` exists | bash 3.2, coreutils 9.11 |
| perl | `advise.sh` with the kiro provider (strips ANSI codes) | system package | 5.34 |
| `glab` (+ `glab auth login`) | MRs, pinned merges (`push_merge_pinned.sh` is GitLab-only today) | https://gitlab.com/gitlab-org/cli | 1.115.0 |

### Worker, reviewer and advisor CLIs (install only the ones you route to)
| CLI | Role | Login | Tested |
|---|---|---|---|
| `codex` (npm `@openai/codex`) | worker, cross-vendor review gate, default advisor (`codex exec -s read-only`) | `codex login` | 0.161.0 |
| `agy` (antigravity) | Gemini worker (preamble route in Orca) | its own login | 1.3.1 |
| `opencode2` | cheap worker on its own provider quota; model from `~/.config/opencode/opencode.jsonc` | its own provider auth | 2.0.20 |
| `kiro-cli` | fallback worker/reviewer when Claude quota is low; advisor with `ADVISE_PROVIDER=kiro` | `kiro-cli login` | 2.21.0 |
| Claude Code itself | Sonnet/Haiku/Opus workers and reviewers through Orca | your Claude login | |

### Optional
| Tool | Why |
|---|---|
| docker | project gates that use throwaway containers (backend suite, isolated stack) |
| codegraph | `codegraph_explore` for code-checked spec facts (repos with a `.codegraph/` index) |
| gh | GitHub issues; merges are GitLab-only today |

## 2. Install the plugin

```
/plugin marketplace add voravitl/rein
/plugin install rein@rein
```
or from a shell: `claude plugin marketplace add voravitl/rein && claude plugin install rein@rein`.

Start a new session. The plugin's **SessionStart** hook (`scripts/ensure-binary.sh`) puts the binary at
`~/.claude/plugins/data/rein-rein/bin/rein` (`${CLAUDE_PLUGIN_DATA}`), which survives plugin updates:
- with Go on `PATH` it builds from the plugin source (about 2 s once modules are cached) and runs the result before it
  replaces anything;
- without a working Go it downloads `rein-<os>-<arch>` from the GitHub release `v<plugin version>` and checks
  `SHA256SUMS`. A release build may be older than the source you installed (it is tagged by version, not by commit),
  so it is marked as a download and replaced by a source build when a session starts with a Go that can build it.
  A failed build is remembered for 6 hours per source and Go version, so a broken toolchain does not cost a rebuild on
  every start; `/rein:setup` (`--force`) retries at once. The checksum proves the file arrived intact, not who built
  it: it trusts this GitHub repository's releases;
- a checksum of the Go sources is stored beside the binary and written last, so a session start after an update that
  changed no code does nothing, and an interrupted install is retried at the next start;
- many workers starting at once install once: a lock directory holds the owner's pid (a killed owner's lock is broken
  at once). The lock only saves work; correctness comes from per-process temp files, validation and an atomic move;
- everything runs inside one deadline (270 s) that fits the hook's 300 s timeout: at most about 120 s waiting for
  another session, a build bounded so at least 60 s remain for the download fallback;
- when nothing works it prints one line into the session that started ("guard binary NOT installed ..." or, when an
  older binary exists, "NOT updated ... the previous guard is still active"). Fix it with `/rein:setup`.

Then run `/rein:setup`: it re-checks the binary, runs the doctor, removes duplicates from a manual install, offers
to link `rein` onto your `PATH`, and helps you write a profile and pack.

**Develop or try without installing:** `claude --plugin-dir ~/src/rein` (the binary goes to `~/.claude/plugins/data/rein-inline/bin/rein`, verified).

**Without the plugin** (guard only): `sh scripts/install.sh` builds `bin/rein`; add the hooks to
`~/.claude/settings.json` as the README shows.

## 3. Configure a project

| File | Purpose | Start from |
|---|---|---|
| `~/.config/rein/profiles/<project>.json` | machine-checked project rules: worktree root, protected containers/ports/commands/scripts, never-edit paths, local artifacts, sandbox exclusions, `pack` | `examples/profile.example.json` |
| `~/.config/rein/packs/<project>/` | the project's gates, safety notes, isolated-stack recipe, deploy notes, rule addenda for workers/reviewers/advisors | `examples/pack/` and `skills/worktree-pipeline/references/project-pack.md` |
| `~/.config/rein/fallback-chain.json` | providers, worker chains per task type, review chains, quota signals (`rein providers`) | `examples/fallback-chain.example.json` |
| `~/.claude/omc/worktree-pipeline/model-ledger.jsonl` | model ledger (written by `rein ledger add/call`) | created on first write |

Profiles and packs hold internal names, hosts and ports: keep them local, never in a public repository.

## 4. Verify the guard

The hook is silent outside a worktree that has a contract. This throwaway check proves it denies inside one:
```sh
REIN=~/.claude/plugins/data/rein-rein/bin/rein     # or: rein, if linked
tmp=$(mktemp -d); git init -q "$tmp/repo" && git -C "$tmp/repo" -c user.name=t -c user.email=t@t commit -q --allow-empty -m init
mkdir -p "$tmp/wt" && git -C "$tmp/repo" worktree add -q "$tmp/wt/demo"
export PIPELINE_CONTRACTS="$tmp/contracts"
$REIN contract new --name demo --issue 1 --run-dir "$tmp/run" --worktree-root "$tmp/wt" --allow 'src/**' --scope S1 >/dev/null
echo '{"hook_event_name":"PreToolUse","tool_name":"Bash","cwd":"'"$tmp/wt/demo"'","tool_input":{"command":"git push"}}' | $REIN hook
# -> {"hookSpecificOutput":{...,"permissionDecision":"deny","permissionDecisionReason":"[rein] workers never push; ..."}}
rm -rf "$tmp"; unset PIPELINE_CONTRACTS
```

## 5. How rein fits with Orca and OMC

```
main session ──> rein:orca-swarm (Opus coordinator)
                   │  OMC: notepad run pointer · session_search resume · wiki_add run log
                   │  advise.sh: OMC critic/tracer/code-reviewer prompts, read-only codex or kiro
                   │  rein contract new ──> rein sandbox ──> orca worktree create / worker-start
                   ▼
         Orca workers (Claude / codex / agy / opencode / kiro), one worktree each
           Claude workers: rein guard hook (plugin) + OS sandbox (settings.local.json)
                   │ worker_done
                   ▼
         rein drift ──> project gates (pack) ──> cross-vendor review ──> MR
                   ▼  (user approves)
         rein:orca-steward: rebase proof · re-gate · pinned merge · isolated stack · cleanup
```

- **Orca** is the orchestrator: it creates worktrees, starts each worker in a visible terminal, and carries messages.
  rein does not replace it; rein makes each worker's room smaller (contract, guard, sandbox) and judges the result
  (drift). Orca's CLI must stay outside the sandbox (`"sandbox_excluded_commands": ["orca *"]`) so sandboxed workers
  can still send `worker_done`.
- **OMC** supplies memory and second opinions: the run pointer survives compaction (`notepad_write_priority`), the
  previous run is found with `session_search`, the run is logged with `wiki_add`, and its agent prompts become
  read-only advisors through `advise.sh`. Do **not** run OMC's unattended modes (`ralph`, `autopilot`, `team`,
  `ultragoal`) in the coordinator session: they arm OMC's git guardrails, which block the steward's pushes and
  merges. Do not use `omc ask` with repo access: it runs codex and agy without a sandbox.
- **Both hooks coexist:** OMC's hooks and rein's hooks are independent. rein's guard only speaks inside a
  contracted worktree, so it adds nothing to ordinary sessions (about 5 ms per tool call).

## 6. Migrating from a manual install

If you used rein before the plugin (hooks in `~/.claude/settings.json`, the skill in `~/.claude/skills/`, agents in
`~/.claude/agents/`), `/rein:setup` step 3 finds them. Otherwise:
1. Move project-specific content (gate scripts, safety notes, repo ids, deploy notes) into a project pack and point
   the profile's `pack` at it.
2. Install the plugin and start a new session; check `rein version` from the data dir.
3. Remove the old `rein hook` entries from `settings.json` (back it up first), so the guard does not run twice.
4. Archive the old skill and agents (`tar czf ...`), then remove them, so trigger phrases route to `rein:*` only.
5. Update sandbox exclusions in each profile if gate scripts moved into the pack.

## 7. Platform notes
| | macOS | Linux | Windows |
|---|---|---|---|
| Guard hook | verified in real sessions (manual settings, and as a plugin with user settings excluded) | CI only | CI only; the hook runs `${CLAUDE_PLUGIN_DATA}/bin/rein` and relies on Windows finding `rein.exe` (not yet seen in a real session) |
| `rein sandbox` (Claude Code OS sandbox) | verified | supported by Claude Code, not yet verified | not available natively; use WSL2 |
| SessionStart installer | verified (Go build, release download, checksum refusal, 6 parallel starts) | not yet | needs Git Bash `sh` |
| Pipeline scripts | verified | should work (bash, python3, GNU timeout) | use WSL2 |
