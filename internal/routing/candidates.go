package routing

// Candidate evaluation for one automatic decision: hard gates first, then task-specific quality and attributed cost evidence,
// then the worker/reviewer pairing. Everything a candidate is excluded for is recorded; nothing is dropped silently.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/voravitl/rein/internal/budget"
	"github.com/voravitl/rein/internal/ledger"
	"github.com/voravitl/rein/internal/providers"
	"github.com/voravitl/rein/internal/run"
)

// Candidate is one provider's evaluation as shown in the decision report.
type Candidate struct {
	Provider     string                     `json:"provider"`
	Role         string                     `json:"role"` // worker | reviewer
	Agent        string                     `json:"agent"`
	Model        string                     `json:"model"`
	Effort       string                     `json:"effort,omitempty"`
	Maker        string                     `json:"maker"`
	Eligible     bool                       `json:"eligible"`
	Excluded     string                     `json:"excluded,omitempty"` // "<code>: <detail>" of the first failing gate
	Rank         int                        `json:"rank,omitempty"`
	Worker       *ledger.WorkerEvidence     `json:"worker_evidence,omitempty"`
	WorkerCost   *ledger.CostEvidence       `json:"worker_cost,omitempty"`
	Reviewer     *ledger.ReviewerEvidence   `json:"reviewer_evidence,omitempty"`
	ReviewCost   *ledger.ReviewCostEvidence `json:"review_cost,omitempty"`
	ReviewerPlan []string                   `json:"reviewer_plan,omitempty"` // the reviewers the cost forecast is bound to
	Cost         *costVec                   `json:"cost,omitempty"`          // per verified success (worker pair) or per review (reviewer)
	BasisValue   *float64                   `json:"basis_value,omitempty"`   // the policy estimate under the declared cost basis
	Funding      *costVec                   `json:"funding,omitempty"`       // worst case reserved for the task
	// BaselineReason says which evidence gap kept this candidate out of the scored ranking when it was selected by the explicit baseline.
	BaselineReason string `json:"baseline_reason,omitempty"`
}

// cand is a Candidate plus the working state of one decision.
type cand struct {
	Candidate
	name         string
	p            providers.Provider
	coolPool     string
	key          ledger.CohortKey
	entry        providers.ModelEntry
	inv          *providers.Inventory
	insufficient bool // excluded only for want of evidence: the one case an explicit baseline may cover
	boundMissing bool // otherwise eligible, but no finite probe bound is declared
	expires      time.Time
	plan         []*cand
	cost, fund   costVec // fund: this attempt's own worst case (a worker's attempt, or one review)
	reserve      costVec // a worker's plan for the reviews that follow: capacity only, charged under the reviews' own attempts
	rounds       float64
}

func (c *cand) exclude(code, format string, a ...any) string {
	c.Eligible = false
	c.Excluded = code + ": " + fmt.Sprintf(format, a...)
	return c.Excluded
}

// reviewLaunchable are the harnesses a review can actually be run on.
// ponytail: mirrors skills/worktree-pipeline/scripts/advise.sh; a reviewer on another harness would cost a probe and a reservation and then not run.
var reviewLaunchable = []string{"codex", "kiro", "claude"}

// fingerprint identifies the harness configuration, tools and context regime that evidence belongs to.
func fingerprint(p providers.Provider) string {
	caps := slices.Clone(p.Capabilities)
	sort.Strings(caps)
	h := sha256.Sum256([]byte(strings.Join([]string{p.Agent, p.Launch, p.Effort, strings.Join(caps, ","), strconv.Itoa(p.ContextTokens)}, "|")))
	return hex.EncodeToString(h[:])[:12]
}

// ProviderFingerprint is the configuration fingerprint evidence rows of a provider carry (ledger fixture --provider derives it).
func ProviderFingerprint(p providers.Provider) string { return fingerprint(p) }

// procAlive reports whether pid is still the same live process. Unknown counts as not alive: an unproven launcher is settled-unknown.
func procAlive(pid int, start int64) bool {
	s, err := run.ProcStart(pid)
	return err == nil && (start == 0 || s == start)
}

func (a *auto) evOpts(days int) ledger.EvidenceOptions {
	return ledger.EvidenceOptions{Now: a.now(), MaxAge: time.Duration(days) * 24 * time.Hour, Alive: procAlive, Maker: modelMaker, ExcludeAttempt: a.attempt}
}

func (a *auto) maxRounds() float64 {
	if a.budgetProf != nil && a.budgetProf.Budget != nil && a.budgetProf.Budget.MaxReviewRounds > 0 {
		return float64(a.budgetProf.Budget.MaxReviewRounds)
	}
	return 2
}

