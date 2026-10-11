package guard

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/routing"
	"github.com/voravitl/rein/internal/run"
	"github.com/voravitl/rein/internal/spec"
	"mvdan.cc/sh/v3/syntax"
)

// The coordinator's Bash rules, on top of the worker's shell parser (bash.go): the parser finds the writes, these
// methods decide them under the coordinator's rules (ctx.coord != nil).

// checkSpec runs spec lint on the given spec file against the task's contract (ADR 0002 B3).
// Returns denial reason if lint fails, "" if passes.
func (p *coordPolicy) checkSpec(specPath, taskName string) string {
	c, err := contract.Load(taskName)
	if err != nil {
		return fmt.Sprintf("cannot load contract for task %q: %v", taskName, err)
	}
	return p.runSpecLint(specPath, c)
}

// checkSpecWithContract runs spec lint using a contract path instead of task name.
func (p *coordPolicy) checkSpecWithContract(specPath, contractPath string) string {
	c, err := contract.LoadFile(contractPath)
	if err != nil {
		return fmt.Sprintf("cannot load contract from %q: %v", contractPath, err)
	}
	return p.runSpecLint(specPath, c)
}

// runSpecLint performs the actual spec lint check.
func (p *coordPolicy) runSpecLint(specPath string, c *contract.Contract) string {
	result, _ := spec.Check(specPath, c, "")

	if len(result.Violations) == 0 {
		return "" // All checks passed
	}

	// Build denial message with all violations
	return fmt.Sprintf("spec lint failed with %d violation(s): %s",
		len(result.Violations), strings.Join(result.Violations, "; "))
}

// Legacy fixtures can name rein tasks/files explicitly; opaque Orca IDs never do.
func (p *coordPolicy) checkLegacySpec(rest []string) string {
	text, hasSpec := flagValue(rest, "--spec")
	if !hasSpec {
		return ""
	}
	reason := ""
	if task, has := flagValue(rest, "--task"); has && !strings.HasPrefix(task, "task_") {
		reason = p.checkSpec(text, task)
	} else if file, has := flagValue(rest, "--contract"); has {
		reason = p.checkSpecWithContract(text, file)
	}
	if reason != "" {
		return "SPEC_LINT_FAILED: " + reason
	}
	return ""
}

// bash judges a coordinator Bash command.
func (p *coordPolicy) bash(cmd, cwd string) string {
	x := &ctx{c: &contract.Contract{}, top: p.loc.Top, cwd: cwd, vars: map[string]string{},
		pipeRHS: map[*syntax.Stmt]bool{}, coord: p}
	if h, err := os.UserHomeDir(); err == nil {
		x.vars["HOME"] = h
	}
	return x.script(cmd)
}

// sourcePreflight checks the supported adjacent source-check/launch pair in its current shell state.
func (x *ctx) sourcePreflight(left, right *syntax.Stmt) bool {
	argsOf := func(s *syntax.Stmt) []string {
		call, ok := s.Cmd.(*syntax.CallExpr)
		if !ok || s.Negated || s.Background || len(call.Assigns) != 0 || len(s.Redirs) != 0 {
			return nil
		}
		var args []string
		for _, w := range call.Args {
			args = append(args, x.word(w))
		}
		return args
	}
	l, r := argsOf(left), argsOf(right)
	if len(r) > 3 && base(r[0]) == "rein" && r[1] == "route" && r[2] == "launch" {
		for i, a := range r[3:] {
			if a == "--" {
				r = r[i+4:]
				break
			}
		}
	}
	if len(l) != 5 || base(l[0]) != "rein" || l[1] != "spec" || l[2] != "check" || !filepath.IsAbs(l[3]) || len(r) == 0 || !isOrca(base(r[0])) {
		return false
	}
	wt, has := flagValue(r[1:], "--worktree")
	if !has || !strings.HasPrefix(wt, "path:") || !filepath.IsAbs(strings.TrimPrefix(wt, "path:")) {
		return false
	}
	c, err := contract.Load(filepath.Base(strings.TrimPrefix(wt, "path:")))
	if err != nil || c.Name != l[4] || contract.Real(c.Worktree) != contract.Real(strings.TrimPrefix(wt, "path:")) {
		return false
	}
	if _, err := os.Stat(l[3]); err != nil {
		return false
	}
	return x.coord.runSpecLint(l[3], c) == ""
}

// gitEnvNames point git away from the repository the guard watches.
var gitEnvNames = map[string]bool{"GIT_DIR": true, "GIT_COMMON_DIR": true, "GIT_WORK_TREE": true, "GIT_INDEX_FILE": true}

