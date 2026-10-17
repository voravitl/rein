//go:build !windows

package run

import (
	"errors"
	"os"
	"syscall"
)

func lockFileExclusive(f *os.File) (bool, error) {
	return flock(f, syscall.LOCK_EX)
}

func lockFileShared(f *os.File) (bool, error) {
	return flock(f, syscall.LOCK_SH)
}

func flock(f *os.File, mode int) (bool, error) {
	if err := syscall.Flock(int(f.Fd()), mode|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
