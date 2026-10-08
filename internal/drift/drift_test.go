package drift

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voravitl/rein/internal/contract"
)

func sh(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
}

func write(t *testing.T, p, s string) {
	t.Helper()
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// repo builds origin + a worktree named name on a branch from origin/main, and a contract for it.
func repo(t *testing.T, name string) (wt string, c *contract.Contract) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(root, "contracts"))
	sh(t, root, "git", "init", "-q", "--bare", "o.git")
	sh(t, root, "git", "clone", "-q", "o.git", "m")
	m := filepath.Join(root, "m")
	write(t, filepath.Join(m, "backend/Routing/A.cs"), "a\n")
	write(t, filepath.Join(m, "shared/Common.cs"), "c\n")
	write(t, filepath.Join(m, "VERSION"), "0.1\n")
	write(t, filepath.Join(m, ".gitignore"), "graphify-out/\n")
	sh(t, m, "git", "add", ".")
	sh(t, m, "git", "commit", "-qm", "base")
	sh(t, m, "git", "push", "-q", "origin", "HEAD:main")
	sh(t, m, "git", "fetch", "-q")
	wt = filepath.Join(root, "wts", name)
	sh(t, m, "git", "worktree", "add", "-q", "-b", name, wt, "origin/main")
	var err error
	c, err = contract.New(name, 169, filepath.Join(root, "run"), "backend/Routing/**", "docs/design/**", "S1,S2",
		filepath.Join(root, "wts"), "", "", 5, &contract.Profile{DenyPaths: []string{"VERSION"}, LocalArtifacts: []string{"frontend/node_modules"}})
	if err != nil {
		t.Fatal(err)
	}
	return wt, c
}

func kinds(fs []Finding) string {
	var k []string
	for _, f := range fs {
		k = append(k, f.Kind)
	}
	return strings.Join(k, ",")
}

