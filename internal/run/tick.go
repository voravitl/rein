// Tick is the single-flight periodic health check (ADR 0002 B1). `rein run tick` writes rein-tick.json under flock,
// recording timestamp, pid, status, and sampled worker CPU. The coordinator hook denies new spawns when the tick is
// stale (> 2 minutes).
package run

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// TickFile is the tick state file name under the git common dir (or under $REIN_RUN_DIR).
const TickFile = "rein-tick.json"

// TickStaleThreshold is how old a tick can be before the hook denies new spawns.
const TickStaleThreshold = 2 * time.Minute

// TickState records the last tick's time and health.
type TickState struct {
	Timestamp string            `json:"timestamp"` // RFC3339
	PID       int               `json:"pid"`
	Status    string            `json:"status"` // "ok", "stale"
	Workers   map[string]Worker `json:"workers,omitempty"`
}

// Worker holds sampled CPU and other metrics for a task.
type Worker struct {
	Task       string  `json:"task"`
	PID        int     `json:"pid,omitempty"`
	CPUPercent float64 `json:"cpu_percent,omitempty"`
	LastSeen   string  `json:"last_seen,omitempty"` // RFC3339
}

// Tick runs a single-flight tick under flock, writing the tick state.
func Tick(repo string) error {
	var tickPath, common string

	if redirected() {
		// In test mode, use redirected path directly
		common = repo
		tickPath = TickPath(common)
	} else {
		// In production, locate the git repo
		loc, err := Locate(repo)
		if err != nil {
			return fmt.Errorf("locate repo: %w", err)
		}
		common = loc.Common
		tickPath = TickPath(common)
	}

	lockPath := tickPath + ".lock"

	// Acquire exclusive lock
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("open lock file: %w", err)
	}
	defer lockFile.Close()

	// Try to acquire lock (non-blocking)
	locked, err := lockFileExclusive(lockFile)
	if err != nil {
		return fmt.Errorf("flock: %w", err)
	}
	if !locked {
		// Another tick is running
		return nil
	}
	defer unlockFile(lockFile)

	// Sample workers (placeholder for now - will be enhanced)
	workers := sampleWorkers(common)

	// Write tick state
	state := TickState{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		PID:       os.Getpid(),
		Status:    "ok",
		Workers:   workers,
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal tick state: %w", err)
	}

	tmpPath := tickPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("write tick state: %w", err)
	}

	if err := os.Rename(tmpPath, tickPath); err != nil {
		return fmt.Errorf("rename tick state: %w", err)
	}

	return nil
}

// LoadTick reads the tick state.
func LoadTick(path string) (*TickState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read tick state: %w", err)
	}

	var state TickState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("unmarshal tick state: %w", err)
	}

	return &state, nil
}

// TickPath returns the path to the tick state file.
func TickPath(common string) string {
	if d := os.Getenv("REIN_RUN_DIR"); d != "" {
		return filepath.Join(d, TickFile)
	}
	return filepath.Join(common, TickFile)
}

// IsTickStale checks if the tick is older than TickStaleThreshold.
func IsTickStale(state *TickState) bool {
	if state == nil {
		return true
	}

	ts, err := time.Parse(time.RFC3339, state.Timestamp)
	if err != nil {
		return true
	}

	return time.Since(ts) > TickStaleThreshold
}

// sampleWorkers samples worker CPU and status (placeholder - will be enhanced with real sampling).
func sampleWorkers(common string) map[string]Worker {
	// Placeholder: in a real implementation, this would:
	// 1. Read contract index to find active tasks
	// 2. Sample CPU for each worker process
	// 3. Check seen logs for recent activity
	workers := make(map[string]Worker)
	return workers
}
