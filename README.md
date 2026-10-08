# rein

**Keep cheap and fallback worker models on task.**

When a multi-agent pipeline runs out of quota and falls back to smaller or free models, the work tends to drift:
edits outside the task, "done" claims that are not true, a `git push` nobody asked for, or a command against the
live stack. `rein` holds the reins with three deterministic layers, so the judgement does not depend on the model
behaving well.

```
                 ┌──────────── task contract (rein contract) ────────────┐
                 │ allow / deny globs · scope ids · report path · profile │
                 └──────────────┬─────────────────────────┬──────────────┘
          while the worker runs │                         │ when the worker says "done"
                                ▼                         ▼
                  guard hook (rein hook)          drift check (rein drift)
                  Claude Code workers             every vendor: Claude, codex,
                  blocks the action now           Gemini/Antigravity, opencode, kiro
                                │                         │
                                └──── under an OS sandbox (the real wall) ────┘
```

| Layer | Covers | Catches |
|---|---|---|
| **Contract** | everyone | the rules: which files the task may edit, which never, which scope items the report must cover |
| **Guard hook** | Claude Code workers | `git push` / rebase / `reset --hard`, `gh` / `glab`, docker prune, dependency installs, writes outside the worktree, writes to never-edit or out-of-ownership files, overwriting other tasks' reports or the contract, stopping without a report; plus your project's protected containers, ports, scripts and commands |
| **Drift check** | every vendor | out-of-scope or never-edit files in the diff (renames included), uncommitted work, scope items without a heading/status/evidence, files claimed but not changed, `TODO`/`.skip`/stubs, secrets, missing issue refs, size budget |