const gitEnvReason = "GIT_DIR / GIT_COMMON_DIR / GIT_WORK_TREE / GIT_INDEX_FILE point git away from the repository the guard watches; use `git -C <dir>`"

// stateEnvNames redirect where rein reads the owner's policy, evidence, prices, providers, contracts and run marker. They are the
// owner's configuration: a coordinator that sets, replaces or unsets them in a command could make rein judge its own work by a
// different policy or a different ledger.
var stateEnvNames = map[string]bool{"REIN_PROFILE": true, "PIPELINE_LEDGER": true, "PIPELINE_PRICES": true, "PIPELINE_CONTRACTS": true,
	"PIPELINE_FALLBACK": true, "REIN_RUN_DIR": true}

const stateEnvReason = "REIN_PROFILE / PIPELINE_LEDGER / PIPELINE_PRICES / PIPELINE_CONTRACTS / PIPELINE_FALLBACK / REIN_RUN_DIR (and env -i) change which policy, evidence and state rein uses: they are the owner's configuration, set in the user's environment before the session, never by the coordinator"

// coordEnvName says why a coordinator command may not set, replace or unset an environment variable ("" = it may).
func (x *ctx) coordEnvName(name string) string {
	switch {
	case x.coord == nil:
		return ""
	case gitEnvNames[name]:
		return gitEnvReason
	case stateEnvNames[name]:
		return stateEnvReason
	}
	return ""
}

func (x *ctx) coordAssign(a *syntax.Assign) string {
	if a == nil || a.Name == nil {
		return ""
	}
	return x.coordEnvName(a.Name.Value)
}

func (x *ctx) coordEnvPair(pair string) string {
	if name, _, ok := strings.Cut(pair, "="); ok {
		return x.coordEnvName(name)
	}
	return ""
}

// coordEnvOption judges an option of `env`: -u NAME / --unset=NAME / -uNAME remove a variable, -i / --ignore-environment remove all.
// It reports how many arguments the option takes (0 = not an env-clearing option) and the denial, if any.
func (x *ctx) coordEnvOption(rest []string, i int) (take int, reason string) {
	if x.coord == nil {
		return 0, ""
	}
	a := rest[i]
	switch {
	case (a == "-u" || a == "--unset") && i+1 < len(rest):
		return 2, x.coordEnvName(rest[i+1])
	case strings.HasPrefix(a, "--unset="):
		return 1, x.coordEnvName(strings.TrimPrefix(a, "--unset="))
	case a == "--ignore-environment" || a == "-":
		return 1, stateEnvReason
	case strings.HasPrefix(a, "-u") && len(a) > 2 && !strings.HasPrefix(a, "--"):
		return 1, x.coordEnvName(a[2:])
	case strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "i"):
		return 1, stateEnvReason
	}
	return 0, ""
}

// hasFlag reports whether args hold -name / --name / -name=v / --name=v.
func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == "-"+name || a == "--"+name || strings.HasPrefix(a, "-"+name+"=") || strings.HasPrefix(a, "--"+name+"=") {
			return true
		}
	}
	return false
}

// userOnly names the user-only command (ADR 0002 B0 rule 6) that `rein <rest>` is, or "".
func userOnly(name string, rest []string) string {
	if name != "rein" {
		return ""
	}
	nf := nonFlags(rest)
	switch {
	case len(nf) >= 2 && nf[0] == "run" && nf[1] == "allow":
		return "rein run allow"
	case len(nf) >= 2 && nf[0] == "run" && nf[1] == "end" && hasFlag(rest, "abandon"):
		return "rein run end --abandon"
	case len(nf) >= 2 && nf[0] == "budget" && nf[1] == "raise":
		return "rein budget raise"
	case len(nf) >= 2 && nf[0] == "route" && nf[1] == "reconcile":
		return "rein route reconcile"
	case len(nf) >= 1 && nf[0] == "approve":
		return "rein approve"
	}
	return ""
}

