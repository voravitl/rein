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
)

type Finding struct {
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

type Result struct {
	Name     string    `json:"name"`
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
	secret = regexp.MustCompile(`(AKIA[0-9A-Z]{16}|-----BEGIN [A-Z ]*PRIVATE KEY-----|glpat-[0-9A-Za-z_-]{20}|ghp_[0-9A-Za-z]{36}|sk-[0-9A-Za-z]{32,}|` +
		`(?i)["']?\b[a-z0-9_]*(password|passwd|secret|api[_-]?key|access[_-]?token|client[_-]?secret)\b["']?\s*[:=]\s*["']?[^"'\s,;}{]{8,})`)
	placeholder = regexp.MustCompile(`(?i)(\$\{|\{\{|<[a-z_]+>|changeme|example|placeholder|xxx+|\*\*\*|<sc>|redacted|dummy|test[_-]?pass)`)
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

// Check returns the result, or an error when it cannot judge (the caller must not treat that as clean).
func Check(c *contract.Contract, wt, base string, claimed []string) (*Result, error) {
	r := &Result{Name: c.Name}
	d := func(k, f string, a ...any) { r.Drift = append(r.Drift, Finding{k, fmt.Sprintf(f, a...)}) }
	w := func(k, f string, a ...any) { r.Warnings = append(r.Warnings, Finding{k, fmt.Sprintf(f, a...)}) }

	if _, err := git(wt, "rev-parse", "--verify", base); err != nil {
		return nil, err
	}
	log, err := git(wt, "log", "--format=%h %s", base+"..HEAD")
	if err != nil {
		return nil, err
	}
	commits := lines(log)
	r.Commits = len(commits)
	// --no-renames: a rename shows as a delete of the source plus an add of the destination, so moving an
	// out-of-scope file into an allowed directory still shows the out-of-scope path.
	names, err := git(wt, "diff", "--no-renames", "--name-only", base+"...HEAD")
	if err != nil {
		return nil, err
	}
	files := lines(names)
	r.Files = len(files)
	status, err := git(wt, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	patch, err := git(wt, "diff", "--no-renames", "-U0", base+"...HEAD")
	if err != nil {
		return nil, err
	}
	numstat, err := git(wt, "diff", "--no-renames", "--numstat", base+"...HEAD")
	if err != nil {
		return nil, err
	}

	if len(commits) == 0 {
		d("NO_COMMITS", "no commits on top of %s", base)
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
	for _, l := range lines(status) {
		p := strings.TrimSuffix(strings.TrimSpace(l[3:]), "/")
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
		if m := secret.FindString(line); m != "" && !placeholder.MatchString(m) {
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
	return r, nil
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
