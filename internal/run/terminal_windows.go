package run

import "syscall"

func terminal(fd uintptr) bool {
	var mode uint32
	return syscall.GetConsoleMode(syscall.Handle(fd), &mode) == nil
}
