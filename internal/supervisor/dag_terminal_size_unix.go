//go:build darwin || linux

package supervisor

import (
	"os"
	"syscall"
	"unsafe"
)

// dagTerminalColumns asks the kernel for the terminal width. Pipes, files and
// other non-terminals fail the ioctl and return zero. SyscallConn avoids Fd(),
// which would switch a nonblocking descriptor to blocking mode.
func dagTerminalColumns(out *os.File) int {
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
	return int(size.cols)
}
