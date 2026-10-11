package guard

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/glob"
	"mvdan.cc/sh/v3/syntax"
)

// The Bash guard is a seatbelt, not a sandbox: it parses the command with a real shell grammar and stops the
// actions a worker must never take, plus file writes it can see (redirects and common file commands).
// Writes it cannot see (python -c, node -e, ...) are left to the coordinator's drift check and the OS sandbox.

// portRx builds the "protected port" matcher from the profile; nil when the profile protects no ports.
func portRx(ports []int) *regexp.Regexp {
	if len(ports) == 0 {
		return nil
	}
	ps := make([]string, len(ports))
	for i, p := range ports {
		ps[i] = strconv.Itoa(p)
	}
	return regexp.MustCompile(`(?i)\b(localhost|127\.0\.0\.1|0\.0\.0\.0|\[::1\]|host\.docker\.internal)(:|%3a)(` + strings.Join(ps, "|") + `)\b`)
}

// wrappers run the rest of their arguments as a command.
var wrappers = map[string]bool{"env": true, "sudo": true, "nohup": true, "time": true, "command": true,
	"exec": true, "nice": true, "timeout": true, "gtimeout": true, "stdbuf": true, "caffeinate": true}

type ctx struct {
	c           *contract.Contract
	top         string // real worktree root
	cwd         string
	cwdUnknown  bool              // a cd we could not follow: relative writes are no longer judgeable
	vars        map[string]string // simple VAR=literal assignments seen so far
	deep        int
	ports       *regexp.Regexp
	stdin       stdinKind             // where the statement being checked reads stdin from
	hdoc        string                // the heredoc body, when stdin == stdinHeredoc
	pipeRHS     map[*syntax.Stmt]bool // statements that read a pipe
	specChecked bool                  // current direct call has an adjacent successful source preflight
	coord       *coordPolicy          // set when the coordinator's Bash is judged (coordinator.go): other rules apply
}

type stdinKind int

const (
	stdinTTY     stdinKind = iota // terminal or inherited: unknown to the guard
	stdinPipe                     // a pipeline feeds it
	stdinFile                     // < file, <<< here-string, or another redirect
	stdinHeredoc                  // a literal heredoc, which the guard can read
)

// valueOpts lists, per wrapper, the options that consume the next argument.
var valueOpts = map[string]map[string]bool{
	"env":      {"-u": true, "--unset": true, "-C": true, "--chdir": true},
	"sudo":     {"-u": true, "-g": true, "-h": true, "-p": true, "-C": true, "-D": true, "-r": true, "-t": true, "-U": true, "-T": true},
	"timeout":  {"-s": true, "--signal": true, "-k": true, "--kill-after": true},
	"gtimeout": {"-s": true, "--signal": true, "-k": true, "--kill-after": true},
	"nice":     {"-n": true, "--adjustment": true},
	"time":     {"-f": true, "--format": true, "-o": true, "--output": true},
	"xargs": {"-I": true, "-n": true, "-P": true, "-L": true, "-s": true, "-d": true, "-E": true, "-a": true,
		"--max-args": true, "--max-procs": true, "--max-lines": true, "--delimiter": true, "--arg-file": true, "--eof": true},
}

// textOnly commands only print or search text: a port or script name in their arguments is not an action.
var textOnly = map[string]bool{"git": true, "echo": true, "printf": true, "grep": true, "egrep": true, "fgrep": true,
	"rg": true, "ag": true, "ack": true, "cat": true, "head": true, "tail": true, "less": true, "wc": true}

// CheckBash returns a non-empty reason when the command must be denied.
func CheckBash(cmd string, c *contract.Contract, top, cwd string) string {
	x := &ctx{c: c, top: top, cwd: cwd, vars: map[string]string{}, ports: portRx(c.Profile.ProtectPorts), pipeRHS: map[*syntax.Stmt]bool{}}
	if h, err := os.UserHomeDir(); err == nil {
		x.vars["HOME"] = h
	}
	return x.script(cmd)
}