The guard parses each Bash command with a real shell grammar ([mvdan.cc/sh](https://github.com/mvdan/sh)), so
`bash -c "git -C . push"`, `env X=1 timeout 30 git push` and `echo $(git push)` are caught, while
`git add . && git commit -m "explain why workers never git push"` is not blocked.

> **The guard is a seatbelt, not a wall.** It cannot see writes made through interpreters (`python -c`, `node -e`).
> Pair it with an OS sandbox (Claude Code's Bash sandbox, `codex -s workspace-write`) and always run `rein drift`.

## Install

As a Claude Code plugin (this repository is also its marketplace):

```
/plugin marketplace add voravitl/rein
/plugin install rein@rein
```

Start a new session: the plugin's SessionStart hook builds the binary with Go (or downloads the release binary and
checks `SHA256SUMS`) into the plugin data dir, which survives updates. Then run `/rein:setup` to check every
prerequisite and set up a project. Full prerequisites, Orca and OMC integration, and migration from a manual
install: [`docs/SETUP.md`](docs/SETUP.md).

| Plugin part | Name |
|---|---|
| Guard hooks | SessionStart (install binary), PreToolUse + Stop (`rein hook`, exec form, silent outside a contracted worktree) |
| Skills | `rein:setup`, `rein:worktree-pipeline` (coordinator playbook for Orca, with templates and scripts) |
| Agents | `rein:orca-swarm` (Opus coordinator), `rein:orca-steward` (Sonnet/Haiku mechanical jobs) |

Without the plugin (guard only), build it yourself (Go 1.26+; one static binary, no runtime):

```sh
git clone https://github.com/voravitl/rein && cd rein
sh scripts/install.sh          # builds bin/rein for this machine
sh scripts/build.sh            # optional: dist/rein-<os>-<arch> + SHA256SUMS for macOS, Linux, Windows
```

and add the hook to `~/.claude/settings.json` (do not do this as well as installing the plugin, or the guard runs twice):

```json
{ "hooks": {
  "PreToolUse": [{ "matcher": "Bash|Edit|Write|MultiEdit|NotebookEdit",
                   "hooks": [{ "type": "command", "command": "/path/to/rein/bin/rein", "args": ["hook"], "timeout": 10 }] }],
  "Stop":       [{ "hooks": [{ "type": "command", "command": "/path/to/rein/bin/rein", "args": ["hook"], "timeout": 10 }] }]
} }
```

The hook is silent unless the session runs inside a git worktree that has a contract, so it is safe to install
globally. Measured on Apple Silicon: about 5 ms per call (p50), inside or outside a worker.

## Use

**1. A project profile and pack** (once per project; keep both out of public repos; `/rein:setup` helps):

```sh
mkdir -p ~/.config/rein/profiles ~/.config/rein/packs
cp examples/profile.example.json ~/.config/rein/profiles/myapp.json   # edit containers, ports, scripts
cp -R examples/pack ~/.config/rein/packs/myapp                          # gates, safety notes, rule addenda
```

**2. A contract per task**, written by the coordinator before the worker starts:

```sh
rein contract new --name fix-login --issue 42 --run-dir ~/runs/sprint-7 \
  --profile ~/.config/rein/profiles/myapp.json \
  --allow 'src/auth/**,tests/auth/**' --scope S1,S2,S3 --max-changed-lines 400
```

It prints the ownership block to paste into the worker's spec. The worker's worktree is
`<worktree_root>/<name>`; its report goes to `<run-dir>/reports/<name>.md`.

**2b. Put the worktree in the OS sandbox** (Claude workers; macOS, Linux, WSL2):

```sh
git worktree add ~/worktrees/myapp/fix-login origin/main     # or your orchestrator's "create worktree" step
rein sandbox fix-login                                         # before the worker starts
```

This writes `.claude/settings.local.json` in the worktree: sandbox on, no unsandboxed retries, writes limited to the
worktree, the temp dir and the report file, plus the profile's `sandbox_excluded_commands` (for example your
orchestrator CLI, or a test script that drives docker) and `sandbox_allowed_domains`. Verified on macOS: a
`python -c` write outside the worktree, which the guard cannot see, fails with `Operation not permitted`.
Native Windows has no Claude Code sandbox (use WSL2).

**3. Judge the result** when the worker reports done (any vendor):

```sh
rein drift fix-login --claimed-files src/auth/login.ts,tests/auth/login.test.ts
# exit 0 = no drift (warnings may remain), 1 = drift -> send a fix round, 2 = cannot judge
```

The report must have one heading per scope id with a status (`done`, `partly`, `not done`) and evidence under it.

**4. Track quality and cost per model** (optional):

```sh
rein ledger add --task fix-login --type backend --worker codex:gpt-6.1-sol --reviewer claude:claude-opus-5-5 \
  --rounds 2 --approved --drift 0 --minutes 38
rein ledger report            # rounds-to-approve, blockers, false claims, drift, tokens, credits, USD per model
rein ledger suggest           # routing changes, only when every compared model has >= 3 tasks
```

**5. Find who can take work when a quota runs out** (optional):

```sh
cp examples/fallback-chain.example.json ~/.config/rein/fallback-chain.json   # your providers and chains
rein providers --chain worker:backend --skip-claude
```

Each provider is probed with a one-line prompt and reported as `UP`, `QUOTA` or `DOWN`; the first `UP` member of
each chain is printed. Review chains should only list strong models: when none is up, reviews pause.

## Configuration

| What | Where |
|---|---|
| Contracts (hook lookup) | `~/.cache/worktree-pipeline/contracts/<name>.json`, override `PIPELINE_CONTRACTS` |
| Guard denials log | `~/.cache/worktree-pipeline/logs/guard.log`, override `PIPELINE_LOGDIR` |
| Default profile | `REIN_PROFILE` |
| Project pack | the profile's `pack` field (read by the worktree-pipeline skill; `PIPELINE_PACK` for `advise.sh`) |
| Plugin binary | `${CLAUDE_PLUGIN_DATA}/bin/rein` (`~/.claude/plugins/data/rein-rein/bin/rein`) |
| Ledger / prices | `PIPELINE_LEDGER`, `PIPELINE_PRICES` (`{"<model>": {"usd_per_mtok": N}}`) |
| Fallback chains | `--config`, `PIPELINE_FALLBACK`, `<bin>/../config/fallback-chain.json`, `~/.config/rein/fallback-chain.json` |

## Status

| | macOS | Linux | Windows |
|---|---|---|---|
| Unit tests + hook smoke test (CI on every push) | ✅ | ✅ | ✅ |
| Real Claude Code session: guard hook (exec form) | ✅ verified (manual settings and as plugin) | not yet | not yet |
| Real Claude Code session: plugin SessionStart installer | ✅ verified (`--plugin-dir`) | not yet | not yet |
| Real Claude Code session: `rein sandbox` | ✅ verified | not yet | n/a (no native sandbox; use WSL2) |

On Windows the hooks run `${CLAUDE_PLUGIN_DATA}/bin/rein` and rely on Windows finding `rein.exe`; that has not been
seen in a real Claude Code session yet. Git Bash paths (`/c/...`, `/tmp/...`) are mapped to native paths before they
are judged (found by the Windows CI leg). No release has been published yet, so machines without Go cannot install
the binary until the first `v*` tag.

Design and the reasons for Go: [`docs/adr/0001-rein-supervisor.md`](docs/adr/0001-rein-supervisor.md).

## Development

```sh
gofmt -w . && go vet ./... && go test ./...
```

## License

MIT