func billingProblem(p providers.Provider) string {
	b := p.Billing
	switch {
	case b == nil || b.Pool == "":
		return "no billing.pool (provider/account/pool/window qualifier)"
	case !slices.Contains([]string{"api", "subscription", "credits", "free"}, b.Mode):
		return "billing.mode must be api, subscription, credits or free"
	case !slices.Contains(ledger.Units, b.Unit):
		return fmt.Sprintf("billing.unit must be one of %v", ledger.Units)
	}
	return ""
}

func boundProblem(p providers.Provider) string {
	b := p.ProbeBound
	if b == nil || b.Calls < 1 || math.IsNaN(b.Amount) || math.IsInf(b.Amount, 0) || b.Amount < 0 {
		return "no finite probe_bound {calls >= 1, amount >= 0 in billing.unit}"
	}
	return ""
}

// gate applies the hard gates in order and returns the first failure ("" = passed). Quality, cost and the probe bound come last so
// that a candidate is blamed for the evidence or bound it lacks only when nothing else disqualified it.
func (a *auto) gate(cd *cand) string {
	p := cd.p
	maker, pool, err := identity(p)
	if err != nil {
		return cd.exclude("invalid_provider", "%v", err)
	}
	cd.coolPool, cd.Maker = pool, maker
	if ledger.IsAliasModel(p.Model) {
		return cd.exclude("alias_model", "%q is a moving alias, not an exact model identity", p.Model)
	}
	if cd.Role == "reviewer" && !slices.Contains(reviewLaunchable, p.Agent) {
		return cd.exclude("reviewer_not_launchable", "reviews run on %v only, not on %s", reviewLaunchable, p.Agent)
	}
	if cd.Role == "worker" && !hookInstalled(a.c, p.Agent) {
		return cd.exclude("hook_not_installed", "no %s guard hook installed in this worktree", p.Agent)
	}
	if a.in.SkipClaude && (p.Agent == "claude" || pool == "claude") {
		return cd.exclude("pool_unavailable", "claude pool skipped by request")
	}
	if blocked(a.cool, pool) {
		return cd.exclude("pool_unavailable", "quota pool %s is cooling down", pool)
	}
	if why, bad := a.failed[cd.name]; bad {
		return cd.exclude("failed_this_prepare", "%s", why)
	}
	// the harness inventory of THIS decision
	inv := a.inv[p.Agent]
	if inv == nil {
		return cd.exclude("no_inventory", "no discovery result for harness %s", p.Agent)
	}
	cd.inv = inv
	switch inv.Status {
	case providers.InvComplete:
		if !a.pol.Accepts(string(inv.Freshness)) {
			return cd.exclude("freshness_not_accepted", "%s catalog is %s, policy accepts %v", p.Agent, inv.Freshness, a.pol.AcceptFreshness)
		}
		entry, ok := inv.Has(p.Model)
		if !ok {
			return cd.exclude("model_not_in_catalog", "%s does not list %s", p.Agent, p.Model)
		}
		if entry.Alias {
			return cd.exclude("alias_model", "%s lists %s as a moving alias", p.Agent, p.Model)
		}
		cd.entry = entry
		if p.Effort != "" && len(entry.Variants) > 0 && !slices.Contains(entry.Variants, p.Effort) {
			return cd.exclude("effort_not_in_catalog", "%s lists %s with efforts %v, not %q", p.Agent, p.Model, entry.Variants, p.Effort)
		}
	case providers.InvUnsupported:
		if !a.pol.Accepts(string(providers.FreshnessUnknown)) {
			return cd.exclude("freshness_not_accepted", "no catalog adapter for %s (freshness unknown), policy accepts %v", p.Agent, a.pol.AcceptFreshness)
		}
	default:
		return cd.exclude("harness_excluded", "refresh of %s is %s: %s", p.Agent, inv.Status, inv.Reason)
	}
	for _, l := range inv.Limits {
		if (l.Allowed != nil && !*l.Allowed) || (l.UsedPercent != nil && *l.UsedPercent >= 100) {
			return cd.exclude("quota_exhausted_observed", "%s reports no usable quota in %s", p.Agent, l.ID)
		}
	}
	have := map[string]bool{}
	for _, c := range append(slices.Clone(p.Capabilities), cd.entry.Capabilities...) {
		have[strings.ToLower(c)] = true
	}
	for _, need := range a.class.Capabilities {
		if need == "repository-edit" && cd.Role == "reviewer" {
			continue // a reviewer is read-only; every other requirement (tests, browser, input:image, ...) still applies
		}
		if !have[need] {
			return cd.exclude("missing_capability", "%s does not declare %s", cd.name, need)
		}
	}
	// the smaller of the configured and the catalogued window: a configuration cannot claim more than the harness offers
	ctxHave := p.ContextTokens
	if c := cd.entry.ContextTokens; c > 0 && (ctxHave == 0 || c < ctxHave) {
		ctxHave = c
	}
	switch {
	case ctxHave == 0:
		return cd.exclude("context_unknown", "no declared or catalogued context window")
	case ctxHave < a.class.Context:
		return cd.exclude("context_too_small", "%d tokens available, %d expected", ctxHave, a.class.Context)
	}
	if why := billingProblem(p); why != "" {
		return cd.exclude("billing_missing", "%s", why)
	}
	cd.expires = a.baseExpiry
	if p.Billing.Mode == "api" {
		key := p.Billing.PriceKey
		if key == "" {
			key = p.Model
		}
		r, ok := a.prices[key]
		if !ok {
			return cd.exclude("rates_missing", "no provenance rates for %q in prices.json (legacy usd_per_mtok is not enough)", key)
		}
		if err := r.Validate(a.now()); err != nil {
			return cd.exclude("rates_invalid", "%v", err)
		}
		if until, err := time.Parse(time.RFC3339, r.ValidUntil); err == nil {
			cd.expires = minTime(cd.expires, until)
		}
	}
	cd.expires = minTime(cd.expires, inv.QueriedAt.Add(time.Duration(a.pol.InventoryMaxAgeMinutes)*time.Minute))
	return ""
}

