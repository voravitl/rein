package run

import (
	"syscall"
	"unsafe"
)

func terminal(fd uintptr) bool {
	var state syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCGETS, uintptr(unsafe.Pointer(&state)))
	return errno == 0
}