func (x *ctx) script(src string) string {
	if x.deep > 4 {
		return "command nests shells too deeply to check; run the inner command directly"
	}
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), "")
	if err != nil {
		return fmt.Sprintf("could not parse the command (%v); split it into simpler commands", err)
	}
	preflight := map[*syntax.CallExpr]bool{}
	asynchronous := map[*syntax.CallExpr]bool{}
	if x.coord != nil {
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
	}
	var reason string
	syntax.Walk(f, func(n syntax.Node) bool {
		if reason != "" {
			return false
		}
		switch n := n.(type) {
		case *syntax.Stmt:
			x.stdin, x.hdoc = stdinTTY, ""
			if x.pipeRHS[n] {
				x.stdin = stdinPipe
			}
			for _, r := range n.Redirs {
				switch r.Op {
				case syntax.RdrIn, syntax.WordHdoc:
					x.stdin = stdinFile
				case syntax.Hdoc, syntax.DashHdoc:
					x.stdin = stdinFile // unreadable unless the body is literal
					if r.Hdoc != nil {
						if body, ok := literalText(r.Hdoc); ok {
							x.stdin, x.hdoc = stdinHeredoc, body
						}
					}
				}
				switch r.Op {
				case syntax.RdrOut, syntax.AppOut, syntax.RdrAll, syntax.AppAll, syntax.ClbOut, syntax.RdrInOut:
					if r.Word != nil {
						if t := x.word(r.Word); t != "" && !strings.HasPrefix(t, "&") {
							reason = x.write(t)
						}
					}
				}
			}
		case *syntax.BinaryCmd:
			if x.coord != nil && n.Op == syntax.AndStmt {
				if call, ok := n.Y.Cmd.(*syntax.CallExpr); ok && !asynchronous[call] && x.sourcePreflight(n.X, n.Y) {
					preflight[call] = true
				}
			}
			if n.Op == syntax.Pipe || n.Op == syntax.PipeAll {
				x.pipeRHS[n.Y] = true
			}
		case *syntax.DeclClause: // export / declare / local / readonly X=literal
			for _, a := range n.Args {
				if r := x.coordAssign(a); r != "" {
					reason = r
					return false
				}
				x.assign(a)
			}
		case *syntax.CallExpr:
			for _, a := range n.Assigns {
				if r := x.coordAssign(a); r != "" {
					reason = r
					return false
				}
			}
			if len(n.Args) == 0 { // plain assignments: p=../x
				for _, a := range n.Assigns {
					x.assign(a)
				}
				return true
			}
			args := make([]string, 0, len(n.Args))
			for _, w := range n.Args {
				args = append(args, x.word(w))
			}
			x.specChecked = preflight[n]
			reason = x.call(args)
		}
		return reason == ""
	})
	return reason
}

// literalText returns a word's text when it has no expansion in it (a quoted heredoc body).
func literalText(w *syntax.Word) (string, bool) {
	var b strings.Builder
	for _, p := range w.Parts {
		l, ok := p.(*syntax.Lit)
		if !ok {
			return "", false
		}
		b.WriteString(l.Value)
	}
	return b.String(), true
}

func (x *ctx) assign(a *syntax.Assign) {
	if a == nil || a.Name == nil {
		return
	}
	if a.Value == nil || a.Append || a.Array != nil || a.Index != nil {
		delete(x.vars, a.Name.Value)
		return
	}
	if v := x.word(a.Value); !strings.Contains(v, "$?") {
		x.vars[a.Name.Value] = v
	} else {
		delete(x.vars, a.Name.Value)
	}
}

// word returns the literal value of a shell word, resolving $VAR / ${VAR} from earlier literal assignments;
// anything else that expands at run time becomes "$?".
func (x *ctx) word(w *syntax.Word) string {
	var b strings.Builder
	var walk func(parts []syntax.WordPart)
	walk = func(parts []syntax.WordPart) {
		for _, p := range parts {
			switch p := p.(type) {
			case *syntax.Lit:
				b.WriteString(p.Value)
			case *syntax.SglQuoted:
				b.WriteString(p.Value)
			case *syntax.DblQuoted:
				walk(p.Parts)
			case *syntax.ParamExp:
				v, ok := "", false
				if p.Param != nil && p.Exp == nil && p.Repl == nil && p.Slice == nil && p.Index == nil && !p.Length && !p.Excl && !p.Width {
					if p.Param.Value == "PWD" && !x.cwdUnknown {
						v, ok = x.cwd, true
					} else {
						v, ok = x.vars[p.Param.Value]
					}
				}
				if ok {
					b.WriteString(v)
				} else {
					b.WriteString("$?")
				}
			default:
				b.WriteString("$?")
			}
		}
	}
	walk(w.Parts)
	return b.String()
}

func base(a string) string {
	a = strings.ToLower(filepath.Base(filepath.FromSlash(a)))
	return strings.TrimSuffix(a, ".exe")
}

func nonFlags(args []string) []string {
	var out []string
	options := true
	for _, a := range args {
		if options && a == "--" {
			options = false
			continue
		}
		if a != "" && (!options || !strings.HasPrefix(a, "-")) {
			out = append(out, a)
		}
	}
	return out
}

