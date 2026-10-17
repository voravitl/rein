//go:build !windows

package routing

import (
	"errors"
	"fmt"
	"syscall"
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

func leaseGroupGone(group int) error {
	if err := syscall.Kill(-group, 0); errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return fmt.Errorf("local provider process group %d termination is unproven", group)
}
