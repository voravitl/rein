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