func (x *ctx) call(args []string) string {
	if len(args) == 0 {
		return ""
	}
	prof := &x.c.Profile
	name := base(args[0])
	rest := args[1:]
	if x.coord != nil { // the coordinator pushes, merges and drives gh/glab: only its own rules apply
		if r := x.coordCall(args[0], name, rest); r != "" {
			return r
		}
	} else {
		if (isOrca(name) && strings.Contains(strings.Join(rest, " "), "worker-start")) || harnessLaunch(name, rest) || (name == "rein" && len(rest) >= 2 && rest[0] == "route" && rest[1] == "launch") {
			if r := x.contractedRouteCall(args[0], name, rest); r != "" {
				return r
			}
		}
		for _, a := range args {
			if textOnly[name] {
				break
			}
			if x.ports != nil && x.ports.MatchString(a) {
				return fmt.Sprintf("protected port in %q (profile %s: the live stack is off limits)", a, prof.Name)
			}
			for _, s := range prof.OwnerScripts {
				if strings.HasSuffix(filepath.ToSlash(a), s) {
					return fmt.Sprintf("%s belongs to the owner (profile %s)", s, prof.Name)
				}
			}
		}
		if r := x.deniedByProfile(name, rest); r != "" {
			return r
		}
	}
	switch {
	case name == "cd" || name == "pushd":
		t := nonFlags(rest)
		switch {
		case len(t) == 0:
			x.cwd = x.vars["HOME"]
		case strings.Contains(t[0], "$?") || t[0] == "-":
			x.cwdUnknown = true
		default:
			x.cwd = x.abs(t[0])
		}
	case name == "popd":
		x.cwdUnknown = true
	case wrappers[name] || name == "xargs":
		// skip the wrapper's options (and the values they take), VAR=value pairs and a duration, then judge the rest
		vo := valueOpts[name]
		i, sawDuration := 0, false
		envDir := ""
		envDirSet := false
		withEnvDir := func(f func() string) string {
			if name != "env" || !envDirSet {
				return f()
			}
			oldCwd, oldUnknown := x.cwd, x.cwdUnknown
			if strings.Contains(envDir, "$?") || (x.cwdUnknown && !filepath.IsAbs(envDir) && !strings.HasPrefix(envDir, "~/")) {
				x.cwdUnknown = true
			} else {
				x.cwd = x.abs(envDir)
				x.cwdUnknown = false
			}
			defer func() { x.cwd, x.cwdUnknown = oldCwd, oldUnknown }()
			return f()
		}
	opts:
		for i < len(rest) {
			a := rest[i]
			if name == "env" {
				switch {
				case a == "-S" || a == "--split-string":
					if i+1 >= len(rest) {
						return "env split-string needs a literal argument to check"
					}
					if envDirSet && (envSplitHasChdir(rest[i+1]) || envSplitArgsHaveChdir(rest[i+2:])) {
						return "env split-string cannot combine with another chdir; use explicit env arguments"
					}
					return withEnvDir(func() string { return x.envSplit(rest[i+1], rest[i+2:]) })
				case strings.HasPrefix(a, "--split-string="):
					payload := strings.TrimPrefix(a, "--split-string=")
					if envDirSet && (envSplitHasChdir(payload) || envSplitArgsHaveChdir(rest[i+1:])) {
						return "env split-string cannot combine with another chdir; use explicit env arguments"
					}
					return withEnvDir(func() string { return x.envSplit(payload, rest[i+1:]) })
				case strings.HasPrefix(a, "-S"):
					if envDirSet && (envSplitHasChdir(a[2:]) || envSplitArgsHaveChdir(rest[i+1:])) {
						return "env split-string cannot combine with another chdir; use explicit env arguments"
					}
					return withEnvDir(func() string { return x.envSplit(a[2:], rest[i+1:]) })
				case (a == "-C" || a == "--chdir") && i+1 < len(rest):
					envDir, envDirSet = rest[i+1], true
					i += 2
					continue
				case strings.HasPrefix(a, "--chdir="):
					envDir, envDirSet = strings.TrimPrefix(a, "--chdir="), true
					i++
					continue
				case strings.HasPrefix(a, "-C") && len(a) > 2:
					envDir, envDirSet = a[2:], true
					i++
					continue
				case a == "-C" || a == "--chdir":
					return "env chdir needs a directory to check"
				}
				take, why := x.coordEnvOption(rest, i)
				if why != "" {
					return why
				}
				if take > 0 {
					i += take
					continue
				}
			}
			switch {
			case name == "time" && (a == "-o" || a == "--output") && i+1 < len(rest):
				if r := x.write(rest[i+1]); r != "" {
					return r
				}
				i += 2
				continue
			case vo[a]:
				i += 2
				continue
			case strings.HasPrefix(a, "-"):
				if name == "env" && x.coord != nil && a != "--" && a != "-v" && a != "--debug" && a != "-0" && a != "--null" && !strings.HasPrefix(a, "--chdir=") && !strings.HasPrefix(a, "-C") {
					return "env option cannot be checked; use explicit env arguments"
				}
				i++
				continue
			case name == "env" && strings.Contains(a, "="):
				if r := x.coordEnvPair(a); r != "" {
					return r
				}
				i++
				continue
			case (name == "timeout" || name == "gtimeout") && !sawDuration && isNumberish(a):
				sawDuration = true
				i++
				continue
			}
			break opts // first positional: the wrapped command starts here
		}
		if i < len(rest) {
			return withEnvDir(func() string { return x.call(rest[i:]) })
		}
	case name == "bash" || name == "sh" || name == "zsh" || name == "dash" || name == "ksh":
		return x.shell(rest)
	case name == "find":
		return x.find(rest)
	case name == "eval":
		x.deep++
		defer func() { x.deep-- }()
		return x.script(strings.Join(rest, " "))
	case name == "git":
		return x.git(rest)
	case (name == "glab" || name == "gh") && x.coord == nil:
		return "no GitLab/GitHub actions from a worker (no MR, comment or API call)"
	case (name == "docker" || name == "podman") && x.coord == nil:
		return x.docker(rest)
	case (name == "npm" || name == "pnpm" || name == "yarn" || name == "bun") && x.coord == nil:
		if len(rest) > 0 {
			switch rest[0] {
			case "install", "i", "add", "ci", "update", "upgrade", "remove", "uninstall":
				return "no dependency changes (symlink node_modules instead)"
			case "exec", "x":
				return x.call(rest[1:])
			}
		}
		if name == "yarn" && len(rest) == 0 {
			return "no dependency changes (bare `yarn` installs)"
		}
	case name == "npx" || name == "bunx" || name == "pnpx":
		return x.call(rest)
	case name == "tee" || name == "touch" || name == "truncate":
		targets := rest
		options := true
		for i := 0; i < len(targets); i++ {
			a := targets[i]
			if options && a == "--" {
				options = false
				continue
			}
			if options && ((name == "truncate" && (a == "-s" || a == "--size" || a == "-r" || a == "--reference")) ||
				(name == "touch" && (a == "-t" || a == "-d" || a == "--date" || a == "-r" || a == "--reference" || a == "--time"))) {
				i++
				continue
			}
			if a == "" || (options && strings.HasPrefix(a, "-")) {
				continue
			}
			if r := x.write(a); r != "" {
				return r
			}
		}
	case name == "rm" || name == "rmdir" || name == "unlink":
		for _, t := range nonFlags(rest) {
			if r := x.write(t); r != "" {
				return r
			}
		}
	case name == "cp" || name == "ln" || name == "install":
		if t := nonFlags(rest); len(t) >= 2 {
			return x.write(t[len(t)-1])
		}
	case name == "mv":
		for _, t := range nonFlags(rest) { // the source disappears too
			if r := x.write(t); r != "" {
				return r
			}
		}
	case name == "sed" || name == "perl":
		inPlace := false
		for _, a := range rest {
			if a == "-i" || strings.HasPrefix(a, "-i") || a == "--in-place" || strings.HasPrefix(a, "--in-place=") ||
				(name == "perl" && strings.HasPrefix(a, "-p") && strings.Contains(a, "i")) {
				inPlace = true
			}
		}
		if inPlace {
			files := nonFlags(rest)
			hasScriptOpt := false
			for _, a := range rest {
				if a == "-e" || a == "-f" || a == "--expression" {
					hasScriptOpt = true
				}
			}
			if !hasScriptOpt && len(files) > 0 {
				files = files[1:] // first non-flag is the script
			}
			for _, t := range files {
				if r := x.write(t); r != "" {
					return r
				}
			}
		}
	}
	return ""
}

