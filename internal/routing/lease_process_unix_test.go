//go:build !windows

package routing

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// A fast provider can exit before the launcher records its process group. The
// child is then an unreaped zombie: macOS getpgid reports ESRCH for it while
// Linux still reports the group. The group proof must not depend on that race.
func TestDedicatedLeaseGroupAcceptsExitedUnreapedLeader(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait()
	time.Sleep(300 * time.Millisecond) // exited, not yet reaped
	if err := dedicatedLeaseGroup(cmd.Process.Pid); err != nil {
		t.Fatalf("exited group leader refused: %v", err)
	}
}

// The fallback must still refuse a child that never left the launcher's group.
func TestDedicatedLeaseGroupRefusesSharedGroup(t *testing.T) {
	for _, exited := range []bool{false, true} {
		cmd := exec.Command("/bin/sh", "-c", "sleep 2")
		if exited {
			cmd = exec.Command("/bin/sh", "-c", "exit 0")
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		if exited {
			time.Sleep(300 * time.Millisecond)
		}
		err := dedicatedLeaseGroup(cmd.Process.Pid)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if err == nil {
			t.Fatalf("exited=%v: child sharing the launcher's process group was accepted", exited)
		}
	}
}
