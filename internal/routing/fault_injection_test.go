package routing

// Controlled quota fault injection (Phase B slice B5, fake probes only).
// Wires B2 (ClassifyProbe) into B3 (Record + DeriveWait) through a real
// Prepare refusal: every pool exhausts at once, no writer is chosen, both
// pools cool with no invented reset, and the task parks in WAITING_QUOTA
// with the supervisor as next actor.
//
// Nothing here touches a real provider: every probe is the TestProbeProcess
// helper (it prints one configured line and exits 0). A simulated quota
// event is NOT a real quota exhaustion; this test proves the supervisor
// path (probe -> classify -> record -> derive), not provider behavior.

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

// quotaFixture builds a world with two providers on distinct pools whose
// probes both print a line matching their pool's configured quota signal.
func quotaFixture(t *testing.T) (*contract.Contract, string, string, providers.Config) {
	t.Helper()
	d := t.TempDir()
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(d, "contracts"))
	t.Setenv("PIPELINE_LEDGER", filepath.Join(d, "ledger"))
	os.MkdirAll(contract.IndexDir(), 0700)
	wt := filepath.Join(d, "worktree")
	os.MkdirAll(wt, 0700)
	c := &contract.Contract{Name: "task", Worktree: wt, Allow: []string{"**"}, Scope: []string{"S1"}, ReportPath: filepath.Join(d, "report"), HooksGeneration: "g1", HooksInstalled: []string{"claude", "codex"}}
	log := filepath.Join(d, "probes")
	p := filepath.Join(d, "config.json")
	cfg := providers.Config{Providers: map[string]providers.Provider{
		"qa": {Agent: "claude", Model: "claude-sonnet", Launch: "shell", Probe: []string{os.Args[0], "-test.run=TestProbeProcess", "--", log, "qa", "quota-exhausted"}},
		"qb": {Agent: "codex", Model: "gpt-6.1-sol", Launch: "shell", Probe: []string{os.Args[0], "-test.run=TestProbeProcess", "--", log, "qb", "out of quota"}},
	}, WorkerChains: map[string][]string{"backend": {"qa", "qb"}}, QuotaSignals: map[string][]string{"claude": {"quota-exhausted"}, "codex": {"out of quota"}}}
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
	return c, p, log, cfg
}

func TestQuotaFaultInjectionParksTask(t *testing.T) {
	c, p, log, cfg := quotaFixture(t)

	// 1. Every pool exhausts: Prepare probes the chain in order and refuses
	// to choose any writer.
	if _, err := Prepare(c, "run", "worker:backend", "", p, false, 10*time.Second); err == nil || !strings.Contains(err.Error(), "no eligible provider") {
		t.Fatalf("Prepare = %v, want no-eligible-provider refusal", err)
	}
	if b, _ := os.ReadFile(log); string(b) != "qa\nqb\n" {
		t.Fatalf("probe order = %q, want ordered chain qa then qb", string(b))
	}

	// 2. Both pools cool with no invented reset time (unknown stays unknown:
	// explicit clear is the only way back).
	s, err := Status()
	if err != nil {
		t.Fatal(err)
	}
	for pool, wantProvider := range map[string]string{"claude": "qa", "codex": "qb"} {
		cd, ok := s[pool]
		if !ok || !blocked(s, pool) {
			t.Fatalf("pool %s not blocked: %+v", pool, s)
		}
		if cd.Until != nil {
			t.Fatalf("pool %s has invented reset %v, want unknown", pool, cd.Until)
		}
		if !strings.Contains(cd.Reason, wantProvider) {
			t.Errorf("pool %s reason %q names no provider", pool, cd.Reason)
		}
	}

	// 3. B2: each probe result classifies as exhaustion, never rate limit.
	for _, n := range []string{"qa", "qb"} {
		r := providers.ProbeWithRetry(&cfg, n, 10*time.Second)
		if r.State != "quota" {
			t.Fatalf("probe %s = %+v, want quota state", n, r)
		}
		if got := ClassifyProbe(r); got != ClassQuotaExhausted {
			t.Fatalf("ClassifyProbe(%s) = %q, want QUOTA_EXHAUSTED", n, got)
		}
	}

	// 4. B3: record the supervisor events; a redelivery dedupes (no second ack).
	now := time.Now()
	evs := []Event{
		{EventID: "fault-qa-1", Run: "run", Task: c.Name, Attempt: "a1", Source: "test", At: now, Kind: KindProbe, Class: ClassQuotaExhausted, Pool: "claude", Evidence: "[quota-exhausted] quota-exhausted"},
		{EventID: "fault-qb-1", Run: "run", Task: c.Name, Attempt: "a1", Source: "test", At: now.Add(time.Second), Kind: KindProbe, Class: ClassQuotaExhausted, Pool: "codex", Evidence: "[out of quota] out of quota"},
	}
	for _, e := range evs {
		if dup, err := Record(e); dup || err != nil {
			t.Fatalf("Record(%s) = dup=%v err=%v", e.EventID, dup, err)
		}
	}
	if dup, err := Record(evs[0]); !dup || err != nil {
		t.Fatalf("redelivery = dup=%v err=%v, want dedupe without re-ack", dup, err)
	}

	// 5. The persisted events fold to WAITING_QUOTA owned by the supervisor.
	w := DeriveWait(c.Name, "a1", mustReadEvents(t))
	if w.State != WaitQuota || w.NextActor != "supervisor" {
		t.Fatalf("DeriveWait = %+v, want WAITING_QUOTA/supervisor", w)
	}
	if w.Reason == "" || w.EventID == "" || w.Since.IsZero() {
		t.Fatalf("incomplete wait: %+v", w)
	}
}
