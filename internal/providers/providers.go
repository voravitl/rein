// Package providers probes which model providers can take work now and picks the first available one per
// fallback chain. Config: fallback-chain.json (providers, worker_chains, review_chains, quota_signals).
package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type Provider struct {
	Maker  string   `json:"maker,omitempty"`
	Pool   string   `json:"pool,omitempty"`
	Agent  string   `json:"agent"`
	Model  string   `json:"model"`
	Launch string   `json:"launch"`
	Cost   string   `json:"cost"` // descriptive text only; scored selection reads Billing
	Probe  []string `json:"probe"`

	// Automatic selection (docs/ROUTING_SELECTION_DESIGN.md). All optional: ordered chains ignore them.
	Effort        string   `json:"effort,omitempty"`         // reasoning-effort variant this provider is launched with
	Capabilities  []string `json:"capabilities,omitempty"`   // e.g. repository-edit, tests, browser, image-input
	ContextTokens int      `json:"context_tokens,omitempty"` // usable context window; 0 = unknown
	Billing       *Billing `json:"billing,omitempty"`
	ProbeBound    *Bound   `json:"probe_bound,omitempty"`
}

// Billing keeps monetary marginal cost, subscription allocation and native quota consumption separate. USD, Kiro
// credits, agy credits and subscription-limit percentages are different units and are never added together.
type Billing struct {
	Mode     string `json:"mode"`                // api | subscription | credits | free
	Unit     string `json:"unit"`                // native unit of Pool: usd | kiro_credits | agy_credits | subscription_percent
	Pool     string `json:"pool"`                // provider/account/pool/window qualifier shared by every provider drawing on it
	PriceKey string `json:"price_key,omitempty"` // key in prices.json for api billing; default: the provider model
}

// Bound is the declared worst case of one availability probe in Billing.Unit. The probe command is single-shot
// and its whole process group is killed at the timeout, so Calls is enforced by rein; Amount is the owner's declared cap per call.
type Bound struct {
	Calls  int     `json:"calls"`
	Amount float64 `json:"amount"`
}

// DiscoverySpec pins the executable used for catalog discovery of one harness (argv prefix; adapter flags are appended).
type DiscoverySpec struct {
	Command []string `json:"command"`
}

type Config struct {
	Providers    map[string]Provider `json:"providers"`
	WorkerChains map[string][]string `json:"worker_chains"`
	ReviewChains map[string][]string `json:"review_chains"`
	QuotaSignals map[string][]string `json:"quota_signals"`
	// Discovery optionally overrides the executable per harness (codex, kiro, antigravity, opencode, opencode2, claude).
	Discovery map[string]DiscoverySpec `json:"discovery,omitempty"`
}

type Result struct {
	Name    string  `json:"name"`
	State   string  `json:"state"` // up | quota | down
	Detail  string  `json:"detail,omitempty"`
	Seconds float64 `json:"seconds"`
}

// ConfigPath: explicit flag > $PIPELINE_FALLBACK > <binary dir>/../config/fallback-chain.json > ~/.config/rein/fallback-chain.json.
func ConfigPath(flag string) string {
	if flag != "" {
		return flag
	}
	if p := os.Getenv("PIPELINE_FALLBACK"); p != "" {
		return p
	}
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), "..", "config", "fallback-chain.json")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "rein", "fallback-chain.json")
}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(c.Providers) == 0 {
		return nil, errors.New(path + ": no providers")
	}
	return &c, nil
}

var (
	ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)
	okRx = regexp.MustCompile(`(?m)^\s*>?\s*OK\.?\s*$`)
)

