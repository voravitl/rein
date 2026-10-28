//go:build !windows

package supervisor

import (
	"os"
	"syscall"
	"testing"
)

func makeDAGStatusFIFO(t *testing.T, path string) func() {
	t.Helper()
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	// If a regression blocks a reader, opening and closing both ends releases it
	// before fixture cleanup restores the environment and removes paths.
	return func() {
		f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK, 0)
		if err == nil {
			_ = f.Close()
		}
	}
}