// coordCall applies the coordinator-only rules to one command: user-only commands and Orca spawns.
func (x *ctx) coordCall(raw, name string, rest []string) string {
	cmd, via := userOnly(name, rest), ""
	if cmd == "" && strings.Contains(raw, "$?") { // a command name that expands at run time could be rein
		if cmd = userOnly("rein", rest); cmd != "" {
			via = " (the command name expands at run time, so the guard reads it as rein)"
		}
	}
	if cmd != "" {
		return fmt.Sprintf("`%s` is for the user%s: run it in your own terminal; the coordinator never authorizes itself", cmd, via)
	}
	if name == "unset" {
		for _, a := range rest {
			if r := x.coordEnvName(a); r != "" {
				return r
			}
		}
	}
	if name == "rein" && len(rest) >= 1 && rest[0] == "route" && hasFlag(rest[1:], "config") {
		return "ROUTE_CONFIG: the approved providers are the owner's configuration (PIPELINE_FALLBACK or ~/.config/rein/fallback-chain.json), not a per-call choice: leave --config out"
	}
	if name == "rein" && len(rest) >= 2 && rest[0] == "route" && rest[1] == "launch" {
		return x.coordRouteLaunch(rest[2:])
	}
	if isOrca(name) && len(rest) >= 2 && rest[0] == "terminal" && rest[1] == "create" {
		if command, ok := flagValue(rest, "--command"); ok {
			if strings.Contains(command, "$?") {
				return "ROUTE_REQUIRED: terminal --command must be a statically checkable routed launch"
			}
			child := *x
			child.deep++
			child.vars = map[string]string{}
			if reason := child.script(command); reason != "" {
				return reason
			}
		}
	}
	if harnessLaunch(name, rest) {
		return "ROUTE_REQUIRED: use `rein route launch --task <contract> --run <run> -- <harness argv>` so the selected model and route are checked and recorded"
	}
	if isOrca(name) && slices.Contains(rest, "worker-start") {
		return x.coordWorkerStart(rest)
	}
	return ""
}

func isOrca(name string) bool {
	return name == "orca" || name == "orca-dev" || name == "orca-ide"
}

func flagValue(args []string, name string) (string, bool) {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1], true
		}
		if v, ok := strings.CutPrefix(a, name+"="); ok {
			return v, true
		}
	}
	return "", false
}

// coordWorkerStart denies `orca orchestration worker-start` into an existing worktree of the repo that has no task
// contract: its worker would run without a contract or guard. `terminal send` names only a terminal handle, which
// says nothing about a worktree, so it cannot be judged here.
// Also runs spec lint if --spec is provided (ADR 0002 B3).
// Also checks scope ruling for task approval (ADR 0002 B4.4).
func (x *ctx) coordWorkerStart(rest []string, routed ...bool) string {
	// Check tick staleness and budget before allowing worker spawn
	if reason := x.coord.checkTickAndBudget(); reason != "" {
		return reason
	}

	if x.coord.m.Run != "" {
		count := 0
		for _, a := range rest {
			if a == "--worktree" || strings.HasPrefix(a, "--worktree=") {
				count++
			}
		}
		if count > 1 {
			return "ROUTE_REQUIRED: worker-start needs one unambiguous --worktree"
		}
	}
	if x.coord.m.Run != "" && (len(routed) == 0 || !routed[0]) {
		return "ROUTE_REQUIRED: use `rein route launch` for worker-start so launch admission is recorded"
	}
	wt, has := flagValue(rest, "--worktree")
	if !has || strings.HasPrefix(wt, "new-") {
		return x.coord.checkLegacySpec(rest) // preserve the existing new-worktree policy
	}

	dir := ""
	switch {
	case wt == "current" || wt == "active":
		if x.cwdUnknown {
			return fmt.Sprintf("--worktree %s after a cd the guard could not follow; use path:<absolute worktree>", wt)
		}
		dir = x.cwd
	case strings.HasPrefix(wt, "path:"):
		dir = x.abs(strings.TrimPrefix(wt, "path:"))
	default:
		return fmt.Sprintf("--worktree %q cannot be checked against the task contracts; use path:<absolute worktree>", wt)
	}
	tree := x.coord.treeOf(contract.Real(dir))
	if tree == "" {
		return x.coord.checkLegacySpec(rest)
	}
	c, err := contract.Load(filepath.Base(tree))
	if err != nil || contract.Real(c.Worktree) != contract.Real(dir) {
		return fmt.Sprintf("worker-start into %s: that exact worktree has no readable task contract, so its worker would run unguarded; %s", dir, coordHow)
	}
	if inline, hasSpec := flagValue(rest, "--spec"); hasSpec {
		task, hasTask := flagValue(rest, "--task")
		if _, legacyFile := flagValue(rest, "--contract"); legacyFile || (hasTask && !strings.HasPrefix(task, "task_")) {
			if reason := x.coord.checkLegacySpec(rest); reason != "" {
				return reason
			}
		} else {
			result, _ := spec.CheckText(inline, c, "")
			if len(result.Violations) != 0 {
				return fmt.Sprintf("SPEC_LINT_FAILED: %s", strings.Join(result.Violations, "; "))
			}
		}
	}

	if !x.coord.m.Allows("task", c.Name) {
		return fmt.Sprintf("task %q is not in the approved scope ruling: ask the user to run `rein run allow --task %s --reason <text>` before spawning this worker", c.Name, c.Name)
	}
	if task, has := flagValue(rest, "--task"); has && strings.HasPrefix(task, "task_") && !x.specChecked {
		return "SPEC_PREFLIGHT_REQUIRED: launch an Orca Task ID with `rein spec check <absolute-source-spec> <rein-contract-name> && orca orchestration worker-start --task <task_id> --worktree path:<exact-worktree> ...`; do not invent a source spec"
	}
	if reason := x.coord.checkTickAndBudget(c); reason != "" {
		return reason
	}
	return x.checkWorkerRoute(c, rest)
}

