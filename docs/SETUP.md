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
| Vendor hooks (`rein hooks install`, section 8) | codex, agy, kiro, opencode verified in real runs | not yet | not yet |
| SessionStart installer | verified (Go build, release download, checksum refusal, 6 parallel starts) | not yet | needs Git Bash `sh` |
| Pipeline scripts | verified | should work (bash, python3, GNU timeout) | use WSL2 |

## 8. Guarding codex, agy, kiro and opencode workers

`rein hook --vendor <claude|codex|agy|kiro|opencode>` reads that CLI's hook event, maps it to one normalised action
(bash command, files written, cwd), runs the same judge as for Claude (`CheckBash`, ownership check, the report check on
stop) and answers in the CLI's own dialect. `rein hooks install <task>` writes the project hook files into the
contract's worktree (run it after the worktree exists), adds them to `<git-common-dir>/info/exclude`, records the
vendors in the contract (`hooks_installed`, both copies) and prints the launch flags. A file the repository already
tracks is refused, not overwritten (it would show up as the worker's uncommitted change).

| Vendor | File written | Launch (required) | Blocked by | Write tools judged | Stop (report check) | Known gaps |
|---|---|---|---|---|---|---|
| codex 0.161.0 | `.codex/hooks.json` (PreToolUse + Stop) | `--dangerously-bypass-hook-trust` **and** the two `-c hooks.PreToolUse=...` / `-c hooks.Stop=...` flags that `rein hooks install` prints | Claude-style `permissionDecision: deny` (model sees `Command blocked by PreToolUse hook: [rein] ...`) | `apply_patch` (every `*** Add/Update/Delete File:` and `*** Move to:` path in the patch), `Edit`/`Write`/`MultiEdit` | `decision: block`; the worker is sent back to write the report (verified) | shell-run `apply_patch` heredocs and interpreter writes are not parsed; Orca `worker-start --agent codex` cannot pass the flags |
| agy 1.3.1 | `.agents/hooks.json` (PreToolUse + flat Stop) | none; start agy inside the worktree | JSON `decision: deny` (model sees `tool call denied by pre-tool hook: [rein] ...`) | `write_to_file`, `replace_file_content`, `multi_replace_file_content`, and any tool whose args carry `TargetFile` | `decision: continue` with the reason; agy re-enters the loop (verified) | a write tool that names its file with another key is allowed and only logged |
| kiro-cli 2.21.0 | `.kiro/agents/rein.json` | `kiro-cli chat --agent rein ...` on **every** launch (the file defines the agent; without the flag no hook runs) | exit 2 + reason on stderr (model sees `PreToolHook blocked the tool execution: [rein] ...`) | `fs_write` (create, str_replace, insert, append), `write` | exit 2 only **warns**: kiro cannot force a continue, so a missing report is caught by `rein drift` (NO_REPORT) | `--agent rein` replaces the user's default agent (its MCP servers and prompt) for that run |
| opencode2 2.0.20 | `.opencode/plugins/rein/server.js` (v2 plugin, loads without config) | none; start opencode2 inside the worktree | the plugin pipes `{tool,input,cwd}` to `rein hook --vendor opencode` and throws on exit 2 (model sees the `[rein] ...` text; in a parallel batch it may see the first refusal for every call) | `edit`, `write`, `multiedit`, `patch`, `apply_patch` (`path`/`filePath`/`file_path`, or a patch body) | no stop hook is wired; `rein drift` (NO_REPORT) is the backstop | the `execute` code-mode tool (MCP catalog) is unclassified; a plugin that cannot run the binary denies the call |
| Claude (opt-in) | `.claude/settings.local.json` `hooks`, merged with the sandbox block | none | as in section 4 | Edit, Write, MultiEdit, NotebookEdit | `decision: block` | only for machines without the plugin: with the plugin installed the guard would run twice |

**Policy for tools the guard cannot classify:** they are allowed (reads must work) but every call is written to the seen
log with the tool name, and `rein drift` judges the resulting diff. A tool known to write files never bypasses the
ownership check: if its target cannot be read from the event, the call is denied.

**Binding.** Installed hooks run `rein hook --vendor X --task <name>` (the opencode plugin embeds the task and the
canonical worktree path). A bound hook judges that task's contract and worktree whatever directory the CLI reports, and
**denies** when the contract is missing or broken or the event cannot be parsed. The global Claude plugin hook (no
`--task`) keeps its lookup by directory and stays silent outside workers.

**Bash rules added for the vendors (they also apply to Claude workers through the global plugin hook):**
- Tool `workdir` arguments (codex, opencode) are the command's cwd. A mutating `git` command whose cwd is outside the
  worktree is judged as git in another repository, except in a scratch directory under the system temp dir (not the
  task worktrees' parent, the run dir or the contracts dir).
- A patch passed through a shell command is judged like an `apply_patch` call only when the command runs `apply_patch`
  or `applypatch` (a marker with no readable path is denied); `grep -F '*** Begin Patch' file` is judged as a plain
  command.
- Shells that read commands from stdin are denied: `bash -s`, `sh -i`, `bash` with no script, `... | bash`,
  `bash < file`, whatever positional arguments follow. A **literal heredoc** (`bash <<'EOF' ... EOF`) is judged: its
  body goes through the same checks. `bash script.sh`, `bash -c '...'`, `bash -euo pipefail -c ...`, `sh -e x.sh` stay
  allowed. (codex `write_stdin` does not reach PreToolUse, so typed input could not be judged.)
- The installed hook files are never editable by the worker: `.codex/hooks.json`, `.agents/hooks.json`,
  `.kiro/agents/rein.json`, `.opencode/plugins/rein/**`, `.claude/settings.local.json`, and the directories that hold
  them, against `rm`, `mv`, `cp` targets, redirects, `sed -i`, `tee`, `truncate`. An operand with glob characters is
  denied when it could expand to one of those paths or directories (`.codex/*`, `.*`, `.[a-z]*`; a leading dot needs a
  literal dot, so `*` alone is fine). `find` with `-delete`, `-exec`, `-execdir`, `-ok` or `-okdir` is denied when its
  search root holds a hook file (`.` included). `git stash --all/-a/--include-untracked/-u` is denied (it moves untracked
  and ignored files); `git clean -f` was already denied.
- Remaining known gaps (the guard is a seatbelt; drift and the OS sandbox are the backstops): python/node/other REPLs and
  `-e`/`-c` interpreter writes, `script`, `tmux`/`screen`, a patch applied after a `cd` in the same command (judged
  against the starting cwd), commands run by a tool the guard does not see (codex `mcp__*` runtimes, opencode `execute`,
  kiro `use_subagent`).

`rein hooks install` also refuses to write through a symlink (file or parent), keeps sibling hooks of a shared group,
calls `${CLAUDE_PLUGIN_DATA}/bin/rein` when it exists (else the running binary, with a warning if that is under a temp
or version-numbered directory), and quotes it for POSIX `sh` (Git Bash on Windows; PowerShell and cmd.exe quoting are
not covered).

**Seen log and `GUARD_INACTIVE`.** Every hook call inside a contracted worktree appends
`<ts> <vendor> <event> <tool> gen=<install id>` to `<contracts dir>/seen/<task>.log` (beside the contract index, so the
worker's and the coordinator's environments agree even if `PIPELINE_LOGDIR` differs). `rein drift <task>` reports
`GUARD_INACTIVE` (exit 1, naming the likely missing launch flag) when the worktree has commits and no `pretool` line of
the current install generation exists for the vendor named by `--expect-guard <vendor>`. When hooks are installed for
more than one vendor `--expect-guard` is **required** (otherwise exit 2, "cannot judge"), so a kiro fallback launched
without `--agent rein` is caught even after earlier codex lines; with a single installed vendor it defaults to that one. Each install records `hooks_installed_at` and a new
`hooks_generation`. The drift result also carries `head`; every revision range uses that sha, and a HEAD that moves
during the check is "cannot judge" (exit 2).

**Unclassified tools.** Probed with codex 0.161.0: `exec_command` reaches PreToolUse as `Bash` (judged like any
command); `write_stdin` does **not** reach PreToolUse (see the stdin-shell rule above); the JS runtime arrives as
`mcp__cua_repl__js` (allowed and logged). Not verified: kiro `use_subagent` (whether subagent tool calls run the agent's
hooks) and opencode's `execute` code-mode tool. agy treats any tool with a path-like argument (`TargetFile`,
`AbsolutePath`, `FilePath`, `Path`, `File`) as a write unless it is known to only read (`view_*`, `read_*`, `list_*`,
`find_*`, `grep_*`, `search_*`).

**Codex trust (verified with codex 0.161.0).**
- An untrusted project hook is skipped silently. The persisted trust is a hash in `~/.codex/config.toml`
  (`[hooks.state."<abs>/.codex/hooks.json:pre_tool_use:0:0"]`); rein never writes it.
- `--dangerously-bypass-hook-trust` (on `codex` and `codex exec`) is the only per-invocation way to run unreviewed hooks.
  A `-c hooks.state...` trust override does not work: the dotted key splits at the dot in `.codex`, and the inline-table
  form is accepted but not honoured.
- Project `.codex/hooks.json` is only read for a trusted project, and for a **linked git worktree** codex reads the main
  checkout's `.codex`, not the worktree's (verified: the worktree file was ignored even when trusted). Hooks passed with
  `-c hooks.PreToolUse=[...]` / `-c hooks.Stop=[...]` do not depend on either, so the launch line uses them (plus the
  bypass flag; without the flag these hooks are skipped too). The `.codex/hooks.json` file is still written for
  standalone clones.
- `codex exec` also adds a `[projects."<dir>"] trust_level = "trusted"` entry to `~/.codex/config.toml` for the directory
  it runs in: that is codex's own behaviour, remove such entries for throwaway directories if you do not want them.
- `orca orchestration worker-start --agent codex` takes only `--model` and `--effort` (no extra CLI arguments), so a codex
  worker that must be guarded is started in a shell terminal with the preamble route, or with the direct `codex exec`
  fallback, using the printed flags.

**Not verified:** Linux and Windows runs of any vendor hook; kiro and agy behaviour on a version other than the ones
listed; whether codex honours `hooks.state` trust given through `CODEX_HOME` (not tried).
