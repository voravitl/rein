//go:build !windows

package routing

import (
	"errors"
	"fmt"
	"syscall"
	"time"
)

func dedicatedLeaseGroup(pid int) error {
	group, err := syscall.Getpgid(pid)
	if errors.Is(err, syscall.ESRCH) {
		// A provider that exits before the launcher records it is an unreaped
		// zombie. macOS getpgid reports ESRCH for a zombie while Linux still
		// returns its group, so probe the group itself. The pid cannot be reused
		// until the launcher reaps it, so a live group with id == pid is the
		// child's own group: kill(-pid, 0) answers nil or EPERM for it and
		// ESRCH when the child stayed in the launcher's group.
		if kerr := syscall.Kill(-pid, 0); kerr == nil || errors.Is(kerr, syscall.EPERM) {
			return nil
		}
		return err
	}
	if err != nil {
		return err
	}
	if group != pid {
		return errors.New("local provider must have its own process group")
	}
	return nil
}

// SIGKILL reaps nothing by itself: group members still dying keep the group
// visible, and orphaned descendants of a killed leader (setpgid raced the
// child out) stay zombies until the OS reaper gets to them. 2026-10-18 CI:
// the group of a fast-failing worker still held its reparented sleeper, so
// the immediate probe returned EPERM and a proven-completed worker was
// misreported as unproven termination. The grace only observes the interim
// between kill and reap; anything that outlives it stays refused.
const leaseGroupGoneGrace = time.Second

func leaseGroupGone(group int) error {
	if err := syscall.Kill(-group, 0); errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return fmt.Errorf("local provider process group %d termination is unproven", group)
}

func awaitLeaseGroupGone(group int) error {
	deadline := time.Now().Add(leaseGroupGoneGrace)
	for {
		if err := leaseGroupGone(group); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return leaseGroupGone(group)
		}
		time.Sleep(40 * time.Millisecond)
	}
}
