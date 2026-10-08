package run

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGetTaskStatusHealthy(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("REIN_RUN_DIR", tmpDir)
	t.Setenv("PIPELINE_CONTRACTS", tmpDir)

	// Create tick with healthy worker
	tick := &TickState{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		PID:       os.Getpid(),
		Status:    "ok",
		Workers: map[string]Worker{
			"test-task": {
				Task:       "test-task",
				PID:        12345,
				CPUPercent: 50.0,
				LastSeen:   time.Now().UTC().Format(time.RFC3339),
			},
		},
	}

	tickPath := TickPath(tmpDir)
	data, _ := json.MarshalIndent(tick, "", "  ")
	os.WriteFile(tickPath, data, 0644)

	// Create seen log with recent activity
	seenDir := filepath.Join(tmpDir, "seen")
	os.MkdirAll(seenDir, 0755)
	seenPath := filepath.Join(seenDir, "test-task.log")
	seenContent := time.Now().UTC().Format(time.RFC3339) + " codex PostToolUse Bash\n"
	os.WriteFile(seenPath, []byte(seenContent), 0644)

	result, err := GetTaskStatus(tmpDir, "test-task")
	if err != nil {
		t.Fatalf("GetTaskStatus failed: %v", err)
	}

	if result.Status != StatusHealthy {
		t.Errorf("expected HEALTHY, got %s (message: %s)", result.Status, result.Message)
	}
}

func TestGetTaskStatusBusy(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("REIN_RUN_DIR", tmpDir)
	t.Setenv("PIPELINE_CONTRACTS", tmpDir)

	// Create tick
	tick := &TickState{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		PID:       os.Getpid(),
		Status:    "ok",
		Workers: map[string]Worker{
			"test-task": {
				Task:       "test-task",
				PID:        12345,
				CPUPercent: 75.0,
				LastSeen:   time.Now().UTC().Format(time.RFC3339),
			},
		},
	}

	tickPath := TickPath(tmpDir)
	data, _ := json.MarshalIndent(tick, "", "  ")
	os.WriteFile(tickPath, data, 0644)

	// Create seen log with in-flight PreToolUse
	seenDir := filepath.Join(tmpDir, "seen")
	os.MkdirAll(seenDir, 0755)
	seenPath := filepath.Join(seenDir, "test-task.log")
	seenContent := time.Now().UTC().Format(time.RFC3339) + " codex PreToolUse Bash\n"
	os.WriteFile(seenPath, []byte(seenContent), 0644)

	result, err := GetTaskStatus(tmpDir, "test-task")
	if err != nil {
		t.Fatalf("GetTaskStatus failed: %v", err)
	}

	if result.Status != StatusBusy {
		t.Errorf("expected BUSY, got %s (message: %s)", result.Status, result.Message)
	}
}

func TestGetTaskStatusStuck(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("REIN_RUN_DIR", tmpDir)
	t.Setenv("PIPELINE_CONTRACTS", tmpDir)

	// Create tick
	tick := &TickState{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		PID:       os.Getpid(),
		Status:    "ok",
		Workers: map[string]Worker{
			"test-task": {
				Task:       "test-task",
				PID:        12345,
				CPUPercent: 0.1, // Flat CPU
				LastSeen:   time.Now().UTC().Add(-20 * time.Minute).Format(time.RFC3339),
			},
		},
	}

	tickPath := TickPath(tmpDir)
	data, _ := json.MarshalIndent(tick, "", "  ")
	os.WriteFile(tickPath, data, 0644)

	// Create seen log with old activity
	seenDir := filepath.Join(tmpDir, "seen")
	os.MkdirAll(seenDir, 0755)
	seenPath := filepath.Join(seenDir, "test-task.log")
	seenContent := time.Now().UTC().Add(-20*time.Minute).Format(time.RFC3339) + " codex PostToolUse Bash\n"
	os.WriteFile(seenPath, []byte(seenContent), 0644)

	result, err := GetTaskStatus(tmpDir, "test-task")
	if err != nil {
		t.Fatalf("GetTaskStatus failed: %v", err)
	}

	if result.Status != StatusStuck {
		t.Errorf("expected STUCK, got %s (message: %s)", result.Status, result.Message)
	}
}

func TestGetTaskStatusDead(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("REIN_RUN_DIR", tmpDir)
	t.Setenv("PIPELINE_CONTRACTS", tmpDir)

	// Create tick without the worker
	tick := &TickState{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		PID:       os.Getpid(),
		Status:    "ok",
		Workers:   map[string]Worker{},
	}

	tickPath := TickPath(tmpDir)
	data, _ := json.MarshalIndent(tick, "", "  ")
	os.WriteFile(tickPath, data, 0644)

	// Create contract to show task exists (use contract.PathOf location)
	contractPath := filepath.Join(tmpDir, "test-task.json")
	os.WriteFile(contractPath, []byte("{}"), 0644)

	result, err := GetTaskStatus(tmpDir, "test-task")
	if err != nil {
		t.Fatalf("GetTaskStatus failed: %v", err)
	}

	if result.Status != StatusDead {
		t.Errorf("expected DEAD, got %s (message: %s)", result.Status, result.Message)
	}
}