func (x *ctx) envSplit(payload string, trailing []string) string {
	// GNU env splitting differs from shell parsing: only plain literal words are safe to inspect this way.
	for _, c := range payload {
		if strings.ContainsRune("\\'\"$", c) || c > '~' || (c < ' ' && c != '\t' && c != '\n') {
			return "env split-string quoting, escapes or expansion cannot be checked; use explicit env arguments"
		}
	}
	if x.deep >= 4 {
		return "env split-string nests too deeply to check; use explicit env arguments"
	}
	args := append([]string{"env"}, strings.Fields(payload)...)
	args = append(args, trailing...)
	x.deep++
	defer func() { x.deep-- }()
	return x.call(args)
}

func envSplitHasChdir(payload string) bool {
	return envSplitArgsHaveChdir(strings.Fields(payload))
}

func envSplitArgsHaveChdir(args []string) bool {
	for _, arg := range args {
		if arg == "-C" || arg == "--chdir" || strings.HasPrefix(arg, "-C") && len(arg) > 2 || strings.HasPrefix(arg, "--chdir=") {
			return true
		}
	}
	return false
}

var numberish = regexp.MustCompile(`^\d+(\.\d+)?[smhd]?$`)

func isNumberish(s string) bool { return numberish.MatchString(s) }

var gitValueOpts = map[string]bool{"-C": true, "-c": true, "--git-dir": true, "--work-tree": true, "--namespace": true,
	"--exec-path": true, "--config-env": true, "--super-prefix": true}