func minTime(a, b time.Time) time.Time {
	switch {
	case a.IsZero():
		return b
	case b.IsZero():
		return a
	case b.Before(a):
		return b
	}
	return a
}

// evalWorker adds the worker's quality and attributed-cost evidence. The probe bound is checked last.
func (a *auto) evalWorker(cd *cand) {
	if a.gate(cd) != "" {
		return
	}
	p := cd.p
	cd.key = ledger.CohortKey{Agent: p.Agent, Model: p.Model, Effort: p.Effort, Config: fingerprint(p), Kind: a.class.Kind, Tier: a.class.Tier, Suite: a.pol.Suite}
	o := a.evOpts(a.pol.Worker.MaxEvidenceAgeDays)
	ev := ledger.EvaluateWorker(a.rows, cd.key, o)
	cd.Worker = &ev
	if why, insufficient := qualifyWorker(ev, a.pol.Worker); why != "" {
		cd.insufficient = insufficient
		cd.exclude("unqualified", "%s", why)
		return
	}
	cost := ledger.EvaluateWorkerCost(a.rows, cd.key, o)
	cd.WorkerCost = &cost
	if !cost.Available {
		cd.insufficient = true
		cd.exclude("cost_unavailable", "%s (unknown cost is never zero)", cost.Reason)
		return
	}
	if !ev.Oldest.IsZero() {
		cd.expires = minTime(cd.expires, ev.Oldest.Add(o.MaxAge))
	}
	cd.Eligible = true
}

// evalReviewer adds the reviewer's separate calibration floor and per-review cost.
func (a *auto) evalReviewer(cd *cand) {
	if a.gate(cd) != "" {
		return
	}
	p := cd.p
	cd.key = ledger.CohortKey{Agent: p.Agent, Model: p.Model, Effort: p.Effort, Config: fingerprint(p), Kind: a.class.Kind, Tier: a.class.Tier, Suite: a.pol.Suite}
	o := a.evOpts(a.pol.Reviewer.MaxEvidenceAgeDays)
	ev := ledger.EvaluateReviewer(a.rows, cd.key, o)
	cd.Reviewer = &ev
	if why, insufficient := qualifyReviewer(ev, a.pol.Reviewer); why != "" {
		cd.insufficient = insufficient
		cd.exclude("unqualified", "reviewer: %s", why)
		return
	}
	rc := ledger.EvaluateReviewCost(a.rows, cd.name, p.Model, o)
	cd.ReviewCost = &rc
	if !rc.Available {
		cd.insufficient = true
		cd.exclude("cost_unavailable", "review cost: %s", rc.Reason)
		return
	}
	if !ev.Oldest.IsZero() {
		cd.expires = minTime(cd.expires, ev.Oldest.Add(o.MaxAge))
	}
	cd.Eligible = true
}

// checkBound is the last gate: an otherwise eligible candidate with no finite probe bound is never probed.
func (a *auto) checkBound(cd *cand) {
	if cd.Eligible {
		if why := boundProblem(cd.p); why != "" {
			cd.boundMissing = true
			cd.exclude("probe_bound_required", "%s", why)
		}
	}
}

