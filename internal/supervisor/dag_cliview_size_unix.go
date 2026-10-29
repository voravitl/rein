//go:build darwin || linux

package supervisor

import (
	"os"
	"syscall"
	"unsafe"
)

// DAGTerminalRows asks the kernel for the terminal height of out. Pipes,
// files and other non-terminals return zero, which disables TUI clipping.
func DAGTerminalRows(out *os.File) int {
	if out == nil {
		return 0
	}
	conn, err := out.SyscallConn()
	if err != nil {
		return 0
	}
	var size struct{ rows, cols, xpixel, ypixel uint16 }
	var errno syscall.Errno
	if err := conn.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&size)))
	}); err != nil || errno != 0 {
		return 0
	}
	return int(size.rows)
}
