package providers

import (
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestClassifyAndPick(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("probe fixtures use sh")
	}
	c := &Config{
		Providers: map[string]Provider{
			"quota":   {Agent: "codex", Probe: []string{"sh", "-c", "echo 'ERROR: You have hit your usage limit'; exit 1"}},
			"agy3":    {Agent: "antigravity", Probe: []string{"sh", "-c", "echo boom; exit 3"}},
			"kiro-ok": {Agent: "kiro", Probe: []string{"sh", "-c", "printf '> OK\\n Credits: 0.2\\n'"}},
			"auth":    {Agent: "opencode2", Probe: []string{"sh", "-c", "echo 'Error: not logged in'; exit 1"}},
			"missing": {Agent: "opencode2", Probe: []string{"no-such-cli-xyz"}},
			"slow":    {Agent: "codex", Probe: []string{"sh", "-c", "sleep 5"}},
			"flaky":   {Agent: "codex", Probe: []string{"sh", "-c", "if [ -f \"$FLAKY\" ]; then echo OK; else touch \"$FLAKY\"; sleep 5; fi"}},
		},
		WorkerChains: map[string][]string{"backend": {"quota", "agy3", "auth", "kiro-ok"}, "docs": {"quota", "missing"}},
		ReviewChains: map[string][]string{"r": {"quota", "slow"}},
		QuotaSignals: map[string][]string{"codex": {"usage limit"}, "kiro": {"credit"}},
	}
	t.Setenv("FLAKY", filepath.Join(t.TempDir(), "flaky-seen"))
	names := []string{"quota", "agy3", "kiro-ok", "auth", "missing", "slow", "flaky"}
	res := Check(c, names, 500*time.Millisecond)
	want := map[string]string{"quota": "quota", "agy3": "quota", "kiro-ok": "up", "auth": "down", "missing": "down", "slow": "down", "flaky": "up"}
	for n, s := range want {
		if res[n].State != s {
			t.Errorf("%s: state %s (%s), want %s", n, res[n].State, res[n].Detail, s)
		}
	}
	p := Pick(c, res, "")
	if p["worker:backend"] != "kiro-ok" || p["worker:docs"] != "" || p["review:r"] != "" {
		t.Errorf("picks %v", p)
	}
}
