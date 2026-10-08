package budget

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/ledger"
	"github.com/voravitl/rein/internal/run"
)

func TestCheckNoBudget(t *testing.T) {
	// Setup test environment
	tmpDir := t.TempDir()
	t.Setenv("REIN_RUN_DIR", tmpDir)
	t.Setenv("PIPELINE_LEDGER", filepath.Join(tmpDir, "ledger.jsonl"))

	// Create a minimal profile with no budget
	prof := &contract.Profile{Name: "test"}

	// Create a marker
	markerPath := filepath.Join(tmpDir, "rein-run.json")
	m := &run.Marker{
		Schema:    1,
		Run:       "test-run",
		StartedAt: time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339),
		Root:      tmpDir,
		SessionID: "test-session",
		PID:       os.Getpid(),
		StartTime: time.Now().Unix(),
	}

	// Write marker manually for this test
	if err := writeTestMarker(markerPath, m); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	// Check should return ExitOK when no budget is defined
	result, err := Check(markerPath, prof, "")
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}

	if result.Code != ExitOK {
		t.Errorf("expected ExitOK, got %d", result.Code)
	}
}

func TestCheckSoftLimit(t *testing.T) {
	// Setup test environment
	tmpDir := t.TempDir()
	t.Setenv("REIN_RUN_DIR", tmpDir)
	ledgerPath := filepath.Join(tmpDir, "ledger.jsonl")
	t.Setenv("PIPELINE_LEDGER", ledgerPath)

	// Create a profile with budget
	prof := &contract.Profile{
		Name: "test",
		Budget: &contract.Budget{
			Pools: map[string]contract.PoolCaps{
				"claude_tokens": {RunCap: 1000},
			},
			SoftRatio: 0.7,
		},
	}

	// Create a marker
	startTime := time.Now().UTC().Add(-1 * time.Hour)
	markerPath := filepath.Join(tmpDir, "rein-run.json")
	m := &run.Marker{
		Schema:    1,
		Run:       "test-run",
		StartedAt: startTime.Format(time.RFC3339),
		Root:      tmpDir,
		SessionID: "test-session",
		PID:       os.Getpid(),
		StartTime: time.Now().Unix(),
	}

	if err := writeTestMarker(markerPath, m); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	// Create ledger with spend at 750 (soft limit is 700)
	tokens := 750
	if err := ledger.Append(ledger.Row{
		Kind:         "task",
		Run:          "test-run",
		Task:         "test-task",
		WorkerTokens: &tokens,
		RecordedAt:   time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("append ledger: %v", err)
	}

	// Check should return ExitSoft
	result, err := Check(markerPath, prof, "")
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}

	if result.Code != ExitSoft {
		t.Errorf("expected ExitSoft, got %d (message: %s)", result.Code, result.Message)
	}
}

func TestCheckHardLimit(t *testing.T) {
	// Setup test environment
	tmpDir := t.TempDir()
	t.Setenv("REIN_RUN_DIR", tmpDir)
	ledgerPath := filepath.Join(tmpDir, "ledger.jsonl")
	t.Setenv("PIPELINE_LEDGER", ledgerPath)

	// Create a profile with budget
	prof := &contract.Profile{
		Name: "test",
		Budget: &contract.Budget{
			Pools: map[string]contract.PoolCaps{
				"claude_tokens": {RunCap: 1000},
			},
		},
	}

	// Create a marker
	startTime := time.Now().UTC().Add(-1 * time.Hour)
	markerPath := filepath.Join(tmpDir, "rein-run.json")
	m := &run.Marker{
		Schema:    1,
		Run:       "test-run",
		StartedAt: startTime.Format(time.RFC3339),
		Root:      tmpDir,
		SessionID: "test-session",
		PID:       os.Getpid(),
		StartTime: time.Now().Unix(),
	}

	if err := writeTestMarker(markerPath, m); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	// Create ledger with spend at 1000 (hard cap)
	tokens := 1000
	if err := ledger.Append(ledger.Row{
		Kind:         "task",
		Run:          "test-run",
		Task:         "test-task",
		WorkerTokens: &tokens,
		RecordedAt:   time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("append ledger: %v", err)
	}

	// Check should return ExitHard
	result, err := Check(markerPath, prof, "")
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}

	if result.Code != ExitHard {
		t.Errorf("expected ExitHard, got %d (message: %s)", result.Code, result.Message)
	}
}

