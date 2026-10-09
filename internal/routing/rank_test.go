package routing

import (
	"strings"
	"testing"
	"time"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/ledger"
)

func vec(pairs ...any) costVec {
	v := newVec()
	for i := 0; i < len(pairs); i += 3 {
		_ = v.add(pairs[i].(string), pairs[i+1].(string), pairs[i+2].(float64))
	}
	return v
}

func ids(items []rankItem, order []int) string {
	var out []string
	for _, i := range order {
		out = append(out, items[i].ID)
	}
	return strings.Join(out, ",")
}

func TestRankObjectivesAndComparability(t *testing.T) {
	monetary := basis{cfg: &contract.CostBasis{Mode: "monetary_marginal"}, poolMode: map[string]string{"sub": "subscription", "free": "free", "kiro": "credits"}, now: time.Now()}
	items := []rankItem{
		{ID: "api-cheap", Q: 60, Cost: vec("api", "usd", 0.10)},
		{ID: "api-strong", Q: 90, Cost: vec("api", "usd", 0.50)},
		{ID: "sub", Q: 70, Cost: vec("sub", "subscription_percent", 5.0)},                // known zero cash price, quota is checked elsewhere
		{ID: "credits", Q: 99, Cost: vec("kiro", "kiro_credits", 1.0)},                   // no measured cash cost: never the cheapest
		{ID: "mixed", Q: 80, Cost: vec("api", "usd", 0.20, "kiro", "kiro_credits", 1.0)}, // one unknown part makes the whole unknown
	}
	order, dropped, err := rank("balanced", items, monetary)
	if err != nil || ids(items, order) != "sub,api-cheap,api-strong" || len(dropped) != 2 {
		t.Fatalf("balanced: %s dropped=%v err=%v", ids(items, order), dropped, err)
	}
	for i, why := range dropped {
		if !strings.HasPrefix(why, "cost_basis_unknown") {
			t.Errorf("%s: %s", items[i].ID, why)
		}
	}
	// quality-first keeps unknown-cost candidates (cost is not what orders them), best quality first
	order, dropped, err = rank("quality_first", items, monetary)
	if err != nil || ids(items, order) != "credits,api-strong,mixed,sub,api-cheap" || len(dropped) != 0 {
		t.Fatalf("quality_first: %s %v %v", ids(items, order), dropped, err)
	}

	// equal quality: a known cost never loses to an unknown one, whatever the order they came in
	tie := []rankItem{{ID: "unknown", Q: 80, Cost: vec("kiro", "kiro_credits", 0.01)}, {ID: "known", Q: 80, Cost: vec("api", "usd", 9.0)}, {ID: "zero", Q: 80, Cost: vec("sub", "subscription_percent", 3.0)}}
	if order, _, err = rank("quality_first", tie, monetary); err != nil || ids(tie, order) != "zero,known,unknown" {
		t.Errorf("quality tie: %s %v", ids(tie, order), err)
	}

	// with no declared basis only candidates drawing on the same single pool are comparable
	same := []rankItem{{ID: "b", Q: 50, Cost: vec("api", "usd", 2.0)}, {ID: "a", Q: 50, Cost: vec("api", "usd", 1.0)}}
	if order, _, err = rank("balanced", same, basis{}); err != nil || ids(same, order) != "a,b" {
		t.Errorf("same pool: %s %v", ids(same, order), err)
	}
	other := append(same, rankItem{ID: "c", Q: 50, Cost: vec("kiro", "kiro_credits", 0.1)})
	if _, _, err = rank("balanced", other, basis{}); ErrCode(err) != CodeCostBasis {
		t.Errorf("USD and credits cannot be ranked against each other without a declared basis: %v", err)
	}
	multi := []rankItem{{ID: "x", Cost: vec("a", "usd", 1.0, "b", "usd", 1.0)}, {ID: "y", Cost: vec("a", "usd", 0.5, "b", "usd", 0.5)}}
	if _, _, err = rank("balanced", multi, basis{}); ErrCode(err) != CodeCostBasis {
		t.Errorf("a multi-pool cost is not one number: %v", err)
	}
	if order, _, err = rank("balanced", multi[:1], basis{}); err != nil || len(order) != 1 {
		t.Errorf("a lone candidate needs no comparison: %v", err)
	}
	if order, _, err = rank("quality_first", other, basis{}); err != nil || len(order) != 3 {
		t.Errorf("quality-first never needs a cost basis: %v", err)
	}
	if order, _, err = rank("balanced", nil, basis{}); err != nil || len(order) != 0 {
		t.Errorf("nothing to rank: %v", err)
	}
}

