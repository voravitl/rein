package routing

// Closing the reservations of an attempt, and the bounded calibration gate. Reserved amounts are worst cases: settling
// charges the probe at its declared bound, treats a pool as measured only when a measured, non-zero charge of the attempt
// says so, and charges the reserved bound for everything else that ran. What never ran is released, not charged.

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/voravitl/rein/internal/budget"
	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/ledger"
)

const CodeCalibrationExhausted = "calibration_cap_exhausted"

// settleKinds are the reserved item kinds whose usage the ledger can prove. A pool is measured for a kind when the attempt
// has a measured (not approximate), non-zero charge of one of the proof components in it. review_reserve is not here: it is
// capacity for reviews that reserve and are charged under their own attempts, so it is released, never charged.
var settleKinds = []struct {
	kind, component string
	proof           []string
}{
	{"funding", "worker", []string{"worker", "repair", "fallback"}},
	{"review_funding", "review", []string{"review"}},
	{"calibration", "calibration", []string{"calibration"}},
}

// usageComponents are the charges that count as the attempt's own usage in a settled hold's Used.
var usageComponents = []string{"worker", "repair", "fallback", "review", "calibration"}

// attemptRan reports whether anything executed under an attempt's receipt: a launch record, or the receipt's use marker.
func attemptRan(rows []ledger.Row, attempt string) bool {
	_, launched := ledger.LaunchRow(rows, attempt)
	return launched || ReceiptUsed(attempt)
}

// SettleAttempt closes the reservations still held for an attempt once its work and its charges are recorded, and ends the
// task lease an Orca-dispatched worker kept. It refuses while the attempt's worker is still running.
//
// Per pool: the probe is charged at its declared bound (its actual usage is unknown). A worker's own funding, a review's
// funding or a calibration call is released beyond the charges the ledger holds when a measured, non-zero charge of that kind
// exists in the pool; without one the whole reserved bound is charged, because unknown usage is never released as zero. A plan
// whose attempt never ran releases its funding uncharged. A zero or approximate charge is not proof of usage.
func SettleAttempt(attempt string) ([]budget.Hold, error) {
	holds, err := budget.HoldsFor(attempt)
	if err != nil {
		return nil, err
	}
	rows, err := ledger.LoadStrict("")
	if err != nil {
		return nil, fmt.Errorf("ledger unreadable: %w", err)
	}
	for _, h := range holds {
		for _, r := range rows {
			if r.Kind == "cost" && r.Attempt == attempt && (r.Run != h.Run || r.Task != h.Task) {
				return nil, fmt.Errorf("charge %s run/task contradicts reservation %s", r.ChargeID, h.ID)
			}
		}
	}
	ran := attemptRan(rows, attempt)
	have := map[string]map[string]float64{} // pool -> component -> every charge recorded for the attempt
	proven := map[string]map[string]bool{}  // pool -> component -> a measured, non-zero charge exists
	seen := map[string]bool{}
	for _, r := range rows {
		if r.Kind != "cost" || r.Attempt != attempt || r.Pool == "" || r.Amount == nil || r.ChargeID == "" || seen[r.ChargeID] {
			continue
		}
		seen[r.ChargeID] = true
		if have[r.Pool] == nil {
			have[r.Pool], proven[r.Pool] = map[string]float64{}, map[string]bool{}
		}
		have[r.Pool][r.Component] += *r.Amount
		if !r.Approx && *r.Amount > 0 {
			proven[r.Pool][r.Component] = true
		}
	}
	var out []budget.Hold
	for _, h := range holds {
		if h.State != "reserved" {
			continue
		}
		if err := EndLease(h.Task, attempt); err != nil {
			return out, err
		}
		unit, kinds := map[string]string{}, map[string]map[string]float64{}
		for _, it := range h.Items {
			unit[it.Pool] = it.Unit
			if kinds[it.Kind] == nil {
				kinds[it.Kind] = map[string]float64{}
			}
			kinds[it.Kind][it.Pool] += it.Amount
		}
		usage := map[string]float64{}
		for _, pool := range slices.Sorted(maps.Keys(unit)) {
			used := 0.0
			for _, c := range usageComponents {
				used += have[pool][c]
			}
			charge := func(kind, component string, amount float64, note string) error {
				id := h.ID + ":" + kind + ":" + pool
				if seen[id] {
					return nil // recorded by an earlier, interrupted settlement
				}
				row, err := ledger.NewChargeRow(attempt, id, component, pool, unit[pool], "", "", amount)
				if err != nil {
					return err
				}
				row.Run, row.Task, row.Decision, row.Approx, row.Notes = h.Run, h.Task, h.Decision, true, note
				if err := ledger.Append(row); err != nil {
					return fmt.Errorf("record %s charge: %w", kind, err)
				}
				used += amount
				return nil
			}
			if amt := kinds["probe"][pool]; amt > 0 {
				if err := charge("probe", "probe", amt, "charged at the declared probe bound: actual usage unknown"); err != nil {
					return out, err
				}
			}
			for _, k := range settleKinds {
				amt := kinds[k.kind][pool]
				switch {
				case amt == 0:
					continue
				case !ran && (k.kind == "funding" || k.kind == "review_funding"):
					continue // nothing executed under this receipt, so there is nothing to charge
				case slices.ContainsFunc(k.proof, func(c string) bool { return proven[pool][c] }):
					continue // measured: the ledger already holds the usage; the rest of the plan is released
				}
				if err := charge(k.kind, k.component, amt, "no measured "+k.component+" charge recorded: charged at the reserved bound"); err != nil {
					return out, err
				}
			}
			usage[pool] = used
		}
		done, err := budget.Settle(h.ID, usage)
		if err != nil {
			return out, err
		}
		out = append(out, *done)
	}
	return out, nil
}

