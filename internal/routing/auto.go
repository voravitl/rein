package routing

// Automatic task and model selection (docs/ROUTING_SELECTION_DESIGN.md). One entry point, PrepareAuto, replaces manual chain
// and model choice: classify the task, refresh every harness inventory, gate and score the candidates on task-specific evidence,
// rank them by the owner's declared objective, atomically reserve the probe and the funding plan, probe the top candidate and
// persist an evidence-bound receipt. A failed top candidate starts a NEW decision with a NEW refresh; nothing is carried over.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/voravitl/rein/internal/budget"
	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/ledger"
	"github.com/voravitl/rein/internal/providers"
	"github.com/voravitl/rein/internal/run"
)

const (
	modeScored   = "scored"
	modeBaseline = "baseline_insufficient_evidence"
	receiptTTL   = 30 * time.Minute // the ceiling on how long a receipt may be used (as for ordered chains)
)

// AutoInput is everything one automatic preparation needs. The coordinator supplies TaskProfile; everything else is owner or
// system state. There is deliberately no field for a chain, model, provider or effort.
type AutoInput struct {
	Contract    *contract.Contract
	Run         string
	TaskProfile []byte            // the coordinator's description of the task (JSON)
	ConfigPath  string            // fallback-chain.json: the approved providers and their billing
	Owner       *contract.Profile // the owner's profile (REIN_PROFILE); its selection policy wins over the contract's copy
	Budget      *contract.Profile // the profile whose budget applies (nil = the contract's)
	MarkerPath  string            // run marker, "" when no run is active
	WorkerModel string            // review: the coordinator's claim, which must match the ledger's proof of the worker identity
	Base        string            // review: the git ref the checkout's actual changes are taken against (default origin/main)
	SkipClaude  bool
	Timeout     time.Duration // per probe and per harness refresh
	Now         func() time.Time
	// Discover and Probe default to the real adapters; tests inject fakes.
	Discover func(context.Context, *providers.Config, providers.DiscoverOptions) []providers.Inventory
	Probe    func(*providers.Config, string, time.Duration) providers.Result
	Diff     func(worktree, base string) ([]string, error)
}

// InventoryView is the summary of one harness refresh shown in the report (the full snapshot is kept beside the receipt).
type InventoryView struct {
	Harness   string              `json:"harness"`
	Adapter   string              `json:"adapter"`
	Version   string              `json:"version,omitempty"`
	Scope     string              `json:"scope"`
	QueriedAt time.Time           `json:"queried_at"`
	Status    providers.InvStatus `json:"status"`
	Freshness providers.Freshness `json:"freshness"`
	Models    int                 `json:"models"`
	Reason    string              `json:"reason,omitempty"`
}

// Round is one decision: a fresh refresh, the evaluation of every candidate, and what happened to the top one.
type Round struct {
	Decision    string            `json:"decision"`
	Parent      string            `json:"parent,omitempty"`
	At          time.Time         `json:"at"`
	Inventories []InventoryView   `json:"inventories"`
	Candidates  []Candidate       `json:"candidates"`
	Probed      string            `json:"probed,omitempty"`
	Probe       *providers.Result `json:"probe,omitempty"`
	Hold        string            `json:"hold,omitempty"`
	Outcome     string            `json:"outcome"` // selected | probe_quota | probe_down | no_candidate | budget_insufficient
	cands       []*cand
}

// Launch is the coordinator-facing outcome: the agent, model, effort and launch route to use exactly as returned.
type Launch struct {
	Provider string `json:"provider"`
	Agent    string `json:"agent"`
	Model    string `json:"model"`
	Effort   string `json:"effort,omitempty"`
	Route    string `json:"launch"` // shell | orca
	Maker    string `json:"maker"`
	Pool     string `json:"pool"`
	Chain    string `json:"chain"`
}

func launchOf(d *Decision) *Launch {
	return &Launch{Provider: d.Provider, Agent: d.Agent, Model: d.Model, Effort: d.Effort, Route: d.Launch, Maker: d.Maker, Pool: d.Pool, Chain: d.Chain}
}

// Comparison says exactly what a choice does and does not claim. Candidates whose cost is unknown are excluded from the
// comparison and are never claimed to be more expensive.
type Comparison struct {
	Objective   string   `json:"objective"`
	CostBasis   string   `json:"cost_basis"` // none | monetary_marginal | converted
	Compared    []string `json:"compared"`   // ranked candidates, best first
	UnknownCost []string `json:"excluded_unknown_cost,omitempty"`
	Claim       string   `json:"claim"`
}

