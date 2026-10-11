package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"time"
)

// QualityFloor is the quality-floor shape. It lived only inside Budget; automatic selection reuses it as a standalone
// policy so quality gates work with no spending cap configured.
type QualityFloor struct {
	MinApprovalRate float64 `json:"min_approval_rate,omitempty"`
	// MaxFalseClaims is an allowed COUNT inside the complete cohort, not a rate. nil = not configured (0 is a value).
	MaxFalseClaims *int `json:"max_false_claims,omitempty"`
}

// WorkerPolicy is what a worker model must have shown before automatic selection may rank it.
type WorkerPolicy struct {
	QualityFloor
	MinCompleteAttempts int `json:"min_complete_attempts,omitempty"`
	MaxEvidenceAgeDays  int `json:"max_evidence_age_days,omitempty"` // older settled attempts do not count
}

// ReviewerPolicy is the separate calibration floor of a reviewer, judged on frozen red/clean fixtures. Approval
// frequency is never reviewer quality.
type ReviewerPolicy struct {
	MinDefectFixtures    int      `json:"min_defect_fixtures,omitempty"`
	MinCleanFixtures     int      `json:"min_clean_fixtures,omitempty"`
	MinRecall            float64  `json:"min_recall,omitempty"`      // lower Wilson endpoint of defect detection
	MinSpecificity       float64  `json:"min_specificity,omitempty"` // lower Wilson endpoint on clean fixtures
	MaxFalsePositiveRate *float64 `json:"max_false_positive_rate,omitempty"`
	MaxEvidenceAgeDays   int      `json:"max_evidence_age_days,omitempty"`
}

// Conversion turns one native unit of a pool into a common comparison unit. It is a policy estimate, shown as such.
type Conversion struct {
	PerUnit   float64 `json:"per_unit"`
	Source    string  `json:"source"`
	AsOf      string  `json:"as_of"` // RFC 3339
	ValidDays int     `json:"valid_days"`
}

// CostBasis declares how costs in different native units are compared. Without it only candidates drawing on the same
// single pool are comparable.
type CostBasis struct {
	Mode        string                `json:"mode"`                  // monetary_marginal | converted
	Unit        string                `json:"unit,omitempty"`        // converted: name of the common unit
	Conversions map[string]Conversion `json:"conversions,omitempty"` // converted: per pool key
}

// Calibration is the standing authorization and the fixed caps for qualifying a newly discovered model on frozen
// fixtures. Discovery alone never enrolls a model in paid experiments.
type Calibration struct {
	Authorization string             `json:"authorization"` // the owner's standing authorization (text or standing.md reference)
	MaxCalls      int                `json:"max_calls"`
	PoolCaps      map[string]float64 `json:"pool_caps"`
	MaxCashUSD    *float64           `json:"max_cash_usd"`
}

// Selection is the owner's policy for automatic task and model selection.
type Selection struct {
	Objective              string         `json:"objective,omitempty"`                 // balanced | quality_first
	AcceptFreshness        []string       `json:"accept_freshness,omitempty"`          // remote_verified | client_catalog | unknown
	InventoryMaxAgeMinutes int            `json:"inventory_max_age_minutes,omitempty"` // lifetime of catalog evidence
	Suite                  string         `json:"evaluation_suite,omitempty"`          // only evidence from this suite version counts
	Worker                 WorkerPolicy   `json:"worker"`
	Reviewer               ReviewerPolicy `json:"reviewer"`
	CostBasis              *CostBasis     `json:"cost_basis,omitempty"`
	Calibration            *Calibration   `json:"calibration,omitempty"`
	// BaselineChain names a worker chain the owner explicitly accepts as an UNSCORED baseline when no candidate has
	// enough evidence. The result reports selection_mode=baseline_insufficient_evidence.
	BaselineChain string `json:"baseline_chain,omitempty"`
	// AllowManualChains lets `rein route prepare` keep working beside automatic selection. By default, once a policy exists
	// the coordinator cannot pick a chain or model around the scored ordering.
	AllowManualChains bool `json:"allow_manual_chains,omitempty"`
}

// EffectiveSelection returns the selection policy with the worker quality floor taken from the legacy
// budget.quality_floor when the selection itself does not set one. nil when the profile has no policy.
func (p *Profile) EffectiveSelection() *Selection {
	if p == nil || p.Selection == nil {
		return nil
	}
	s := *p.Selection
	if s.Worker.MinApprovalRate == 0 && s.Worker.MaxFalseClaims == nil && p.Budget != nil {
		s.Worker.QualityFloor = p.Budget.QualityFloor
	}
	return &s
}

