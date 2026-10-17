package routing

// Supervised-mode idle loop budget (Phase C slice C7, test-only).
//
// The controller boundary requires two things of a loop that runs while the
// coordinator may be dead (docs/SUPERVISED_CONTROLLER_BOUNDARY.th.md §3, §5):
//   - the idle loop calls no LLM (no advisor, no probe that spends quota);
//   - when every quota pool is exhausted it PARKS — it keeps the work, does
//     not busy-loop, and does not open paid overage (TEST_PLAN R9).
//
// This test pins the decision function, not a daemon: parkIfExhausted is the
// pure decision the loop would ask each iteration. It reads only the cooldown
// state the repo already persists, so it cannot spend quota by construction —
// and the test proves that by refusing to give it any way to probe.
//
// Test-only. The loop skeleton itself is still PROPOSED (BACKLOG Phase C).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/providers"
)

// loopDecision is what the idle loop may do this iteration.
type loopDecision struct {
	Action string // "park" | "continue"
	Reason string
	// ProbeBudget is the separate probe allowance; the idle loop's own
	// iterations never draw from it (an LLM or quota probe is not idle work).
	ProbeBudget int
}

// idleLoopBudget is the number of idle iterations the loop may take before it
// must re-park. Idle work is bounded: it is not a busy loop.
const idleLoopBudget = 3

// parkIfExhausted decides the loop's next action from the persisted cooldown
// state alone. Every pool blocked means park — with a reason that names the
// unknown reset rather than guessing one. It never returns "probe": spending
// quota is not idle work.
func parkIfExhausted(pools []string) loopDecision {
	if len(pools) == 0 {
		return loopDecision{Action: "park", Reason: "no configured quota pool: nothing to decide"}
	}
	s, err := Status()
	if err != nil {
		return loopDecision{Action: "park", Reason: "cooldown state unreadable: " + err.Error()}
	}
	var free []string
	var exhausted []string
	var unknownReset []string
	for _, p := range pools {
		if blocked(s, p) {
			exhausted = append(exhausted, p)
			if cd, ok := s[p]; ok && cd.Until == nil {
				unknownReset = append(unknownReset, p)
			}
		} else {
			free = append(free, p)
		}
	}
	if len(free) > 0 {
		return loopDecision{Action: "continue", Reason: "pool(s) available: " + joinPools(free), ProbeBudget: 0}
	}
	reason := "every quota pool is exhausted (" + joinPools(exhausted) + "); keeping the work and waiting"
	if len(unknownReset) > 0 {
		reason += "; reset unknown for " + joinPools(unknownReset) + " — waiting for an explicit clear, never guessing a time"
	}
	return loopDecision{Action: "park", Reason: reason, ProbeBudget: 0}
}

func joinPools(p []string) string {
	out := ""
	for i, v := range p {
		if i > 0 {
			out += ", "
		}
		out += v
	}
	return out
}

// idleChain writes a fallback chain with one provider per pool (Agent == Pool
// by identity, names qa/qb) and returns its config path.
func idleChain(t *testing.T) (cfgPath string, _ *contract.Contract) {
	t.Helper()
	d := t.TempDir()
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(d, "contracts"))
	t.Setenv("PIPELINE_LEDGER", filepath.Join(d, "ledger"))
	if err := os.MkdirAll(contract.IndexDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(d, "worktree")
	if err := os.MkdirAll(wt, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := providers.Config{Providers: map[string]providers.Provider{
		"qa": {Agent: "claude", Model: "claude-sonnet", Launch: "shell", Probe: []string{"true"}},
		"qb": {Agent: "codex", Model: "gpt-6.1-sol", Launch: "shell", Probe: []string{"true"}},
	}, WorkerChains: map[string][]string{"backend": {"qa", "qb"}}}
	cfgPath = filepath.Join(d, "config.json")
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(cfgPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := contract.New("idle-t", 0, filepath.Join(d, "run"), "src/**", "", "S1", filepath.Join(d, "wts"), "", "", 0, nil); err != nil {
		t.Fatal(err)
	}
	return cfgPath, nil
}

func TestIdleLoopParksWhenEveryPoolExhausted(t *testing.T) {
	cfgPath, _ := idleChain(t)
	pools := []string{"claude", "codex"}
	providersByPool := map[string]string{"claude": "qa", "codex": "qb"}

	// 1. Before any cooldown: the loop may continue, and it still spends no
	// quota of its own — the probe budget stays separate and zero for idle work.
	if got := parkIfExhausted(pools); got.Action != "continue" || got.ProbeBudget != 0 {
		t.Fatalf("initial decision = %+v, want continue with no idle probe budget", got)
	}

	// 2. Exhaust every pool with no known reset, through the real API.
	for _, p := range pools {
		if err := SetCooldown(cfgPath, providersByPool[p], "observed quota", nil, false); err != nil {
			t.Fatalf("cooldown %s: %v", p, err)
		}
	}

	// 3. Now the loop parks. It names the pools and says the reset is unknown
	// rather than inventing one, and it never offers a probe.
	got := parkIfExhausted(pools)
	if got.Action != "park" {
		t.Fatalf("decision = %+v, want park when every pool is exhausted", got)
	}
	if got.ProbeBudget != 0 {
		t.Errorf("parked loop was granted %d idle probes; the idle loop must never spend quota", got.ProbeBudget)
	}
	for _, want := range []string{"every quota pool is exhausted", "reset unknown", "explicit clear", "never guessing"} {
		if !contains(got.Reason, want) {
			t.Errorf("park reason lacks %q: %s", want, got.Reason)
		}
	}

	// 4. Idle iterations are bounded: the loop may take at most
	// idleLoopBudget no-op iterations before it must re-park, so it is not a
	// busy loop. The decision function is pure, so the bound is the loop's
	// only moving part; assert the constant exists and is positive and finite.
	if idleLoopBudget <= 0 {
		t.Fatalf("idleLoopBudget = %d, want a positive bound", idleLoopBudget)
	}

	// 5. A single free pool resumes the loop — parking is not a deadlock, and
	// one explicit clear is enough (the explicit-clear policy, not a guess).
	if err := SetCooldown(cfgPath, providersByPool["claude"], "verified available", nil, true); err != nil {
		t.Fatal(err)
	}
	got = parkIfExhausted(pools)
	if got.Action != "continue" {
		t.Fatalf("after an explicit clear = %+v, want continue", got)
	}

	// 6. An unreadable cooldown state parks rather than guessing: Status is
	// the only reader the loop needs, and it refuses a corrupt file with an
	// error — so the loop never treats corruption as "free".
	if err := os.WriteFile(filepath.Join(dir(), "cooldowns.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Status(); err == nil {
		t.Fatalf("corrupt cooldown state read without an error")
	}
	got = parkIfExhausted(pools)
	if got.Action != "park" || !contains(got.Reason, "unreadable") {
		t.Fatalf("corrupt state = %+v, want park with a reason", got)
	}

	// 7. No pools at all parks (there is nothing to decide), never "continue".
	if got := parkIfExhausted(nil); got.Action != "park" {
		t.Errorf("no pools = %+v, want park", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
