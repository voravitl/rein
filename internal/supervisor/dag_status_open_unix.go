//go:build !windows

package supervisor

import (
	"os"
	"syscall"
)

func openDAGStatusFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}
