// Package routing binds configured model choices to contracted launches and quota pools.
package routing

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/ledger"
	"github.com/voravitl/rein/internal/providers"
	"github.com/voravitl/rein/internal/verdict"
)

type Decision struct {
	Task            string             `json:"task"`
	Run             string             `json:"run"`
	Worktree        string             `json:"worktree"`
	ContractHash    string             `json:"contract_hash"`
	HooksGeneration string             `json:"hooks_generation"`
	ConfigPath      string             `json:"config_path"`
	ConfigHash      string             `json:"config_hash"`
	Chain           string             `json:"chain"`
	WorkerModel     string             `json:"worker_model,omitempty"`
	Provider        string             `json:"provider"`
	Agent           string             `json:"agent"`
	Model           string             `json:"model"`
	Launch          string             `json:"launch"`
	Maker           string             `json:"maker"`
	Pool            string             `json:"pool"`
	PreparedAt      time.Time          `json:"prepared_at"`
	Attempts        []providers.Result `json:"attempts"`
}
type Cooldown struct {
	Until  *time.Time `json:"until,omitempty"`
	Reason string     `json:"reason"`
}

func dir() string { return filepath.Join(contract.IndexDir(), "routes") }
func receipt(c *contract.Contract) (string, error) {
	if c == nil || c.Name == "" || filepath.Base(c.Name) != c.Name || c.Name == "." || c.Name == ".." {
		return "", errors.New("invalid task name")
	}
	if !filepath.IsAbs(c.Worktree) || filepath.Clean(c.Worktree) != c.Worktree {
		return "", errors.New("exact worktree required")
	}
	index, err := filepath.EvalSymlinks(contract.IndexDir())
	if err != nil {
		return "", err
	}
	worktree, err := filepath.EvalSymlinks(c.Worktree)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(worktree, index)
	if err != nil {
		return "", err
	}
	if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return "", errors.New("routing state must be outside worktree")
	}
	return filepath.Join(dir(), "decisions", c.Name+".json"), nil
}
func hash(b []byte) string                     { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func contractHash(c *contract.Contract) string { b, _ := json.Marshal(c); return hash(b) }
func atomic(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".route-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
func state() (map[string]Cooldown, error) {
	out := map[string]Cooldown{}
	b, err := os.ReadFile(filepath.Join(dir(), "cooldowns.json"))
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	if out == nil {
		return nil, errors.New("invalid cooldown state")
	}
	for k, v := range out {
		if k == "" || v.Reason == "" {
			return nil, errors.New("invalid cooldown record")
		}
	}
	return out, nil
}

// shortcut: interrupted processes leave a lock; remove it only after proving no routing writer remains.
func lock() (func(), error) {
	if err := os.MkdirAll(dir(), 0700); err != nil {
		return nil, err
	}
	p := filepath.Join(dir(), ".lock")
	if err := os.Mkdir(p, 0700); err != nil {
		return nil, fmt.Errorf("routing state busy: %w", err)
	}
	return func() { _ = os.Remove(p) }, nil
}
func identity(p providers.Provider) (maker, pool string, err error) {
	if _, err = ledger.ParseAgentModel(p.Agent + ":" + p.Model); err != nil {
		return
	}
	if (p.Launch != "shell" && p.Launch != "orca") || len(p.Probe) == 0 || p.Probe[0] == "" {
		err = errors.New("provider requires explicit launch and probe")
		return
	}
	maker = modelMaker(p.Model)
	if p.Maker != "" {
		if maker != "unknown" && maker != p.Maker {
			err = errors.New("provider maker contradicts model")
			return
		}
		maker = p.Maker
	}

	pool = p.Pool
	if p.Agent == "claude" {
		pool = "claude"
	}
	if pool == "" {
		pool = p.Agent
	}
	return
}
func blocked(s map[string]Cooldown, pool string) bool {
	v, ok := s[pool]
	return ok && (v.Until == nil || time.Now().Before(*v.Until))
}
func config(path string) (*providers.Config, string, string, error) {
	path, err := filepath.Abs(providers.ConfigPath(path))
	if err != nil {
		return nil, "", "", err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, "", "", err
	}
	var c providers.Config
	err = json.Unmarshal(b, &c)
	if err == nil && len(c.Providers) == 0 {
		err = errors.New("no providers")
	}
	return &c, path, hash(b), err
}
func eligible(cfg *providers.Config, chain, worker string) ([]string, error) {
	names, ok := cfg.Chains()[chain]
	if !ok || len(names) == 0 {
		return nil, errors.New("unknown or empty routing chain")
	}
	if strings.HasPrefix(chain, "review:") && modelMaker(worker) == "unknown" {
		return nil, errors.New("review requires known worker model maker")
	}
	for _, n := range names {
		p, ok := cfg.Providers[n]
		if !ok {
			return nil, fmt.Errorf("unknown provider %s", n)
		}
		if _, _, err := identity(p); err != nil {
			return nil, fmt.Errorf("provider %s: %w", n, err)
		}
	}
	return names, nil
}
func record(kind string, d *Decision) error {
	b, _ := json.Marshal(d)
	return ledger.Append(ledger.Row{Kind: kind, Task: d.Task, Run: d.Run, Role: d.Chain, Provider: d.Provider, Model: d.Model, Purpose: "contracted routing", Notes: string(b)})
}

// Prepare probes only eligible providers in chain order and persists a launch receipt after ledger success.
func Prepare(c *contract.Contract, run, chain, workerModel, configPath string, skipClaude bool, timeout time.Duration) (*Decision, error) {
	path, err := receipt(c)
	if err != nil {
		return nil, err
	}
	unlock, err := lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err = os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if strings.HasPrefix(chain, "worker:") && (c.HooksGeneration == "" || len(c.HooksInstalled) == 0) {
		return nil, errors.New("install worker hooks before routing")
	}
	if run == "" || timeout <= 0 {
		return nil, errors.New("explicit run and positive timeout required")
	}
	cfg, cp, ch, err := config(configPath)
	if err != nil {
		return nil, err
	}
	names, err := eligible(cfg, chain, workerModel)
	if err != nil {
		return nil, err
	}
	s, err := state()
	if err != nil {
		return nil, err
	}
	d := &Decision{Task: c.Name, Run: run, Worktree: c.Worktree, ContractHash: contractHash(c), HooksGeneration: c.HooksGeneration, ConfigPath: cp, ConfigHash: ch, Chain: chain, WorkerModel: workerModel, PreparedAt: time.Now()}
	for _, n := range names {
		p := cfg.Providers[n]
		if strings.HasPrefix(chain, "worker:") && !hookInstalled(c, p.Agent) {
			d.Attempts = append(d.Attempts, providers.Result{Name: n, State: "skipped", Detail: "worker hook not installed"})
			continue
		}
		maker, pool, _ := identity(p)
		if strings.HasPrefix(chain, "review:") && (maker == "unknown" || maker == modelMaker(workerModel)) {
			d.Attempts = append(d.Attempts, providers.Result{Name: n, State: "skipped", Detail: "same or unknown model maker"})
			continue
		}
		if blocked(s, pool) || (skipClaude && (p.Agent == "claude" || pool == "claude")) {
			d.Attempts = append(d.Attempts, providers.Result{Name: n, State: "skipped", Detail: "pool unavailable"})
			continue
		}
		pending := *d
		pending.Provider = n
		pending.Agent = p.Agent
		pending.Model = p.Model
		pending.Launch = p.Launch
		pending.Maker = maker
		pending.Pool = pool
		pending.Attempts = append(append([]providers.Result(nil), d.Attempts...), providers.Result{Name: n, State: "pending", Detail: "probe intent"})
		if err = record("routing_probe", &pending); err != nil {
			return nil, fmt.Errorf("probe ledger: %w", err)
		}
		r := providers.Probe(cfg, n, timeout)
		pending.Attempts[len(pending.Attempts)-1] = r
		if err = record("routing_probe_result", &pending); err != nil {
			return nil, fmt.Errorf("probe result ledger: %w", err)
		}
		d.Attempts = append(d.Attempts, r)
		if r.State == "quota" {
			s[pool] = Cooldown{Reason: "provider quota: " + n}
			if err = atomic(filepath.Join(dir(), "cooldowns.json"), s); err != nil {
				return nil, err
			}
		}
		if r.State == "up" {
			d.Provider = n
			d.Agent = p.Agent
			d.Model = p.Model
			d.Launch = p.Launch
			d.Maker = maker
			d.Pool = pool
			break
		}
	}
	if err = record("routing", d); err != nil {
		return nil, fmt.Errorf("routing ledger: %w", err)
	}
	if d.Provider == "" {
		return nil, errors.New("no eligible provider available")
	}
	if err = atomic(path, d); err != nil {
		return nil, err
	}
	return d, nil
}

// Validate fails closed on stale or mismatched launch identity; it does not spend quota probing.
func Validate(c *contract.Contract, run, agent, model string) (*Decision, error) {
	path, err := receipt(c)
	if err != nil {
		return nil, err
	}
	if run == "" || agent == "" || model == "" {
		return nil, errors.New("launch requires run, agent and model")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("prepare routing first: %w", err)
	}
	var d Decision
	if err = json.Unmarshal(b, &d); err != nil {
		return nil, err
	}
	if d.Task != c.Name || d.Run != run || d.Agent != agent || d.Model != model || d.Worktree != c.Worktree || d.ContractHash != contractHash(c) || d.HooksGeneration != c.HooksGeneration || d.PreparedAt.IsZero() || time.Since(d.PreparedAt) > 30*time.Minute || time.Until(d.PreparedAt) > time.Minute {
		return nil, errors.New("routing receipt stale or launch identity mismatch")
	}
	cfg, cp, ch, err := config(d.ConfigPath)
	if err != nil {
		return nil, err
	}
	if cp != d.ConfigPath || ch != d.ConfigHash {
		return nil, errors.New("routing config changed")
	}
	names, err := eligible(cfg, d.Chain, d.WorkerModel)
	if err != nil {
		return nil, err
	}
	found := false
	for _, n := range names {
		if n == d.Provider {
			found = true
		}
	}
	if !found {
		return nil, errors.New("provider outside chain")
	}
	p := cfg.Providers[d.Provider]
	maker, pool, err := identity(p)
	if err != nil {
		return nil, err
	}
	if p.Agent != d.Agent || p.Model != d.Model || p.Launch != d.Launch || maker != d.Maker || pool != d.Pool {
		return nil, errors.New("provider identity mismatch")
	}
	if strings.HasPrefix(d.Chain, "review:") && (maker == "unknown" || maker == modelMaker(d.WorkerModel)) {
		return nil, errors.New("review maker must differ")
	}
	if strings.HasPrefix(d.Chain, "worker:") && !hookInstalled(c, d.Agent) {
		return nil, errors.New("selected worker hook not installed")
	}
	s, err := state()
	if err != nil {
		return nil, err
	}
	if blocked(s, pool) {
		return nil, errors.New("selected quota pool unavailable")
	}
	return &d, nil
}
func RecordLaunch(d *Decision) error { return record("routing_launch", d) }

// SetCooldown records a known reset; nil means blocked until an explicit clear.
func SetCooldown(configPath, name, reason string, until *time.Time, clear bool) error {
	cfg, _, _, err := config(configPath)
	if err != nil {
		return err
	}
	p, ok := cfg.Providers[name]
	if !ok {
		return errors.New("unknown provider")
	}
	_, pool, err := identity(p)
	if err != nil {
		return err
	}
	if strings.TrimSpace(reason) == "" {
		return errors.New("cooldown and clear require reason")
	}
	unlock, err := lock()
	if err != nil {
		return err
	}
	defer unlock()
	s, err := state()
	if err != nil {
		return err
	}
	kind := "routing_cooldown"
	if clear {
		kind = "routing_clear"
	}
	notes, _ := json.Marshal(map[string]any{"scope": "quota pool outside task scope", "reason": reason, "until": until, "pool": pool})
	if err = ledger.Append(ledger.Row{Kind: kind, Provider: name, Model: p.Model, Purpose: pool, Notes: string(notes)}); err != nil {
		return fmt.Errorf("cooldown ledger: %w", err)
	}
	if clear {
		delete(s, pool)
	} else {
		s[pool] = Cooldown{Until: until, Reason: reason}
	}
	return atomic(filepath.Join(dir(), "cooldowns.json"), s)
}

// Status reads cooldown state without probing providers.
func Status() (map[string]Cooldown, error) { return state() }

func modelMaker(model string) string {
	parts := strings.Split(model, "/")
	return verdict.NormalizeModelMaker(parts[len(parts)-1])
}

func hookInstalled(c *contract.Contract, agent string) bool {
	vendor := agent
	if agent == "antigravity" {
		vendor = "agy"
	}
	if agent == "opencode2" {
		vendor = "opencode"
	}
	for _, v := range c.HooksInstalled {
		if v == vendor {
			return true
		}
	}
	return false
}