var gitReadOnly = map[string]bool{"log": true, "show": true, "diff": true, "status": true, "rev-parse": true,
	"ls-files": true, "grep": true, "blame": true, "cat-file": true, "rev-list": true, "describe": true,
	"shortlog": true, "range-diff": true, "merge-base": true, "for-each-ref": true, "show-ref": true, "help": true, "version": true}

func (x *ctx) git(args []string) string {
	repo := ""
	redirect := false // --git-dir / --work-tree: git is pointed away from the directory the guard looks at
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		a := args[i]
		if gitValueOpts[a] && i+1 < len(args) {
			if a == "-C" || a == "--work-tree" || a == "--git-dir" {
				repo = args[i+1]
				redirect = redirect || a != "-C"
			}
			i += 2
			continue
		}
		if strings.HasPrefix(a, "--git-dir=") || strings.HasPrefix(a, "--work-tree=") {
			repo = a[strings.Index(a, "=")+1:]
			redirect = true
		}
		i++
	}
	if i >= len(args) {
		return ""
	}
	sub, rest := args[i], args[i+1:]
	if x.coord != nil {
		return x.coordGit(repo, redirect, sub, rest)
	}
	if repo == "" && !x.cwdUnknown && !inside(x.cwd, x.top) && outsideWrite(x.cwd, x.cwd, x.c, x.top) != "" {
		repo = x.cwd // git runs in whatever directory the tool (or an earlier cd) put it in; scratch dirs in temp are fine
	}
	if repo != "" && !gitReadOnly[sub] {
		if p := x.abs(repo); !inside(p, x.top) {
			return fmt.Sprintf("git %s in another repository (%s) is not this task's work", sub, repo)
		}
	}
	has := func(pred func(string) bool) bool {
		for _, a := range rest {
			if pred(a) {
				return true
			}
		}
		return false
	}
	if r := x.gitOutputs(sub, rest); r != "" {
		return r
	}
	switch sub {
	case "rm", "mv":
		for _, arg := range rest {
			if arg == "--" {
				break
			}
			// Git accepts unambiguous abbreviations of long options.
			option := strings.SplitN(arg, "=", 2)[0]
			if strings.HasPrefix(option, "--") && len(option) > 2 && strings.HasPrefix("--pathspec-from-file", option) {
				return "git pathspec files cannot be checked for ownership; use literal paths"
			}
		}
		saved := x.cwd
		if repo != "" {
			x.cwd = x.abs(repo)
		}
		defer func() { x.cwd = saved }()
		for _, t := range nonFlags(rest) {
			if r := x.write(t); r != "" {
				return r
			}
		}
	case "bundle":
		if len(rest) > 1 && rest[0] == "create" {
			return x.write(rest[1])
		}
	case "clone":
		if t := nonFlags(rest); len(t) >= 2 {
			return x.write(t[1])
		}
	case "push":
		return "workers never push; the coordinator pushes after the gates"
	case "rebase":
		return "rebasing is the steward's job (rebase_proof.sh)"
	case "reset":
		if has(func(a string) bool { return a == "--hard" || a == "--merge" || a == "--keep" }) {
			return "git reset --hard discards work; the steward handles history"
		}
	case "stash":
		if has(func(a string) bool {
			return a == "--all" || a == "--include-untracked" || (strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.ContainsAny(a, "au"))
		}) {
			return "git stash --all/--include-untracked moves untracked and ignored files, including the guard's hook files"
		}
	case "clean":
		if has(func(a string) bool {
			return a == "--force" || (strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "f"))
		}) {
			return "git clean -f deletes untracked files"
		}
	case "branch":
		if has(func(a string) bool { return a == "-D" || a == "--force" || (a == "-d" && false) }) ||
			(has(func(a string) bool { return a == "--delete" }) && has(func(a string) bool { return a == "--force" || a == "-f" })) {
			return "force-deleting branches is the steward's job"
		}
	case "checkout", "restore":
		if has(func(a string) bool { return a == "." || a == "./" || a == ":/" }) {
			return "discarding the working tree (checkout/restore .) loses work"
		}
	case "worktree":
		if len(rest) > 0 && (rest[0] == "remove" || rest[0] == "prune" || rest[0] == "add" || rest[0] == "move") {
			return "worktree management is the steward's job"
		}
	case "filter-branch", "filter-repo", "update-ref", "replace":
		return "history rewriting is not a worker's job"
	}
	return ""
}

