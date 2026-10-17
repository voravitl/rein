//go:build linux

package orca

import (
	"errors"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// censusScan reads every /proc/<pid>/stat into one pinned table. State is the first state letter,
// PPID is field 4, Start is the start-time clock ticks since boot (field 22). A comm name may hold
// spaces and parentheses, so fields are taken after the last ')'.
func censusScan() ([]censusRow, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	rows := []censusRow{}
	for _, e := range entries {
		if !e.IsDir() || e.Name()[0] < '1' { // skip non-numeric entries; all pids start with a digit
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if errors.Is(err, fs.ErrNotExist) { // process vanished mid-scan: not in this sample
			continue
		}
		if errors.Is(err, syscall.ESRCH) { // dying pid reveals itself as ESRCH here, not ENOENT
			continue
		}
		if err != nil {
			return nil, err
		}
		row, err := parseProcStat(string(b))
		if err != nil {
			return nil, err
		}
		row.PID = pid // parseProcStat has no name context; unify at the scan site
		rows = append(rows, row)
	}
	return rows, nil
}

func parseProcStat(s string) (censusRow, error) {
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return censusRow{}, errors.New("unreadable /proc stat")
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 20 {
		return censusRow{}, errors.New("short /proc stat")
	}
	ppid, err := strconv.Atoi(f[1])
	if err != nil {
		return censusRow{}, err
	}
	start, err := strconv.ParseInt(f[19], 10, 64)
	if err != nil {
		return censusRow{}, err
	}
	return censusRow{State: f[0][:1], PPID: ppid, Start: start}, nil
}

func censusMarker() string { return "ticks_since_boot" }

// censusBoot is the machine-constant boot id: two hosts or two boots never share one.
func censusBoot() string {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
