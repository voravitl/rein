package guard

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
	c     *contract.Contract
	top   string // real worktree root
	cwd   string
	deep  int
	ports *regexp.Regexp
}

// CheckBash returns a non-empty reason when the command must be denied.
func CheckBash(cmd string, c *contract.Contract, top, cwd string) string {
	return (&ctx{c: c, top: top, cwd: cwd, ports: portRx(c.Profile.ProtectPorts)}).script(cmd)
}

func (x *ctx) script(src string) string {
	if x.deep > 4 {
		return "command nests shells too deeply to check; run the inner command directly"
	}
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), "")
	if err != nil {
		return fmt.Sprintf("could not parse the command (%v); split it into simpler commands", err)
	}
	var reason string
	syntax.Walk(f, func(n syntax.Node) bool {
		if reason != "" {
			return false
		}
		switch n := n.(type) {
		case *syntax.Stmt:
			for _, r := range n.Redirs {
				switch r.Op {
				case syntax.RdrOut, syntax.AppOut, syntax.RdrAll, syntax.AppAll, syntax.ClbOut, syntax.RdrInOut:
					if r.Word != nil {
						if t := word(r.Word); t != "" && !strings.HasPrefix(t, "&") {
							reason = x.write(t, false)
						}
					}
				}
			}
		case *syntax.CallExpr:
			args := make([]string, 0, len(n.Args))
			for _, w := range n.Args {
				args = append(args, word(w))
			}
			reason = x.call(args)
		}
		return reason == ""
	})
	return reason
}

// word returns the literal value of a shell word; parts that expand at run time become "$?".
func word(w *syntax.Word) string {
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
	for _, a := range args {
		if a != "" && !strings.HasPrefix(a, "-") {
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
	for _, a := range args {
		if x.ports != nil && x.ports.MatchString(a) {
			return fmt.Sprintf("protected port in %q (profile %s: the live stack is off limits)", a, prof.Name)
		}
		for _, s := range prof.OwnerScripts {
			if strings.HasSuffix(filepath.ToSlash(a), s) {
				return fmt.Sprintf("%s belongs to the owner (profile %s)", s, prof.Name)
			}
		}
	}
	name := base(args[0])
	rest := args[1:]
	if r := x.deniedByProfile(name, rest); r != "" {
		return r
	}
	switch {
	case wrappers[name]:
		// skip the wrapper's own options and VAR=value pairs, then judge the wrapped command
		i := 0
		for i < len(rest) && (strings.HasPrefix(rest[i], "-") || strings.Contains(rest[i], "=") ||
			((name == "timeout" || name == "gtimeout" || name == "nice") && i == 0 && isNumberish(rest[i]))) {
			i++
		}
		return x.call(rest[i:])
	case name == "xargs":
		for i, a := range rest {
			if !strings.HasPrefix(a, "-") {
				return x.call(rest[i:])
			}
		}
	case name == "bash" || name == "sh" || name == "zsh" || name == "dash" || name == "ksh":
		for i, a := range rest {
			if (a == "-c" || (strings.HasPrefix(a, "-") && strings.Contains(a, "c") && !strings.HasPrefix(a, "--"))) && i+1 < len(rest) {
				x.deep++
				defer func() { x.deep-- }()
				return x.script(rest[i+1])
			}
		}
	case name == "eval":
		x.deep++
		defer func() { x.deep-- }()
		return x.script(strings.Join(rest, " "))
	case name == "git":
		return x.git(rest)
	case name == "glab" || name == "gh":
		return "no GitLab/GitHub actions from a worker (no MR, comment or API call)"
	case name == "docker" || name == "podman":
		return x.docker(rest)
	case name == "npm" || name == "pnpm" || name == "yarn" || name == "bun":
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
		for _, t := range nonFlags(rest) {
			if r := x.write(t, false); r != "" {
				return r
			}
		}
	case name == "rm" || name == "rmdir" || name == "unlink":
		for _, t := range nonFlags(rest) {
			if r := x.write(t, true); r != "" {
				return r
			}
		}
	case name == "cp" || name == "ln" || name == "install":
		if t := nonFlags(rest); len(t) >= 2 {
			return x.write(t[len(t)-1], false)
		}
	case name == "mv":
		for _, t := range nonFlags(rest) { // the source disappears too
			if r := x.write(t, false); r != "" {
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
				if r := x.write(t, false); r != "" {
					return r
				}
			}
		}
	}
	return ""
}

func isNumberish(s string) bool {
	return regexp.MustCompile(`^\d+(\.\d+)?[smhd]?$`).MatchString(s)
}

var gitValueOpts = map[string]bool{"-C": true, "-c": true, "--git-dir": true, "--work-tree": true, "--namespace": true,
	"--exec-path": true, "--config-env": true, "--super-prefix": true}
var gitReadOnly = map[string]bool{"log": true, "show": true, "diff": true, "status": true, "rev-parse": true,
	"ls-files": true, "grep": true, "blame": true, "cat-file": true, "rev-list": true, "describe": true,
	"shortlog": true, "range-diff": true, "merge-base": true, "for-each-ref": true, "show-ref": true, "help": true, "version": true}

func (x *ctx) git(args []string) string {
	repo := ""
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		a := args[i]
		if gitValueOpts[a] && i+1 < len(args) {
			if a == "-C" || a == "--work-tree" || a == "--git-dir" {
				repo = args[i+1]
			}
			i += 2
			continue
		}
		if strings.HasPrefix(a, "--git-dir=") || strings.HasPrefix(a, "--work-tree=") {
			repo = a[strings.Index(a, "=")+1:]
		}
		i++
	}
	if i >= len(args) {
		return ""
	}
	sub, rest := args[i], args[i+1:]
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
	switch sub {
	case "push":
		return "workers never push; the coordinator pushes after the gates"
	case "rebase":
		return "rebasing is the steward's job (rebase_proof.sh)"
	case "reset":
		if has(func(a string) bool { return a == "--hard" || a == "--merge" || a == "--keep" }) {
			return "git reset --hard discards work; the steward handles history"
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
		if len(rest) > 0 && (rest[0] == "remove" || rest[0] == "prune") {
			return "worktree removal is the steward's job"
		}
	case "filter-branch", "filter-repo", "update-ref", "replace":
		return "history rewriting is not a worker's job"
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
	if !filepath.IsAbs(p) {
		p = filepath.Join(x.cwd, p)
	}
	return contract.Real(p)
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

// write judges one write target. Deletions only need to stay inside the worktree and off the never-edit list;
// writes to an EXISTING file must also be inside the ownership (new scratch files are left to the drift check).
func (x *ctx) write(target string, deletion bool) string {
	if target == "" || strings.Contains(target, "$?") || target == "/dev/null" || strings.HasPrefix(target, "/dev/fd/") ||
		target == "/dev/stdout" || target == "/dev/stderr" {
		return ""
	}
	p := x.abs(target)
	if !inside(p, x.top) {
		return outsideWrite(p, target, x.c, x.top)
	}
	rel := filepath.ToSlash(mustRel(x.top, p))
	if glob.Match(rel, x.c.Deny) {
		return fmt.Sprintf("%s is on the never-edit list of task %s", rel, x.c.Name)
	}
	if !deletion {
		if _, err := os.Stat(p); err == nil && !glob.Match(rel, x.c.Allow) {
			return fmt.Sprintf("%s is outside the ownership of task %s (%s); ask the coordinator if the task needs it", rel, x.c.Name, strings.Join(x.c.Allow, ", "))
		}
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
