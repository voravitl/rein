package providers

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOrcaQuotaGuardStopsEveryProbeSurface(t *testing.T) {
	for _, surface := range []string{"single", "retry", "parallel"} {
		t.Run(surface, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "called")
			orca := installTestOrca(t, filepath.Join(t.TempDir(), "response"))
			raw, _ := json.Marshal(map[string]any{"require_orca_quota": true, "providers": map[string]Provider{"p": {Agent: "codex", Model: "test", Probe: []string{orca, "probe", marker}}}})
			var cfg Config
			if err := json.Unmarshal(raw, &cfg); err != nil {
				t.Fatal(err)
			}
			var r Result
			switch surface {
			case "single":
				r = Probe(&cfg, "p", time.Second)
			case "retry":
				r = ProbeWithRetry(&cfg, "p", time.Second)
			case "parallel":
				r = Check(&cfg, []string{"p"}, time.Second)["p"]
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Error("quota guard bypassed: model command ran without window mapping")
			}
			if r.State != "down" {
				t.Errorf("expected quota refusal: %+v", r)
			}
		})
	}
}

func TestQuotaGateWindowSelectionAndFreshness(t *testing.T) {
	now := time.Now()
	stamp := now.UnixMilli()
	reset := now.Add(time.Hour).UnixMilli()
	pct := func(v float64) *float64 { return &v }
	base := QuotaSnapshot{Provider: "antigravity", Status: "ok", ObservedAt: &stamp, Windows: []QuotaWindow{
		{Name: "Gemini weekly", Remaining: pct(96), ResetsAt: &reset},
		{Name: "Gemini five hour", Remaining: pct(98), ResetsAt: &reset},
		{Name: "Claude weekly", Remaining: pct(0), ResetsAt: &reset},
	}}
	p := Provider{Agent: "antigravity", Model: "owner-verified-gemini", QuotaWindows: []string{"Gemini weekly", "Gemini five hour"}, QuotaReservePercent: 5}
	if err := checkQuotaWindows(p, []QuotaSnapshot{base}, now); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*Provider, *QuotaSnapshot)
	}{
		{"exhausted correct group", func(p *Provider, s *QuotaSnapshot) { p.QuotaWindows = []string{"Claude weekly"} }},
		{"one window exhausted", func(p *Provider, s *QuotaSnapshot) {
			s.Windows = append([]QuotaWindow(nil), s.Windows...)
			s.Windows[1].Remaining = pct(0)
		}},
		{"reserve", func(p *Provider, s *QuotaSnapshot) { p.QuotaReservePercent = 96 }},
		{"unknown percent", func(p *Provider, s *QuotaSnapshot) {
			s.Windows = append([]QuotaWindow(nil), s.Windows...)
			s.Windows[0].Remaining = nil
		}},
		{"missing window", func(p *Provider, s *QuotaSnapshot) { p.QuotaWindows = []string{"unknown"} }},
		{"duplicate window", func(p *Provider, s *QuotaSnapshot) {
			s.Windows = append(append([]QuotaWindow(nil), s.Windows...), s.Windows[0])
		}},
		{"duplicate mapping", func(p *Provider, s *QuotaSnapshot) { p.QuotaWindows = []string{"Gemini weekly", "Gemini weekly"} }},
		{"stale", func(p *Provider, s *QuotaSnapshot) { n := now.Add(-6 * time.Minute).UnixMilli(); s.ObservedAt = &n }},
		{"future", func(p *Provider, s *QuotaSnapshot) { n := now.Add(2 * time.Minute).UnixMilli(); s.ObservedAt = &n }},
		{"unavailable", func(p *Provider, s *QuotaSnapshot) { s.Status = "unavailable" }},
		{"expired reset", func(p *Provider, s *QuotaSnapshot) {
			n := now.Add(-time.Second).UnixMilli()
			s.Windows = append([]QuotaWindow(nil), s.Windows...)
			s.Windows[0].ResetsAt = &n
		}},
		{"free unknown", func(p *Provider, s *QuotaSnapshot) { p.Agent = "opencode2" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pp, ss := p, base
			tc.change(&pp, &ss)
			if err := checkQuotaWindows(pp, []QuotaSnapshot{ss}, now); err == nil {
				t.Fatal("unsafe quota accepted")
			}
		})
	}
}

