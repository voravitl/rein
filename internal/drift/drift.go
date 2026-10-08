// Package drift judges a worker's result against its task contract, from files and text only (never from the
// model's own summary). It works for every vendor because it runs at the coordinator, not inside the worker.
package drift

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/glob"
	"github.com/voravitl/rein/internal/tier"
)

type Finding struct {
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

type Result struct {
	Name     string    `json:"name"`
	Head     string    `json:"head"` // the worktree revision this verdict judged
	Tier     string    `json:"tier"` // computed tier (T1/T2/T3)
	Drift    []Finding `json:"drift"`
	Warnings []Finding `json:"warnings"`
	Commits  int       `json:"commits"`
	Files    int       `json:"files"`
}

func withChildren(gs []string) []string {
	out := make([]string, 0, len(gs))
	for _, g := range gs {
		out = append(out, strings.TrimSuffix(g, "/")+"/**")
	}
	return out
}

var (
	fake = regexp.MustCompile(`(TODO|FIXME|\.skip\(|\.only\(|\[Fact\(Skip|\[Ignore|throw new NotImplementedException|it\.todo|xit\(|xdescribe\()`)
	// Secrets: known token shapes, private keys, and credential-named keys with a literal value in code,
	// JSON (`"password": "x"`), YAML (`password: x`) or env files (`PASSWORD=x`).
	// token shapes that are secrets wherever they appear
	secretToken = regexp.MustCompile(`AKIA[0-9A-Z]{16}|-----BEGIN [A-Z ]*PRIVATE KEY-----|glpat-[0-9A-Za-z_-]{20}|ghp_[0-9A-Za-z]{36}|sk-[0-9A-Za-z]{32,}`)
	// credential-named key with a QUOTED literal value: "password": "x", api_key = 'x', password: "x"
	secretQuoted = regexp.MustCompile(`(?i)\b[a-z0-9_]*(password|passwd|secret|api[_-]?key|access[_-]?token|client[_-]?secret)\b["']?\s*[:=]\s*["']([^"'\n]{8,})["']`)
	// env/YAML line form with an unquoted value: PASSWORD=x / password: x (whole line)
	secretLine  = regexp.MustCompile(`(?i)^\s*[a-z0-9_]*(password|passwd|secret|api[_-]?key|access[_-]?token|client[_-]?secret)[a-z0-9_]*\s*[:=]\s*([^\s"'#]{8,})\s*$`)
	placeholder = regexp.MustCompile(`(?i)(\$\{|\{\{|<[a-z_]+>|changeme|example|placeholder|xxx+|\*\*\*|<sc>|redacted|dummy|test[_-]?pass)`)
	expression  = regexp.MustCompile(`(?i)(\(|process\.env|os\.environ|getenv|environment\.|config\.|settings\.|\$[a-z_{])`)
	statusWords = regexp.MustCompile(`(?i)\b(done|partly|partial|not done|skipped|blocked)\b`)
	incomplete  = regexp.MustCompile(`(?i)\b(partly|partial|not done|skipped|blocked)\b`)
)

func git(wt string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", wt}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// midCheck is a test seam: it runs just before the final HEAD re-check, where a test can move HEAD.
var midCheck func()

// Options are the coordinator's extra expectations for one check.
type Options struct {
	ExpectGuard string // vendor whose guard hook was supposed to run for this worker launch ("" = any installed one)
}

// Check returns the result, or an error when it cannot judge (the caller must not treat that as clean).
func Check(c *contract.Contract, wt, base string, claimed []string) (*Result, error) {
	return CheckWith(c, wt, base, claimed, Options{})
}

// launchHint names, per vendor, the launch mistake that most likely left its hook silent.
var launchHint = map[string]string{
	"codex":    "codex needs --dangerously-bypass-hook-trust plus the -c hooks flags printed by rein hooks install (an untrusted project hook is skipped silently, and a linked worktree's .codex is not read)",
	"kiro":     "kiro needs --agent rein on every launch",
	"agy":      "agy must be started inside the worktree",
	"opencode": "opencode2 must be started inside the worktree",
	"claude":   "no rein plugin hook and no hook in .claude/settings.local.json",
}

// guardVendors are the vendors whose hook calls count as proof: the one named by --expect-guard, else every
// installed vendor.
func guardVendors(c *contract.Contract, opt Options) []string {
	if opt.ExpectGuard != "" {
		return []string{opt.ExpectGuard}
	}
	return c.HooksInstalled
}

// guardSeen reports whether the seen log holds a PreToolUse call of one of the vendors from the contract's
// current install generation. Lines of an earlier install (a reused task name) or of another vendor (an earlier
// codex attempt before a kiro fallback) do not count.
func guardSeen(c *contract.Contract, vendors []string) bool {
	b, err := os.ReadFile(contract.SeenPath(c.Name))
	if err != nil {
		return false
	}
	want := map[string]bool{}
	for _, v := range vendors {
		want[v] = true
	}
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) < 4 || f[2] != "pretool" || !want[f[1]] {
			continue
		}
		if c.HooksGeneration == "" || (len(f) > 4 && f[4] == "gen="+c.HooksGeneration) {
			return true
		}
	}
	return false
}

