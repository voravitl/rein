package run

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/voravitl/rein/internal/contract"
)

// This is a read-only view of routing's lease. Observation never releases a
// lease, kills a process, or starts a replacement worker.
type workerLeaseObservation struct {
	Task            string `json:"task"`
	PID             int    `json:"pid"`
	PIDStart        int64  `json:"pid_start"`
	Child           int    `json:"child"`
	ChildStart      int64  `json:"child_start"`
	ChildGroup      int    `json:"child_group"`
	Dispatched      bool   `json:"dispatched"`
	DispatchPending bool   `json:"dispatch_pending"`
	OrcaTask        string `json:"orca_task"`
	Dispatch        string `json:"dispatch"`
}

func sampleWorkers(common string) map[string]Worker {
	workers := make(map[string]Worker)
	paths, _ := filepath.Glob(filepath.Join(contract.IndexDir(), "*.json"))
	for _, path := range paths {
		c, err := contract.LoadFile(path)
		if err != nil || !contract.ValidName(c.Name) {
			continue
		}
		loc, err := Locate(c.Worktree)
		if err != nil || real(loc.Common) != real(common) {
			continue
		}
		w := Worker{Task: c.Name, Observation: "unknown"}
		workers[c.Name] = w
		b, err := os.ReadFile(filepath.Join(contract.IndexDir(), "routes", "leases", c.Name+".json"))
		var lease workerLeaseObservation
		if err != nil || json.Unmarshal(b, &lease) != nil || lease.Task != c.Name || lease.Dispatched || lease.DispatchPending || lease.OrcaTask != "" || lease.Dispatch != "" {
			continue
		}
		// A launcher alone does not establish a worker's identity. A process group
		// may outlive its leader, so leader disappearance alone is never DEAD.
		if lease.Child <= 0 || lease.ChildStart == 0 {
			continue
		}
		start, err := ProcStart(lease.Child)
		if errors.Is(err, ErrNoProc) || (err == nil && start != lease.ChildStart) {
			launcherStart, launcherErr := ProcStart(lease.PID)
			launcherGone := errors.Is(launcherErr, ErrNoProc) || (launcherErr == nil && lease.PIDStart != 0 && launcherStart != lease.PIDStart)
			if launcherGone && lease.ChildGroup == 0 {
				w.Observation = "gone"
				workers[c.Name] = w
			}
			continue
		}
		if err != nil {
			continue
		}
		w.PID = lease.Child
		// ps is bounded and used only by tick, never on the synchronous hook path.
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		out, err := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(w.PID), "-o", "%cpu=").Output()
		cancel()
		if err != nil {
			continue
		}
		cpu, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
		if err != nil {
			continue
		}
		// Recheck identity after sampling to reject PID reuse during ps.
		if after, err := ProcStart(w.PID); err != nil || after != lease.ChildStart {
			continue
		}
		w.Observation, w.CPUPercent = "alive", cpu
		age, _ := checkSeenLogGeneration(contract.SeenPath(c.Name), c.HooksGeneration)
		if age < 24*time.Hour {
			w.LastSeen = time.Now().Add(-age).UTC().Format(time.RFC3339)
		}
		workers[c.Name] = w
	}
	return workers
}