func TestQuotaGateReadsOrcaAndRechecksBetweenCalls(t *testing.T) {
	d := t.TempDir()
	response := filepath.Join(d, "response.json")
	marker := filepath.Join(d, "probe")
	orca := installTestOrca(t, response)
	write := func(used int) {
		t.Helper()
		data := fmt.Sprintf(`{"ok":true,"result":{"rateLimits":{"codex":{"status":"ok","updatedAt":%d,"weekly":{"usedPercent":%d}}}}}`, time.Now().UnixMilli(), used)
		if err := os.WriteFile(response, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := Config{RequireOrcaQuota: true, Providers: map[string]Provider{"p": {Agent: "codex", Model: "test", QuotaWindows: []string{"weekly"}, Probe: []string{orca, "probe", marker}}}}
	write(20)
	if r := Probe(&cfg, "p", time.Second); r.State != "up" {
		t.Fatal(r)
	}
	write(100)
	if r := Probe(&cfg, "p", time.Second); r.State != "down" {
		t.Fatal(r)
	}
	b, _ := os.ReadFile(marker)
	if string(b) != "x" {
		t.Fatalf("exhausted quota ran second model command: %q", b)
	}
	if err := os.WriteFile(response, []byte(`{"ok":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cfg.CheckQuota("p"); err == nil {
		t.Fatal("failed source accepted")
	}
}

func TestQuotaGateDoesNotHideWeeklyExhaustionBehindSession(t *testing.T) {
	now := time.Now()
	stamp := now.UnixMilli()
	zero, full := float64(0), float64(100)
	for _, agent := range []string{"claude", "codex"} {
		p := Provider{Agent: agent, Model: "test", QuotaWindows: []string{"session"}}
		s := QuotaSnapshot{Provider: agent, Status: "ok", ObservedAt: &stamp, Windows: []QuotaWindow{{Name: "session", Remaining: &full}, {Name: "weekly", Remaining: &zero}}}
		if err := checkQuotaWindows(p, []QuotaSnapshot{s}, now); err == nil {
			t.Fatalf("%s accepted exhausted weekly limit", agent)
		}
	}
}
func TestQuotaGateRefusesUnboundRemoteScope(t *testing.T) {
	t.Setenv("ORCA_ENVIRONMENT", "remote-host")
	cfg := Config{RequireOrcaQuota: true}
	if err := cfg.CheckQuota("p"); err == nil {
		t.Fatal("accepted unbound remote scope")
	}
}

func TestQuotaRetryRechecksAfterTimeoutWithoutSecondModelCall(t *testing.T) {
	dir := t.TempDir()
	response := filepath.Join(dir, "response")
	marker := filepath.Join(dir, "calls")
	orca := installTestOrca(t, response)
	raw := func(used int) string {
		return fmt.Sprintf(`{"ok":true,"result":{"rateLimits":{"codex":{"status":"ok","updatedAt":%d,"weekly":{"usedPercent":%d}}}}}`, time.Now().UnixMilli(), used)
	}
	if err := os.WriteFile(response, []byte(raw(0)), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REIN_TEST_EXHAUSTED_JSON", raw(100))
	cfg := Config{RequireOrcaQuota: true, Providers: map[string]Provider{"p": {Agent: "codex", Model: "test", QuotaWindows: []string{"weekly"}, Probe: []string{orca, "probe", marker}}}}
	r := ProbeWithRetry(&cfg, "p", time.Second)
	b, _ := os.ReadFile(marker)
	if string(b) != "x" || r.State != "down" || !strings.Contains(r.Detail, "quota precheck") {
		t.Fatalf("retry spent despite exhausted quota: %+v calls=%q", r, b)
	}
}
