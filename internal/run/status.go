// Task status ladder: HEALTHY, BUSY, SLOW, STUCK, THRASHING, DEAD, UNKNOWN (ADR 0002 B1).
package run

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// TaskStatus represents the health state of a worker.
type TaskStatus string

const (
	StatusHealthy   TaskStatus = "HEALTHY"
	StatusBusy      TaskStatus = "BUSY"
	StatusSlow      TaskStatus = "SLOW"
	StatusStuck     TaskStatus = "STUCK"
	StatusThrashing TaskStatus = "THRASHING"
	StatusDead      TaskStatus = "DEAD"
	StatusUnknown   TaskStatus = "UNKNOWN"
)

// TaskStatusResult holds the task status assessment.
type TaskStatusResult struct {
	Status       TaskStatus
	Message      string
	Task         string
	LastSeenAt   *time.Time
	LastCommitAt *time.Time
	CPUPercent   float64
	Denials      int
}

// GetTaskStatus determines the status of a task.
func GetTaskStatus(common, task string) (*TaskStatusResult, error) {
	result := &TaskStatusResult{
		Task:   task,
		Status: StatusUnknown,
	}

	// Load tick to get worker info
	tickPath := TickPath(common)
	tick, err := LoadTick(tickPath)
	if err != nil {
		return nil, fmt.Errorf("load tick: %w", err)
	}

	if tick == nil {
		result.Message = "no tick data available"
		return result, nil
	}

	// Check if worker exists in tick
	worker, ok := tick.Workers[task]
	if !ok {
		// Worker not in tick - check if task exists
		contractPath := filepath.Join(common, "contracts", task+".json")
		if _, err := os.Stat(contractPath); os.IsNotExist(err) {
			result.Message = "task not found"
			return result, nil
		}

		// Task exists but not in tick - could be DEAD or just not started
		result.Status = StatusDead
		result.Message = "worker not found in tick"
		return result, nil
	}

	result.CPUPercent = worker.CPUPercent

	// Parse last seen
	if worker.LastSeen != "" {
		if ts, err := time.Parse(time.RFC3339, worker.LastSeen); err == nil {
			result.LastSeenAt = &ts
		}
	}

	// Check seen log for recent activity
	seenPath := filepath.Join(common, "seen", task+".log")
	seenAge, inFlight := checkSeenLog(seenPath)

	// Determine status based on signals
	now := time.Now()

	// BUSY: seen log shows in-flight tool call
	if inFlight {
		result.Status = StatusBusy
		result.Message = "tool call in progress"
		return result, nil
	}

	// HEALTHY: recent seen activity and normal CPU
	if result.LastSeenAt != nil && now.Sub(*result.LastSeenAt) < 5*time.Minute {
		if result.CPUPercent > 0.5 && result.CPUPercent < 95 {
			result.Status = StatusHealthy
			result.Message = "recent activity, normal CPU"
			return result, nil
		}
	}

	// SLOW: has activity but slow progress
	if result.LastSeenAt != nil && now.Sub(*result.LastSeenAt) < 10*time.Minute {
		result.Status = StatusSlow
		result.Message = "slow progress"
		return result, nil
	}

	// STUCK: no recent activity and flat CPU
	if seenAge > 15*time.Minute && result.CPUPercent < 1.0 {
		result.Status = StatusStuck
		result.Message = "no progress, flat CPU"
		return result, nil
	}

	// THRASHING: high denial rate (check guard log)
	denials := countDenials(common, task, 10*time.Minute)
	result.Denials = denials
	if denials > 5 {
		result.Status = StatusThrashing
		result.Message = fmt.Sprintf("high denial rate (%d denials)", denials)
		return result, nil
	}

	// Default to UNKNOWN when we can't determine state
	result.Status = StatusUnknown
	result.Message = "insufficient data to determine status"
	return result, nil
}

// checkSeenLog checks the seen log for recent activity and in-flight status.
// Returns age of last event and whether a tool call is in-flight.
func checkSeenLog(path string) (time.Duration, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 24 * time.Hour, false // Assume old if can't read
	}

	lines := splitLines(string(data))
	if len(lines) == 0 {
		return 24 * time.Hour, false
	}

	// Parse last line for timestamp and event type
	lastLine := lines[len(lines)-1]
	parts := splitFields(lastLine)
	if len(parts) < 3 {
		return 24 * time.Hour, false
	}

	// parts[0] is timestamp, parts[2] is event type
	ts, err := time.Parse(time.RFC3339, parts[0])
	if err != nil {
		return 24 * time.Hour, false
	}

	age := time.Since(ts)
	inFlight := parts[2] == "PreToolUse" // PostToolUse clears in-flight

	return age, inFlight
}

// countDenials counts guard denials in the last duration.
func countDenials(common, task string, since time.Duration) int {
	// Guard log path
	logDir := os.Getenv("PIPELINE_LOGDIR")
	if logDir == "" {
		home, _ := os.UserHomeDir()
		logDir = filepath.Join(home, ".cache", "worktree-pipeline", "logs")
	}

	guardLogPath := filepath.Join(logDir, "guard.log")
	data, err := os.ReadFile(guardLogPath)
	if err != nil {
		return 0
	}

	// Count denials for this task in the time window
	count := 0
	cutoff := time.Now().Add(-since)
	lines := splitLines(string(data))

	for _, line := range lines {
		if !containsSubstring(line, task) || !containsSubstring(line, "DENY") {
			continue
		}

		// Try to parse timestamp from line (format varies, so this is best-effort)
		parts := splitFields(line)
		if len(parts) > 0 {
			if ts, err := time.Parse(time.RFC3339, parts[0]); err == nil {
				if ts.After(cutoff) {
					count++
				}
			}
		}
	}

	return count
}

// Helper functions
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func splitFields(s string) []string {
	var fields []string
	start := 0
	inField := false
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\t' {
			if inField {
				fields = append(fields, s[start:i])
				inField = false
			}
		} else {
			if !inField {
				start = i
				inField = true
			}
		}
	}
	if inField {
		fields = append(fields, s[start:])
	}
	return fields
}

func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && indexSubstring(s, substr) >= 0
}

func indexSubstring(s, substr string) int {
	if len(substr) == 0 {
		return 0
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
