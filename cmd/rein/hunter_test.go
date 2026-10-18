package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/voravitl/rein/internal/hunter"
)

func TestCmdHunterReportGetList(t *testing.T) {
	d := t.TempDir()
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(d, "contracts"))
	finding := hunter.Finding{Task: "t", Run: "r", Baseline: strings.Repeat("a", 40), File: "file.go", Line: 1, Expected: "reject", Actual: "accept", Invariant: "deny", Severity: "high", Confidence: "high"}
	data, _ := json.Marshal(finding)
	input := filepath.Join(d, "finding.json")
	os.WriteFile(input, data, 0600)
	if code := cmdHunter([]string{"report", "--input", input}); code != 0 {
		t.Fatalf("report exit %d", code)
	}
	if code := cmdHunter([]string{"report", "--input", input}); code != 0 {
		t.Fatalf("dedupe exit %d", code)
	}
	all, err := hunter.List()
	if err != nil || len(all) != 1 {
		t.Fatalf("dedupe %+v %v", all, err)
	}
	if code := cmdHunter([]string{"get", "--id", all[0].ID}); code != 0 {
		t.Fatalf("get exit %d", code)
	}
	if code := cmdHunter([]string{"list"}); code != 0 {
		t.Fatalf("list exit %d", code)
	}
	for _, args := range [][]string{nil, {"report"}, {"get"}, {"list", "extra"}, {"approve", "--id", "H-0001"}, {"verify", "--id", "H-0001"}, {"reproduce", "--id", "H-0001"}} {
		if code := cmdHunter(args); code != 2 {
			t.Fatalf("%v code %d", args, code)
		}
	}
	os.WriteFile(input, append(data, []byte(" {}")...), 0600)
	if code := cmdHunter([]string{"report", "--input", input}); code != 1 {
		t.Fatalf("trailing JSON accepted: %d", code)
	}
	if code := cmdHunter([]string{"get", "--id", "missing"}); code != 1 {
		t.Fatalf("unknown finding: %d", code)
	}
}

func TestCmdHunterExecutionHelper(t *testing.T) {
	if len(os.Args) > 1 && os.Args[len(os.Args)-1] == "hunter-cli-helper" {
		if b, err := os.ReadFile("baseline.txt"); err != nil {
			os.Exit(99)
		} else if string(b) == "fixed" {
			os.Exit(0)
		}
		os.Stdout.WriteString("actual failing check\n")
		os.Exit(6)
	}
}

func TestCmdHunterReproduce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows reproduction deliberately blocked without descendant containment")
	}
	d := t.TempDir()
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(d, "contracts"))
	repo := filepath.Join(d, "repo")
	os.Mkdir(repo, 0700)
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = repo
		c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		b, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("git %v: %v %s", args, e, b)
		}
		return strings.TrimSpace(string(b))
	}
	git("init", "-q")
	os.WriteFile(filepath.Join(repo, "baseline.txt"), []byte("baseline"), 0600)
	git("add", ".")
	git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "baseline")
	sha := git("rev-parse", "HEAD")
	id, _, err := hunter.Report(hunter.Finding{Task: "t", Run: "r", Baseline: sha, File: "baseline.txt", Line: 1, Expected: "exit0", Actual: "exit6", Invariant: "test", Severity: "low", Confidence: "high"})
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"reproduce", "--id", id, "--repo", repo, "--attempt", "cli-1", "--timeout", "5s", "--expect-exit", "6", "--", os.Args[0], "-test.run=^TestCmdHunterExecutionHelper$", "hunter-cli-helper"}
	if code := cmdHunter(args); code != 0 {
		t.Fatalf("reproduce %d", code)
	}
	got, err := hunter.Get(id)
	if err != nil || len(got.Attempts) != 1 {
		t.Fatalf("finding %+v %v", got, err)
	}
	a := got.Attempts[0]
	if !a.Observed || a.Outcome != "reproduced" || a.Exit != 6 || a.Revision != sha || a.EvidenceHash == "" {
		t.Fatalf("attempt %+v", a)
	}
	data, err := os.ReadFile(a.EvidencePath)
	if err != nil || !strings.Contains(string(data), "actual failing check") {
		t.Fatalf("evidence %s %v", data, err)
	}
	if code := cmdHunter(args); code != 1 {
		t.Fatalf("attempt reused: %d", code)
	}
}