func TestDriftCatchesEverything(t *testing.T) {
	wt, c := repo(t, "bad")
	write(t, filepath.Join(wt, "backend/Routing/A.cs"), "a\n// TODO finish\n")
	write(t, filepath.Join(wt, "backend/Routing/config.json"), `{"password": "production-password"}`+"\n") // codex finding: JSON key
	write(t, filepath.Join(wt, "VERSION"), "0.2\n")
	write(t, filepath.Join(wt, "other.txt"), "x\n")
	sh(t, wt, "git", "mv", "shared/Common.cs", "backend/Routing/Common.cs") // codex finding: rename hides the source
	sh(t, wt, "git", "add", "-A")
	sh(t, wt, "git", "commit", "-qm", "feat: stuff")
	write(t, filepath.Join(wt, "backend/Routing/B.cs"), "stray\n")
	write(t, c.ReportPath, "# report\nS1 S2\n") // codex finding: placeholder ids, no headings
	r, err := Check(c, wt, "origin/main", []string{"backend/Routing/A.cs", "backend/Routing/C.cs"})
	if err != nil {
		t.Fatal(err)
	}
	got := kinds(r.Drift)
	for _, want := range []string{"UNCOMMITTED", "DENIED_PATH", "OUT_OF_SCOPE", "FAKE_COMPLETION", "SECRET", "SCOPE_MISSING", "FALSE_CLAIM"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
	found := false
	for _, f := range r.Drift {
		if f.Kind == "OUT_OF_SCOPE" && f.Detail == "shared/Common.cs" {
			found = true
		}
	}
	if !found {
		t.Errorf("rename source shared/Common.cs not reported: %+v", r.Drift)
	}
	if !strings.Contains(kinds(r.Warnings), "COMMIT_REF") || !strings.Contains(kinds(r.Warnings), "OVER_BUDGET") {
		t.Errorf("warnings: %s", kinds(r.Warnings))
	}
}

func TestDriftClean(t *testing.T) {
	wt, c := repo(t, "good")
	write(t, filepath.Join(wt, "backend/Routing/A.cs"), "a\nb\n")
	write(t, filepath.Join(wt, "backend/Routing/appsettings.json"), `{"password": "${DB_PASSWORD}"}`+"\n") // placeholder, not a secret
	sh(t, wt, "git", "add", "-A")
	sh(t, wt, "git", "commit", "-qm", "fix(routing): edge case #169")
	write(t, filepath.Join(wt, "graphify-out/x"), "ignored\n")
	write(t, c.ReportPath, "# report\n## S1 done\nfiles: backend/Routing/A.cs\n\n## S2 partly\nreason: out of time\n")
	r, err := Check(c, wt, "origin/main", []string{"backend/Routing/A.cs", "backend/Routing/appsettings.json"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Drift) != 0 {
		t.Errorf("clean worker drifted: %+v", r.Drift)
	}
	if !strings.Contains(kinds(r.Warnings), "SCOPE_INCOMPLETE") {
		t.Errorf("partly not warned: %s", kinds(r.Warnings))
	}
}

func TestDriftHeadingWithoutStatusOrEvidence(t *testing.T) {
	wt, c := repo(t, "thin")
	write(t, filepath.Join(wt, "backend/Routing/A.cs"), "a\nb\n")
	sh(t, wt, "git", "commit", "-qam", "fix: x #169")
	write(t, c.ReportPath, "## S1\nsome words\n## S2 done\n")
	r, err := Check(c, wt, "origin/main", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := kinds(r.Drift); !strings.Contains(got, "SCOPE_NO_STATUS") || !strings.Contains(got, "SCOPE_NO_EVIDENCE") {
		t.Errorf("got %s", got)
	}
}

func TestDriftCannotJudge(t *testing.T) {
	_, c := repo(t, "x")
	if _, err := Check(c, t.TempDir(), "origin/main", nil); err == nil {
		t.Error("a non-repo must be an error, never clean")
	}
}

func TestSecretsEveryMatchAndExpressions(t *testing.T) {
	cases := map[string]bool{
		`{"password":"${DB_PASSWORD}","api_key":"abcdefghijklmno"}`: true,  // 6: placeholder must not hide the second
		`const password = process.env.DB_PASSWORD;`:                 false, // 8: an expression, not a literal
		`password = os.environ["DB_PASSWORD"]`:                      false,
		`DB_PASSWORD=s3cr3tvalue99`:                                 true,  // env file
		`password: hunter2hunter2`:                                  true,  // YAML
		`"password": "<sc>"`:                                        false, // pipeline placeholder
		`token := ghp_abcdefghijklmnopqrstuvwxyz0123456789`:         true,
	}
	for line, want := range cases {
		if got := hasSecret(line); got != want {
			t.Errorf("hasSecret(%q) = %v, want %v", line, got, want)
		}
	}
}

func TestNonASCIIPathsAreJudgedAsWritten(t *testing.T) {
	wt, c := repo(t, "thai")
	c.Allow = []string{"**"}
	c.Deny = append(c.Deny, "private/**")
	write(t, filepath.Join(wt, "private/ไทย.md"), "x\n") // 7: git would quote this name without -z
	sh(t, wt, "git", "add", "-A")
	sh(t, wt, "git", "commit", "-qm", "docs #169")
	write(t, c.ReportPath, "## S1 done\nx\n## S2 done\ny\n")
	r, err := Check(c, wt, "origin/main", nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range r.Drift {
		if f.Kind == "DENIED_PATH" && f.Detail == "private/ไทย.md" {
			found = true
		}
	}
	if !found {
		t.Errorf("non-ASCII denied path not caught: %+v", r.Drift)
	}
}

func TestGuardInactiveAndHead(t *testing.T) {
	wt, c := repo(t, "g")
	write(t, filepath.Join(wt, "backend/Routing/A.cs"), "a2\n")
	sh(t, wt, "git", "add", "-A")
	sh(t, wt, "git", "commit", "-qm", "feat: x #169")
	head := strings.TrimSpace(gitOut(t, wt, "rev-parse", "HEAD"))
	inactive := func(r *Result) string {
		for _, f := range r.Drift {
			if f.Kind == "GUARD_INACTIVE" {
				return f.Detail
			}
		}
		return ""
	}

	r, err := Check(c, wt, "origin/main", nil) // nothing installed, nothing expected
	if err != nil {
		t.Fatal(err)
	}
	if r.Head != head {
		t.Errorf("Head = %q, want %q", r.Head, head)
	}
	if inactive(r) != "" {
		t.Error("no hooks installed and none expected: no GUARD_INACTIVE")
	}

	c.HooksInstalled, c.HooksGeneration = []string{"codex", "kiro"}, "g1"
	if _, err := Check(c, wt, "origin/main", nil); err == nil || !strings.Contains(err.Error(), "--expect-guard") {
		t.Errorf("several installed vendors without --expect-guard must be unjudgeable, got %v", err)
	}
	codex := Options{ExpectGuard: "codex"}
	r, _ = CheckWith(c, wt, "origin/main", nil, codex)
	if msg := inactive(r); !strings.Contains(msg, "--dangerously-bypass-hook-trust") {
		t.Errorf("GUARD_INACTIVE must name the codex launch flag, got %q", msg)
	}
	if rk, _ := CheckWith(c, wt, "origin/main", nil, Options{ExpectGuard: "kiro"}); !strings.Contains(inactive(rk), "--agent rein") {
		t.Errorf("GUARD_INACTIVE must name the kiro launch flag, got %q", inactive(rk))
	}

	// the seen log lives beside the contract index, whatever PIPELINE_LOGDIR says in either environment
	t.Setenv("PIPELINE_LOGDIR", t.TempDir())
	if !strings.HasPrefix(contract.SeenPath("g"), contract.IndexDir()) {
		t.Errorf("seen path %s must be under the contract index %s", contract.SeenPath("g"), contract.IndexDir())
	}
	// evidence that does not count: a stale generation, a post-tool line, an unrelated vendor
	write(t, contract.SeenPath("g"), "2026-10-08T10:00:00 codex pretool Bash gen=old\n"+
		"2026-10-08T10:00:01 codex stop - gen=g1\n"+
		"2026-10-08T10:00:02 agy pretool Bash gen=g1\n")
	if r, _ = CheckWith(c, wt, "origin/main", nil, codex); inactive(r) == "" {
		t.Error("stale generation / stop-only / other-vendor lines must not satisfy the guard")
	}
	write(t, contract.SeenPath("g"), "2026-10-08T10:00:00 codex pretool Bash gen=g1\n")
	if r, _ = CheckWith(c, wt, "origin/main", nil, codex); inactive(r) != "" {
		t.Error("a current-generation pretool line of an installed vendor proves the guard ran")
	}
	// codex ran, but the fallback launch was kiro without --agent rein: expecting kiro catches it
	if r, _ = CheckWith(c, wt, "origin/main", nil, Options{ExpectGuard: "kiro"}); !strings.Contains(inactive(r), "kiro") {
		t.Errorf("--expect-guard kiro must not be satisfied by codex lines, got %q", inactive(r))
	}

	_ = os.Remove(contract.SeenPath("g"))
	c.HooksInstalled, c.HooksGeneration = nil, ""
	if r, _ = CheckWith(c, wt, "origin/main", nil, Options{ExpectGuard: "codex"}); inactive(r) == "" {
		t.Error("--expect-guard with an empty seen log must be drift")
	}
}

func TestHeadMovingMeansCannotJudge(t *testing.T) {
	wt, c := repo(t, "m")
	write(t, filepath.Join(wt, "backend/Routing/A.cs"), "a2\n")
	sh(t, wt, "git", "add", "-A")
	sh(t, wt, "git", "commit", "-qm", "feat: x #169")
	midCheck = func() { sh(t, wt, "git", "commit", "-q", "--allow-empty", "-m", "late #169") }
	defer func() { midCheck = nil }()
	if _, err := Check(c, wt, "origin/main", nil); err == nil || !strings.Contains(err.Error(), "HEAD moved") {
		t.Errorf("want a HEAD-moved error, got %v", err)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