func (a *auto) comparison(round *Round) *Comparison {
	cmp := &Comparison{Objective: a.pol.Objective, CostBasis: "none"}
	if a.pol.CostBasis != nil {
		cmp.CostBasis = a.pol.CostBasis.Mode
	}
	byRank := map[int]string{}
	for _, c := range round.cands {
		if c.Role != a.role() {
			continue
		}
		if c.Rank > 0 {
			byRank[c.Rank] = c.Provider
		}
		if code, _, _ := strings.Cut(c.Excluded, ":"); code == "cost_basis_unknown" || code == "cost_unavailable" {
			cmp.UnknownCost = append(cmp.UnknownCost, c.Provider)
		}
	}
	for r := 1; r <= len(byRank)+len(round.cands); r++ {
		if name, ok := byRank[r]; ok {
			cmp.Compared = append(cmp.Compared, name)
		}
	}
	sort.Strings(cmp.UnknownCost)
	switch {
	case len(cmp.Compared) < 2:
		cmp.Claim = "only one candidate passed every gate: no price comparison was made"
	case a.pol.Objective == "quality_first":
		cmp.Claim = "highest quality lower bound among the compared candidates; comparable cost only breaks ties"
	default:
		cmp.Claim = "lowest comparable cost per independently verified task among the compared candidates only; candidates with unknown or unavailable cost were excluded, not assumed more expensive"
	}
	return cmp
}

// AutoResult is the decision report: the objective, the evidence, every excluded alternative and the receipt to launch.
type AutoResult struct {
	Attempt        string             `json:"attempt"`
	Decision       string             `json:"decision,omitempty"`
	Objective      string             `json:"objective"`
	PolicySource   string             `json:"policy_source,omitempty"` // owner (REIN_PROFILE) | contract (the copy made when the contract was created)
	SelectionMode  string             `json:"selection_mode,omitempty"`
	Class          *Class             `json:"classification,omitempty"`
	Launch         *Launch            `json:"launch,omitempty"` // what the coordinator launches, unchanged
	Receipt        *Decision          `json:"receipt,omitempty"`
	Selected       *Candidate         `json:"selected,omitempty"`
	Rounds         []*Round           `json:"rounds"`
	Comparison     *Comparison        `json:"comparison,omitempty"`
	SharedOverhead map[string]float64 `json:"shared_overhead,omitempty"` // zero-inference discovery is not charged; any measured shared charge is reported once
	Error          string             `json:"error,omitempty"`
	ErrorCode      string             `json:"error_code,omitempty"`
}

type auto struct {
	in         AutoInput
	ctx        context.Context
	c          *contract.Contract
	cfg        *providers.Config
	cfgPath    string
	cfgHash    string
	pol        *contract.Selection
	budgetProf *contract.Profile
	class      *Class
	profileRaw []byte
	attempt    string
	now        func() time.Time
	prices     map[string]ledger.Rates
	poolMode   map[string]string
	baseExpiry time.Time
	res        *AutoResult

	// per decision
	inv    map[string]*providers.Inventory
	invs   []providers.Inventory
	rows   []ledger.Row
	cool   map[string]Cooldown
	failed map[string]string

	workerMaker   string // review: the maker of the worker that actually ran
	workerAttempt string // review: the attempt whose review capacity this review takes over
	mode          string // scored | baseline_insufficient_evidence
	win           *cand  // the candidate whose probe came up, until its receipt is persisted
	winHold       *budget.Hold
}

func newID(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return prefix + "-" + hex.EncodeToString(b[:])
}

func hashBytes(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func artifactDir(task string) string { return filepath.Join(dir(), "artifacts", task) }

func removeArtifacts(task string) { _ = os.RemoveAll(artifactDir(task)) }

func writeArtifact(task, name string, b []byte) error {
	d := artifactDir(task)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(d, name), b, 0o600)
}

// poolModes maps each billing pool to its mode and refuses a configuration that gives one pool two modes or units.
func poolModes(cfg *providers.Config) (map[string]string, error) {
	modes, units := map[string]string{}, map[string]string{}
	names := make([]string, 0, len(cfg.Providers))
	for n := range cfg.Providers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		b := cfg.Providers[n].Billing
		if b == nil || b.Pool == "" {
			continue
		}
		if m, ok := modes[b.Pool]; ok && (m != b.Mode || units[b.Pool] != b.Unit) {
			return nil, refuse(CodeSelectionPolicy, "billing pool %q is configured with different modes or units: one pool has one mode and one unit", b.Pool)
		}
		modes[b.Pool], units[b.Pool] = b.Mode, b.Unit
	}
	return modes, nil
}

