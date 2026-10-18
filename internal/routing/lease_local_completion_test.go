//go:build !windows

package routing

import (
	"io"
	"os"
	"os/exec"
	"syscall"
	"testing"
)

func TestLeaseLocalChildHelper(t *testing.T) {
	mode := os.Getenv("REIN_LEASE_CHILD_MODE")
	if mode == "" {
		return
	}
	if mode == "descendant" {
		if _, err := io.ReadFull(os.Stdin, make([]byte, 1)); err != nil {
			t.Fatal(err)
		}
		child := exec.Command(os.Args[0], "-test.run=^TestLeaseLocalChildHelper$")
		child.Env = append(os.Environ(), "REIN_LEASE_CHILD_MODE=hold")
		child.Stdin = os.Stdin
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		return
	}
	if mode == "descendant_exit" {
		if _, err := io.ReadFull(os.Stdin, make([]byte, 1)); err != nil {
			t.Fatal(err)
		}
		// a short-lived orphan in the leader's group: still running when the
		// parent probes, then reparented to the OS reaper like the real worker
		child := exec.Command("sleep", "0.35")
		if child.Start() != nil {
			t.Fatal("descendant_exit sleep")
		}
		return
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func startLeaseLocalChild(t *testing.T, mode string) (*Lease, *exec.Cmd, *os.File, string) {
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
	t.Cleanup(func() { in.Close(); out.Close() })
	cmd := exec.Command(os.Args[0], "-test.run=^TestLeaseLocalChildHelper$")
	cmd.Env = append(os.Environ(), "REIN_LEASE_CHILD_MODE="+mode)
	cmd.Stdin = in
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() })
	if err := l.SetChildGroup(cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	return l, cmd, out, r.Attempt
}

func TestCompleteLocalRequiresActualChildAndGroupTermination(t *testing.T) {
	l, cmd, input, attempt := startLeaseLocalChild(t, "hold")
	if err := l.CompleteLocal(); err == nil {
		t.Fatal("live local child was released")
	}
	l.Release()
	if TaskBusy(l.Task) == "" {
		t.Fatal("Release removed a lease with a live local child")
	}
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := l.CompleteLocal(); err != nil {
		t.Fatal(err)
	}
	if _, err := SettleAttempt(attempt); err != nil {
		t.Fatalf("proven local completion cannot settle: %v", err)
	}
}

// TestCompleteLocalToleratesEphemeralGroupWindow proves the completion release
// observes the temporary window where a SIGKILLed orphan still holds the group
// (running or zombie before the OS reaper takes it) instead of declaring the
// launch unprovably done. It then proves the lease settles.
func TestCompleteLocalToleratesEphemeralGroupWindow(t *testing.T) {
	l, cmd, input, attempt := startLeaseLocalChild(t, "descendant_exit")
	if _, err := input.Write([]byte("1")); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil { // leader is gone; the orphan still holds its group briefly
		t.Fatal(err)
	}
	if err := l.CompleteLocal(); err != nil {
		t.Fatalf("ephemeral group window was not released: %v", err)
	}
	if _, err := SettleAttempt(attempt); err != nil {
		t.Fatalf("proven local completion cannot settle: %v", err)
	}
}

func TestCompleteLocalRefusesSurvivingDescendant(t *testing.T) {
	l, cmd, input, _ := startLeaseLocalChild(t, "descendant")
	if _, err := input.Write([]byte("1")); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := l.CompleteLocal(); err == nil {
		t.Fatal("exited leader hid a surviving descendant")
	}
	if err := EndLease(l.Task, l.Attempt); err == nil {
		t.Fatal("public settlement bypassed the surviving process group")
	}
}

func TestCompleteLocalRefusesRemoteOrChangedLease(t *testing.T) {
	l, cmd, input, _ := startLeaseLocalChild(t, "hold")
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	copy := *l
	copy.Attempt = "different-attempt"
	if err := copy.CompleteLocal(); err == nil {
		t.Fatal("stale caller completed a different lease")
	}
	if err := l.MarkDispatchPending(); err != nil {
		t.Fatal(err)
	}
	if err := l.CompleteLocal(); err == nil {
		t.Fatal("local exit was accepted as remote death proof")
	}
}
