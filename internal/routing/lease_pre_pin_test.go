//go:build !windows

package routing

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"

	"github.com/voravitl/rein/internal/run"
)

func startFencedHoldChild(t *testing.T) (*Lease, *os.File, *exec.Cmd, *AutoResult) {
	t.Helper()
	w := newWorld(t)
	w.startRun()
	r := mustPrepare(t, w)
	l, err := AcquireLease(w.c, r.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	in, out, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = in.Close(); _ = out.Close() })
	cmd := exec.Command(os.Args[0], "-test.run=^TestLeaseLocalChildHelper$")
	cmd.Env = append(os.Environ(), "REIN_LEASE_CHILD_MODE=hold")
	cmd.Stdin = in
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() })
	return l, out, cmd, r
}

// The birth-time pin inside SetChildGroup races an instant-exiting child: linux reports ErrNoProc for an
// already-zombied child while darwin still reads its start, so the same launch parked one platform and not
// the other. These tests replay that window deterministically through the procStartOf seam.
func TestSetChildGroupRecordsFinishedWithoutPin(t *testing.T) {
	l, out, cmd, r := startFencedHoldChild(t)

	prev := procStartOf
	procStartOf = func(int) (int64, error) { return 0, run.ErrNoProc }
	err := l.SetChildGroup(cmd.Process.Pid)
	procStartOf = prev
	if err != nil {
		t.Fatalf("a fenced child that finishes before the birth-time pin cannot launch: %v", err)
	}
	if l.Child != cmd.Process.Pid || l.ChildGroup != cmd.Process.Pid || l.ChildStart != 0 || !l.ChildStartUnknown {
		t.Fatalf("finished-without-pin identity was not recorded: %+v", l)
	}
	if err := l.SetChildGroup(cmd.Process.Pid); err == nil || !strings.Contains(err.Error(), "cannot be replaced") {
		t.Fatalf("recorded identity was replaced: %v", err)
	}
	if err := l.CompleteLocal(); err == nil {
		t.Fatal("local release proceeded while the recorded group was still live")
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := l.CompleteLocal(); err != nil {
		t.Fatalf("proven group absence must release the finished child: %v", err)
	}
	if _, err := SettleAttempt(r.Attempt); err != nil {
		t.Fatalf("proven local completion cannot settle: %v", err)
	}
}

func TestSetChildGroupStillFailsClosedWhenTheBirthTimeIsUnreadable(t *testing.T) {
	l, _, cmd, _ := startFencedHoldChild(t)

	prev := procStartOf
	procStartOf = func(int) (int64, error) { return 0, errors.New("procfs read failed") }
	err := l.SetChildGroup(cmd.Process.Pid)
	procStartOf = prev
	if err == nil || !strings.Contains(err.Error(), "cannot be proven") {
		t.Fatalf("an unreadable birth time must keep the launch fail-closed: %v", err)
	}
	if l.Child != 0 {
		t.Fatalf("an identity that cannot be proven was recorded: %+v", l)
	}
}
