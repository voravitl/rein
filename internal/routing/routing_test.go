package routing

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/ledger"
	"github.com/voravitl/rein/internal/providers"
)

func fixture(t *testing.T) (*contract.Contract, string, string) {
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
		"a": {Agent: "claude", Model: "claude-sonnet", Launch: "shell", Probe: []string{os.Args[0], "-test.run=TestProbeProcess", "--", log, "a", "quota"}},
		"b": {Agent: "claude", Model: "claude-opus", Launch: "shell", Probe: []string{os.Args[0], "-test.run=TestProbeProcess", "--", log, "b", "OK"}},
		"c": {Agent: "codex", Model: "gpt-6.1-sol", Launch: "shell", Probe: []string{os.Args[0], "-test.run=TestProbeProcess", "--", log, "c", "OK"}},
	}, WorkerChains: map[string][]string{"backend": {"a", "b", "c"}}, ReviewChains: map[string][]string{"backend": {"a", "c"}}, QuotaSignals: map[string][]string{"claude": {"quota"}}}
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
	return c, p, log
}
func TestSequentialQuotaAndValidation(t *testing.T) {
	c, p, log := fixture(t)
	d, err := Prepare(c, "run", "worker:backend", "", p, false, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if d.Provider != "c" {
		t.Fatal(d)
	}
	b, _ := os.ReadFile(log)
	if string(b) != "a\nc\n" {
		t.Fatal(string(b))
	}
	if _, err = Validate(c, "run", "codex", "gpt-6.1-sol"); err != nil {
		t.Fatal(err)
	}
	_, err = Prepare(c, "run", "worker:backend", "", p, false, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(log)
	if string(b) != "a\nc\nc\n" {
		t.Fatal(string(b))
	}
	for _, v := range [][3]string{{"other", "codex", "gpt-6.1-sol"}, {"run", "claude", "gpt-6.1-sol"}, {"run", "codex", "gpt-other"}} {
		if _, err = Validate(c, v[0], v[1], v[2]); err == nil {
			t.Fatal("accepted mismatch", v)
		}
	}
	c.HooksGeneration = "g2"
	if _, err = Validate(c, "run", "codex", "gpt-6.1-sol"); err == nil {
		t.Fatal("accepted stale contract")
	}
}
func TestReviewAndFailureInvalidate(t *testing.T) {
	c, p, _ := fixture(t)
	if _, err := Prepare(c, "run", "review:backend", "gpt-6.1-sol", p, false, time.Second); err == nil {
		t.Fatal("same-maker fallback accepted")
	}
	if _, err := Prepare(c, "run", "worker:backend", "", p, false, time.Second); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PIPELINE_LEDGER", t.TempDir())
	if _, err := Prepare(c, "run", "worker:backend", "", p, false, time.Second); err == nil {
		t.Fatal("ledger failure accepted")
	}
	if _, err := Validate(c, "run", "codex", "gpt-6.1-sol"); err == nil {
		t.Fatal("old receipt usable")
	}
}
func TestUnknownConfigAndCooldownState(t *testing.T) {
	c, p, _ := fixture(t)
	if _, err := Prepare(c, "run", "worker:missing", "", p, false, time.Second); err == nil {
		t.Fatal("unknown chain accepted")
	}
	if _, err := Prepare(c, "run", "worker:backend", "", p, true, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Validate(c, "run", "codex", "gpt-6.1-sol"); err == nil {
		t.Fatal("changed config accepted")
	}
	if err := os.WriteFile(filepath.Join(contract.IndexDir(), "routes", "cooldowns.json"), []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(c, "run", "worker:backend", "", p, false, time.Second); err == nil {
		t.Fatal("malformed accepted")
	}
}

func TestProbeProcess(t *testing.T) {
	if len(os.Args) < 6 || os.Args[len(os.Args)-4] != "--" {
		return
	}
	a := os.Args[len(os.Args)-3:]
	f, err := os.OpenFile(a[0], os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(2)
	}
	fmt.Fprintln(f, a[1])
	f.Close()
	fmt.Println(a[2])
	os.Exit(0)
}

func TestStaleReceiptAndKnownReset(t *testing.T) {
	c, p, _ := fixture(t)
	d, err := Prepare(c, "run", "worker:backend", "", p, false, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	d.PreparedAt = time.Now().Add(-31 * time.Minute)
	path, _ := receipt(c)
	if err = atomic(path, d); err != nil {
		t.Fatal(err)
	}
	if _, err = Validate(c, "run", "codex", "gpt-6.1-sol"); err == nil {
		t.Fatal("expired accepted")
	}
	past := time.Now().Add(-time.Second)
	if err = SetCooldown(p, "a", "known reset", &past, false); err != nil {
		t.Fatal(err)
	}
	s, err := Status()
	if err != nil || blocked(s, "claude") {
		t.Fatal(s, err)
	}
	if err = SetCooldown(p, "a", "quota", nil, false); err != nil {
		t.Fatal(err)
	}
	if err = SetCooldown(p, "b", "reset verified", nil, true); err != nil {
		t.Fatal(err)
	}
	s, err = Status()
	if err != nil || blocked(s, "claude") {
		t.Fatal(s, err)
	}
}
func TestInvalidProviderAndIndependentPool(t *testing.T) {
	for _, field := range []string{"agent", "model", "launch", "unknown-launch", "probe"} {
		t.Run(field, func(t *testing.T) {
			c, p, _ := fixture(t)
			cfg, err := providers.Load(p)
			if err != nil {
				t.Fatal(err)
			}
			v := cfg.Providers["c"]
			switch field {
			case "agent":
				v.Agent = "unknown"
			case "model":
				v.Model = ""
			case "launch":
				v.Launch = ""
			case "unknown-launch":
				v.Launch = "garbage"
			case "probe":
				v.Probe = nil
			}
			cfg.Providers["c"] = v
			b, _ := json.Marshal(cfg)
			os.WriteFile(p, b, 0600)
			if _, err = Prepare(c, "run", "worker:backend", "", p, false, time.Second); err == nil {
				t.Fatal("invalid provider accepted")
			}
		})
	}
	p := providers.Provider{Agent: "kiro", Model: "claude-sonnet", Launch: "shell", Probe: []string{"test"}}
	maker, pool, err := identity(p)
	if err != nil || maker != "anthropic" || pool != "kiro" {
		t.Fatal(maker, pool, err)
	}
	p.Agent = "claude"
	p.Pool = "per-model"
	_, pool, err = identity(p)
	if err != nil || pool != "claude" {
		t.Fatal(pool, err)
	}
}
func TestHooksAndMalformedState(t *testing.T) {
	c, p, _ := fixture(t)
	c.HooksGeneration = ""
	if _, err := Prepare(c, "run", "worker:backend", "", p, false, time.Second); err == nil {
		t.Fatal("unhooked worker accepted")
	}
	c.HooksGeneration = "g1"
	if err := os.MkdirAll(dir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir(), "cooldowns.json"), []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(c, "run", "worker:backend", "", p, false, time.Second); err == nil {
		t.Fatal("malformed state accepted")
	}
}

func TestProviderModelNamesAndReservedTask(t *testing.T) {
	for model, want := range map[string]string{"opencode-go/claude-opus-5.5": "anthropic", "opencode-go/glm-5.3": "glm", "opencode/deepseek-r1": "deepseek", "opencode/muse-pilot": "unknown"} {
		if got := modelMaker(model); got != want {
			t.Fatal(model, got, want)
		}
	}
	c, p, _ := fixture(t)
	c.Name = "cooldowns"
	if _, err := Prepare(c, "run", "worker:backend", "", p, false, time.Second); err != nil {
		t.Fatal(err)
	}
	s, err := Status()
	if err != nil || !blocked(s, "claude") {
		t.Fatal(s, err)
	}
	if _, err = Validate(c, "run", "codex", "gpt-6.1-sol"); err != nil {
		t.Fatal(err)
	}
}

func TestMissingWorkerVendorSkipped(t *testing.T) {
	c, p, log := fixture(t)
	c.HooksInstalled = []string{"codex"}
	d, err := Prepare(c, "run", "worker:backend", "", p, false, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if d.Provider != "c" {
		t.Fatal(d)
	}
	b, _ := os.ReadFile(log)
	if string(b) != "c\n" {
		t.Fatal(string(b))
	}
}

func TestLedgerFailurePreventsProbe(t *testing.T) {
	c, p, log := fixture(t)
	t.Setenv("PIPELINE_LEDGER", t.TempDir())
	if _, err := Prepare(c, "run", "worker:backend", "", p, false, time.Second); err == nil {
		t.Fatal("ledger failure accepted")
	}
	if b, err := os.ReadFile(log); err == nil {
		t.Fatalf("spent probe despite ledger failure: %s", b)
	}
}

func TestCooldownRequiresEvidenceAndLedger(t *testing.T) {
	_, p, _ := fixture(t)
	if err := SetCooldown(p, "a", "", nil, true); err == nil {
		t.Fatal("clear without reason accepted")
	}
	t.Setenv("PIPELINE_LEDGER", t.TempDir())
	if err := SetCooldown(p, "a", "observed quota", nil, false); err == nil {
		t.Fatal("unrecorded cooldown accepted")
	}
	s, err := Status()
	if err != nil || len(s) != 0 {
		t.Fatal("state mutated after ledger failure", s, err)
	}
	t.Setenv("PIPELINE_LEDGER", filepath.Join(t.TempDir(), "ledger"))
	if err = SetCooldown(p, "a", "quota evidence", nil, false); err != nil {
		t.Fatal(err)
	}
	rows, err := ledger.Load("")
	if err != nil || len(rows) != 1 || rows[0].Kind != "routing_cooldown" || rows[0].Purpose != "claude" {
		t.Fatal(rows, err)
	}
	t.Setenv("PIPELINE_LEDGER", t.TempDir())
	if err = SetCooldown(p, "b", "reset evidence", nil, true); err == nil {
		t.Fatal("unrecorded clear accepted")
	}
	s, err = Status()
	if err != nil || !blocked(s, "claude") {
		t.Fatal("state cleared after ledger failure", s, err)
	}
}
