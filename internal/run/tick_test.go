package run

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTick(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("REIN_RUN_DIR", tmpDir)

	// Create a fake repo structure
	common := filepath.Join(tmpDir, "common")
	if err := os.MkdirAll(common, 0755); err != nil {
		t.Fatalf("mkdir common: %v", err)
	}

	// Run tick
	if err := Tick(tmpDir); err != nil {
		t.Fatalf("Tick failed: %v", err)
	}

	// Verify tick file was created
	tickPath := TickPath(common)
	state, err := LoadTick(tickPath)
	if err != nil {
		t.Fatalf("LoadTick failed: %v", err)
	}

	if state == nil {
		t.Fatal("expected tick state, got nil")
	}

	if state.Status != "ok" {
		t.Errorf("expected status ok, got %s", state.Status)
	}

	if state.PID != os.Getpid() {
		t.Errorf("expected pid %d, got %d", os.Getpid(), state.PID)
	}

	// Verify timestamp is recent
	ts, err := time.Parse(time.RFC3339, state.Timestamp)
	if err != nil {
		t.Fatalf("parse timestamp: %v", err)
	}

	if time.Since(ts) > 5*time.Second {
		t.Errorf("timestamp is too old: %v", ts)
	}
}

func TestIsTickStale(t *testing.T) {
	// Fresh tick
	fresh := &TickState{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		PID:       os.Getpid(),
		Status:    "ok",
	}

	if IsTickStale(fresh) {
		t.Error("fresh tick should not be stale")
	}

	// Stale tick
	stale := &TickState{
		Timestamp: time.Now().UTC().Add(-5 * time.Minute).Format(time.RFC3339),
		PID:       os.Getpid(),
		Status:    "ok",
	}

	if !IsTickStale(stale) {
		t.Error("old tick should be stale")
	}

	// Nil tick
	if !IsTickStale(nil) {
		t.Error("nil tick should be stale")
	}
}

func TestTickSingleFlight(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("REIN_RUN_DIR", tmpDir)

	// Create a fake repo structure
	common := filepath.Join(tmpDir, "common")
	if err := os.MkdirAll(common, 0755); err != nil {
		t.Fatalf("mkdir common: %v", err)
	}

	// Run multiple ticks concurrently
	done := make(chan error, 5)
	for i := 0; i < 5; i++ {
		go func() {
			done <- Tick(tmpDir)
		}()
	}

	// Collect results
	var errors []error
	for i := 0; i < 5; i++ {
		if err := <-done; err != nil {
			errors = append(errors, err)
		}
	}

	// All should succeed (some may skip due to lock)
	if len(errors) > 0 {
		t.Errorf("expected no errors, got %d: %v", len(errors), errors)
	}

	// Verify only one tick file exists and is valid
	tickPath := TickPath(common)
	state, err := LoadTick(tickPath)
	if err != nil {
		t.Fatalf("LoadTick failed: %v", err)
	}

	if state == nil {
		t.Fatal("expected tick state, got nil")
	}
}

// Red check: changing the stale threshold should make tests fail
func TestIsTickStaleRedCheck(t *testing.T) {
	// Create a tick that is 3 minutes old
	old := &TickState{
		Timestamp: time.Now().UTC().Add(-3 * time.Minute).Format(time.RFC3339),
		PID:       os.Getpid(),
		Status:    "ok",
	}

	// With threshold of 2 minutes, this should be stale
	if !IsTickStale(old) {
		t.Error("RED CHECK FAIL: tick older than 2 minutes should be stale")
	}

	// If we change TickStaleThreshold to 5 minutes in the code, this test will fail,
	// proving the red check works
}

func TestTickPath(t *testing.T) {
	// Without redirect
	os.Unsetenv("REIN_RUN_DIR")
	path := TickPath("/some/common")
	expected := "/some/common/rein-tick.json"
	if path != expected {
		t.Errorf("expected %s, got %s", expected, path)
	}

	// With redirect
	t.Setenv("REIN_RUN_DIR", "/tmp/test")
	path = TickPath("/some/common")
	expected = "/tmp/test/rein-tick.json"
	if path != expected {
		t.Errorf("expected %s, got %s", expected, path)
	}
}

func TestLoadTickNonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	tickPath := filepath.Join(tmpDir, "nonexistent.json")

	state, err := LoadTick(tickPath)
	if err != nil {
		t.Fatalf("LoadTick should not error on nonexistent file: %v", err)
	}

	if state != nil {
		t.Error("expected nil state for nonexistent file")
	}
}

func TestLoadTickInvalid(t *testing.T) {
	tmpDir := t.TempDir()
	tickPath := filepath.Join(tmpDir, "invalid.json")

	// Write invalid JSON
	if err := os.WriteFile(tickPath, []byte("{invalid json"), 0644); err != nil {
		t.Fatalf("write invalid json: %v", err)
	}

	_, err := LoadTick(tickPath)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestTickStateRoundtrip(t *testing.T) {
	tmpDir := t.TempDir()
	tickPath := filepath.Join(tmpDir, "tick.json")

	// Create a state
	orig := &TickState{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		PID:       12345,
		Status:    "ok",
		Workers: map[string]Worker{
			"task-1": {
				Task:       "task-1",
				PID:        67890,
				CPUPercent: 45.5,
				LastSeen:   time.Now().UTC().Format(time.RFC3339),
			},
		},
	}

	// Write it
	data, err := json.MarshalIndent(orig, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if err := os.WriteFile(tickPath, data, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Read it back
	loaded, err := LoadTick(tickPath)
	if err != nil {
		t.Fatalf("LoadTick: %v", err)
	}

	if loaded.PID != orig.PID {
		t.Errorf("PID: expected %d, got %d", orig.PID, loaded.PID)
	}

	if loaded.Status != orig.Status {
		t.Errorf("Status: expected %s, got %s", orig.Status, loaded.Status)
	}

	if len(loaded.Workers) != 1 {
		t.Fatalf("expected 1 worker, got %d", len(loaded.Workers))
	}

	w := loaded.Workers["task-1"]
	if w.Task != "task-1" {
		t.Errorf("Task: expected task-1, got %s", w.Task)
	}

	if w.PID != 67890 {
		t.Errorf("Worker PID: expected 67890, got %d", w.PID)
	}

	if w.CPUPercent != 45.5 {
		t.Errorf("CPUPercent: expected 45.5, got %f", w.CPUPercent)
	}
}
