package run

import (
	"errors"
	"io/fs"
	"os"
	"strconv"
	"strings"
)

// ProcStart returns the start time of pid in clock ticks since boot (field 22 of /proc/<pid>/stat). ErrNoProc when
// the process does not exist or is a zombie.
func ProcStart(pid int) (int64, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if errors.Is(err, fs.ErrNotExist) {
		return 0, ErrNoProc
	}
	if err != nil {
		return 0, err
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')') // the command name may hold spaces and parentheses
	if i < 0 {
		return 0, errors.New("unreadable /proc stat")
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 20 {
		return 0, errors.New("short /proc stat")
	}
	if f[0] == "Z" || f[0] == "X" {
		return 0, ErrNoProc
	}
	return strconv.ParseInt(f[19], 10, 64)
}

func bootID() string {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