func (a *auto) loadPolicy() error {
	var sel *contract.Selection
	a.res.PolicySource = "owner"
	if a.in.Owner != nil {
		sel = a.in.Owner.EffectiveSelection()
	}
	if sel == nil {
		sel, a.res.PolicySource = a.c.Profile.EffectiveSelection(), "contract"
	}
	if sel == nil {
		a.res.PolicySource = ""
		return refuse(CodeSelectionPolicy, "no selection policy: add \"selection\" to the owner's profile (REIN_PROFILE); nothing is defaulted")
	}
	if problems := sel.Problems(); len(problems) > 0 {
		code := CodeSelectionPolicy
		for _, p := range problems {
			if strings.HasPrefix(p, "selection.worker.") || strings.HasPrefix(p, "selection.reviewer.") {
				code = CodeQualityPolicy
			}
		}
		return refuse(code, "%s", strings.Join(problems, "; "))
	}
	a.pol = sel
	return nil
}

func (a *auto) workerNames() ([]string, error) {
	names := slices.Clone(a.cfg.WorkerChains[a.class.Policy])
	if len(names) == 0 {
		return nil, refuse(CodeNoCandidate, "no worker chain %q is configured: it defines the approved candidate set for this kind of task", a.class.Policy)
	}
	return uniqSorted(names), nil
}

func (a *auto) reviewerNames() ([]string, error) {
	var names []string
	for _, chain := range a.cfg.ReviewChains {
		names = append(names, chain...)
	}
	if len(names) == 0 {
		return nil, refuse(CodeNoCandidate, "no review chains are configured: automatic selection needs approved reviewers")
	}
	return uniqSorted(names), nil
}

func uniqSorted(in []string) []string {
	out := slices.Clone(in)
	sort.Strings(out)
	return slices.Compact(out)
}

func (a *auto) newCand(name, role string) *cand {
	p := a.cfg.Providers[name]
	return &cand{name: name, p: p, Candidate: Candidate{Provider: name, Role: role, Agent: p.Agent, Model: p.Model, Effort: p.Effort}}
}

func views(invs []providers.Inventory) []InventoryView {
	out := make([]InventoryView, len(invs))
	for i, v := range invs {
		out[i] = InventoryView{Harness: v.Harness, Adapter: v.Adapter, Version: v.Version, Scope: v.Scope, QueriedAt: v.QueriedAt, Status: v.Status,
			Freshness: v.Freshness, Models: len(v.Models), Reason: v.Reason}
	}
	return out
}

func agentsOf(cfg *providers.Config, names []string) []string {
	var out []string
	for _, n := range names {
		out = append(out, cfg.Providers[n].Agent)
	}
	return uniqSorted(out)
}