func inactiveDetail(c *contract.Contract, vendors []string) string {
	var hints []string
	for _, v := range vendors {
		if h, ok := launchHint[v]; ok {
			hints = append(hints, h)
		}
	}
	if len(c.HooksInstalled) == 0 {
		return fmt.Sprintf("a guard was expected (%s) but no hooks are installed for this task and no hook call was seen (%s); run rein hooks install %s", strings.Join(vendors, ","), contract.SeenPath(c.Name), c.Name)
	}
	return fmt.Sprintf("no PreToolUse call from %s of the current hooks install was seen in %s although the worktree has commits; likely: %s",
		strings.Join(vendors, ","), contract.SeenPath(c.Name), strings.Join(hints, "; "))
}

// CheckWith is Check with options.
func CheckWith(c *contract.Contract, wt, base string, claimed []string, opt Options) (*Result, error) {
	if opt.ExpectGuard == "" && len(c.HooksInstalled) > 1 {
		// one vendor's hook calls must not vouch for another vendor's launch
		return nil, fmt.Errorf("hooks are installed for several vendors (%s): pass --expect-guard <vendor that ran this task>", strings.Join(c.HooksInstalled, ","))
	}
	r := &Result{Name: c.Name}
	d := func(k, f string, a ...any) { r.Drift = append(r.Drift, Finding{k, fmt.Sprintf(f, a...)}) }
	w := func(k, f string, a ...any) { r.Warnings = append(r.Warnings, Finding{k, fmt.Sprintf(f, a...)}) }

	if _, err := git(wt, "rev-parse", "--verify", base); err != nil {
		return nil, err
	}
	head, err := git(wt, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	r.Head = strings.TrimSpace(head) // every range below uses this sha, never the moving HEAD
	log, err := git(wt, "log", "--format=%h %s", base+".."+r.Head)
	if err != nil {
		return nil, err
	}
	commits := lines(log)
	r.Commits = len(commits)
	// --no-renames: a rename shows as a delete of the source plus an add of the destination, so moving an
	// out-of-scope file into an allowed directory still shows the out-of-scope path.
	names, err := git(wt, "diff", "--no-renames", "--name-only", "-z", base+"..."+r.Head)
	if err != nil {
		return nil, err
	}
	files := nulSplit(names) // -z: no C-style quoting, so non-ASCII names match the globs as written
	r.Files = len(files)

	// Compute tier from allow globs, then escalate from actual changed files (ADR B2.1)
	baseTier := tier.EvaluateFromAllowGlobs(c.Allow, c.Profile.SensitivePaths)
	computedTier := tier.EvaluateFromChangedFiles(files, c.Profile.SensitivePaths, baseTier)
	r.Tier = computedTier.String()

	status, err := git(wt, "status", "--porcelain", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	patch, err := git(wt, "-c", "core.quotePath=false", "diff", "--no-renames", "-U0", base+"..."+r.Head)
	if err != nil {
		return nil, err
	}
	numstat, err := git(wt, "diff", "--no-renames", "--numstat", base+"..."+r.Head)
	if err != nil {
		return nil, err
	}

	if len(commits) == 0 {
		d("NO_COMMITS", "no commits on top of %s", base)
	}
	if gv := guardVendors(c, opt); (len(c.HooksInstalled) > 0 || opt.ExpectGuard != "") && len(commits) > 0 && !guardSeen(c, gv) {
		d("GUARD_INACTIVE", "%s", inactiveDetail(c, gv))
	}
	if c.Issue > 0 {
		var miss []string
		for _, l := range commits {
			if !strings.Contains(l, "#"+strconv.Itoa(c.Issue)) {
				miss = append(miss, l)
			}
		}
		if len(miss) > 0 {
			w("COMMIT_REF", "%d commit(s) without #%d: %s", len(miss), c.Issue, strings.Join(first(miss, 3), "; "))
		}
	}

	var dirty []string
	for _, l := range nulSplit(status) {
		if len(l) < 4 {
			continue
		}
		p := strings.TrimSuffix(l[3:], "/")
		arts := c.Profile.LocalArtifacts
		if strings.HasPrefix(l, "?? ") && glob.Match(p, append(arts, withChildren(arts)...)) {
			continue
		}
		dirty = append(dirty, p)
	}
	if len(dirty) > 0 {
		d("UNCOMMITTED", "%d uncommitted/untracked: %s", len(dirty), strings.Join(first(dirty, 6), ", "))
	}

	for _, f := range files {
		switch {
		case glob.Match(f, c.Deny):
			d("DENIED_PATH", "%s", f)
		case !glob.Match(f, c.Allow):
			d("OUT_OF_SCOPE", "%s", f)
		}
	}

	cur := ""
	for _, l := range strings.Split(patch, "\n") {
		if strings.HasPrefix(l, "+++ ") {
			cur = strings.TrimPrefix(strings.TrimPrefix(l, "+++ "), "b/")
			continue
		}
		if !strings.HasPrefix(l, "+") {
			continue
		}
		line := l[1:]
		if fake.MatchString(line) {
			d("FAKE_COMPLETION", "%s: %s", cur, trunc(strings.TrimSpace(line), 100))
		}
		if hasSecret(line) {
			d("SECRET", "%s: <redacted line>", cur)
		}
	}

	if fi, err := os.Stat(c.ReportPath); err != nil || fi.Size() == 0 {
		d("NO_REPORT", "%s", c.ReportPath)
	} else {
		b, _ := os.ReadFile(c.ReportPath)
		checkScope(string(b), c.Scope, d, w)
	}

	if len(claimed) > 0 {
		inDiff := map[string]bool{}
		for _, f := range files {
			inDiff[f] = true
		}
		claimedSet := map[string]bool{}
		for _, f := range claimed {
			rel := f
			if filepath.IsAbs(f) {
				if r2, err := relTo(wt, f); err == nil {
					rel = r2
				}
			}
			claimedSet[rel] = true
			if !inDiff[rel] {
				d("FALSE_CLAIM", "claimed modified but not in the diff: %s", rel)
			}
		}
		for _, f := range files {
			if !claimedSet[f] {
				w("UNCLAIMED_CHANGE", "%s", f)
			}
		}
	}

	if c.MaxChangedLines > 0 {
		total := 0
		for _, l := range lines(numstat) {
			p := strings.Split(l, "\t")
			a, e1 := strconv.Atoi(p[0])
			b, e2 := strconv.Atoi(p[1])
			if e1 == nil && e2 == nil {
				total += a + b
			}
		}
		if total > c.MaxChangedLines {
			w("OVER_BUDGET", "%d changed lines > %d", total, c.MaxChangedLines)
		}
	}
	if midCheck != nil {
		midCheck()
	}
	if h2, err := git(wt, "rev-parse", "HEAD"); err != nil || strings.TrimSpace(h2) != r.Head {
		return nil, fmt.Errorf("HEAD moved during the check (judged %.12s); cannot judge, run it again", r.Head)
	}
	return r, nil
}

// hasSecret checks every candidate on the line independently, so a placeholder cannot hide a later literal.
func hasSecret(line string) bool {
	if secretToken.MatchString(line) {
		return true
	}
	for _, m := range secretQuoted.FindAllStringSubmatch(line, -1) {
		if v := m[2]; !placeholder.MatchString(v) && !expression.MatchString(v) {
			return true
		}
	}
	if m := secretLine.FindStringSubmatch(line); m != nil {
		if v := m[2]; !placeholder.MatchString(v) && !expression.MatchString(v) {
			return true
		}
	}
	return false
}

// checkScope needs one markdown heading per scope id, and a status word on the heading or in its section.
func checkScope(report string, ids []string, d, w func(string, string, ...any)) {
	ls := strings.Split(report, "\n")
	for _, id := range ids {
		idRx := regexp.MustCompile(`\b` + regexp.QuoteMeta(id) + `\b`)
		at := -1
		for i, l := range ls {
			if strings.HasPrefix(strings.TrimSpace(l), "#") && idRx.MatchString(l) {
				at = i
				break
			}
		}
		if at < 0 {
			d("SCOPE_MISSING", "%s has no heading in the report", id)
			continue
		}
		end := len(ls)
		for j := at + 1; j < len(ls); j++ {
			if strings.HasPrefix(strings.TrimSpace(ls[j]), "#") {
				end = j
				break
			}
		}
		section := strings.Join(ls[at:end], "\n")
		body := strings.TrimSpace(strings.Join(ls[at+1:end], "\n"))
		switch {
		case !statusWords.MatchString(section):
			d("SCOPE_NO_STATUS", "%s: no status (done / partly / not done)", id)
		case body == "":
			d("SCOPE_NO_EVIDENCE", "%s: heading only, no files/tests/evidence under it", id)
		case incomplete.MatchString(section):
			w("SCOPE_INCOMPLETE", "%s: report says partly/not done", id)
		}
	}
}

func relTo(base, p string) (string, error) {
	r, err := filepath.Rel(contract.Real(base), contract.Real(p))
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("outside")
	}
	return filepath.ToSlash(r), nil
}

func nulSplit(s string) []string {
	var out []string
	for _, x := range strings.Split(s, "\x00") {
		if x != "" {
			out = append(out, x)
		}
	}
	return out
}

func first(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
