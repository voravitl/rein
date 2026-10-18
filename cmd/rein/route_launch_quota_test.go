package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voravitl/rein/internal/providers"
	"github.com/voravitl/rein/internal/routing"
)

func TestRouteQuotaScopeRejectsExplicitOrcaTargets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	for _, guard := range []bool{false, true} {
		b, _ := json.Marshal(providers.Config{RequireOrcaQuota: guard, Providers: map[string]providers.Provider{"w": {Agent: "antigravity", Model: "m"}}})
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
		for _, flags := range [][]string{{"--environment", "other"}, {"--environment=other"}, {"--pairing-code", "fixture-private"}, {"--pairing-code=fixture-private"}, {"--on", "other"}, {"--on=other"}} {
			argv := append([]string{"orca", "orchestration", "worker-start"}, flags...)
			err := validateRouteQuotaScope(&routing.Decision{ConfigPath: path}, argv)
			if err != nil && strings.Contains(err.Error(), "fixture-private") {
				t.Fatal("selector value leaked")
			}
			if (err != nil) != guard {
				t.Fatalf("guard=%t flags=%v error=%v", guard, flags, err)
			}
		}
	}
	if err := validateRouteQuotaScope(&routing.Decision{ConfigPath: path}, []string{"orca", "orchestration", "worker-start"}); err != nil {
		t.Fatal(err)
	}
	if err := validateRouteQuotaScope(&routing.Decision{}, []string{"codex", "exec", "--model", "m", "prompt"}); err != nil {
		t.Fatal(err)
	}
}

func TestRouteQuotaScopeStopsDispatchAfterHealthyLocalQuota(t *testing.T) {
	e := newE2E(t, "orca")
	raw, err := os.ReadFile(e.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var cfg providers.Config
	if err = json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.RequireOrcaQuota = true
	p := cfg.Providers["w"]
	p.QuotaWindows = []string{"weekly"}
	cfg.Providers["w"] = p
	raw, _ = json.Marshal(cfg)
	if err = os.WriteFile(e.cfgPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(e.root, "orca")
	marker := filepath.Join(e.root, "dispatched")
	quota := filepath.Join(e.root, "quota.json")
	t.Setenv("REIN_QUOTA_SCOPE_MARKER", marker)
	t.Setenv("REIN_QUOTA_SCOPE_RESPONSE", quota)
	t.Setenv("PATH", e.root+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ORCA_ENVIRONMENT", "")
	t.Setenv("ORCA_PAIRING_CODE", "")
	response := fmt.Sprintf(`{"ok":true,"result":{"rateLimits":{"antigravity":{"status":"ok","updatedAt":%d,"weekly":{"usedPercent":10}}}}}`, time.Now().UnixMilli())
	if err = os.WriteFile(quota, []byte(response), 0600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nif [ \"$1\" = account ]; then cat \"$REIN_QUOTA_SCOPE_RESPONSE\"; else touch \"$REIN_QUOTA_SCOPE_MARKER\"; fi\n"
	if err = os.WriteFile(cli, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if out, code := capture(t, func() int {
		return cmdRoute([]string{"auto", "--task", "task", "--run", "run", "--profile-file", e.profile, "--config", e.cfgPath, "--timeout", "30s"})
	}); code != 0 {
		t.Fatalf("prepare %d: %s", code, out)
	}
	if _, err := routing.Validate(e.c, "run", "antigravity", "gemini-w-high"); err != nil {
		t.Fatal("healthy local quota did not validate:", err)
	}
	args := []string{"--task", "task", "--run", "run", "--", cli, "orchestration", "worker-start", "--agent", "antigravity", "--run", "run", "--worktree", "path:" + e.wt, "--model", "gemini-w-high", "--effort", "high", "--environment", "other"}
	out, code := capture(t, func() int { return cmdRouteLaunch(args) })
	if code == 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("remote dispatch attempted: %v", err)
	}
}
