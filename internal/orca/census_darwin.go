//go:build darwin

package orca

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

// censusScan reads the process table through /bin/ps, the kernel's own formatter — deliberately without
// depending on kinfo struct offsets for pid/ppid, which no installed SDK header on this machine documents.
// The per-pid start marker still comes from kern.proc.pid directly (the same sysctl internal/run uses),
// so Start stays exact even when ps itself only shows second-level times.
func censusScan() ([]censusRow, error) {
	out, err := exec.Command("/bin/ps", "-axo", "pid=,ppid=,stat=").CombinedOutput()
	if err != nil {
		return nil, err
	}
	rows := []censusRow{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 { // pid=,ppid=,stat= emits values only, no header line
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		ppid, err := strconv.Atoi(f[1])
		if err != nil {
			return nil, err
		}
		start, err := censusDarwinStart(pid)
		if err == syscall.ESRCH { // gone between ps and the sysctl: not in this sample
			continue
		}
		if err != nil {
			return nil, err
		}
		rows = append(rows, censusRow{PID: pid, PPID: ppid, Start: start, State: f[2][:1]})
	}
	return rows, nil
}

// censusDarwinStart reads the start marker for one pid from kern.proc.pid (microseconds since the
// epoch — the same source internal/run.ProcStart read, so neither reader can disagree with the other).
func censusDarwinStart(pid int) (int64, error) {
	mib := [4]int32{1, 14, 1, int32(pid)} // CTL_KERN, KERN_PROC, KERN_PROC_PID, pid
	var buf [648]byte                     // one kinfo_proc; first field is the start timeval
	n := uintptr(len(buf))
	_, _, e := syscall.Syscall6(syscall.SYS___SYSCTL, uintptr(unsafe.Pointer(&mib[0])), uintptr(len(mib)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)), 0, 0)
	if e != 0 {
		return 0, e
	}
	if n == 0 {
		return 0, syscall.ESRCH
	}
	tv := *(*syscall.Timeval)(unsafe.Pointer(&buf[0]))
	return int64(tv.Sec)*1_000_000 + int64(tv.Usec), nil
}

func censusMarker() string { return "usec_since_epoch" }

// censusBoot is kern.boottime seconds: machine-constant per boot of this host.
func censusBoot() string {
	mib := [2]int32{1, 21} // CTL_KERN, KERN_BOOTTIME
	var tv syscall.Timeval
	n := uintptr(unsafe.Sizeof(tv))
	_, _, e := syscall.Syscall6(syscall.SYS___SYSCTL, uintptr(unsafe.Pointer(&mib[0])), uintptr(len(mib)),
		uintptr(unsafe.Pointer(&tv)), uintptr(unsafe.Pointer(&n)), 0, 0)
	if e != 0 || n == 0 {
		return ""
	}
	return strconv.FormatInt(int64(tv.Sec), 10)
}