// PrepareAuto selects and probes a model for one task. On failure the report so far is returned with the error, so the
// refusal can show every excluded alternative.
func PrepareAuto(ctx context.Context, in AutoInput) (*AutoResult, error) {
	c := in.Contract
	path, err := receipt(c)
	if err != nil {
		return nil, err
	}
	if in.Run == "" || in.Timeout <= 0 {
		return nil, errors.New("explicit run and positive timeout required")
	}
	unlock, err := run.LockFile(filepath.Join(dir(), "prepare", c.Name+".lock"), 0)
	if err != nil {
		return nil, fmt.Errorf("another preparation of task %s is running: %w", c.Name, err)
	}
	defer unlock()
	// the receipt this preparation replaces may never have been used: release what it reserved before it disappears
	if err = releaseUnused(path); err != nil {
		return nil, err
	}
	// a failed or refused preparation must leave no usable receipt behind
	if err = os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	removeArtifacts(c.Name)

	a := &auto{in: in, ctx: ctx, c: c, now: in.Now, failed: map[string]string{}, profileRaw: in.TaskProfile, mode: modeScored}
	if a.now == nil {
		a.now = time.Now
	}
	a.attempt = newID("at")
	a.res = &AutoResult{Attempt: a.attempt, Rounds: []*Round{}}
	fail := func(err error) (*AutoResult, error) {
		a.res.Error, a.res.ErrorCode = err.Error(), ErrCode(err)
		return a.res, err
	}
	if a.cfg, a.cfgPath, a.cfgHash, err = config(in.ConfigPath); err != nil {
		return fail(err)
	}
	if err = a.loadPolicy(); err != nil {
		return fail(err)
	}
	a.res.Objective = a.pol.Objective
	a.budgetProf = in.Budget
	if a.budgetProf == nil {
		a.budgetProf = &c.Profile
	}
	tp, err := DecodeTaskProfile(in.TaskProfile)
	if err != nil {
		return fail(err)
	}
	if strings.EqualFold(strings.TrimSpace(tp.Phase), "review") {
		if err = a.addActualDiff(tp); err != nil {
			return fail(err)
		}
	}
	if a.class, err = Classify(c, in.Owner, tp); err != nil {
		return fail(err)
	}
	a.res.Class = a.class
	if a.class.Phase == "worker" && (c.HooksGeneration == "" || len(c.HooksInstalled) == 0) {
		return fail(errors.New("install worker hooks before routing"))
	}
	if a.poolMode, err = poolModes(a.cfg); err != nil {
		return fail(err)
	}
	if a.prices, err = ledger.LoadRates(ledger.PricesPath()); err != nil {
		return fail(err)
	}
	a.baseExpiry = a.now().Add(receiptTTL)

	var workers, reviewers []string
	if a.class.Phase == "worker" {
		if workers, err = a.workerNames(); err != nil {
			return fail(err)
		}
	}
	if reviewers, err = a.reviewerNames(); err != nil {
		return fail(err)
	}
	maxDecisions := min(len(workers)+len(reviewers)+1, 12)

	parent := ""
	for n := 1; n <= maxDecisions; n++ {
		if ctx.Err() != nil {
			return fail(ctx.Err())
		}
		decision := fmt.Sprintf("%s.%d", a.attempt, n)
		round := &Round{Decision: decision, Parent: parent, At: a.now(), Candidates: []Candidate{}}
		a.res.Rounds = append(a.res.Rounds, round)
		ordered, err := a.decide(round, workers, reviewers)
		if err != nil {
			round.Candidates = candidateViews(round.cands)
			round.Outcome = "refused"
			return fail(err)
		}
		if len(ordered) == 0 && (n == 1 || a.mode == modeBaseline) {
			ordered = a.baselineOrder(round)
		}
		round.Candidates = candidateViews(round.cands)
		if len(ordered) == 0 {
			round.Outcome = "no_candidate"
			return fail(a.noCandidate(round))
		}
		d, probed, err := a.tryOrdered(round, ordered)
		round.Candidates = candidateViews(round.cands)
		if err == nil && d != nil {
			err = a.persist(path, d, round, ordered)
		}
		if err != nil { // a probe that came up but yields no receipt has still spent its bound
			return fail(a.abandonWinner(round, err))
		}
		if d != nil {
			a.win = nil
			return a.res, nil
		}
		if !probed {
			return fail(refuse(CodeNoCandidate, "no ranked candidate fits the remaining budget (see the reservation exclusions)"))
		}
		parent = decision // the top candidate failed its probe: refresh everything and rank again
	}
	return fail(refuse(CodeNoCandidate, "every ranked candidate failed its availability probe"))
}

func candidateViews(cs []*cand) []Candidate {
	out := make([]Candidate, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Candidate)
	}
	return out
}

// decide runs one decision: refresh, evaluate, pair, rank. It returns the ranked eligible candidates, best first.
func (a *auto) decide(round *Round, workers, reviewers []string) ([]*cand, error) {
	var err error
	if a.cool, err = state(); err != nil {
		return nil, err
	}
	if a.rows, err = ledger.LoadStrict(""); err != nil {
		return nil, fmt.Errorf("ledger unreadable, cannot judge evidence: %w", err)
	}
	// a previous snapshot can never authorize a new selection: discover again, for every harness involved
	disc := a.in.Discover
	if disc == nil {
		disc = providers.Discover
	}
	a.invs = disc(a.ctx, a.cfg, providers.DiscoverOptions{Dir: a.c.Worktree, Timeout: a.in.Timeout, Harnesses: agentsOf(a.cfg, slices.Concat(workers, reviewers))})
	a.inv = map[string]*providers.Inventory{}
	for i := range a.invs {
		a.inv[a.invs[i].Harness] = &a.invs[i]
	}
	round.Inventories = views(a.invs)

	var revs []*cand
	for _, name := range reviewers {
		cd := a.newCand(name, "reviewer")
		a.evalReviewer(cd)
		a.checkBound(cd)
		revs = append(revs, cd)
	}
	if a.class.Phase == "review" {
		if err := a.resolveWorker(); err != nil {
			return nil, err
		}
		return a.rankReviewers(round, revs)
	}
	var ws []*cand
	for _, name := range workers {
		cd := a.newCand(name, "worker")
		a.evalWorker(cd)
		ws = append(ws, cd)
	}
	revInsufficient := slices.ContainsFunc(revs, func(r *cand) bool { return r.insufficient })
	for _, w := range ws {
		if !w.Eligible {
			continue
		}
		plan, why := a.plan(w.Maker, revs)
		if why != "" {
			w.insufficient = revInsufficient
			w.exclude("no_qualified_independent_reviewers", "%s", why)
			continue
		}
		if err := a.pair(w, plan); err != nil {
			w.exclude("unit_conflict", "%v", err)
			continue
		}
		if u, ok := w.Funding.Unit[w.p.Billing.Pool]; ok && u != w.p.Billing.Unit {
			w.exclude("unit_conflict", "pool %s is %s in billing but %s in the cost evidence", w.p.Billing.Pool, w.p.Billing.Unit, u)
			continue
		}
		a.checkBound(w)
	}
	round.cands = append(slices.Clone(ws), revs...)
	return a.rankWorkers(round, ws)
}

