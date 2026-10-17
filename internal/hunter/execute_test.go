package hunter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestHunterExecHelper(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "hunter-helper" {
		return
	}
	mode := os.Args[len(os.Args)-1]
	if mode == "sleep" {
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	b, err := os.ReadFile("baseline.txt")
	if err != nil || string(b) != "committed baseline\n" {
		os.Exit(99)
	}
	fmt.Print("actual baseline check: ", mode)
	if err := os.MkdirAll("evidence", 0700); err != nil {
		os.Exit(98)
	}
	if err := os.WriteFile("evidence/test-output", []byte("isolated write"), 0600); err != nil {
		os.Exit(98)
	}
	if mode == "fail" {
		os.Exit(7)
	}
	os.Exit(0)
}

func hunterRepo(t *testing.T) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("reproduction requires descendant containment")
	}
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = repo
		c.Env = reproEnvironment()
		b, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("git %v: %v %s", args, e, b)
		}
		return strings.TrimSpace(string(b))
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "baseline.txt"), []byte("committed baseline\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "baseline.txt")
	git("-c", "user.name=Rein test", "-c", "user.email=test@example.invalid", "commit", "-qm", "baseline")
	return repo, git("rev-parse", "HEAD")
}

func TestReproduceActualExitAndDurableEvidence(t *testing.T) {
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
	repo, sha := hunterRepo(t)
	f := testFinding("t", "baseline.txt", 1)
	f.Baseline = sha
	id, _, err := Report(f)
	if err != nil {
		t.Fatal(err)
	}
	// Dirty source content is never included in the exact committed baseline.
	os.WriteFile(filepath.Join(repo, "baseline.txt"), []byte("uncommitted"), 0600)
	for _, tc := range []struct {
		mode    string
		expect  int
		outcome string
		exit    int
	}{{"fail", 7, "reproduced", 7}, {"pass", 7, "not-reproduced", 0}, {"fail", 8, "not-reproduced", 7}} {
		name := fmt.Sprintf("%s-%d", tc.mode, tc.expect)
		a, err := Reproduce(context.Background(), id, repo, name, 5*time.Second, tc.expect, []string{os.Args[0], "-test.run=^TestHunterExecHelper$", "hunter-helper", tc.mode})
		if err != nil {
			t.Fatal(err)
		}
		if a.Outcome != tc.outcome || a.Exit != tc.exit || !a.Observed || a.Revision != sha {
			t.Fatalf("attempt %+v", a)
		}
		data, err := os.ReadFile(a.EvidencePath)
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256(data)
		if a.EvidenceHash != hex.EncodeToString(h[:]) {
			t.Fatal("evidence hash mismatch")
		}
		var evidence Execution
		if err := json.Unmarshal(data, &evidence); err != nil {
			t.Fatal(err)
		}
		if evidence.Contract == nil || evidence.Contract.Name == "t" || evidence.Contract.Worktree != a.Cwd || len(evidence.Contract.Allow) != 2 {
			t.Fatalf("missing isolated contract: %+v", evidence.Contract)
		}
		if !strings.Contains(evidence.Output, "actual baseline check") || evidence.Revision != sha || evidence.Attempt != name || evidence.Exit != tc.exit {
			t.Fatalf("evidence %+v", evidence)
		}
		if _, err := os.Stat(a.Cwd); !os.IsNotExist(err) {
			t.Fatalf("clone not removed: %v", err)
		}
		before := string(data)
		if _, err := Reproduce(context.Background(), id, repo, name, time.Second, 7, []string{"missing"}); err == nil {
			t.Fatal("reused attempt")
		}
		after, _ := os.ReadFile(a.EvidencePath)
		if string(after) != before {
			t.Fatal("evidence overwritten")
		}
	}
	if _, err := os.Stat(filepath.Join(repo, "test-output")); !os.IsNotExist(err) {
		t.Fatal("test wrote into source")
	}
	got, err := Get(id)
	if err != nil || len(got.Attempts) != 3 || got.State != Candidate || got.Strength != ReproducedOnce {
		t.Fatalf("finding %+v %v", got, err)
	}
}

func TestReproduceSetupAndTimeoutNeverProveBug(t *testing.T) {
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
	repo, sha := hunterRepo(t)
	f := testFinding("t", "baseline.txt", 1)
	f.Baseline = sha
	id, _, err := Report(f)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		argv    []string
		timeout time.Duration
	}{{"missing", []string{filepath.Join(t.TempDir(), "missing")}, 5 * time.Second}, {"timeout", []string{os.Args[0], "-test.run=^TestHunterExecHelper$", "hunter-helper", "sleep"}, time.Second}} {
		start := time.Now()
		a, err := Reproduce(context.Background(), id, repo, tc.name, tc.timeout, 7, tc.argv)
		if err != nil {
			t.Fatal(err)
		}
		if time.Since(start) > 5*time.Second {
			t.Fatal("unbounded execution")
		}
		if a.Outcome != "setup-failure" || a.Exit != -1 {
			t.Fatalf("setup counted as bug: %+v", a)
		}
	}
	got, _ := Get(id)
	if got.Strength != StaticSupported {
		t.Fatal("setup failure promoted strength")
	}
	if err := Move(id, Reproduced, "claimed"); err == nil {
		t.Fatal("setup opened reproduced gate")
	}
	if _, err := Reproduce(context.Background(), id, repo, "../escape", time.Second, 7, []string{"true"}); err == nil {
		t.Fatal("unsafe identity accepted")
	}
	f2 := testFinding("t", "baseline.txt", 2)
	f2.Baseline = "abc123"
	id2, _, _ := Report(f2)
	if _, err := Reproduce(context.Background(), id2, repo, "short", time.Second, 7, []string{"true"}); err == nil {
		t.Fatal("abbreviated baseline accepted")
	}
	f3 := testFinding("t", "baseline.txt", 3)
	f3.Baseline = strings.Repeat("1", 40)
	id3, _, _ := Report(f3)
	if _, err := Reproduce(context.Background(), id3, repo, "stale", time.Second, 7, []string{"true"}); err == nil {
		t.Fatal("stale baseline accepted")
	}
}
