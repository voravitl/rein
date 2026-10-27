//go:build !windows

package supervisor

import (
	"os"
	"syscall"
)

func openArtifactFile(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}
