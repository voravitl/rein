package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/voravitl/rein/internal/run"
)

func TestControllerExecRefusesSupervisedBeforeOrca(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable fixture")
	}
	repo := t.TempDir()
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "commit", "--allow-empty", "-qm", "init")
	bin := t.TempDir()
	marker := filepath.Join(bin, "called")
	t.Setenv("CONTROLLER_TEST_CALLED", marker)
	if err := os.WriteFile(filepath.Join(bin, "orca"), []byte("#!/bin/sh\nprintf called > \"$CONTROLLER_TEST_CALLED\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	_, err := run.StartSupervised(repo, "r", nil, run.Owner{SessionID: "s", PID: os.Getpid()}, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	args := []string{repo, "--run", "r", "--", "--peek"}
	if code := cmdControllerExec(args); code == 0 {
		t.Fatal("supervised legacy check authorized")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("Orca called: %v", err)
	}
	loc, _ := run.Locate(repo)
	if err := os.Remove(loc.Marker); err != nil {
		t.Fatal(err)
	}
	if code := cmdControllerExec(args); code != 0 {
		t.Fatalf("legacy check: %d", code)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]string{{repo, "--run", "r", "--", "--run", "other"}, {repo, "--run", "r", "--", "--timeout-ms", "999999"}, {repo, "--run", "r", "--", "--peek", "--peek"}} {
		if code := cmdControllerExec(bad); code != 2 {
			t.Fatalf("bad argv accepted: %v %d", bad, code)
		}
	}
}