// gitOutputs judges the options that write a file: --output=F / --output F (diff, log, archive), -o F (archive),
// -o DIR (format-patch).
func (x *ctx) gitOutputs(sub string, rest []string) string {
	for j, a := range rest {
		var target string
		switch {
		case strings.HasPrefix(a, "--output="), strings.HasPrefix(a, "--output-directory="):
			target = a[strings.Index(a, "=")+1:]
		case (a == "--output" || a == "--output-directory" || (a == "-o" && (sub == "archive" || sub == "format-patch"))) && j+1 < len(rest):
			target = rest[j+1]
		}
		if target != "" {
			if r := x.write(target); r != "" {
				return r
			}
		}
	}
	return ""
}

var dockerGlobalValue = map[string]bool{"--context": true, "-c": true, "-H": true, "--host": true, "--log-level": true,
	"-l": true, "--config": true, "--tlscacert": true, "--tlscert": true, "--tlskey": true}
var dockerTouch = map[string]bool{"stop": true, "kill": true, "rm": true, "restart": true, "exec": true, "start": true,
	"pause": true, "unpause": true, "update": true, "rename": true, "cp": true, "commit": true, "rmi": true, "attach": true}

func (x *ctx) docker(args []string) string {
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		if dockerGlobalValue[args[i]] {
			i++
		}
		i++
	}
	if i >= len(args) {
		return ""
	}
	sub, rest := args[i], args[i+1:]
	if r := x.deniedByProfile("docker", append([]string{sub}, rest...)); r != "" {
		return r
	}
	if (sub == "system" || sub == "volume" || sub == "image" || sub == "container" || sub == "network" || sub == "builder") &&
		len(rest) > 0 && rest[0] == "prune" {
		return "never prune docker"
	}
	if sub == "container" && len(rest) > 0 { // docker container rm app-api == docker rm app-api
		sub, rest = rest[0], rest[1:]
	}
	if dockerTouch[sub] {
		for _, a := range nonFlags(rest) {
			for _, pre := range x.c.Profile.ProtectContainerPrefixes {
				if strings.HasPrefix(a, pre) {
					return fmt.Sprintf("never touch the live stack (%s %s; profile %s)", sub, a, x.c.Profile.Name)
				}
			}
		}
	}
	return ""
}

// deniedByProfile matches "cmd" or "cmd sub" entries of the profile's deny_commands.
func (x *ctx) deniedByProfile(name string, rest []string) string {
	sub := ""
	if nf := nonFlags(rest); len(nf) > 0 {
		sub = nf[0]
	}
	for _, d := range x.c.Profile.DenyCommands {
		f := strings.Fields(d)
		if len(f) == 0 || f[0] != name {
			continue
		}
		if len(f) == 1 || (len(f) >= 2 && f[1] == sub) {
			return fmt.Sprintf("`%s` is not allowed for workers (profile %s)", d, x.c.Profile.Name)
		}
	}
	return ""
}

func (x *ctx) abs(p string) string {
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(h, p[2:])
		}
	}
	if runtime.GOOS == "windows" {
		p = gitBashToNative(p, filepath.VolumeName(x.top), os.TempDir())
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(x.cwd, p)
	}
	return contract.Real(p)
}

var driveRx = regexp.MustCompile(`^/([a-zA-Z])(/|$)`)

