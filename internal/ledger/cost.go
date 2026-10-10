package ledger

// Attributed cost evidence (docs/ROUTING_SELECTION_DESIGN.md, "Cost evidence and selection objective"). Every charge is one
// amount in one native unit of one pool, recorded once; units and pools are never merged, and a cost that is partial,
// conflicting or backed by no verified success is UNAVAILABLE, never zero.

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"reflect"
	"slices"
)

// Components a charge can belong to. shared and calibration never enter a production cohort's totals.
var Components = []string{"worker", "review", "repair", "probe", "fallback", "shared", "calibration"}

// Units a charge can be measured in. They are different currencies: USD, credits and quota percentages never add up.
var Units = []string{"usd", "kiro_credits", "agy_credits", "subscription_percent", "tokens"}

// workerComponents are the charges a cohort's cost per verified success is made of (review is priced per reviewer).
var workerComponents = []string{"worker", "repair", "probe", "fallback"}

// NewChargeRow builds a validated cost row. An empty chargeID gets a random one; pass a stable ID to make a retried
// recording idempotent.
func NewChargeRow(attempt, chargeID, component, pool, unit, provider, model string, amount float64) (Row, error) {
	switch {
	case attempt == "" || pool == "":
		return Row{}, fmt.Errorf("a charge needs an attempt and a pool")
	case !slices.Contains(Components, component):
		return Row{}, fmt.Errorf("component %q: want one of %v", component, Components)
	case !slices.Contains(Units, unit):
		return Row{}, fmt.Errorf("unit %q: want one of %v", unit, Units)
	case math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0:
		return Row{}, fmt.Errorf("amount %v: want finite >= 0", amount)
	}
	if chargeID == "" {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return Row{}, err
		}
		chargeID = hex.EncodeToString(b[:])
	}
	return Row{Kind: "cost", Attempt: attempt, ChargeID: chargeID, Component: component, Pool: pool, Unit: unit,
		Provider: provider, Model: model, Amount: &amount}, nil
}

// sameCharge ignores recording time and prose, but preserves every accounting and cohort field.
func sameCharge(a, b Row) bool {
	a.RecordedAt, b.RecordedAt = "", ""
	a.Notes, b.Notes = "", ""
	return reflect.DeepEqual(a, b)
}

func validateCharges(rows []Row) error {
	seen := map[[2]string]Row{}
	for _, r := range rows {
		if r.Kind != "cost" || r.ChargeID == "" {
			continue
		}
		k := [2]string{r.Attempt, r.ChargeID}
		if first, ok := seen[k]; ok && !sameCharge(first, r) {
			return fmt.Errorf("conflicting duplicate charge %q of attempt %q", r.ChargeID, r.Attempt)
		}
		seen[k] = r
	}
	return nil
}

type charge struct {
	row *Row
	amt float64
}

// charges returns the valid, deduplicated charge rows; conflicted lists attempts that recorded one ChargeID with different content.
// Bound-charged rows (Approx) count: they are the conservative figure when usage is unknown, and are reported as such by the caller.
func charges(rows []Row, o EvidenceOptions) (out []charge, conflicted map[string]bool) {
	conflicted = map[string]bool{}
	seen := map[[2]string]*Row{}
	for i := range rows {
		r := &rows[i]
		if r.Kind != "cost" || r.Attempt == "" || r.Attempt == o.ExcludeAttempt || r.ChargeID == "" || r.Pool == "" || r.Source == "memory" ||
			r.Amount == nil || math.IsNaN(*r.Amount) || math.IsInf(*r.Amount, 0) || *r.Amount < 0 ||
			!slices.Contains(Units, r.Unit) || !slices.Contains(Components, r.Component) {
			continue
		}
		k := [2]string{r.Attempt, r.ChargeID}
		if first := seen[k]; first != nil {
			if !sameCharge(*first, *r) {
				conflicted[r.Attempt] = true
			}
			continue
		}
		seen[k] = r
		out = append(out, charge{r, *r.Amount})
	}
	return out, conflicted
}

// CostEvidence is the attributed cost of one worker cohort: every settled attempt, failed or not, over verified successes.
type CostEvidence struct {
	Key        CohortKey          `json:"key"`
	Attempts   int                `json:"attempts"`
	Successes  int                `json:"successes"`
	Totals     map[string]float64 `json:"totals"`      // pool -> total over ALL settled attempts (worker, repair, probe, fallback)
	Units      map[string]string  `json:"units"`       // pool -> unit
	MaxAttempt map[string]float64 `json:"max_attempt"` // pool -> largest single-attempt total
	PerSuccess map[string]float64 `json:"per_success,omitempty"`
	Approx     bool               `json:"approx,omitempty"` // some counted charge was made at a reserved bound, not measured
	Available  bool               `json:"available"`
	Reason     string             `json:"reason,omitempty"`
	Hash       string             `json:"hash"`
}

