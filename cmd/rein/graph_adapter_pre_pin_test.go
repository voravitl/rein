//go:build !windows

package main

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// A provider may finish inside the window between entering its fenced process group and the birth-time read,
// so the recorded execution carries no pinned start; the group-absence proof must still be the release gate.
func TestGraphLocalGoneAcceptsFinishedWithoutPin(t *testing.T) {
	cmd := exec.Command("cat")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() })

	st := func(unknown bool, start int64) graphExecution {
		return graphExecution{Gone: true, PID: cmd.Process.Pid, PIDStart: start, PIDStartUnknown: unknown, CompletedAt: time.Now().UTC()}
	}
	if err := graphLocalGone(st(true, 0)); err == nil {
		t.Fatal("live provider group was released without a proof")
	}
	if err := graphLocalGone(st(false, 0)); err == nil {
		t.Fatal("evidence without a pinned start and without the unpinned flag was accepted")
	}
	if err := graphLocalGone(st(true, int64(cmd.Process.Pid))); err == nil {
		t.Fatal("contradictory identity was accepted")
	}
	if err := in.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := graphLocalGone(st(true, 0)); err != nil {
		t.Fatalf("proven group absence must release the finished child: %v", err)
	}
}