func TestCheckReviewRoundsCap(t *testing.T) {
	// Setup test environment
	tmpDir := t.TempDir()
	t.Setenv("REIN_RUN_DIR", tmpDir)
	ledgerPath := filepath.Join(tmpDir, "ledger.jsonl")
	t.Setenv("PIPELINE_LEDGER", ledgerPath)

	// Create a profile with budget
	prof := &contract.Profile{
		Name: "test",
		Budget: &contract.Budget{
			MaxReviewRounds: 2,
		},
	}

	// Create a marker
	startTime := time.Now().UTC().Add(-1 * time.Hour)
	markerPath := filepath.Join(tmpDir, "rein-run.json")
	m := &run.Marker{
		Schema:    1,
		Run:       "test-run",
		StartedAt: startTime.Format(time.RFC3339),
		Root:      tmpDir,
		SessionID: "test-session",
		PID:       os.Getpid(),
		StartTime: time.Now().Unix(),
	}

	if err := writeTestMarker(markerPath, m); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	// Create ledger with 2 review rounds
	rounds := 2
	if err := ledger.Append(ledger.Row{
		Kind:         "task",
		Run:          "test-run",
		Task:         "test-task",
		ReviewRounds: &rounds,
		RecordedAt:   time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("append ledger: %v", err)
	}

	// Check should return ExitHard when rounds >= max
	result, err := Check(markerPath, prof, "test-task")
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}

	if result.Code != ExitHard {
		t.Errorf("expected ExitHard for review rounds cap, got %d (message: %s)", result.Code, result.Message)
	}
}

func TestRaise(t *testing.T) {
	// Setup test environment
	tmpDir := t.TempDir()
	t.Setenv("REIN_RUN_DIR", tmpDir)

	// Create a marker
	markerPath := filepath.Join(tmpDir, "rein-run.json")
	m := &run.Marker{
		Schema:    1,
		Run:       "test-run",
		StartedAt: time.Now().UTC().Format(time.RFC3339),
		Root:      tmpDir,
		SessionID: "test-session",
		PID:       os.Getpid(),
		StartTime: time.Now().Unix(),
	}

	if err := writeTestMarker(markerPath, m); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	// Raise budget
	if err := Raise(markerPath, "claude_tokens", 500, "user requested"); err != nil {
		t.Fatalf("Raise failed: %v", err)
	}

	// Verify raise was recorded
	raises := loadRaises("test-run", markerPath)
	if len(raises) != 1 {
		t.Fatalf("expected 1 raise, got %d", len(raises))
	}

	if raises[0].Pool != "claude_tokens" {
		t.Errorf("expected pool claude_tokens, got %s", raises[0].Pool)
	}
	if raises[0].Amount != 500 {
		t.Errorf("expected amount 500, got %f", raises[0].Amount)
	}
}

func TestCooldowns(t *testing.T) {
	// Setup test environment
	tmpDir := t.TempDir()
	t.Setenv("REIN_RUN_DIR", tmpDir)

	// Save a cooldown
	c := Cooldown{
		Provider: "claude",
		Status:   StatusQuota,
		LastSeen: time.Now().UTC().Format(time.RFC3339),
		Message:  "quota exceeded",
	}

	if err := SaveCooldown("test-run", c); err != nil {
		t.Fatalf("SaveCooldown failed: %v", err)
	}

	// Load cooldowns
	cooldowns, err := LoadCooldowns("test-run")
	if err != nil {
		t.Fatalf("LoadCooldowns failed: %v", err)
	}

	if len(cooldowns) != 1 {
		t.Fatalf("expected 1 cooldown, got %d", len(cooldowns))
	}

	if cooldowns[0].Provider != "claude" {
		t.Errorf("expected provider claude, got %s", cooldowns[0].Provider)
	}
	if cooldowns[0].Status != StatusQuota {
		t.Errorf("expected status quota, got %s", cooldowns[0].Status)
	}
}

// Red check: breaking the soft ratio should make tests fail
func TestCheckRedSoftRatio(t *testing.T) {
	// Setup test environment
	tmpDir := t.TempDir()
	t.Setenv("REIN_RUN_DIR", tmpDir)
	ledgerPath := filepath.Join(tmpDir, "ledger.jsonl")
	t.Setenv("PIPELINE_LEDGER", ledgerPath)

	// Create a profile with budget
	prof := &contract.Profile{
		Name: "test",
		Budget: &contract.Budget{
			Pools: map[string]contract.PoolCaps{
				"claude_tokens": {RunCap: 1000},
			},
			SoftRatio: 0.5, // Changed from 0.7 to 0.5 - this should make the test fail if expectations are not updated
		},
	}

	// Create a marker
	startTime := time.Now().UTC().Add(-1 * time.Hour)
	markerPath := filepath.Join(tmpDir, "rein-run.json")
	m := &run.Marker{
		Schema:    1,
		Run:       "test-run",
		StartedAt: startTime.Format(time.RFC3339),
		Root:      tmpDir,
		SessionID: "test-session",
		PID:       os.Getpid(),
		StartTime: time.Now().Unix(),
	}

	if err := writeTestMarker(markerPath, m); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	// Create ledger with spend at 600 (would be OK with ratio 0.7, but soft with ratio 0.5)
	tokens := 600
	if err := ledger.Append(ledger.Row{
		Kind:         "task",
		Run:          "test-run",
		Task:         "test-task",
		WorkerTokens: &tokens,
		RecordedAt:   time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("append ledger: %v", err)
	}

	// Check should return ExitSoft with ratio 0.5
	result, err := Check(markerPath, prof, "")
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}

	if result.Code != ExitSoft {
		t.Errorf("RED CHECK FAIL: with SoftRatio 0.5 and spend 600, expected ExitSoft, got %d", result.Code)
	}
}

// Helper to write marker for tests (simplified version of internal/run writeMarker)
func writeTestMarker(path string, m *run.Marker) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
