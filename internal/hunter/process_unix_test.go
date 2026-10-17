//go:build !windows

package hunter

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestHunterProcessTreeHelper(t *testing.T) {
	if len(os.Args) < 4 || os.Args[len(os.Args)-3] != "hunter-tree" {
		return
	}
	mode, marker := os.Args[len(os.Args)-2], os.Args[len(os.Args)-1]
	if mode == "child" {
		time.Sleep(1500 * time.Millisecond)
		os.WriteFile(marker, []byte("survived"), 0600)
		os.Exit(0)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestHunterProcessTreeHelper$", "hunter-tree", "child", marker)
	if child.Start() != nil {
		os.Exit(99)
	}
	os.WriteFile(marker+".started", []byte("started"), 0600)
	time.Sleep(30 * time.Second)
	os.Exit(0)
}

func TestReproduceTimeoutKillsDescendants(t *testing.T) {
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(t.TempDir(), "contracts"))
	repo, sha := hunterRepo(t)
	f := testFinding("t", "baseline.txt", 1)
	f.Baseline = sha
	id, _, err := Report(f)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "survived")
	a, err := Reproduce(context.Background(), id, repo, "tree", time.Second, 1, []string{os.Args[0], "-test.run=^TestHunterProcessTreeHelper$", "hunter-tree", "parent", marker})
	if err != nil {
		t.Fatal(err)
	}
	if a.Outcome != "setup-failure" {
		t.Fatalf("timeout was bug proof: %+v", a)
	}
	if _, err := os.Stat(marker + ".started"); err != nil {
		t.Fatalf("descendant test did not reach execution: %v", err)
	}
	time.Sleep(time.Second)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("descendant survived cancellation")
	}
}