func (x *ctx) coordRouteLaunch(args []string) string {
	split := -1
	for i, a := range args {
		if a == "--" {
			split = i
			break
		}
	}
	if split < 0 || split+1 >= len(args) {
		return "ROUTE_REQUIRED: route launch requires -- <actual harness argv>"
	}
	outer, inner := args[:split], args[split+1:]
	task, has := flagValue(outer, "--task")
	runID, hasRun := flagValue(outer, "--run")
	if !has || !hasRun || runID != x.coord.m.Run {
		return "ROUTE_MISMATCH: route launch needs the contract and active --run"
	}
	c, err := contract.Load(task)
	if err != nil {
		return "ROUTE_INVALID: " + err.Error()
	}
	if isOrca(base(inner[0])) {
		return x.coordWorkerStart(inner[1:], true)
	}
	agent := base(inner[0])
	switch agent {
	case "kiro-cli":
		agent = "kiro"
	case "agy":
		agent = "antigravity"
	}
	model, ok := flagValue(inner[1:], "--model")
	if !ok {
		model, ok = flagValue(inner[1:], "-m")
	}
	if !ok {
		return "ROUTE_REQUIRED: routed harness needs an explicit --model"
	}
	d, err := routing.Validate(c, runID, agent, model)
	if err != nil {
		return "ROUTE_INVALID: " + err.Error()
	}
	if !strings.HasPrefix(d.Chain, "worker:") {
		return "ROUTE_INVALID: worker launch requires a worker routing chain"
	}
	if d.Launch != "shell" {
		return "ROUTE_INVALID: direct harness requires a shell launch route"
	}
	if !x.coord.m.Allows("task", c.Name) {
		return "ROUTE_INVALID: contract is not in the approved scope ruling"
	}
	if reason := x.coord.checkTickAndBudget(); reason != "" {
		return reason
	}
	return x.coord.checkTickAndBudget(c)
}

func harnessLaunch(name string, args []string) bool {
	if !slices.Contains([]string{"claude", "codex", "agy", "kiro-cli", "opencode", "opencode2"}, name) {
		return false
	}
	if len(args) == 1 && slices.Contains([]string{"--help", "-h", "--version", "-v", "help", "version", "models"}, args[0]) {
		return false
	}
	if len(args) == 2 && args[0] == "auth" && args[1] == "status" {
		return false
	}
	return true
}

func (x *ctx) checkWorkerRoute(c *contract.Contract, args []string) string {
	if x.coord.m.Run == "" {
		return ""
	}
	values := map[string]string{}
	for _, flag := range []string{"--run", "--agent", "--model"} {
		count := 0
		for i, a := range args {
			if a == flag {
				count++
				if i+1 < len(args) {
					values[flag] = args[i+1]
				}
			} else if v, ok := strings.CutPrefix(a, flag+"="); ok {
				count++
				values[flag] = v
			}
		}
		if count != 1 || values[flag] == "" || strings.HasPrefix(values[flag], "-") || strings.Contains(values[flag], "$?") {
			return "ROUTE_REQUIRED: worker-start requires one explicit --run, --agent and --model from `rein route prepare`"
		}
	}
	if values["--run"] != x.coord.m.Run {
		return "ROUTE_MISMATCH: worker-start --run must equal the active run " + x.coord.m.Run
	}
	d, err := routing.Validate(c, values["--run"], values["--agent"], values["--model"])
	if err != nil {
		return "ROUTE_INVALID: " + err.Error()
	}
	if !strings.HasPrefix(d.Chain, "worker:") || d.Launch != "orca" {
		return "ROUTE_INVALID: Orca worker launch requires a worker chain and Orca launch route"
	}
	if slices.Contains([]string{"codex", "kiro", "opencode", "opencode2"}, d.Agent) {
		return "ROUTE_INVALID: native harness flags are not forwarded by Orca; use a shell route"
	}
	return ""
}