// Problems lists every missing or invalid required value by JSON path. Empty means the policy is usable. Nothing is
// defaulted: a missing floor is reported, never invented.
func (s *Selection) Problems() []string {
	var out []string
	bad := func(path, why string) { out = append(out, "selection."+path+": "+why) }
	if s == nil {
		return []string{"selection: policy missing"}
	}
	if s.Objective != "balanced" && s.Objective != "quality_first" {
		bad("objective", "must be balanced or quality_first")
	}
	if len(s.AcceptFreshness) == 0 {
		bad("accept_freshness", "declare at least one of remote_verified, client_catalog, unknown")
	}
	for _, f := range s.AcceptFreshness {
		if f != "remote_verified" && f != "client_catalog" && f != "unknown" {
			bad("accept_freshness", fmt.Sprintf("unknown freshness class %q", f))
		}
	}
	if s.InventoryMaxAgeMinutes <= 0 {
		bad("inventory_max_age_minutes", "must be > 0")
	}
	if s.Suite == "" {
		bad("evaluation_suite", "required")
	}
	w := s.Worker
	if !(w.MinApprovalRate > 0 && w.MinApprovalRate <= 1) {
		bad("worker.min_approval_rate", "must be in (0,1]")
	}
	if w.MaxFalseClaims == nil || *w.MaxFalseClaims < 0 {
		bad("worker.max_false_claims", "required, >= 0 (an allowed count in the complete cohort)")
	}
	if w.MinCompleteAttempts <= 0 {
		bad("worker.min_complete_attempts", "must be > 0")
	}
	if w.MaxEvidenceAgeDays <= 0 {
		bad("worker.max_evidence_age_days", "must be > 0")
	}
	r := s.Reviewer
	if r.MinDefectFixtures <= 0 || r.MinCleanFixtures <= 0 {
		bad("reviewer.min_defect_fixtures/min_clean_fixtures", "both must be > 0")
	}
	if !(r.MinRecall > 0 && r.MinRecall <= 1) {
		bad("reviewer.min_recall", "must be in (0,1]")
	}
	if !(r.MinSpecificity > 0 && r.MinSpecificity <= 1) {
		bad("reviewer.min_specificity", "must be in (0,1]")
	}
	if r.MaxFalsePositiveRate == nil || *r.MaxFalsePositiveRate < 0 || *r.MaxFalsePositiveRate > 1 {
		bad("reviewer.max_false_positive_rate", "required, in [0,1]")
	}
	if r.MaxEvidenceAgeDays <= 0 {
		bad("reviewer.max_evidence_age_days", "must be > 0")
	}
	if cb := s.CostBasis; cb != nil {
		switch cb.Mode {
		case "monetary_marginal":
		case "converted":
			if cb.Unit == "" || len(cb.Conversions) == 0 {
				bad("cost_basis", "converted mode needs unit and conversions")
			}
			for k, c := range cb.Conversions {
				if !(c.PerUnit > 0) || math.IsInf(c.PerUnit, 0) || math.IsNaN(c.PerUnit) || c.Source == "" || c.ValidDays <= 0 {
					bad("cost_basis.conversions."+k, "needs finite per_unit > 0, source and valid_days > 0")
				}
				if _, err := time.Parse(time.RFC3339, c.AsOf); err != nil {
					bad("cost_basis.conversions."+k+".as_of", "must be RFC 3339")
				}
			}
		default:
			bad("cost_basis.mode", "must be monetary_marginal or converted")
		}
	}
	if c := s.Calibration; c != nil {
		if c.Authorization == "" || c.MaxCalls <= 0 || len(c.PoolCaps) == 0 || c.MaxCashUSD == nil || *c.MaxCashUSD < 0 {
			bad("calibration", "needs authorization, max_calls > 0, pool_caps and max_cash_usd (0 = no cash)")
		}
	}
	return out
}

// Accepts reports whether the policy accepts catalog evidence of this freshness class.
func (s *Selection) Accepts(freshness string) bool {
	for _, f := range s.AcceptFreshness {
		if f == freshness {
			return true
		}
	}
	return false
}

// Hash binds a decision to the exact policy it was made under.
func (s *Selection) Hash() string {
	b, _ := json.Marshal(s)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
