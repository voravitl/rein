package run

import (
	"syscall"
	"unsafe"
)

// ProcStart returns an opaque start time of pid (microseconds since the epoch, from kern.proc.pid: the first
// field of kinfo_proc is the process start timeval). ErrNoProc when the process does not exist.
func ProcStart(pid int) (int64, error) {
	mib := [4]int32{1, 14, 1, int32(pid)} // CTL_KERN, KERN_PROC, KERN_PROC_PID, pid
	var buf [648]byte                     // sizeof(struct kinfo_proc)
	n := uintptr(len(buf))
	_, _, e := syscall.Syscall6(syscall.SYS___SYSCTL, uintptr(unsafe.Pointer(&mib[0])), uintptr(len(mib)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)), 0, 0)
	if e != 0 {
		if e == syscall.ESRCH {
			return 0, ErrNoProc
		}
		return 0, e
	}
	if n == 0 {
		return 0, ErrNoProc
	}
	tv := *(*syscall.Timeval)(unsafe.Pointer(&buf[0]))
	return int64(tv.Sec)*1_000_000 + int64(tv.Usec), nil
}

func bootID() string { return "" }