func probe(ctx context.Context, name string, p Provider, signals []string, timeout time.Duration) Result {
	start := time.Now()
	if len(p.Probe) == 0 {
		return Result{Name: name, State: "down", Detail: "no probe command"}
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, p.Probe[0], p.Probe[1:]...)
	setGroup(cmd) // a wrapper (sh -c, npx, uv run, timeout) must not leave a descendant spending after the bound has expired
	cmd.Cancel = func() error { killGroup(cmd); return nil }
	cmd.WaitDelay = 2 * time.Second // a killed wrapper's children may hold the output pipes; do not wait for them
	if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
	}
	out, err := cmd.CombinedOutput()
	killGroup(cmd) // descendants that outlived a probe which already answered
	secs := time.Since(start).Seconds()
	text := ansi.ReplaceAllString(string(out), "")
	var execErr *exec.Error
	switch {
	case errors.As(err, &execErr):
		return Result{name, "down", "CLI not installed", 0}
	case cctx.Err() == context.DeadlineExceeded:
		return Result{name, "down", fmt.Sprintf("timeout %s", timeout), secs}
	}
	// Success first: a healthy kiro answer also prints "Credits: N", which must not read as a credit problem.
	if err == nil && okRx.MatchString(text) {
		return Result{name, "up", "", secs}
	}
	low := strings.ToLower(text)
	hit := ""
	for _, s := range signals {
		if strings.Contains(low, strings.ToLower(s)) {
			hit = s
			break
		}
	}
	if code := cmd.ProcessState; p.Agent == "antigravity" && code != nil && code.ExitCode() == 3 && hit == "" {
		hit = "exit 3"
	}
	last := lastLine(text)
	if hit != "" {
		return Result{name, "quota", "[" + hit + "] " + last, secs}
	}
	return Result{name, "down", last, secs}
}

func lastLine(s string) string {
	ls := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(ls) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(ls[i]); l != "" {
			if len(l) > 140 {
				l = l[:140]
			}
			return l
		}
	}
	return "no output"
}

// Check probes the named providers in parallel.
func Check(c *Config, names []string, timeout time.Duration) map[string]Result {
	out := map[string]Result{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, n := range names {
		p, ok := c.Providers[n]
		if !ok {
			continue
		}
		wg.Add(1)
		go func(n string, p Provider) {
			defer wg.Done()
			r := probe(context.Background(), n, p, c.QuotaSignals[p.Agent], timeout)
			if r.State == "down" && strings.HasPrefix(r.Detail, "timeout") {
				// one slow answer must not drop a working provider from the chain: retry a timeout once
				r2 := probe(context.Background(), n, p, c.QuotaSignals[p.Agent], timeout)
				r2.Seconds += r.Seconds
				if r2.State == "down" && strings.HasPrefix(r2.Detail, "timeout") {
					r2.Detail += " (twice)"
				}
				r = r2
			}
			mu.Lock()
			out[n] = r
			mu.Unlock()
		}(n, p)
	}
	wg.Wait()
	return out
}

// Chains returns "worker:<k>" / "review:<k>" -> ordered provider names.
func (c *Config) Chains() map[string][]string {
	all := map[string][]string{}
	for k, v := range c.WorkerChains {
		all["worker:"+k] = v
	}
	for k, v := range c.ReviewChains {
		all["review:"+k] = v
	}
	return all
}

// Pick returns the first provider that is up in each chain that had at least one tested member.
func Pick(c *Config, results map[string]Result, only string) map[string]string {
	picks := map[string]string{}
	for chain, order := range c.Chains() {
		if only != "" && chain != only {
			continue
		}
		tested := false
		for _, n := range order {
			r, ok := results[n]
			if !ok {
				continue
			}
			tested = true
			if r.State == "up" {
				picks[chain] = n
				break
			}
		}
		if tested {
			if _, ok := picks[chain]; !ok {
				picks[chain] = ""
			}
		}
	}
	return picks
}

func Print(w io.Writer, c *Config, names []string, results map[string]Result, picks map[string]string) {
	tag := map[string]string{"up": "UP   ", "quota": "QUOTA", "down": "DOWN "}
	for _, n := range names {
		r, ok := results[n]
		if !ok {
			continue
		}
		p := c.Providers[n]
		fmt.Fprintf(w, "%s %-18s %-48s %5.1fs  %s\n", tag[r.State], n, p.Agent+":"+p.Model, r.Seconds, p.Cost)
		if r.Detail != "" {
			fmt.Fprintf(w, "        %s\n", r.Detail)
		}
	}
	fmt.Fprintln(w)
	keys := make([]string, 0, len(picks))
	for k := range picks {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := picks[k]
		if v == "" {
			if strings.HasPrefix(k, "review:") {
				v = "NONE AVAILABLE: pause reviews and ask the user (a review never falls back to a weaker model)"
			} else {
				v = "NONE AVAILABLE: pause this task type and ask the user"
			}
		}
		fmt.Fprintf(w, "%-45s -> %s\n", k, v)
	}
}

// Probe checks one provider without probing unused fallback candidates.
func Probe(c *Config, name string, timeout time.Duration) Result {
	p, ok := c.Providers[name]
	if !ok {
		return Result{Name: name, State: "down", Detail: "unknown provider"}
	}
	return probe(context.Background(), name, p, c.QuotaSignals[p.Agent], timeout)
}