func TestCmdHunterVerifyRuntime(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires process containment")
	}
	d := t.TempDir()
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(d, "contracts"))
	repo := filepath.Join(d, "repo")
	os.Mkdir(repo, 0700)
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = repo
		c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		b, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("%s %v", b, e)
		}
		return strings.TrimSpace(string(b))
	}
	git("init", "-q")
	os.WriteFile(filepath.Join(repo, "baseline.txt"), []byte("baseline"), 0600)
	os.WriteFile(filepath.Join(repo, "test.txt"), []byte("immutable check"), 0600)
	git("add", ".")
	git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "baseline")
	sha := git("rev-parse", "HEAD")
	id, _, e := hunter.Report(hunter.Finding{Task: "t", Run: "r", Baseline: sha, File: "baseline.txt", Line: 1, Expected: "exit0", Actual: "exit6", Invariant: "test", Severity: "low", Confidence: "high"})
	if e != nil {
		t.Fatal(e)
	}
	if c := cmdHunter([]string{"reproduce", "--id", id, "--repo", repo, "--attempt", "red", "--expect-exit", "6", "--", os.Args[0], "-test.run=^TestCmdHunterExecutionHelper$", "hunter-cli-helper"}); c != 0 {
		t.Fatal(c)
	}
	os.WriteFile(filepath.Join(repo, "baseline.txt"), []byte("fixed"), 0600)
	git("add", ".")
	git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "fixed")
	fixed := git("rev-parse", "HEAD")
	args := []string{"verify", "--id", id, "--repo", repo, "--attempt", "green", "--fixed", fixed, "--red-attempt", "red", "--test-paths-json", `["test.txt"]`, "--", os.Args[0], "-test.run=^TestCmdHunterExecutionHelper$", "hunter-cli-helper"}
	if c := cmdHunter(args); c != 0 {
		t.Fatalf("verification exit %d", c)
	}
	f, _ := hunter.Get(id)
	if f.State == hunter.VerifiedFixed {
		t.Fatal("CLI self-approved finding")
	}
	if c := cmdHunter(args); c != 1 {
		t.Fatal("overwrote immutable verification", c)
	}
}

func TestCmdHunterClosureRequiresProofAndHumanTerminal(t *testing.T) {
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
	if code := cmdHunter([]string{"review", "--id", "H-0001", "--repo", t.TempDir(), "--verification-digest", strings.Repeat("a", 64), "--attempt", "verify", "--worker", "same", "--reviewer", "same", "--report", "missing"}); code != 1 {
		t.Fatalf("self review exit %d", code)
	}
	prior := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	os.Stdin = r
	defer func() { os.Stdin = prior }()
	if code := cmdHunter([]string{"approve", "--id", "H-0001", "--repo", t.TempDir(), "--verification-digest", strings.Repeat("a", 64), "--review-digest", strings.Repeat("b", 64), "--owner", "claimed", "--reason", "claimed"}); code != 1 {
		t.Fatalf("nonterminal approval exit %d", code)
	}
	all, err := hunter.List()
	if err != nil || len(all) != 0 {
		t.Fatalf("approval created state: %+v %v", all, err)
	}
}

func TestCmdHunterScanReportRequiresExplicitScope(t *testing.T) {
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
	for _, args := range [][]string{{"scan-report"}, {"scan-report", "--task", "t", "--baseline", strings.Repeat("a", 40), "--covered-json", "[\"source\"]"}} {
		if code := cmdHunter(args); code != 2 {
			t.Fatalf("implicit scope exit %d", code)
		}
	}
	if code := cmdHunter([]string{"scan-report", "--task", "t", "--baseline", strings.Repeat("a", 40), "--covered-json", "[\"source\"]", "--skipped-json", "[]"}); code != 0 {
		t.Fatalf("scoped report exit %d", code)
	}
}
