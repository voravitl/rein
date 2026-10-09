package guard

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/voravitl/rein/internal/contract"
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
	if reason := x.script(cmd); reason != "" {
		return reason
	}
	return x.taskSpecPreflight(cmd)
}

// Opaque Orca IDs have no local source spec. Require an explicit, successful source
// preflight in the same shell call; this is not durable Orca Task-spec attestation.
func (x *ctx) taskSpecPreflight(cmd string) string {
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(cmd), "")
	if err != nil {
		return err.Error()
	}
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
	checked := map[*syntax.CallExpr]bool{}
	asynchronous := map[*syntax.CallExpr]bool{}
	syntax.Walk(f, func(n syntax.Node) bool {
		if stmt, ok := n.(*syntax.Stmt); ok && (stmt.Background || stmt.Negated) {
			syntax.Walk(stmt, func(child syntax.Node) bool {
				if call, ok := child.(*syntax.CallExpr); ok {
					asynchronous[call] = true
				}
				return true
			})
		}
		return true
	})
	syntax.Walk(f, func(n syntax.Node) bool {
		b, ok := n.(*syntax.BinaryCmd)
		if !ok || b.Op != syntax.AndStmt {
			return true
		}
		left, right := argsOf(b.X), argsOf(b.Y)
		if len(left) != 5 || base(left[0]) != "rein" || left[1] != "spec" || left[2] != "check" || !filepath.IsAbs(left[3]) || len(right) == 0 || !isOrca(base(right[0])) {
			return true
		}
		wt, has := flagValue(right[1:], "--worktree")
		if !has || !strings.HasPrefix(wt, "path:") || !filepath.IsAbs(strings.TrimPrefix(wt, "path:")) {
			return true
		}
		c, err := contract.Load(filepath.Base(strings.TrimPrefix(wt, "path:")))
		if err != nil || c.Name != left[4] || contract.Real(c.Worktree) != contract.Real(strings.TrimPrefix(wt, "path:")) {
			return true
		}
		if _, err := os.Stat(left[3]); err != nil || x.coord.runSpecLint(left[3], c) != "" {
			return true
		}
		if call := b.Y.Cmd.(*syntax.CallExpr); !asynchronous[call] {
			checked[call] = true
		}
		return true
	})
	reason := ""
	syntax.Walk(f, func(n syntax.Node) bool {
		if reason != "" {
			return false
		}
		if stmt, ok := n.(*syntax.Stmt); ok {
			if call, ok := stmt.Cmd.(*syntax.CallExpr); ok && len(call.Args) > 0 && slices.Contains([]string{"bash", "sh", "zsh", "dash", "ksh"}, base(x.word(call.Args[0]))) {
				for _, redirect := range stmt.Redirs {
					if redirect.Hdoc != nil {
						if body, ok := literalText(redirect.Hdoc); ok {
							reason = x.taskSpecPreflight(body)
						}
					}
				}
			}
		}
		call, ok := n.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		var args []string
		for _, w := range call.Args {
			args = append(args, x.word(w))
		}
		name := base(args[0])
		if name == "bash" || name == "sh" || name == "zsh" || name == "dash" || name == "ksh" {
			for i, arg := range args[1:] {
				if strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && strings.Contains(arg, "c") && i+2 < len(args) {
					reason = x.taskSpecPreflight(args[i+2])
					break
				}
			}
			return reason == ""
		}
		if name == "eval" {
			reason = x.taskSpecPreflight(strings.Join(args[1:], " "))
			return reason == ""
		}
		if wrappers[name] || name == "xargs" {
			for i, arg := range args[1:] {
				if isOrca(base(arg)) {
					args = args[i+1:]
					break
				}
			}
		}
		if !isOrca(base(args[0])) {
			return true
		}
		task, has := flagValue(args, "--task")
		wt, _ := flagValue(args, "--worktree")
		if slices.Contains(args, "worker-start") && has && strings.HasPrefix(task, "task_") && !strings.HasPrefix(wt, "new-") && wt != "" && !checked[call] && (wt == "current" || wt == "active" || x.coord.treeOf(contract.Real(strings.TrimPrefix(wt, "path:"))) != "") {
			reason = "SPEC_PREFLIGHT_REQUIRED: launch an Orca Task ID with `rein spec check <absolute-source-spec> <rein-contract-name> && orca orchestration worker-start --task <task_id> --worktree path:<exact-worktree> ...`; do not invent a source spec"
			return false
		}
		return reason == ""
	})
	return reason
}

// gitEnvNames point git away from the repository the guard watches.
var gitEnvNames = map[string]bool{"GIT_DIR": true, "GIT_COMMON_DIR": true, "GIT_WORK_TREE": true, "GIT_INDEX_FILE": true}

const gitEnvReason = "GIT_DIR / GIT_COMMON_DIR / GIT_WORK_TREE / GIT_INDEX_FILE point git away from the repository the guard watches; use `git -C <dir>`"

func (x *ctx) coordAssign(a *syntax.Assign) string {
	if x.coord != nil && a != nil && a.Name != nil && gitEnvNames[a.Name.Value] {
		return gitEnvReason
	}
	return ""
}

func (x *ctx) coordEnvPair(pair string) string {
	if x.coord == nil {
		return ""
	}
	if name, _, ok := strings.Cut(pair, "="); ok && gitEnvNames[name] {
		return gitEnvReason
	}
	return ""
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
func (x *ctx) coordWorkerStart(rest []string) string {
	// Check tick staleness and budget before allowing worker spawn
	if reason := x.coord.checkTickAndBudget(); reason != "" {
		return reason
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
		if reason := x.coord.runSpecLint(inline, c); reason != "" {
			return "SPEC_LINT_FAILED: " + reason
		}
	}
	if !x.coord.m.Allows("task", c.Name) {
		return fmt.Sprintf("task %q is not in the approved scope ruling: ask the user to run `rein run allow --task %s --reason <text>` before spawning this worker", c.Name, c.Name)
	}
	return ""
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
