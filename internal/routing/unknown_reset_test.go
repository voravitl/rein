package routing

// Unknown-reset discipline (Phase C slice C8, test-only — closes R10).
//
// The contract is: a cooldown with Until=nil stays blocked forever, and only
// an explicit `SetCooldown(..., clear=true)` with a reason unblocks it
// (docs/CLI_REFERENCE.md §"route status": "Quota without known reset stays
// blocked until explicit clear"; SetCooldown itself documents
// "nil means blocked until an explicit clear").
//
// This test pins the four behaviors that make that real:
//  1. an unknown-reset cooldown blocks its pool and stays blocked across
//     Status reads (no silent expiry);
//  2. an unknown reset is distinguishable from a known one (Until == nil
//     survives a write/read round-trip);
//  3. a KNOWN reset in the past unblocks (the one case time may clear);
//  4. only an explicit clear with a reason unblocks an unknown reset —
//     never a re-prepare, never a missing file, never a guess.
//
// Plus the two fail-closed edges of the API itself:
//  5. clearing or cooling without a reason is refused;
//  6. Prepare refuses while the pool is blocked and succeeds after the
//     explicit clear (the refusal/acceptance the supervisor relies on).
//
// Test-only. No production path changes.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/providers"
)

// resetChain writes a one-provider fallback chain (provider "q" on the claude
// pool) and returns its config path.
func resetChain(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(d, "contracts"))
	t.Setenv("PIPELINE_LEDGER", filepath.Join(d, "ledger"))
	if err := os.MkdirAll(contract.IndexDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := providers.Config{Providers: map[string]providers.Provider{
		"q": {Agent: "claude", Model: "claude-sonnet", Launch: "shell", Probe: []string{"true"}},
	}, WorkerChains: map[string][]string{"backend": {"q"}}}
	p := filepath.Join(d, "config.json")
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(d, "worktree"), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func resetContract(t *testing.T, d string) *contract.Contract {
	t.Helper()
	return &contract.Contract{Name: "task", Worktree: filepath.Join(d, "worktree"),
		Allow: []string{"**"}, Scope: []string{"S1"},
		ReportPath:      filepath.Join(d, "report"),
		HooksGeneration: "g1", HooksInstalled: []string{"claude"}}
}

func TestUnknownResetNeverClearsItself(t *testing.T) {
	p := resetChain(t)
	c := resetContract(t, filepath.Dir(p))

	// 1. Unknown reset blocks and stays blocked across reads.
	if err := SetCooldown(p, "q", "observed quota", nil, false); err != nil {
		t.Fatal(err)
	}
	s, err := Status()
	if err != nil {
		t.Fatal(err)
	}
	cd, ok := s["claude"]
	if !ok || !blocked(s, "claude") {
		t.Fatalf("unknown-reset pool not blocked: %+v", s)
	}
	// 2. The unknown-ness survives the write/read round-trip (Until == nil,
	// not a zero time that could read as "expired").
	if cd.Until != nil {
		t.Fatalf("unknown reset gained a time: %+v", cd.Until)
	}
	if s2, err := Status(); err != nil || !blocked(s2, "claude") {
		t.Fatalf("unknown reset expired on re-read: %+v %v", s2, err)
	}

	// 3. A known reset in the past unblocks — the only case time may clear.
	past := time.Now().Add(-time.Second).UTC()
	if err := SetCooldown(p, "q", "known reset at "+past.Format(time.RFC3339), &past, false); err != nil {
		t.Fatal(err)
	}
	if s, err := Status(); err != nil || blocked(s, "claude") {
		t.Fatalf("elapsed known reset still blocks: %+v %v", s, err)
	}
	// And a known reset in the future still blocks.
	future := time.Now().Add(time.Hour).UTC()
	if err := SetCooldown(p, "q", "known reset at "+future.Format(time.RFC3339), &future, false); err != nil {
		t.Fatal(err)
	}
	if s, err := Status(); err != nil || !blocked(s, "claude") {
		t.Fatalf("future known reset does not block: %+v %v", s, err)
	}

	// 4. Back to unknown; only an explicit clear with a reason unblocks.
	if err := SetCooldown(p, "q", "observed quota again", nil, false); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(c, "run", "worker:backend", "", p, false, 10*time.Second); err == nil || !strings.Contains(err.Error(), "no eligible provider") {
		t.Fatalf("Prepare = %v, want refusal while the pool is blocked", err)
	}
	if err := SetCooldown(p, "q", "verified available", nil, true); err != nil {
		t.Fatal(err)
	}
	if s, err := Status(); err != nil || blocked(s, "claude") {
		t.Fatalf("explicit clear did not unblock: %+v %v", s, err)
	}

	// 5. Cooling or clearing without a reason is refused.
	if err := SetCooldown(p, "q", "  ", nil, false); err == nil {
		t.Error("cooldown without a reason accepted")
	}
	if err := SetCooldown(p, "q", "", nil, true); err == nil {
		t.Error("clear without a reason accepted")
	}
	if s, err := Status(); err != nil || blocked(s, "claude") {
		t.Fatalf("refused clear changed state: %+v %v", s, err)
	}
}