func (a *auto) rankWorkers(round *Round, ws []*cand) ([]*cand, error) {
	var live []*cand
	var items []rankItem
	for _, w := range ws {
		if w.Eligible && !w.boundMissing {
			live = append(live, w)
			lat := math.Inf(1)
			if w.Worker.MeanMinutes != nil {
				lat = *w.Worker.MeanMinutes
			}
			items = append(items, rankItem{ID: w.Agent + ":" + w.Model + "/" + w.name, Q: w.Worker.Q, Latency: lat, Cost: w.cost})
		}
	}
	return a.applyRank(live, items)
}

func (a *auto) applyRank(live []*cand, items []rankItem) ([]*cand, error) {
	b := a.basis()
	order, dropped, err := rank(a.pol.Objective, items, b)
	for i, why := range dropped {
		live[i].exclude("cost_basis_unknown", "%s", strings.TrimPrefix(why, "cost_basis_unknown: "))
	}
	if err != nil {
		return nil, err
	}
	out := make([]*cand, 0, len(order))
	for pos, i := range order {
		cd := live[i]
		cd.Rank = pos + 1
		if v, ok, _ := b.value(items[i].Cost); ok && b.cfg != nil {
			cd.BasisValue = &v
			cd.expires = minTime(cd.expires, b.expiry(items[i].Cost))
		}
		out = append(out, cd)
	}
	return out, nil
}

// resolveWorker proves the identity of the worker that actually ran from the ledger. The coordinator's claim may only agree with it.
func (a *auto) resolveWorker() error {
	_, model, err := ledger.WorkerIdentity(a.rows, a.in.Run, a.c.Name)
	if err != nil {
		return refuse(CodeWorkerIdentity, "%v", err)
	}
	if a.in.WorkerModel != "" && a.in.WorkerModel != model {
		return refuse(CodeWorkerIdentity, "the coordinator says the worker was %q but the ledger records %q", a.in.WorkerModel, model)
	}
	if a.workerMaker = modelMaker(model); a.workerMaker == "unknown" {
		return refuse(CodeWorkerIdentity, "the maker of worker model %q is unknown, so reviewer independence cannot be proven", model)
	}
	a.workerAttempt, _ = ledger.WorkerAttempt(a.rows, a.in.Run, a.c.Name)
	return nil
}

// rankReviewers: a review selects ONE reviewer; a T3 task calls again with exclude_makers for the maker that already reviewed.
func (a *auto) rankReviewers(round *Round, revs []*cand) ([]*cand, error) {
	round.cands = revs
	var live []*cand
	var items []rankItem
	for _, r := range revs {
		if !r.Eligible || r.boundMissing {
			continue
		}
		switch {
		case r.Maker == "unknown" || r.Maker == a.workerMaker:
			r.exclude("same_or_unknown_maker", "reviewer maker %s vs worker maker %s", r.Maker, a.workerMaker)
			continue
		case slices.Contains(a.class.ExcludeMakers, r.Maker):
			r.exclude("maker_already_reviewed", "maker %s already has a verdict for this revision", r.Maker)
			continue
		}
		r.cost, r.fund = reviewVec(r), newVec()
		_ = r.fund.addAll(r.ReviewCost.MaxReview, r.ReviewCost.Units, 1) // this call funds one review round
		if u, ok := r.fund.Unit[r.p.Billing.Pool]; ok && u != r.p.Billing.Unit {
			r.exclude("unit_conflict", "pool %s is %s in billing but %s in the cost evidence", r.p.Billing.Pool, r.p.Billing.Unit, u)
			continue
		}
		r.Cost, r.Funding = &r.cost, &r.fund
		live = append(live, r)
		items = append(items, rankItem{ID: r.Agent + ":" + r.Model + "/" + r.name, Q: r.Reviewer.Q, Latency: math.Inf(1), Cost: r.cost})
	}
	return a.applyRank(live, items)
}

