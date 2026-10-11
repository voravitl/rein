package routing

// Evidence binding of automatic receipts. A receipt is the only permission to launch, so everything it rests on is hashed into
// it at preparation and recomputed at launch: a changed policy, price, quality record, task profile, catalog snapshot or
// contract invalidates it. Receipt expiry is the earliest expiry of any evidence it uses.

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

	"github.com/voravitl/rein/internal/budget"
	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/ledger"
	"github.com/voravitl/rein/internal/providers"
)

// qualityHashFor hashes the settled evidence a receipt rests on, recomputed from the ledger as it is now: the chosen provider's
// worker (or reviewer) cohort and attributed cost, and for a worker the cohorts and review costs of the reviewers its forecast
// is bound to. The attempt itself is excluded, so launching never invalidates its own receipt.
func qualityHashFor(rows []ledger.Row, d *Decision, cfg *providers.Config, pol *contract.Selection, now time.Time) (string, error) {
	p, ok := cfg.Providers[d.Provider]
	if !ok {
		return "", fmt.Errorf("provider %q is not configured", d.Provider)
	}
	key := func(p providers.Provider) ledger.CohortKey {
		return ledger.CohortKey{Agent: p.Agent, Model: p.Model, Effort: d.Effort, Config: d.ConfigFingerprint, Kind: d.Kind, Tier: d.Tier, Suite: d.Suite}
	}
	opts := func(days int) ledger.EvidenceOptions {
		return ledger.EvidenceOptions{Now: now, MaxAge: time.Duration(days) * 24 * time.Hour, Alive: procAlive, Maker: modelMaker, ExcludeAttempt: d.Attempt}
	}
	h := sha256.New()
	add := func(parts ...string) {
		for _, s := range parts {
			h.Write([]byte(s))
			h.Write([]byte{0})
		}
	}
	reviewer := func(name string, rp providers.Provider, effort, fp string) {
		o := opts(pol.Reviewer.MaxEvidenceAgeDays)
		k := ledger.CohortKey{Agent: rp.Agent, Model: rp.Model, Effort: effort, Config: fp, Kind: d.Kind, Tier: d.Tier, Suite: d.Suite}
		add(ledger.EvaluateReviewer(rows, k, o).Hash, ledger.EvaluateReviewCost(rows, name, rp.Model, effort, fp, o).Hash)
	}
	if strings.HasPrefix(d.Chain, "review:") {
		reviewer(d.Provider, p, d.Effort, d.ConfigFingerprint)
		return hex.EncodeToString(h.Sum(nil)), nil
	}
	o := opts(pol.Worker.MaxEvidenceAgeDays)
	add(ledger.EvaluateWorker(rows, key(p), o).Hash, ledger.EvaluateWorkerCost(rows, key(p), o).Hash)
	for _, name := range d.ReviewerPlan {
		rp, ok := cfg.Providers[name]
		if !ok {
			return "", fmt.Errorf("planned reviewer %q is not configured", name)
		}
		reviewer(name, rp, rp.Effort, fingerprint(rp))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// pricingHashFor hashes the billing, probe bound, public rates and cost-basis conversions behind a receipt's cost forecast.
func pricingHashFor(cfg *providers.Config, prices map[string]ledger.Rates, pol *contract.Selection, d *Decision) string {
	type entry struct {
		Name    string             `json:"name"`
		Billing *providers.Billing `json:"billing,omitempty"`
		Bound   *providers.Bound   `json:"bound,omitempty"`
	}
	var entries []entry
	var keys []string
	for _, name := range append([]string{d.Provider}, d.ReviewerPlan...) {
		p := cfg.Providers[name]
		entries = append(entries, entry{name, p.Billing, p.ProbeBound})
		if p.Billing != nil && p.Billing.Mode == "api" {
			k := p.Billing.PriceKey
			if k == "" {
				k = p.Model
			}
			keys = append(keys, k)
		}
	}
	b, _ := json.Marshal(struct {
		Entries []entry             `json:"entries"`
		Rates   string              `json:"rates"`
		Basis   *contract.CostBasis `json:"basis"`
	}{entries, ledger.RatesHash(prices, keys), pol.CostBasis})
	return hashBytes(b)
}

// resolvePolicy applies the same precedence as preparation: the owner's profile (REIN_PROFILE) wins over the contract's copy.
func resolvePolicy(c *contract.Contract) (*contract.Selection, *contract.Profile, error) {
	owner, err := contract.LoadProfile("")
	if err != nil {
		return nil, nil, err
	}
	sel := owner.EffectiveSelection()
	if sel == nil {
		sel = c.Profile.EffectiveSelection()
	}
	if sel == nil || len(sel.Problems()) > 0 {
		return nil, nil, errors.New("selection policy missing or invalid")
	}
	return sel, owner, nil
}

// validateAuto recomputes everything an automatic receipt was bound to. It spends no quota and runs no provider.
func validateAuto(c *contract.Contract, d *Decision, cfg *providers.Config) error {
	now := time.Now()
	if (d.SelectionMode != modeScored && d.SelectionMode != modeBaseline) || d.Attempt == "" || d.Hold == "" || d.ExpiresAt == nil {
		return errors.New("incomplete automatic routing receipt")
	}
	if now.After(*d.ExpiresAt) {
		return errors.New("routing evidence expired; prepare again")
	}
	sel, owner, err := resolvePolicy(c)
	if err != nil {
		return err
	}
	if sel.Hash() != d.PolicyHash {
		return errors.New("selection policy changed since preparation")
	}
	raw, err := os.ReadFile(filepath.Join(artifactDir(c.Name), "profile.json"))
	if err != nil || hashBytes(raw) != d.TaskProfileHash {
		return errors.New("task profile changed or missing since preparation")
	}
	tp, err := DecodeTaskProfile(raw)
	if err != nil {
		return err
	}
	cl, err := Classify(c, owner, tp)
	if err != nil {
		return fmt.Errorf("classification no longer holds: %w", err)
	}
	chain := "worker:" + cl.Policy
	if cl.Phase == "review" {
		chain = "review:auto"
	}
	if cl.Kind != d.Kind || cl.Tier != d.Tier || chain != d.Chain {
		return errors.New("classification changed since preparation")
	}
	snap, err := os.ReadFile(filepath.Join(artifactDir(c.Name), "inventory.json"))
	var invs []providers.Inventory
	if err != nil || json.Unmarshal(snap, &invs) != nil || providers.HashInventories(invs) != d.InventoryHash {
		return errors.New("catalog snapshot changed or missing since preparation")
	}
	rows, err := ledger.LoadStrict("")
	if err != nil {
		return fmt.Errorf("ledger unreadable: %w", err)
	}
	if q, err := qualityHashFor(rows, d, cfg, sel, now); err != nil || q != d.QualityHash {
		return errors.New("quality evidence changed since preparation (a new settled attempt, fixture or charge)")
	}
	prices, err := ledger.LoadRates(ledger.PricesPath())
	if err != nil {
		return err
	}
	if pricingHashFor(cfg, prices, sel, d) != d.PricingHash {
		return errors.New("pricing, billing or cost basis changed since preparation")
	}
	if cl.Phase == "review" {
		if revision, err := reviewRevision(c.Worktree, d.ReviewBase); err != nil || d.ReviewBase == "" || revision != d.ReviewRevision {
			return errors.New("review checkout changed since preparation; prepare again")
		}
		// the reviewer's independence rests on the worker that ACTUALLY ran
		if _, model, err := ledger.WorkerIdentity(rows, d.Run, d.Task); err != nil || model != d.WorkerModel {
			return errors.New("the worker identity proven by the ledger changed since preparation")
		}
	}
	holds, err := budget.HoldsFor(d.Attempt)
	if err != nil {
		return err
	}
	for _, h := range holds {
		if h.ID == d.Hold && h.State == "reserved" {
			return nil
		}
	}
	return errors.New("the reservation behind this receipt is no longer held")
}

// ManualPrepareAllowed refuses ordered-chain preparation once the owner configured automatic selection: the coordinator consumes
// the returned model unchanged and cannot override the scored ordering by passing a chain of its own. The owner can opt out with
// selection.allow_manual_chains. Receipt validation (route check, route launch) is not affected.
func ManualPrepareAllowed(c *contract.Contract) error {
	owner, err := contract.LoadProfile("")
	if err != nil {
		return err
	}
	sel := owner.EffectiveSelection()
	if sel == nil {
		sel = c.Profile.EffectiveSelection()
	}
	if sel != nil && !sel.AllowManualChains {
		return refuse(CodeSelectionPolicy, "automatic selection is configured for this project: use `rein route auto`; a hand-picked chain or model would bypass the scored ordering (the owner can set selection.allow_manual_chains)")
	}
	return nil
}