func (a *auto) basis() basis {
	return basis{cfg: a.pol.CostBasis, poolMode: a.poolMode, now: a.now()}
}

// plan chooses the reviewers a worker's cost forecast is bound to: qualified, maker-independent of the worker, one per maker, as
// many distinct makers as the tier needs. It returns why not when the tier's review requirement cannot be met right now.
func (a *auto) plan(workerMaker string, revs []*cand) ([]*cand, string) {
	var pool []*cand
	for _, r := range revs {
		if r.Eligible && !r.boundMissing && r.Maker != "" && r.Maker != "unknown" && r.Maker != workerMaker && !slices.Contains(a.class.ExcludeMakers, r.Maker) {
			pool = append(pool, r)
		}
	}
	items := make([]rankItem, len(pool))
	for i, r := range pool {
		items[i] = rankItem{ID: r.Agent + ":" + r.Model + "/" + r.name, Q: r.Reviewer.Q, Latency: math.Inf(1), Cost: reviewVec(r)}
	}
	order, _, err := rank(a.pol.Objective, items, a.basis())
	if err != nil { // costs not comparable here: order by quality instead; the worker-level ranking decides if that matters
		order, _, _ = rank("quality_first", items, a.basis())
	}
	need := a.class.RequiredMakers()
	var chosen []*cand
	seen := map[string]bool{}
	for _, i := range order {
		if r := pool[i]; !seen[r.Maker] {
			seen[r.Maker] = true
			chosen = append(chosen, r)
		}
		if len(chosen) == need {
			return chosen, ""
		}
	}
	return nil, fmt.Sprintf("%d qualified independent reviewer maker(s) available, tier %s needs %d", len(seen), a.class.Tier, need)
}

func reviewVec(r *cand) costVec {
	v := newVec()
	_ = v.addAll(r.ReviewCost.PerReview, r.ReviewCost.Units, 1)
	return v
}

// pair prices a worker together with its reviewer plan: cost per verified success including the review regime it is bound to,
// and the worst case to reserve for the task.
func (a *auto) pair(w *cand, plan []*cand) error {
	rounds := a.maxRounds()
	if w.Worker.MeanRounds != nil {
		rounds = math.Max(1, *w.Worker.MeanRounds)
	}
	cost, fund, reserve := newVec(), newVec(), newVec()
	if err := cost.addAll(w.WorkerCost.PerSuccess, w.WorkerCost.Units, 1); err != nil {
		return err
	}
	if err := fund.addAll(w.WorkerCost.MaxAttempt, w.WorkerCost.Units, 1); err != nil {
		return err
	}
	for _, r := range plan {
		if err := cost.addAll(r.ReviewCost.PerReview, r.ReviewCost.Units, rounds); err != nil {
			return err
		}
		if err := reserve.addAll(r.ReviewCost.MaxReview, r.ReviewCost.Units, a.maxRounds()); err != nil {
			return err
		}
		w.expires = minTime(w.expires, r.expires)
		w.ReviewerPlan = append(w.ReviewerPlan, r.name)
	}
	total := newVec() // what the task may need in all: one pool keeps one unit across the worker and its reviews
	for _, v := range []costVec{fund, reserve} {
		if err := total.addAll(v.Amount, v.Unit, 1); err != nil {
			return err
		}
	}
	w.plan, w.cost, w.fund, w.reserve, w.rounds = plan, cost, fund, reserve, rounds
	w.Cost, w.Funding = &cost, &total
	return nil
}

// items is the whole reservation of a candidate: its probe bound plus the funding plan, checked and recorded together. A worker
// holds its own attempt (funding) and sets review capacity aside (review_reserve, handed to the review that follows); a review
// pays for itself (review_funding), and that is charged under the review's own attempt.
func (a *auto) items(cd *cand) []budget.Item {
	b := cd.p.Billing
	out := []budget.Item{{Pool: b.Pool, Unit: b.Unit, Amount: cd.p.ProbeBound.Amount, Calls: cd.p.ProbeBound.Calls, Kind: "probe"}}
	own := "funding"
	if cd.Role == "reviewer" {
		own = "review_funding"
	}
	add := func(v costVec, kind string) {
		pools := make([]string, 0, len(v.Amount))
		for pool := range v.Amount {
			pools = append(pools, pool)
		}
		sort.Strings(pools)
		for _, pool := range pools {
			out = append(out, budget.Item{Pool: pool, Unit: v.Unit[pool], Amount: v.Amount[pool], Kind: kind})
		}
	}
	add(cd.fund, own)
	add(cd.reserve, "review_reserve")
	return out
}