// contractedRouteCall applies launch admission to child spawns from a hooked worker.
func (x *ctx) contractedRouteCall(raw, name string, args []string) string {
	loc, found := run.Find(x.top)
	if !found {
		return ""
	}
	m, err := run.Load(loc.Marker)
	if err != nil {
		return "ROUTE_INVALID: cannot load run: " + err.Error()
	}
	if !loc.Belongs(m) || !m.OwnerAlive() || m.Run == "" {
		return ""
	}
	checked := *x
	checked.coord = &coordPolicy{m: m, loc: loc}
	return checked.coordCall(raw, name, args)
}

// coordFind judges find: -delete removes its roots, -exec/-ok run a command per file (judged with {} as the target).
func (x *ctx) coordFind(args, roots []string) string {
	del := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-delete":
			del = true
		case "-exec", "-execdir", "-ok", "-okdir":
			j := i + 1
			for j < len(args) && args[j] != ";" && args[j] != `\;` && args[j] != "+" {
				j++
			}
			if r := x.call(args[i+1 : j]); r != "" {
				return r
			}
			i = j
		}
	}
	if !del {
		return ""
	}
	if len(roots) == 0 {
		roots = []string{"."}
	}
	for _, r := range roots {
		if reason := x.write(r); reason != "" {
			return reason
		}
	}
	return ""
}

// coordGit judges a git command: the options that write files, and the commands that rewrite the work tree or
// index of a worktree of the repo. push, commit, tag, worktree add and the read-only commands stay allowed.
func (x *ctx) coordGit(repo string, redirect bool, sub string, rest []string) string {
	if redirect {
		return "git --git-dir / --work-tree point git away from the repository the guard watches; use `git -C <dir>`"
	}
	if r := x.gitOutputs(sub, rest); r != "" {
		return r
	}
	dir := x.cwd
	if repo != "" {
		dir = x.abs(repo)
	}
	if !(x.cwdUnknown && repo == "") && x.coord.treeOf(contract.Real(dir)) == "" {
		return "" // another repository
	}
	if sub == "rm" || sub == "mv" { // judged like rm / mv: each operand is a write
		saved := x.cwd
		x.cwd = dir
		defer func() { x.cwd = saved }()
		for _, t := range nonFlags(rest) {
			if r := x.write(t); r != "" {
				return r
			}
		}
		return ""
	}
	if why := gitTreeMutation(sub, rest); why != "" {
		return fmt.Sprintf("git %s %s; %s", sub, why, coordHow)
	}
	return ""
}

// gitTreeMutation says why a git command rewrites the work tree or the index, "" when it does not.
func gitTreeMutation(sub string, rest []string) string {
	has := func(names ...string) bool {
		for _, a := range rest {
			if slices.Contains(names, a) {
				return true
			}
		}
		return false
	}
	nf := nonFlags(rest)
	const why = "rewrites the work tree or index"
	switch sub {
	case "restore":
		if has("--staged", "-S") && !has("--worktree", "-W") {
			return ""
		}
		return why
	case "checkout", "switch":
		if has("--", "-f", "--force", "--theirs", "--ours", "-p", "--patch", "--discard-changes", "-m", "--merge", ".") {
			return why
		}
		if sub == "checkout" && len(nf) >= 2 && !has("-b", "-B", "--orphan") { // git checkout <rev> <path>
			return why
		}
	case "reset":
		if has("--hard", "--merge", "--keep") {
			return why
		}
	case "stash":
		if len(rest) == 0 || strings.HasPrefix(rest[0], "-") || slices.Contains([]string{"push", "save", "pop", "apply", "branch"}, rest[0]) {
			return why
		}
	case "merge", "pull", "cherry-pick", "revert", "rebase", "am":
		return why
	case "clean":
		if has("--force", "-f") || slices.ContainsFunc(rest, func(a string) bool {
			return strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "f")
		}) {
			return "deletes untracked files"
		}
	case "apply":
		if !has("--check", "--stat", "--numstat", "--summary") {
			return why
		}
	}
	return ""
}
