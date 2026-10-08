//go:build !darwin && !linux && !windows

package run

import (
	"errors"
	"os"
	"syscall"
)

// ProcStart has no start time on this platform: it only tells whether the pid exists (0 = unknown start).
func ProcStart(pid int) (int64, error) {
	p, err := os.FindProcess(pid)
	if err != nil {
		return 0, ErrNoProc
	}
	switch err := p.Signal(syscall.Signal(0)); {
	case err == nil:
		return 0, nil
	case errors.Is(err, os.ErrProcessDone), errors.Is(err, syscall.ESRCH):
		return 0, ErrNoProc
	default:
		return 0, err
	}
}

func bootID() string { return "" }