// gitBashToNative maps the POSIX paths Git Bash uses on Windows to native ones, so an absolute POSIX path is
// never mistaken for a relative one: /c/x -> C:\x, /tmp/x -> <temp>\x, /x -> <volume of the worktree>\x.
func gitBashToNative(p, volume, temp string) string {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") {
		return p
	}
	if m := driveRx.FindStringSubmatch(p); m != nil {
		return strings.ToUpper(m[1]) + `:\` + strings.ReplaceAll(strings.TrimPrefix(p[len(m[0]):], "/"), "/", `\`)
	}
	if p == "/tmp" || strings.HasPrefix(p, "/tmp/") {
		return strings.TrimRight(temp, `\`) + strings.ReplaceAll(strings.TrimPrefix(p, "/tmp"), "/", `\`)
	}
	if volume == "" {
		volume = "C:"
	}
	return volume + strings.ReplaceAll(p, "/", `\`)
}

func inside(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func tempDirs() []string {
	ds := []string{contract.Real(os.TempDir())}
	if os.PathSeparator == '/' {
		ds = append(ds, "/tmp", "/private/tmp", "/var/tmp", "/private/var/folders")
	}
	return ds
}

// write judges every shell write target, including new paths and deletions.
func (x *ctx) write(target string) string {
	if target == "" || target == "/dev/null" || strings.HasPrefix(target, "/dev/fd/") ||
		target == "/dev/stdout" || target == "/dev/stderr" {
		return ""
	}
	if strings.Contains(target, "$?") {
		return fmt.Sprintf("write target %q expands at run time and cannot be checked; use a literal path", target)
	}
	if x.cwdUnknown && !filepath.IsAbs(target) && !strings.HasPrefix(target, "~/") {
		return fmt.Sprintf("relative write %q after a cd the guard could not follow; use an absolute path or cd to a literal path", target)
	}
	if x.coord != nil {
		return x.coord.judgePath(x.abs(target), target)
	}
	if x.globHitsHook(target) {
		return fmt.Sprintf("%s could match the rein guard files of task %s; workers never change them", target, x.c.Name)
	}
	p := x.abs(target)
	if p == x.top || inside(x.top, p) {
		return fmt.Sprintf("%s is the worktree itself (or contains it)", target)
	}
	if inside(p, x.top) {
		if rel := filepath.ToSlash(mustRel(x.top, p)); rel == ".git" || strings.HasPrefix(rel, ".git/") {
			return "the worktree's .git is off limits"
		}
	}
	if !inside(p, x.top) {
		return outsideWrite(p, target, x.c, x.top)
	}
	rel := filepath.ToSlash(mustRel(x.top, p))
	if glob.Match(rel, x.c.Deny) {
		return fmt.Sprintf("%s is on the never-edit list of task %s", rel, x.c.Name)
	}
	if protectedHook(x.top, rel) {
		return fmt.Sprintf("%s belongs to the rein guard installed for task %s; workers never change it", rel, x.c.Name)
	}
	owned := glob.Match(rel, x.c.Allow)
	if info, err := os.Stat(p); err == nil && info.IsDir() {
		owned = owned || glob.Match(rel+"/", x.c.Allow)
	}
	if !owned {
		return fmt.Sprintf("%s is outside the ownership of task %s (%s); ask the coordinator if the task needs it", rel, x.c.Name, strings.Join(x.c.Allow, ", "))
	}
	return ""
}

func mustRel(base, p string) string {
	r, err := filepath.Rel(base, p)
	if err != nil {
		return p
	}
	return r
}

// outsideWrite judges a write outside the worktree. Order matters: the contract index and the run dir are
// protected BEFORE the temp-dir allowance, because a run dir may itself live under a temp dir.
func outsideWrite(p, shown string, c *contract.Contract, top string) string {
	if c.Writable(p) {
		return ""
	}
	if inside(p, contract.Real(contract.IndexDir())) {
		return "task contracts are the coordinator's; a worker never edits them"
	}
	if inside(p, filepath.Dir(top)) {
		return fmt.Sprintf("%s is in another task's worktree area (%s); stay inside %s", shown, filepath.Dir(top), top)
	}
	runDir := contract.Real(filepath.Dir(filepath.Dir(c.ReportPath)))
	if inside(p, runDir) {
		return fmt.Sprintf("%s belongs to the coordinator's run dir; only your report %s may be written there", shown, c.ReportPath)
	}
	for _, t := range tempDirs() {
		if inside(p, t) {
			return ""
		}
	}
	return fmt.Sprintf("%s is outside your worktree %s", shown, top)
}

// shell judges a shell invocation. `-c script` is judged as that script. Otherwise the shell runs a script file
// (allowed: the drift check reads what it wrote) or reads commands from stdin (-s, -i, or no file at all): those
// are judged when stdin is a literal heredoc and denied otherwise, because the guard cannot see a pipe or a tty.
func (x *ctx) shell(rest []string) string {
	stdinFlag, file := false, ""
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		switch {
		case a == "--":
			if i+1 < len(rest) {
				file = rest[i+1]
			}
			i = len(rest)
		case a == "-o" || a == "+o" || a == "-O" || a == "+O" || a == "--rcfile" || a == "--init-file":
			i++ // the option's value is not the script
		case strings.HasPrefix(a, "--"):
		case strings.HasPrefix(a, "-") && len(a) > 1:
			if strings.Contains(a, "c") && i+1 < len(rest) {
				x.deep++
				defer func() { x.deep-- }()
				return x.script(rest[i+1])
			}
			if strings.ContainsAny(a[1:], "si") {
				stdinFlag = true
			}
		case a != "" && !strings.HasPrefix(a, "+") && file == "":
			file = a
		}
	}
	if file != "" && !stdinFlag {
		return ""
	}
	if x.stdin == stdinHeredoc {
		x.deep++
		defer func() { x.deep-- }()
		return x.script(x.hdoc)
	}
	return "no shells that read commands from stdin (bash, sh -i, bash -s, | bash): run each command directly so the guard can judge it"
}

var globChars = "*?["

// matchesHook reports whether a path pattern (relative to the worktree, slash form) could name a hook file or a
// directory that holds one. Segments are matched like shell globs: a leading dot needs a literal dot.
func matchesHook(top, pattern string) bool {
	segs := strings.Split(pattern, "/")
	cands := append([]string{".codex", ".agents", ".kiro", ".kiro/agents", ".opencode", ".opencode/plugins", ".opencode/plugins/rein", ".claude"}, hookFiles...)
	for _, c := range cands {
		cs := strings.Split(c, "/")
		if len(cs) != len(segs) || !protectedHook(top, c) {
			continue
		}
		ok := true
		for i := range cs {
			if strings.HasPrefix(cs[i], ".") && !strings.HasPrefix(segs[i], ".") {
				ok = false
				break
			}
			if m, err := path.Match(segs[i], cs[i]); err != nil || !m {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// globHitsHook judges a mutating command's operand that contains glob characters: it is denied when the shell
// could expand it to a hook file or to a directory holding one.
func (x *ctx) globHitsHook(target string) bool {
	if !strings.ContainsAny(target, globChars) {
		return false
	}
	p := x.abs(target)
	if inside(p, x.top) {
		return matchesHook(x.top, filepath.ToSlash(mustRel(x.top, p)))
	}
	return false
}

// holdsHook reports whether an installed hook file lives at or under dir.
func (x *ctx) holdsHook(dir string) bool {
	for _, f := range hookFiles {
		hp := filepath.Join(x.top, filepath.FromSlash(f))
		if _, err := os.Stat(hp); err == nil && inside(hp, dir) {
			return true
		}
	}
	return false
}

// find with an action (-delete, -exec...) can remove or rewrite hook files it walks over.
func (x *ctx) find(args []string) string {
	acts := false
	var roots []string
	rootsDone := false
	for _, a := range args {
		switch a {
		case "-delete", "-exec", "-execdir", "-ok", "-okdir":
			acts = true
		}
		if !rootsDone && (strings.HasPrefix(a, "-") || a == "(" || a == "!") {
			rootsDone = true
		}
		if !rootsDone {
			roots = append(roots, a)
		}
	}
	if x.coord != nil {
		return x.coordFind(args, roots)
	}
	if !acts {
		return ""
	}
	if len(roots) == 0 {
		roots = []string{"."}
	}
	for _, root := range roots {
		if x.holdsHook(x.abs(root)) {
			return "find with -delete/-exec over the rein guard files is denied; the installed hook files are not yours to change"
		}
		for i := 0; i < len(args); i++ {
			switch args[i] {
			case "-delete":
				if r := x.write(root); r != "" {
					return r
				}
			case "-execdir", "-okdir":
				return "find directory actions have an unknown execution directory; use -exec with explicit paths"
			case "-exec", "-ok":
				var cmd []string
				for i++; i < len(args) && args[i] != ";" && args[i] != `\;` && args[i] != "+"; i++ {
					cmd = append(cmd, strings.ReplaceAll(args[i], "{}", x.abs(root)))
				}
				if r := x.call(cmd); r != "" {
					return r
				}
			}
		}
	}
	return ""
}
