package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/voravitl/rein/internal/providers"
)

type configModels map[string]string

func (m configModels) String() string { return "harness=model" }
func (m configModels) Set(s string) error {
	k, v, ok := strings.Cut(s, "=")
	if !ok || strings.TrimSpace(v) == "" {
		return errors.New("model must be harness=model-id")
	}
	switch k {
	case "claude", "codex", "antigravity", "opencode2":
	default:
		return fmt.Errorf("unsupported harness %q", k)
	}
	if _, ok := m[k]; ok {
		return fmt.Errorf("duplicate harness %q", k)
	}
	m[k] = v
	return nil
}
func initProviderConfig(dir string, models configModels) (err error) {
	if dir == "" {
		return errors.New("explicit --dir required")
	}
	// Mkdir refuses preexisting directories and symlinks, preserving existing setup.
	if err = os.Mkdir(dir, 0700); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(dir)
		}
	}()
	disabled := false
	cfg := providers.Config{ModelCallsEnabled: &disabled, RequireOrcaQuota: true, Providers: map[string]providers.Provider{}, WorkerChains: map[string][]string{}, ReviewChains: map[string][]string{}}
	for _, h := range []string{"claude", "codex", "antigravity", "opencode2"} {
		maker := map[string]string{"claude": "anthropic", "codex": "openai", "antigravity": "unknown", "opencode2": "unknown"}[h]
		cfg.Providers[h] = providers.Provider{Agent: h, Maker: maker, Model: models[h], Launch: "shell", Cost: "unknown; no price or quota assumed"}
		// Independent chains require an explicit choice; no automatic paid fallback.
		cfg.WorkerChains[h] = []string{h}
	}
	for name, value := range map[string]any{"fallback-chain.json": cfg, "profile.json": map[string]any{"name": "crux"}} {
		b, e := json.MarshalIndent(value, "", "  ")
		if e != nil {
			return e
		}
		if e = os.WriteFile(filepath.Join(dir, name), append(b, '\n'), 0600); e != nil {
			return e
		}
	}
	return nil
}

type setupProvider struct {
	Harness         string `json:"harness"`
	Model           string `json:"model"`
	Installed       bool   `json:"cli_installed"`
	ProbeConfigured bool   `json:"probe_configured"`
}
type setupCheck struct {
	QuotaGuardRequired bool                      `json:"quota_guard_required"`
	Valid              bool                      `json:"valid"`
	ModelCallsEnabled  bool                      `json:"model_calls_enabled"`
	Providers          []setupProvider           `json:"providers"`
	Quotas             []providers.QuotaSnapshot `json:"quotas,omitempty"`
	QuotaError         string                    `json:"quota_error,omitempty"`
	Notes              []string                  `json:"notes"`
}

func checkProviderConfig(dir string) (*setupCheck, error) {
	if dir == "" {
		return nil, errors.New("explicit --dir required")
	}
	cfg, err := providers.Load(filepath.Join(dir, "fallback-chain.json"))
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(dir, "profile.json"))
	if err != nil {
		return nil, err
	}
	var profile map[string]json.RawMessage
	if json.Unmarshal(b, &profile) != nil || profile == nil {
		return nil, errors.New("invalid profile JSON object")
	}
	out := &setupCheck{Valid: true, QuotaGuardRequired: cfg.RequireOrcaQuota, ModelCallsEnabled: cfg.CheckModelCalls() == nil, Providers: []setupProvider{}, Notes: []string{"Orca quota is a conservative veto; a positive reading does not authorize an account or spending. Exact account/model/budget binding remains required.", "Missing/zero budget caps do not prohibit spending. Generated setup disables model calls explicitly.", "No prices, availability, free-model quota or review independence are inferred. Configure probes, budgets and review chains before enabling.", "Routing readiness additionally requires contract, hooks, account binding and runtime verification."}}
	names := make([]string, 0, len(cfg.Providers))
	for n := range cfg.Providers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		p := cfg.Providers[n]
		bin := map[string]string{"claude": "claude", "codex": "codex", "antigravity": "agy", "opencode2": "opencode2"}[p.Agent]
		_, err := exec.LookPath(bin)
		out.Providers = append(out.Providers, setupProvider{Harness: p.Agent, Model: p.Model, Installed: bin != "" && err == nil, ProbeConfigured: len(p.Probe) > 0})
	}
	return out, nil
}

func readOrcaQuota() ([]providers.QuotaSnapshot, error) { return providers.ReadOrcaQuota() }
func cmdConfig(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: rein config init|check --dir PATH [--model harness=id] [--quota orca|native]")
		return 2
	}
	fs := flag.NewFlagSet("config "+args[0], flag.ContinueOnError)
	dir := fs.String("dir", "", "explicit setup directory")
	quota := fs.String("quota", "", "optional read-only quota source: orca or native (Codex only)")
	format := fs.String("format", "json", "check output: json or text (Thai)")
	models := configModels{}
	fs.Var(models, "model", "harness=model-id (repeatable, init only)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 || (*quota != "" && *quota != "orca" && *quota != "native") || (*format != "json" && *format != "text") {
		return 2
	}
	var err error
	switch args[0] {
	case "init":
		if *quota != "" || *format != "json" {
			return 2
		}
		err = initProviderConfig(*dir, models)
		if err == nil {
			fmt.Fprintln(os.Stdout, "Created private Crux config with model calls disabled. Inspect with: rein config check --dir", *dir, "--quota orca")
		}
	case "check":
		if len(models) != 0 {
			return 2
		}
		var result *setupCheck
		result, err = checkProviderConfig(*dir)
		if err == nil {
			if *quota != "" {
				if *quota == "native" {
					result.Quotas, err = providers.ReadNativeQuota(context.Background(), *dir, 15*time.Second)
				} else {
					result.Quotas, err = readOrcaQuota()
				}
				if err != nil {
					result.QuotaError = err.Error()
					err = nil
				}
			}
			if *format == "text" {
				_, err = fmt.Fprint(os.Stdout, configCheckText(result))
			} else {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				err = enc.Encode(result)
			}
		}
	default:
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
