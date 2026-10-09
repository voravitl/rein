package routing

import (
	"testing"

	"github.com/voravitl/rein/internal/providers"
)

// The shipped example is what owners copy: every provider must be a complete automatic candidate, and the chains must name real providers.
func TestShippedExampleConfigIsACompleteAutomaticConfiguration(t *testing.T) {
	cfg, err := providers.Load("../../examples/fallback-chain.example.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, p := range cfg.Providers {
		if why := billingProblem(p); why != "" {
			t.Errorf("%s: %s", name, why)
		}
		if why := boundProblem(p); why != "" {
			t.Errorf("%s: %s", name, why)
		}
		if len(p.Capabilities) == 0 || p.ContextTokens <= 0 {
			t.Errorf("%s: capabilities and context_tokens are required for automatic selection", name)
		}
		if _, _, err := identity(p); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := poolModes(cfg); err != nil {
		t.Errorf("one pool must have one mode and one unit: %v", err)
	}
	for chain, names := range cfg.Chains() {
		for _, n := range names {
			if _, ok := cfg.Providers[n]; !ok {
				t.Errorf("%s names unknown provider %s", chain, n)
			}
		}
	}
	for _, policy := range []string{"high-risk", "backend", "frontend", "ordinary", "mechanical", "docs"} {
		if len(cfg.WorkerChains[policy]) == 0 {
			t.Errorf("no candidate set for policy %s", policy)
		}
	}
}