func TestConvertedBasisNeedsEveryPoolAndAFreshConversion(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	fresh := contract.Conversion{PerUnit: 0.5, Source: "owner", AsOf: now.Add(-24 * time.Hour).Format(time.RFC3339), ValidDays: 30}
	b := basis{cfg: &contract.CostBasis{Mode: "converted", Unit: "u", Conversions: map[string]contract.Conversion{"a": fresh,
		"old": {PerUnit: 1, Source: "owner", AsOf: now.Add(-90 * 24 * time.Hour).Format(time.RFC3339), ValidDays: 30}}}, now: now}
	if v, ok, _ := b.value(vec("a", "kiro_credits", 4.0)); !ok || v != 2 {
		t.Errorf("%v %v", v, ok)
	}
	for name, c := range map[string]costVec{"unconfigured pool": vec("zzz", "usd", 1.0), "expired conversion": vec("old", "usd", 1.0), "one bad part": vec("a", "usd", 1.0, "zzz", "usd", 1.0)} {
		if _, ok, why := b.value(c); ok || why == "" {
			t.Errorf("%s must not be expressible: %v %s", name, ok, why)
		}
	}
	if e := b.expiry(vec("a", "usd", 1.0)); !e.Equal(now.Add(-24*time.Hour).AddDate(0, 0, 30)) {
		t.Errorf("a receipt may not outlive the conversion it used: %v", e)
	}
	if _, ok, _ := (basis{}).value(vec("a", "usd", 1.0)); ok {
		t.Error("no declared basis, no conversion")
	}
	if _, ok, why := (basis{cfg: &contract.CostBasis{Mode: "mystery"}}).value(vec("a", "usd", 1.0)); ok || why == "" {
		t.Error("unknown mode")
	}
}

func TestVecNeverMergesUnits(t *testing.T) {
	v := newVec()
	if err := v.add("p", "usd", 1); err != nil {
		t.Fatal(err)
	}
	if err := v.add("p", "usd", 2); err != nil || v.Amount["p"] != 3 {
		t.Errorf("same unit adds: %v %v", v, err)
	}
	if err := v.add("p", "kiro_credits", 1); err == nil {
		t.Error("USD and credits in one pool must be refused")
	}
	if err := v.addAll(map[string]float64{"q": 2}, map[string]string{"q": "usd"}, 1.5); err != nil || v.Amount["q"] != 3 {
		t.Errorf("%v %v", v, err)
	}
}

func TestQualificationFloors(t *testing.T) {
	pol := contract.WorkerPolicy{QualityFloor: contract.QualityFloor{MinApprovalRate: 0.6, MaxFalseClaims: ip(1)}, MinCompleteAttempts: 5}
	good := ledger.WorkerEvidence{Settled: 10, Complete: 10, Successes: 10, Coverage: 1, FalseClaimsKnown: true, FalseClaims: 1, Lower95: 0.72, Q: 72}
	if why, _ := qualifyWorker(good, pol); why != "" {
		t.Fatalf("control: %s", why)
	}
	for name, mutate := range map[string]func(*ledger.WorkerEvidence){
		"nothing settled": func(e *ledger.WorkerEvidence) { *e = ledger.WorkerEvidence{} },
		"thin":            func(e *ledger.WorkerEvidence) { e.Settled, e.Complete, e.Successes, e.Coverage = 4, 4, 4, 1 },
		"incomplete":      func(e *ledger.WorkerEvidence) { e.Complete, e.Coverage = 9, 0.9 },
		"unknown claims":  func(e *ledger.WorkerEvidence) { e.FalseClaimsKnown = false },
		"too many claims": func(e *ledger.WorkerEvidence) { e.FalseClaims = 2 },
		"below the floor": func(e *ledger.WorkerEvidence) { e.Lower95, e.Q = 0.59, 59 },
	} {
		e := good
		mutate(&e)
		why, insufficient := qualifyWorker(e, pol)
		if why == "" {
			t.Errorf("%s qualified", name)
		}
		// only missing evidence may reach an explicit baseline; a bad record may not
		if wantInsufficient := name != "too many claims" && name != "below the floor"; insufficient != wantInsufficient {
			t.Errorf("%s: insufficient=%v", name, insufficient)
		}
	}
	if why, _ := qualifyWorker(good, contract.WorkerPolicy{QualityFloor: contract.QualityFloor{MinApprovalRate: 0.6}, MinCompleteAttempts: 5}); why == "" {
		t.Error("an unconfigured false-claim limit cannot be satisfied by anything")
	}
	rp := contract.ReviewerPolicy{MinDefectFixtures: 4, MinCleanFixtures: 4, MinRecall: 0.5, MinSpecificity: 0.5, MaxFalsePositiveRate: func() *float64 { v := 0.2; return &v }()}
	rg := ledger.ReviewerEvidence{Rows: 8, CompleteRows: 8, Coverage: 1, DefectFixtures: 4, CleanFixtures: 4, RecallLower: 0.6, SpecificityLower: 0.6, FalsePositiveRate: 0.1}
	if why, _ := qualifyReviewer(rg, rp); why != "" {
		t.Fatalf("control: %s", why)
	}
	for name, mutate := range map[string]func(*ledger.ReviewerEvidence){
		"no fixtures":     func(e *ledger.ReviewerEvidence) { *e = ledger.ReviewerEvidence{} },
		"thin defects":    func(e *ledger.ReviewerEvidence) { e.DefectFixtures = 3 },
		"thin clean":      func(e *ledger.ReviewerEvidence) { e.CleanFixtures = 3 },
		"unadjudicated":   func(e *ledger.ReviewerEvidence) { e.Coverage = 0.75 },
		"low recall":      func(e *ledger.ReviewerEvidence) { e.RecallLower = 0.4 },
		"low specificity": func(e *ledger.ReviewerEvidence) { e.SpecificityLower = 0.4 },
		"false positives": func(e *ledger.ReviewerEvidence) { e.FalsePositiveRate = 0.3 },
	} {
		e := rg
		mutate(&e)
		if why, _ := qualifyReviewer(e, rp); why == "" {
			t.Errorf("reviewer %s qualified", name)
		}
	}
}