// noCandidate explains why nothing could be selected, with the most specific code the evidence supports.
func (a *auto) noCandidate(round *Round) error {
	counts := map[string]int{}
	bound := false
	for _, c := range round.cands {
		if c.Role != a.role() {
			continue
		}
		if code, _, ok := strings.Cut(c.Excluded, ":"); ok {
			counts[code]++
		}
		bound = bound || c.boundMissing
	}
	if bound {
		return refuse(CodeProbeBound, "a candidate is otherwise eligible but declares no finite probe_bound; no inference was run (%s)", summarize(counts))
	}
	return refuse(CodeNoCandidate, "no candidate passed every gate (%s)", summarize(counts))
}

func summarize(counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s x%d", k, counts[k])
	}
	return strings.Join(parts, ", ")
}

// tryOrdered reserves and probes ranked candidates. A reservation that does not fit excludes that candidate and moves down the
// ranking inside the same decision; a failed probe ends the decision (probed=true) so the caller refreshes and re-ranks.
func (a *auto) tryOrdered(round *Round, ordered []*cand) (d *Decision, probed bool, err error) {
	for _, cd := range ordered {
		req := budget.ReserveRequest{Run: a.in.Run, Task: a.c.Name, Attempt: a.attempt, Decision: round.Decision, Items: a.items(cd)}
		if a.class.Phase == "review" {
			req.Handover = a.workerAttempt // the capacity the worker's plan set aside for this review funds it
		}
		hold, err := budget.Reserve(a.in.MarkerPath, a.budgetProf, req)
		var short *budget.ErrInsufficient
		if errors.As(err, &short) {
			cd.exclude("budget_insufficient", "%v", short)
			round.Outcome = "budget_insufficient"
			continue
		}
		if err != nil {
			return nil, probed, err
		}
		round.Probed, round.Hold, probed = cd.name, hold.ID, true
		res, err := a.probe(round, cd, hold)
		if err != nil {
			return nil, probed, err
		}
		round.Probe = &res
		switch res.State {
		case "up":
			round.Outcome = "selected"
			a.win, a.winHold = cd, hold
			d, err := a.receiptFor(round, cd, hold, res)
			return d, probed, err
		case "quota":
			round.Outcome = "probe_quota"
			a.failed[cd.name] = "probe hit a quota signal"
		default:
			round.Outcome = "probe_down"
			a.failed[cd.name] = "probe failed: " + res.Detail
		}
		// the probe's bound is spent whatever the result: charge it and release the funding plan before anything else can fail
		if err := a.chargeProbeAndRelease(round, cd, hold); err != nil {
			return nil, probed, err
		}
		cd.exclude("probe_failed", "%s", a.failed[cd.name])
		if res.State == "quota" {
			if err := a.coolPool(cd); err != nil {
				return nil, probed, err
			}
		}
		return nil, probed, nil
	}
	return nil, probed, nil
}

func (a *auto) row(kind string, round *Round, cd *cand) ledger.Row {
	return ledger.Row{Kind: kind, Task: a.c.Name, Run: a.in.Run, Attempt: a.attempt, Decision: round.Decision, Role: a.chain(), Provider: cd.name,
		Model: cd.p.Model, Purpose: "contracted routing"}
}

// role is the candidate role this phase selects.
func (a *auto) role() string {
	if a.class.Phase == "review" {
		return "reviewer"
	}
	return "worker"
}

func (a *auto) chain() string {
	if a.class.Phase == "review" {
		return "review:auto"
	}
	return "worker:" + a.class.Policy
}

// probe records the intent first (a missing ledger must never leave an untracked model call), runs the single bounded
// availability probe and records the result.
func (a *auto) probe(round *Round, cd *cand, hold *budget.Hold) (providers.Result, error) {
	intent := a.row("routing_probe", round, cd)
	intent.Notes = fmt.Sprintf(`{"hold":%q,"bound_calls":%d,"bound_amount":%g,"unit":%q}`, hold.ID, cd.p.ProbeBound.Calls, cd.p.ProbeBound.Amount, cd.p.Billing.Unit)
	if err := ledger.Append(intent); err != nil {
		// nothing ran: release the whole reservation without charging
		measured := map[string]float64{}
		for _, it := range hold.Items {
			measured[it.Pool] = 0
		}
		_, _ = budget.Settle(hold.ID, measured)
		return providers.Result{}, fmt.Errorf("probe ledger: %w", err)
	}
	probe := a.in.Probe
	if probe == nil {
		probe = providers.Probe
	}
	res := probe(a.cfg, cd.name, a.in.Timeout)
	result := a.row("routing_probe_result", round, cd)
	b, _ := json.Marshal(res)
	result.Notes = string(b)
	if err := ledger.Append(result); err != nil {
		// the probe ran: its bound is spent whatever happens next, and the reservation stays until it is charged
		_ = a.chargeProbeAndRelease(round, cd, hold)
		return res, fmt.Errorf("probe result ledger: %w", err)
	}
	return res, nil
}

