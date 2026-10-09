package routing

// Qualification, cost comparability and ranking for automatic selection (docs/ROUTING_SELECTION_DESIGN.md, "Quality score and
// eligibility" and "Cost evidence and selection objective"). Pure functions: no I/O, so every rule here is directly testable.

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/ledger"
)

// qualifyWorker applies the owner's floors to a worker cohort. "" means qualified. insufficient says the model failed for want of
// evidence (thin or incomplete data), as opposed to a record that is bad: only the former may fall back to an explicit baseline.
func qualifyWorker(ev ledger.WorkerEvidence, p contract.WorkerPolicy) (why string, insufficient bool) {
	switch {
	case ev.Settled == 0:
		return fmt.Sprintf("no settled attempts, need %d", p.MinCompleteAttempts), true
	case ev.Coverage < 1:
		return fmt.Sprintf("coverage %.0f%% < 100%%: some outcome, review or defect fields are unknown", ev.Coverage*100), true
	case ev.Complete < p.MinCompleteAttempts:
		return fmt.Sprintf("thin data: %d complete attempts, need %d", ev.Complete, p.MinCompleteAttempts), true
	case !ev.FalseClaimsKnown:
		return "false-claim assessments are unknown", true
	case p.MaxFalseClaims == nil:
		return "max_false_claims not configured", true
	case ev.FalseClaims > *p.MaxFalseClaims:
		return fmt.Sprintf("%d false claims > allowed %d", ev.FalseClaims, *p.MaxFalseClaims), false
	case ev.Lower95 < p.MinApprovalRate:
		return fmt.Sprintf("Q=%.1f (95%% Wilson lower bound %.3f) below floor %.3f", ev.Q, ev.Lower95, p.MinApprovalRate), false
	}
	return "", false
}

// qualifyReviewer applies the separate reviewer calibration floor to fixture evidence.
func qualifyReviewer(ev ledger.ReviewerEvidence, p contract.ReviewerPolicy) (why string, insufficient bool) {
	switch {
	case ev.Rows == 0:
		return fmt.Sprintf("no fixture judgements: need %d defect and %d clean fixtures", p.MinDefectFixtures, p.MinCleanFixtures), true
	case ev.Coverage < 1:
		return fmt.Sprintf("fixture coverage %.0f%% < 100%%: some rows are unadjudicated or incomplete", ev.Coverage*100), true
	case ev.DefectFixtures < p.MinDefectFixtures || ev.CleanFixtures < p.MinCleanFixtures:
		return fmt.Sprintf("thin fixture data: %d defect (need %d), %d clean (need %d)", ev.DefectFixtures, p.MinDefectFixtures, ev.CleanFixtures, p.MinCleanFixtures), true
	case p.MaxFalsePositiveRate == nil:
		return "max_false_positive_rate not configured", true
	case ev.RecallLower < p.MinRecall:
		return fmt.Sprintf("defect recall lower bound %.3f below %.3f", ev.RecallLower, p.MinRecall), false
	case ev.SpecificityLower < p.MinSpecificity:
		return fmt.Sprintf("specificity lower bound %.3f below %.3f", ev.SpecificityLower, p.MinSpecificity), false
	case ev.FalsePositiveRate > *p.MaxFalsePositiveRate:
		return fmt.Sprintf("false-positive rate %.3f above %.3f", ev.FalsePositiveRate, *p.MaxFalsePositiveRate), false
	}
	return "", false
}

// costVec is a cost in native units. Pools are never merged and a pool keeps one unit: USD, Kiro credits, agy credits and
// subscription percentages are different currencies.
type costVec struct {
	Amount map[string]float64 `json:"amount"`
	Unit   map[string]string  `json:"unit"`
}

func newVec() costVec { return costVec{map[string]float64{}, map[string]string{}} }

func (v costVec) add(pool, unit string, amt float64) error {
	if u, ok := v.Unit[pool]; ok && u != unit {
		return fmt.Errorf("pool %q is measured in both %s and %s", pool, u, unit)
	}
	v.Unit[pool] = unit
	v.Amount[pool] += amt
	return nil
}

func (v costVec) addAll(amounts map[string]float64, units map[string]string, factor float64) error {
	for pool, a := range amounts {
		if err := v.add(pool, units[pool], a*factor); err != nil {
			return err
		}
	}
	return nil
}

// single reports the pool and amount of a one-pool cost.
func (v costVec) single() (string, float64, bool) {
	if len(v.Amount) != 1 {
		return "", 0, false
	}
	for p, a := range v.Amount {
		return p, a, true
	}
	return "", 0, false
}

// basis reduces costs of different pools to one comparable number, but only as the owner declared: measured monetary marginal
// cost, or an explicit pool conversion with provenance. Without a declaration nothing is converted.
type basis struct {
	cfg      *contract.CostBasis
	poolMode map[string]string // pool -> billing mode (api|subscription|credits|free)
	now      time.Time
}