func TestGetTaskStatusSlow(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("REIN_RUN_DIR", tmpDir)
	t.Setenv("PIPELINE_CONTRACTS", tmpDir)

	// Create tick
	tick := &TickState{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		PID:       os.Getpid(),
		Status:    "ok",
		Workers: map[string]Worker{
			"test-task": {
				Task:       "test-task",
				PID:        12345,
				CPUPercent: 30.0,
				LastSeen:   time.Now().UTC().Add(-8 * time.Minute).Format(time.RFC3339),
			},
		},
	}

	tickPath := TickPath(tmpDir)
	data, _ := json.MarshalIndent(tick, "", "  ")
	os.WriteFile(tickPath, data, 0644)

	// Create seen log with somewhat recent activity
	seenDir := filepath.Join(tmpDir, "seen")
	os.MkdirAll(seenDir, 0755)
	seenPath := filepath.Join(seenDir, "test-task.log")
	seenContent := time.Now().UTC().Add(-8*time.Minute).Format(time.RFC3339) + " codex PostToolUse Bash\n"
	os.WriteFile(seenPath, []byte(seenContent), 0644)

	result, err := GetTaskStatus(tmpDir, "test-task")
	if err != nil {
		t.Fatalf("GetTaskStatus failed: %v", err)
	}

	if result.Status != StatusSlow {
		t.Errorf("expected SLOW, got %s (message: %s)", result.Status, result.Message)
	}
}

func TestGetTaskStatusUnknown(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("REIN_RUN_DIR", tmpDir)
	t.Setenv("PIPELINE_CONTRACTS", tmpDir)

	// No tick file
	result, err := GetTaskStatus(tmpDir, "test-task")
	if err != nil {
		t.Fatalf("GetTaskStatus failed: %v", err)
	}

	if result.Status != StatusUnknown {
		t.Errorf("expected UNKNOWN when no tick, got %s", result.Status)
	}
}

// Red check: changing the stuck threshold should make tests fail
func TestGetTaskStatusStuckRedCheck(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("REIN_RUN_DIR", tmpDir)
	t.Setenv("PIPELINE_CONTRACTS", tmpDir)

	// Create tick with worker that has been inactive for exactly 15 minutes (boundary)
	tick := &TickState{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		PID:       os.Getpid(),
		Status:    "ok",
		Workers: map[string]Worker{
			"test-task": {
				Task:       "test-task",
				PID:        12345,
				CPUPercent: 0.1,
				LastSeen:   time.Now().UTC().Add(-15 * time.Minute).Format(time.RFC3339),
			},
		},
	}

	tickPath := TickPath(tmpDir)
	data, _ := json.MarshalIndent(tick, "", "  ")
	os.WriteFile(tickPath, data, 0644)

	// Create seen log with old activity (16 minutes to be safe)
	seenDir := filepath.Join(tmpDir, "seen")
	os.MkdirAll(seenDir, 0755)
	seenPath := filepath.Join(seenDir, "test-task.log")
	seenContent := time.Now().UTC().Add(-16*time.Minute).Format(time.RFC3339) + " codex PostToolUse Bash\n"
	os.WriteFile(seenPath, []byte(seenContent), 0644)

	result, err := GetTaskStatus(tmpDir, "test-task")
	if err != nil {
		t.Fatalf("GetTaskStatus failed: %v", err)
	}

	// With threshold of 15 minutes and age > 15 minutes, should be STUCK
	if result.Status != StatusStuck {
		t.Errorf("RED CHECK FAIL: with age > 15 minutes and flat CPU, expected STUCK, got %s", result.Status)
	}
}

func TestCheckSeenLog(t *testing.T) {
	tmpDir := t.TempDir()

	// Test in-flight
	seenPath := filepath.Join(tmpDir, "seen.log")
	content := time.Now().UTC().Format(time.RFC3339) + " codex PreToolUse Bash\n"
	os.WriteFile(seenPath, []byte(content), 0644)

	age, inFlight := checkSeenLog(seenPath)
	if !inFlight {
		t.Error("expected in-flight for PreToolUse")
	}
	if age > 5*time.Second {
		t.Errorf("expected recent age, got %v", age)
	}

	// Test not in-flight
	content = time.Now().UTC().Format(time.RFC3339) + " codex PostToolUse Bash\n"
	os.WriteFile(seenPath, []byte(content), 0644)

	age, inFlight = checkSeenLog(seenPath)
	if inFlight {
		t.Error("expected not in-flight for PostToolUse")
	}
}

func TestHelperFunctions(t *testing.T) {
	// Test splitLines
	lines := splitLines("a\nb\nc")
	if len(lines) != 3 {
		t.Errorf("expected 3 lines, got %d", len(lines))
	}

	// Test splitFields
	fields := splitFields("a b c")
	if len(fields) != 3 {
		t.Errorf("expected 3 fields, got %d", len(fields))
	}

	// Test containsSubstring
	if !containsSubstring("hello world", "world") {
		t.Error("expected to find 'world' in 'hello world'")
	}

	if containsSubstring("hello", "world") {
		t.Error("expected not to find 'world' in 'hello'")
	}
}