// EvaluateWorkerCost sums the cohort's attributed charges. Review, shared and calibration charges are not part of it.
func EvaluateWorkerCost(rows []Row, key CohortKey, o EvidenceOptions) CostEvidence {
	ev := CostEvidence{Key: key, Totals: map[string]float64{}, Units: map[string]string{}, MaxAttempt: map[string]float64{}}
	all, conflicted := charges(rows, o)
	byAttempt := map[string][]charge{}
	for _, c := range all {
		byAttempt[c.row.Attempt] = append(byAttempt[c.row.Attempt], c)
	}
	var reasons []string
	why := func(s string) {
		if !slices.Contains(reasons, s) {
			reasons = append(reasons, s)
		}
	}
	for _, a := range Attempts(rows, o) {
		if a.Key != key {
			continue
		}
		ev.Attempts++
		if a.Success {
			ev.Successes++
		}
		if conflicted[a.ID] {
			why("conflicting duplicate charge")
		}
		perPool, hasWorker := map[string]float64{}, false
		for _, c := range byAttempt[a.ID] {
			if !slices.Contains(workerComponents, c.row.Component) {
				continue
			}
			hasWorker = hasWorker || c.row.Component == "worker"
			if u, ok := ev.Units[c.row.Pool]; ok && u != c.row.Unit {
				why("unit conflict")
			}
			ev.Units[c.row.Pool] = c.row.Unit
			perPool[c.row.Pool] += c.amt
			ev.Totals[c.row.Pool] += c.amt
			ev.Approx = ev.Approx || c.row.Approx
		}
		if !hasWorker {
			why("attempt " + a.ID + " has no worker charge")
		}
		for pool, v := range perPool {
			ev.MaxAttempt[pool] = math.Max(ev.MaxAttempt[pool], v)
		}
	}
	switch {
	case ev.Attempts == 0:
		why("no settled attempts")
	case ev.Successes == 0:
		why("no verified success")
	}
	ev.Available = len(reasons) == 0
	if ev.Available {
		ev.PerSuccess = map[string]float64{}
		for pool, t := range ev.Totals {
			ev.PerSuccess[pool] = t / float64(ev.Successes)
		}
	} else {
		ev.Reason = joinReasons(reasons)
	}
	ev.Hash = hashJSON(ev)
	return ev
}

func joinReasons(r []string) string {
	s := ""
	for i, x := range r {
		if i > 0 {
			s += "; "
		}
		s += x
	}
	return s
}

// ReviewCostEvidence is what one reviewer provider (an entry of fallback-chain.json: one harness, model, effort and billing pool)
// has cost per review charge.
type ReviewCostEvidence struct {
	Provider  string             `json:"provider"`
	Effort    string             `json:"effort"`
	Config    string             `json:"config"`
	Model     string             `json:"model"`
	Reviews   int                `json:"reviews"`
	PerReview map[string]float64 `json:"per_review"` // pool -> mean charge
	MaxReview map[string]float64 `json:"max_review"` // pool -> largest single charge
	Units     map[string]string  `json:"units"`
	Approx    bool               `json:"approx,omitempty"`
	Available bool               `json:"available"`
	Reason    string             `json:"reason,omitempty"`
	Hash      string             `json:"hash"`
}

// EvaluateReviewCost averages the in-window "review" charges of one reviewer provider. A charge counts only when it names that
// provider, model, effort and configuration. Unattributed or older configuration charges cannot price the current reviewer.
func EvaluateReviewCost(rows []Row, provider, model, effort, config string, o EvidenceOptions) ReviewCostEvidence {
	ev := ReviewCostEvidence{Provider: provider, Model: model, Effort: effort, Config: config, PerReview: map[string]float64{}, MaxReview: map[string]float64{}, Units: map[string]string{}}
	all, conflicted := charges(rows, o)
	count, sum := map[string]int{}, map[string]float64{}
	var reasons []string
	for _, c := range all {
		r := c.row
		if r.Component != "review" || r.Model != model || r.Provider != provider || config == "" || r.Config != config || r.Effort != effort {
			continue
		}
		if _, ok := inWindow(r.RecordedAt, o); !ok {
			continue
		}
		if conflicted[r.Attempt] && !slices.Contains(reasons, "conflicting duplicate charge") {
			reasons = append(reasons, "conflicting duplicate charge")
		}
		if u, ok := ev.Units[r.Pool]; ok && u != r.Unit && !slices.Contains(reasons, "unit conflict") {
			reasons = append(reasons, "unit conflict")
		}
		ev.Units[r.Pool] = r.Unit
		ev.Reviews++
		count[r.Pool]++
		sum[r.Pool] += c.amt
		ev.MaxReview[r.Pool] = math.Max(ev.MaxReview[r.Pool], c.amt)
		ev.Approx = ev.Approx || r.Approx
	}
	for pool, n := range count {
		ev.PerReview[pool] = sum[pool] / float64(n)
	}
	if ev.Reviews == 0 {
		reasons = append(reasons, "no review charges")
	}
	ev.Available = len(reasons) == 0
	ev.Reason = joinReasons(reasons)
	ev.Hash = hashJSON(ev)
	return ev
}

// SharedOverhead totals the "shared" charges (discovery, routing) of an attempt. They are reported once, with the decision,
// and are not part of any candidate's cohort cost, so they cannot be charged twice.
func SharedOverhead(rows []Row, attempt string) (totals map[string]float64, units map[string]string) {
	totals, units = map[string]float64{}, map[string]string{}
	all, _ := charges(rows, EvidenceOptions{})
	for _, c := range all {
		if c.row.Attempt == attempt && c.row.Component == "shared" {
			totals[c.row.Pool] += c.amt
			units[c.row.Pool] = c.row.Unit
		}
	}
	return totals, units
}