// CalibrationInput asks for one bounded calibration call for a model that has no evidence yet.
type CalibrationInput struct {
	Contract   *contract.Contract
	Run        string
	ConfigPath string
	Owner      *contract.Profile
	Budget     *contract.Profile
	MarkerPath string
	Provider   string
	Amount     float64 // worst case of the call, in the provider's billing unit, declared by the calibration runner
	Calls      int
}

// ReserveCalibration reserves one calibration call atomically within the owner's fixed caps (calls, per-pool, cash). Discovery
// alone never reaches this: it needs the owner's standing authorization and caps in the selection policy. At exhaustion the model
// stays unqualified. Running the frozen fixtures and recording their evidence is the calibration runner's job, not this gate's.
func ReserveCalibration(in CalibrationInput) (*budget.Hold, error) {
	var sel *contract.Selection
	if in.Owner != nil {
		sel = in.Owner.EffectiveSelection()
	}
	if sel == nil {
		sel = in.Contract.Profile.EffectiveSelection()
	}
	if sel == nil || sel.Calibration == nil {
		return nil, refuse(CodeSelectionPolicy, "calibration is not authorized: the owner's selection policy has no calibration caps and standing authorization")
	}
	if problems := sel.Problems(); len(problems) > 0 {
		return nil, refuse(CodeSelectionPolicy, "%s", problems[0])
	}
	cfg, _, _, err := config(in.ConfigPath)
	if err != nil {
		return nil, err
	}
	p, ok := cfg.Providers[in.Provider]
	if !ok {
		return nil, fmt.Errorf("unknown provider %q", in.Provider)
	}
	if why := billingProblem(p); why != "" {
		return nil, refuse(CodeSelectionPolicy, "provider %s: %s", in.Provider, why)
	}
	if in.Calls < 1 {
		in.Calls = 1
	}
	prof := in.Budget
	if prof == nil {
		prof = &in.Contract.Profile
	}
	cal := sel.Calibration
	hold, err := budget.Reserve(in.MarkerPath, prof, budget.ReserveRequest{Run: in.Run, Task: in.Contract.Name, Attempt: newID("cal"),
		Items:       []budget.Item{{Pool: p.Billing.Pool, Unit: p.Billing.Unit, Amount: in.Amount, Calls: in.Calls, Kind: "calibration"}},
		Calibration: &budget.CalibrationCaps{MaxCalls: cal.MaxCalls, PoolCaps: cal.PoolCaps, MaxCashUSD: *cal.MaxCashUSD}})
	var short *budget.ErrInsufficient
	if errors.As(err, &short) {
		return nil, refuse(CodeCalibrationExhausted, "%v; the model stays unqualified", short)
	}
	return hold, err
}