// probeCharge is what one failed or abandoned probe is charged: the declared bound, because actual usage is unknown.
func (a *auto) probeCharge(round *Round, cd *cand, hold *budget.Hold) (ledger.Row, error) {
	b := cd.p.Billing
	return ledger.NewChargeRow(a.attempt, hold.ID+":probe:"+b.Pool, "probe", b.Pool, b.Unit, cd.name, cd.p.Model, cd.p.ProbeBound.Amount)
}

// chargeProbeAndRelease charges a probe that will not become a launch at its bound and releases the funding plan.
func (a *auto) chargeProbeAndRelease(round *Round, cd *cand, hold *budget.Hold) error {
	row, err := a.probeCharge(round, cd, hold)
	if err != nil {
		return err
	}
	row.Run, row.Task, row.Decision, row.Approx, row.Notes = a.in.Run, a.c.Name, round.Decision, true, "charged at the declared probe bound: actual usage unknown"
	if err := ledger.Append(row); err != nil {
		return fmt.Errorf("record probe charge (the reservation stays until it is recorded): %w", err)
	}
	measured := map[string]float64{}
	for _, it := range hold.Items {
		measured[it.Pool] = 0
	}
	measured[cd.p.Billing.Pool] = cd.p.ProbeBound.Amount
	_, err = budget.Settle(hold.ID, measured)
	return err
}

// abandonWinner settles the reservation of a candidate whose probe came up but whose receipt could not be written: the probe
// ran, so its bound is charged, and the funding plan is released rather than left reserved behind a receipt that does not exist.
func (a *auto) abandonWinner(round *Round, cause error) error {
	if a.win == nil {
		return cause
	}
	err := a.chargeProbeAndRelease(round, a.win, a.winHold)
	a.win = nil
	if err != nil {
		return fmt.Errorf("%w (and the reservation could not be released: %v)", cause, err)
	}
	return cause
}

// releaseUnused settles the reservation behind the receipt a new preparation replaces when nothing ran under it: its funding is
// released and only its probe is charged, instead of staying reserved behind a receipt that is about to be deleted.
func releaseUnused(path string) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var old Decision
	if json.Unmarshal(b, &old) != nil || old.Attempt == "" || old.Hold == "" {
		return nil
	}
	rows, err := ledger.LoadStrict("")
	if err != nil {
		return fmt.Errorf("ledger unreadable, cannot release the previous reservation: %w", err)
	}
	if attemptRan(rows, old.Attempt) {
		return nil // it ran: whoever ran it settles it with `rein route settle`
	}
	if _, err = SettleAttempt(old.Attempt); err != nil {
		return fmt.Errorf("release the previous reservation (attempt %s): %w", old.Attempt, err)
	}
	return nil
}

// coolPool marks the shared quota pool unavailable until an explicit clear: one provider's quota failure is every provider's on that pool.
func (a *auto) coolPool(cd *cand) error {
	var unlock func()
	var err error
	for i := 0; i < 40; i++ {
		if unlock, err = lock(); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		return err
	}
	defer unlock()
	s, err := state()
	if err != nil {
		return err
	}
	s[cd.coolPool] = Cooldown{Reason: "provider quota: " + cd.name}
	a.cool = s
	return atomic(filepath.Join(dir(), "cooldowns.json"), s)
}

