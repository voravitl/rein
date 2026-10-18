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
