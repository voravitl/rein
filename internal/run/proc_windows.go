package run

import "syscall"

// ProcStart returns the creation time of pid (FILETIME). Unverified on a real Windows run (ADR 0001 rule 2).
func ProcStart(pid int) (int64, error) {
	const queryLimited = 0x1000 // PROCESS_QUERY_LIMITED_INFORMATION
	h, err := syscall.OpenProcess(queryLimited, false, uint32(pid))
	if err != nil {
		if err == syscall.Errno(87) { // ERROR_INVALID_PARAMETER: no such process
			return 0, ErrNoProc
		}
		return 0, err
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err == nil && code != 259 { // STILL_ACTIVE
		return 0, ErrNoProc
	}
	var created, exited, kernel, user syscall.Filetime
	if err := syscall.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return 0, err
	}
	return int64(created.HighDateTime)<<32 | int64(created.LowDateTime), nil
}

func bootID() string { return "" }
