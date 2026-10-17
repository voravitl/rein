package hunter

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTextOnlyFixClaimCannotClose(t *testing.T) {
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
	id := toFixInProgress(t)
	if err := VerifyFix(id, "def456", "def456", "fixed=def456 FAIL@abc123 PASS@def456 existing-pass"); err == nil {
		t.Fatal("caller strings closed finding without runtime or independent review")
	}
	f, _ := Get(id)
	if f.State != FixInProgress {
		t.Fatal("claim changed lifecycle")
	}
}

func TestRuntimeVerifyHelper(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "verify-helper" {
		return
	}
	if os.Args[len(os.Args)-1] == "suite" {
		os.Exit(0)
	}
	b, e := os.ReadFile("value")
	if e != nil {
		os.Exit(99)
	}
	if string(b) != "fixed\n" {
		os.Exit(7)
	}
	os.Exit(0)
}

func verifyFixture(t *testing.T) (string, string, string, string) {
	t.Helper()
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
	repo, _ := hunterRepo(t)
	for p, s := range map[string]string{"value": "broken\n", "check_test.go": "package fixture\n// unchanged regression assertion\n"} {
		if e := os.WriteFile(filepath.Join(repo, p), []byte(s), 0600); e != nil {
			t.Fatal(e)
		}
	}
	g := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = repo
		c.Env = reproEnvironment()
		b, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("git: %s %v", b, e)
		}
		return strings.TrimSpace(string(b))
	}
	g("add", ".")
	g("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "regression")
	baseline := g("rev-parse", "HEAD")
	f := testFinding("t", "value", 1)
	f.Baseline = baseline
	id, _, e := Report(f)
	if e != nil {
		t.Fatal(e)
	}
	_, e = Reproduce(context.Background(), id, repo, "red", time.Minute, 7, []string{os.Args[0], "-test.run=^TestRuntimeVerifyHelper$", "verify-helper", "regression"})
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(repo, "value"), []byte("fixed\n"), 0600); e != nil {
		t.Fatal(e)
	}
	g("add", "value")
	g("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "fix")
	return repo, id, baseline, g("rev-parse", "HEAD")
}

func TestVerifyRuntimeRunsSameCheckAndSuiteWithoutClosing(t *testing.T) {
	repo, id, _, fixed := verifyFixture(t)
	r, e := VerifyRuntime(context.Background(), id, repo, "verify", fixed, "red", []string{"check_test.go"}, []string{os.Args[0], "-test.run=^TestRuntimeVerifyHelper$", "verify-helper", "suite"}, time.Minute)
	if e != nil || r.Status != "WAITING_INDEPENDENT_REVIEW" || r.Regression.Exit != 0 || r.Suite.Exit != 0 {
		t.Fatalf("%+v %v", r, e)
	}
	if _, e = os.Stat(r.EvidencePath); e != nil {
		t.Fatal(e)
	}
	f, _ := Get(id)
	if f.State == VerifiedFixed {
		t.Fatal("runtime verifier self-approved closure")
	}
	if _, e = VerifyRuntime(context.Background(), id, repo, "verify", fixed, "red", []string{"check_test.go"}, []string{"true"}, time.Minute); e == nil {
		t.Fatal("overwrote execution evidence")
	}
}

func TestVerifyRuntimeRejectsWeakeningTamperAndStale(t *testing.T) {
	for _, mode := range []string{"weaken", "tamper", "stale", "unobserved"} {
		t.Run(mode, func(t *testing.T) {
			repo, id, _, fixed := verifyFixture(t)
			switch mode {
			case "weaken":
				os.WriteFile(filepath.Join(repo, "check_test.go"), []byte("package fixture\n// skipped\n"), 0600)
				c := exec.Command("git", "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-am", "weaken")
				c.Dir = repo
				if b, e := c.CombinedOutput(); e != nil {
					t.Fatalf("%s %v", b, e)
				}
				c = exec.Command("git", "rev-parse", "HEAD")
				c.Dir = repo
				b, _ := c.Output()
				fixed = strings.TrimSpace(string(b))
			case "tamper":
				f, _ := Get(id)
				os.WriteFile(f.Attempts[0].EvidencePath, []byte("{}"), 0600)
			case "stale":
				fixed = strings.Repeat("0", 40)
			case "unobserved":
				red := ReproAttempt{Attempt: "claimed", Revision: fixed, Command: "true", Cwd: repo, Outcome: "reproduced", Exit: 7}
				if e := RecordAttempt(id, red); e != nil {
					t.Fatal(e)
				}
			}
			red := "red"
			if mode == "unobserved" {
				red = "claimed"
			}
			if _, e := VerifyRuntime(context.Background(), id, repo, "verify", fixed, red, []string{"check_test.go"}, []string{"true"}, time.Minute); e == nil {
				t.Fatal("accepted invalid proof")
			}
		})
	}
}

func TestVerifyRuntimeFailedSuiteKeepsEvidence(t *testing.T) {
	repo, id, _, fixed := verifyFixture(t)
	r, e := VerifyRuntime(context.Background(), id, repo, "suite-fail", fixed, "red", []string{"check_test.go"}, []string{os.Args[0], "-test.run=^TestHunterExecHelper$", "hunter-helper", "fail"}, time.Minute)
	if e == nil || r.Status != "RUNTIME_FAILED" || r.Regression.Exit != 0 || r.Suite.Exit != 7 {
		t.Fatalf("%+v %v", r, e)
	}
	if _, e = os.Stat(r.EvidencePath); e != nil {
		t.Fatal("failed suite lost evidence", e)
	}
}

func TestVerifyRuntimeReplaysRedToRejectChangedExternalCheck(t *testing.T) {
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
	repo, baseline := hunterRepo(t)
	script := filepath.Join(t.TempDir(), "check.sh")
	os.WriteFile(script, []byte("#!/bin/sh\nexit 7\n"), 0700)
	f := testFinding("t", "baseline.txt", 1)
	f.Baseline = baseline
	id, _, e := Report(f)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = Reproduce(context.Background(), id, repo, "red", time.Minute, 7, []string{script}); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(repo, "newfile"), []byte("fix"), 0600)
	for _, args := range [][]string{{"add", "."}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "fixed"}} {
		c := exec.Command("git", args...)
		c.Dir = repo
		if b, e := c.CombinedOutput(); e != nil {
			t.Fatalf("%s %v", b, e)
		}
	}
	c := exec.Command("git", "rev-parse", "HEAD")
	c.Dir = repo
	b, _ := c.Output()
	fixed := strings.TrimSpace(string(b))
	os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0700)
	r, e := VerifyRuntime(context.Background(), id, repo, "external-check", fixed, "red", []string{"baseline.txt"}, []string{"true"}, time.Minute)
	if e == nil || r.BaselineReplay.Exit != 0 || r.Regression.Exit != -1 || r.Status != "RUNTIME_FAILED" {
		t.Fatalf("external check drift accepted: %+v %v", r, e)
	}
}

func TestVerifyRuntimeRechecksSourceHeadAfterSuite(t *testing.T) {
	repo, id, _, fixed := verifyFixture(t)
	suite := []string{"git", "-C", repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", "advance original HEAD"}
	r, e := VerifyRuntime(context.Background(), id, repo, "head-moved", fixed, "red", []string{"check_test.go"}, suite, time.Minute)
	if e == nil || r.Status != "STALE" || r.Suite.Exit != 0 {
		t.Fatalf("HEAD move accepted: %+v %v", r, e)
	}
	if _, e = os.Stat(r.EvidencePath); e != nil {
		t.Fatal("stale execution evidence lost", e)
	}
}