// receiptFor builds the evidence-bound receipt of the candidate whose probe came up.
func (a *auto) receiptFor(round *Round, cd *cand, hold *budget.Hold, res providers.Result) (*Decision, error) {
	now := a.now()
	d := &Decision{Task: a.c.Name, Run: a.in.Run, Worktree: a.c.Worktree, ContractHash: contractHash(a.c), HooksGeneration: a.c.HooksGeneration,
		ConfigPath: a.cfgPath, ConfigHash: a.cfgHash, Chain: a.chain(), Provider: cd.name, Agent: cd.p.Agent, Model: cd.p.Model, Launch: cd.p.Launch,
		Maker: cd.Maker, Pool: cd.coolPool, PreparedAt: now, Attempts: []providers.Result{res},
		DecisionID: round.Decision, Attempt: a.attempt, ParentDecision: round.Parent, SelectionMode: a.mode, Objective: a.pol.Objective,
		Kind: a.class.Kind, Tier: a.class.Tier, Suite: a.pol.Suite, Effort: cd.p.Effort, ConfigFingerprint: fingerprint(cd.p),
		ReviewerPlan: cd.Candidate.ReviewerPlan, TaskProfileHash: hashBytes(a.profileRaw), InventoryHash: providers.HashInventories(a.invs),
		PolicyHash: a.pol.Hash(), Hold: hold.ID}
	if a.class.Phase == "review" {
		d.WorkerModel = a.reviewedWorker()
	}
	var err error
	if d.QualityHash, err = qualityHashFor(a.rows, d, a.cfg, a.pol, a.now()); err != nil {
		return nil, err
	}
	d.PricingHash = pricingHashFor(a.cfg, a.prices, a.pol, d)
	exp := minTime(cd.expires, now.Add(receiptTTL))
	d.ExpiresAt = &exp
	return d, nil
}

func (a *auto) reviewedWorker() string {
	_, model, _ := ledger.WorkerIdentity(a.rows, a.in.Run, a.c.Name)
	return model
}

// persist writes the receipt, its evidence artifacts and the ledger records. The receipt goes last: no receipt without its evidence.
func (a *auto) persist(path string, d *Decision, round *Round, ordered []*cand) error {
	snap, _ := json.MarshalIndent(a.invs, "", "  ")
	if err := writeArtifact(a.c.Name, "profile.json", a.profileRaw); err != nil {
		return err
	}
	if err := writeArtifact(a.c.Name, "inventory.json", snap); err != nil {
		return err
	}
	a.res.Decision, a.res.SelectionMode, a.res.Receipt, a.res.Launch = round.Decision, d.SelectionMode, d, launchOf(d)
	for _, c := range round.cands {
		if c.name == d.Provider && c.Role == a.role() {
			v := c.Candidate
			a.res.Selected = &v
		}
	}
	a.res.Comparison = a.comparison(round)
	a.res.SharedOverhead, _ = ledger.SharedOverhead(a.rows, a.attempt)
	report, _ := json.MarshalIndent(a.res, "", "  ")
	if err := writeArtifact(a.c.Name, "report.json", report); err != nil {
		return err
	}
	if err := record("routing", d); err != nil {
		return fmt.Errorf("routing ledger: %w", err)
	}
	return atomic(path, d)
}

// baselineOrder is the explicit unscored fallback (selection.baseline_chain). In the first decision, or while it is already in
// use, when no candidate qualified and some were blocked only for want of evidence, those candidates are tried in the chain's
// order. It relaxes the evidence qualification and the reviewer plan and nothing else: a candidate any other gate excluded, or
// that was disqualified on its record, stays out; and the reservation, probe bound, availability probe and receipt are the scored
// path's. It is reported as baseline_insufficient_evidence and is never scored selection.
func (a *auto) baselineOrder(round *Round) []*cand {
	chain := a.pol.BaselineChain
	if chain == "" || !strings.HasPrefix(chain, a.class.Phase+":") {
		return nil
	}
	var out []*cand
	for _, name := range a.cfg.Chains()[chain] {
		for _, cd := range round.cands {
			if cd.name != name || cd.Role != a.role() || !cd.insufficient {
				continue
			}
			if why := boundProblem(cd.p); why != "" { // the eligibility path checks the bound last; this candidate never reached it
				cd.boundMissing = true
				cd.exclude("probe_bound_required", "%s", why)
				continue
			}
			if a.class.Phase == "review" {
				if cd.Maker == "unknown" || cd.Maker == a.workerMaker {
					cd.exclude("same_or_unknown_maker", "reviewer maker %s vs worker maker %s", cd.Maker, a.workerMaker)
					continue
				}
				if slices.Contains(a.class.ExcludeMakers, cd.Maker) {
					cd.exclude("maker_already_reviewed", "maker %s already has a verdict for this revision", cd.Maker)
					continue
				}
			}
			cd.BaselineReason, cd.Excluded, cd.Eligible, cd.insufficient = cd.Excluded, "", true, false
			out = append(out, cd)
		}
	}
	if len(out) > 0 {
		a.mode, a.res.SelectionMode = modeBaseline, modeBaseline
	}
	return out
}