// value is the cost under the declared basis; ok=false (with why) when this cost cannot be expressed in it. A known zero cash
// price (subscription, free) is a value of zero, but still has to pass the quota and budget gates elsewhere.
func (b basis) value(v costVec) (val float64, ok bool, why string) {
	if b.cfg == nil {
		return 0, false, "no cost basis declared"
	}
	pools := make([]string, 0, len(v.Amount))
	for p := range v.Amount {
		pools = append(pools, p)
	}
	sort.Strings(pools)
	for _, pool := range pools {
		amt := v.Amount[pool]
		switch b.cfg.Mode {
		case "monetary_marginal":
			switch {
			case v.Unit[pool] == "usd":
				val += amt
			case b.poolMode[pool] == "subscription" || b.poolMode[pool] == "free":
			default:
				return 0, false, fmt.Sprintf("pool %s (%s) has no measured monetary cost", pool, v.Unit[pool])
			}
		case "converted":
			c, found := b.cfg.Conversions[pool]
			if !found {
				return 0, false, "no conversion configured for pool " + pool
			}
			if exp := conversionExpiry(c); b.now.After(exp) {
				return 0, false, fmt.Sprintf("conversion for pool %s expired %s", pool, exp.Format(time.RFC3339))
			}
			val += amt * c.PerUnit
		default:
			return 0, false, "unknown cost basis mode"
		}
	}
	return val, true, ""
}

func conversionExpiry(c contract.Conversion) time.Time {
	asOf, err := time.Parse(time.RFC3339, c.AsOf)
	if err != nil {
		return time.Time{}
	}
	return asOf.AddDate(0, 0, c.ValidDays)
}

// expiry is the earliest moment a conversion used by v stops being valid (zero when none is used).
func (b basis) expiry(v costVec) time.Time {
	var first time.Time
	if b.cfg == nil || b.cfg.Mode != "converted" {
		return first
	}
	for pool := range v.Amount {
		if c, ok := b.cfg.Conversions[pool]; ok {
			if e := conversionExpiry(c); first.IsZero() || e.Before(first) {
				first = e
			}
		}
	}
	return first
}

// rankItem is what ranking sees of a candidate.
type rankItem struct {
	ID      string  // stable provider/model identity: the last tie-break
	Q       float64 // quality lower bound (percent)
	Latency float64 // observed repair latency, minutes; +Inf when unknown
	Cost    costVec
}

func near(a, b float64) bool {
	return math.Abs(a-b) <= 1e-9*math.Max(1, math.Max(math.Abs(a), math.Abs(b)))
}

// rank orders items by the declared objective and never invents a cost.
//
//	balanced:      hard gates were applied by the caller; then lowest COMPARABLE cost, ties by higher quality lower bound,
//	               lower repair latency, then stable ID. Several candidates whose costs cannot be compared (different pools and
//	               no declared basis) is an error, cost_basis_required, not a guess. Under a declared basis, an item whose cost
//	               cannot be expressed in it is dropped, so an unknown cost is never the cheapest.
//	quality_first: quality lower bound first, then comparable cost (unknown last), latency, ID.
func rank(objective string, items []rankItem, b basis) (order []int, dropped map[int]string, err error) {
	dropped = map[int]string{}
	val := make([]float64, len(items))
	okv := make([]bool, len(items))
	var alive []int
	for i, it := range items {
		if b.cfg != nil {
			v, ok, why := b.value(it.Cost)
			val[i], okv[i] = v, ok
			if !ok && objective == "balanced" {
				dropped[i] = "cost_basis_unknown: " + why
				continue
			}
		}
		alive = append(alive, i)
	}
	if b.cfg == nil {
		// no declared basis: costs compare only when every candidate draws on the same single pool
		same, pool := len(alive) > 0, ""
		for n, i := range alive {
			p, _, one := items[i].Cost.single()
			if !one || (n > 0 && p != pool) {
				same = false
				break
			}
			pool = p
		}
		if same {
			for _, i := range alive {
				_, val[i], _ = items[i].Cost.single()
				okv[i] = true
			}
		} else if objective == "balanced" && len(alive) > 1 {
			return nil, dropped, refuse(CodeCostBasis, "the %d eligible candidates draw on different or multiple pools and no cost basis is declared: set selection.cost_basis (monetary_marginal, or converted with provenance) or narrow the candidates", len(alive))
		}
	}
	lat := func(i int) float64 {
		if items[i].Latency <= 0 || math.IsNaN(items[i].Latency) {
			return math.Inf(1)
		}
		return items[i].Latency
	}
	sort.SliceStable(alive, func(x, y int) bool {
		i, j := alive[x], alive[y]
		costFirst := func() (bool, bool) { // (decided, less)
			switch {
			case okv[i] && okv[j] && !near(val[i], val[j]):
				return true, val[i] < val[j]
			case okv[i] != okv[j]:
				return true, okv[i] // a known cost never loses to an unknown one
			}
			return false, false
		}
		qual := func() (bool, bool) {
			if !near(items[i].Q, items[j].Q) {
				return true, items[i].Q > items[j].Q
			}
			return false, false
		}
		first, second := costFirst, qual
		if objective == "quality_first" {
			first, second = qual, costFirst
		}
		if d, l := first(); d {
			return l
		}
		if d, l := second(); d {
			return l
		}
		if li, lj := lat(i), lat(j); li != lj {
			return li < lj
		}
		return items[i].ID < items[j].ID
	})
	return alive, dropped, nil
}
