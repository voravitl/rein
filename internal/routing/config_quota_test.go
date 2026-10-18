package routing

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/voravitl/rein/internal/providers"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voravitl/rein/internal/budget"
)

func TestQuotaPrecheckExcludesBeforeReservationAndInjectedProbe(t *testing.T) {
	w := newWorld(t)
	w.cfg.RequireOrcaQuota = true
	w.writeConfig()
	result, err := PrepareAuto(context.Background(), w.in())
	if err == nil || result == nil || result.Receipt != nil {
		t.Fatalf("unsafe automatic route: %+v %v", result, err)
	}
	if len(w.probeLog()) != 0 {
		t.Fatal("model probe ran before quota precheck")
	}
	holds, err := budget.HoldsFor(result.Attempt)
	if err != nil || len(holds) != 0 {
		t.Fatalf("reservation before quota precheck: %+v %v", holds, err)
	}
	found := false
	for _, r := range result.Rounds {
		for _, c := range r.Candidates {
			if len(c.Excluded) >= 14 && c.Excluded[:14] == "quota_precheck" {
				found = true
			}
		}
	}
	if !found {
		b, _ := json.MarshalIndent(result, "", "  ")
		t.Fatalf("missing actionable quota exclusion: %s", b)
	}
}

func TestQuotaLaunchValidationRechecksExhaustedQuota(t *testing.T) {
	c, path, log := fixture(t)
	dir := t.TempDir()
	response := filepath.Join(dir, "response")
	if err := os.WriteFile(filepath.Join(dir, "orca"), []byte("#!/bin/sh\ncat \"$REIN_TEST_ORCA_RESPONSE\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("REIN_TEST_ORCA_RESPONSE", response)
	writeQuota := func(used int) {
		t.Helper()
		data := fmt.Sprintf(`{"ok":true,"result":{"rateLimits":{"codex":{"status":"ok","updatedAt":%d,"weekly":{"usedPercent":%d}}}}}`, time.Now().UnixMilli(), used)
		if err := os.WriteFile(response, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg providers.Config
	if err = json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.RequireOrcaQuota = true
	cfg.WorkerChains["backend"] = []string{"c"}
	p := cfg.Providers["c"]
	p.QuotaWindows = []string{"weekly"}
	cfg.Providers["c"] = p
	b, _ = json.Marshal(cfg)
	if err = os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	writeQuota(10)
	d, err := Prepare(c, "run", "worker:backend", "", path, false, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Validate(c, "run", d.Agent, d.Model); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(log)
	writeQuota(100)
	if _, err = Validate(c, "run", d.Agent, d.Model); err == nil || !strings.Contains(err.Error(), "quota precheck") {
		t.Fatalf("launch accepted exhausted quota: %v", err)
	}
	after, _ := os.ReadFile(log)
	if string(before) != string(after) {
		t.Fatal("launch validation invoked a model")
	}
}
