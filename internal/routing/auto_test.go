package routing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	syncatomic "sync/atomic"
	"testing"
	"time"

	"github.com/voravitl/rein/internal/budget"
	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/ledger"
	"github.com/voravitl/rein/internal/providers"
	"github.com/voravitl/rein/internal/run"
)

// The world: three worker candidates and two reviewers, all with real evidence in a temp ledger, fake catalogs and fake probes.
//
//	w-sub     codex   gpt-sub          openai     subscription  pool codex/sub/week   2% per attempt
//	w-usd     opencode2 opencode-go/glm-w  glm    api (usd)     pool glm/api          0.30 USD per attempt
//	w-credits kiro    claude-w         anthropic  credits       pool kiro/team/month  1.5 credits per attempt
//	r-claude  claude  claude-opus-r    anthropic  subscription  pool claude/max/5h    4% per review
//	r-gpt     codex   gpt-r            openai     subscription  pool codex/sub/week   3% per review

func ip(v int) *int              { return &v }
func fl(v float64) *float64      { return &v }
func yes() *bool                 { t := true; return &t }
func no() *bool                  { f := false; return &f }
func ago(d time.Duration) string { return time.Now().Add(-d).UTC().Format(time.RFC3339) }

type world struct {
	t       *testing.T
	c       *contract.Contract
	cfg     providers.Config
	cfgPath string
	log     string
	sel     *contract.Selection
	calls   syncatomic.Int32
	invHook func(call int, inv *providers.Inventory)
	probes  map[string]string // provider -> probe output
	profile func(*TaskProfile)
	marker  string
	budget  *contract.Profile
	tier    string   // risk tier the seeded evidence belongs to (default T1)
	diff    []string // the files the checkout "actually" changes, for a review
	diffErr error
}

func newWorld(t *testing.T) *world {
	t.Helper()
	d := t.TempDir()
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(d, "contracts"))
	t.Setenv("PIPELINE_LEDGER", filepath.Join(d, "ledger"))
	t.Setenv("PIPELINE_PRICES", filepath.Join(d, "prices.json"))
	t.Setenv("REIN_RUN_DIR", filepath.Join(d, "rundir"))
	t.Setenv("REIN_PROFILE", "")
	wt := filepath.Join(d, "worktree")
	for _, p := range []string{wt, contract.IndexDir(), filepath.Join(d, "rundir")} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	initReviewGit(t, wt)
	rates := `{"opencode-go/glm-w":{"input_per_mtok":1,"output_per_mtok":4,"source":"https://example.com/pricing","as_of":"2026-01-01T00:00:00Z","valid_until":"2099-01-01T00:00:00Z"}}`
	if err := os.WriteFile(os.Getenv("PIPELINE_PRICES"), []byte(rates), 0o600); err != nil {
		t.Fatal(err)
	}
	w := &world{t: t, log: filepath.Join(d, "probes"), cfgPath: filepath.Join(d, "config.json"), probes: map[string]string{}, tier: "T1", diff: []string{"src/api/users.go"}}
	w.sel = &contract.Selection{Objective: "balanced", AcceptFreshness: []string{"client_catalog"}, InventoryMaxAgeMinutes: 60, Suite: "s1",
		Worker:    contract.WorkerPolicy{QualityFloor: contract.QualityFloor{MinApprovalRate: 0.5, MaxFalseClaims: ip(0)}, MinCompleteAttempts: 5, MaxEvidenceAgeDays: 30},
		Reviewer:  contract.ReviewerPolicy{MinDefectFixtures: 4, MinCleanFixtures: 4, MinRecall: 0.4, MinSpecificity: 0.4, MaxFalsePositiveRate: fl(0.5), MaxEvidenceAgeDays: 30},
		CostBasis: &contract.CostBasis{Mode: "monetary_marginal"}}
	w.c = &contract.Contract{Name: "task", Worktree: wt, Allow: []string{"src/api/**", "docs/**"}, Scope: []string{"S1"}, ReportPath: filepath.Join(d, "report"),
		HooksGeneration: "g1", HooksInstalled: []string{"codex", "claude", "kiro", "opencode"}, MaxChangedLines: 100,
		Profile: contract.Profile{SensitivePaths: []string{"infra/secrets/**"}}}
	bill := func(mode, unit, pool string) *providers.Billing {
		return &providers.Billing{Mode: mode, Unit: unit, Pool: pool}
	}
	prov := func(agent, model string, b *providers.Billing, amount float64) providers.Provider {
		return providers.Provider{Agent: agent, Model: model, Launch: "shell", Effort: "high", Capabilities: []string{"repository-edit", "tests"}, ContextTokens: 200000,
			Billing: b, ProbeBound: &providers.Bound{Calls: 1, Amount: amount}}
	}
	w.cfg = providers.Config{
		Providers: map[string]providers.Provider{
			"w-sub":     prov("codex", "gpt-sub", bill("subscription", "subscription_percent", "codex/sub/week"), 0.5),
			"w-usd":     prov("opencode2", "opencode-go/glm-w", bill("api", "usd", "glm/api"), 0.01),
			"w-credits": prov("kiro", "claude-w", bill("credits", "kiro_credits", "kiro/team/month"), 1),
			"r-claude":  prov("claude", "claude-opus-r", bill("subscription", "subscription_percent", "claude/max/5h"), 0.5),
			"r-gpt":     prov("codex", "gpt-r", bill("subscription", "subscription_percent", "codex/sub/week"), 0.5),
		},
		WorkerChains: map[string][]string{"backend": {"w-sub", "w-usd", "w-credits"}, "high-risk": {"w-sub"}},
		ReviewChains: map[string][]string{"all": {"r-claude", "r-gpt"}},
		QuotaSignals: map[string][]string{"codex": {"quota"}},
	}
	w.seedAll()
	return w
}

// writeConfig (re)writes the provider config with fake probes.
func (w *world) writeConfig() {
	w.t.Helper()
	cfg := w.cfg
	cfg.Providers = map[string]providers.Provider{}
	for n, p := range w.cfg.Providers {
		out := "OK"
		if v, ok := w.probes[n]; ok {
			out = v
		}
		p.Probe = []string{os.Args[0], "-test.run=TestProbeProcess", "--", w.log, n, out}
		cfg.Providers[n] = p
	}
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(w.cfgPath, b, 0o600); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) seed(rows ...ledger.Row) {
	w.t.Helper()
	f, err := os.OpenFile(ledger.Path(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		w.t.Fatal(err)
	}
	defer f.Close()
	for _, r := range rows {
		if r.RecordedAt == "" {
			r.RecordedAt = ago(time.Hour)
		}
		b, _ := json.Marshal(r)
		fmt.Fprintf(f, "%s\n", b)
	}
}

// seedWorker records n settled attempts of provider name, the first `ok` of them verified successes.
func (w *world) seedWorker(name string, n, ok int, perAttempt float64, reviewerAgent, reviewerModel string) {
	w.t.Helper()
	p := w.cfg.Providers[name]
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("seed-%s-%s-%d-%d", name, w.tier, n, i)
		at := ago(time.Duration(i+2) * time.Hour)
		launch := ledger.Row{Kind: "routing_launch", Attempt: id, Run: "seed", Task: "seed", Worker: &ledger.AgentModel{Agent: p.Agent, Model: p.Model}, Type: "backend",
			Effort: p.Effort, Config: fingerprint(p), Suite: "s1", Tier: w.tier, RecordedAt: at}
		out := ledger.Row{Kind: "task", Attempt: id, Run: "seed", Task: "seed", Source: "measured", RecordedAt: at, ReviewRounds: ip(1), Minutes: fl(8),
			GatesPassed: yes(), Reviewer: &ledger.AgentModel{Agent: reviewerAgent, Model: reviewerModel}, ReviewSHA: "sha", Approved: true,
			Blockers: ip(0), Highs: ip(0), FalseClaims: ip(0), Drift: ip(0)}
		if i >= ok {
			out = ledger.Row{Kind: "task", Attempt: id, Run: "seed", Task: "seed", Source: "measured", RecordedAt: at, GatesPassed: no(), FalseClaims: ip(0)}
		}
		ch, err := ledger.NewChargeRow(id, "w", "worker", p.Billing.Pool, p.Billing.Unit, p.Agent, p.Model, perAttempt)
		if err != nil {
			w.t.Fatal(err)
		}
		ch.RecordedAt = at
		w.seed(launch, out, ch)
	}
}

// seedReviewer records frozen red/clean fixture judgements and per-review charges.
func (w *world) seedReviewer(name string, defect, detected, clean, falsePos int, perReview float64) {
	w.t.Helper()
	p := w.cfg.Providers[name]
	base := ledger.Row{Kind: "fixture", Reviewer: &ledger.AgentModel{Agent: p.Agent, Model: p.Model}, Type: "backend", Effort: p.Effort, Config: fingerprint(p),
		Suite: "s1", Tier: w.tier, Adjudicated: yes(), RecordedAt: ago(time.Hour)}
	for i := 0; i < defect; i++ {
		r := base
		r.Fixture, r.FixtureClass = fmt.Sprintf("d%d", i), "defect"
		r.Detected = map[bool]*bool{true: yes(), false: no()}[i < detected]
		w.seed(r)
	}
	for i := 0; i < clean; i++ {
		r := base
		r.Fixture, r.FixtureClass = fmt.Sprintf("c%d", i), "clean"
		r.FalsePositive = map[bool]*bool{true: yes(), false: no()}[i < falsePos]
		w.seed(r)
	}
	for i := 0; i < 3; i++ {
		ch, err := ledger.NewChargeRow(fmt.Sprintf("rv-%s-%s-%d", name, w.tier, i), "rv", "review", p.Billing.Pool, p.Billing.Unit, name, p.Model, perReview)
		if err != nil {
			w.t.Fatal(err)
		}
		ch.RecordedAt = ago(time.Hour)
		ch.Effort, ch.Config = p.Effort, fingerprint(p)
		w.seed(ch)
	}
}

func (w *world) seedAll() {
	w.seedWorker("w-sub", 5, 5, 2, "claude", "claude-opus-r")
	w.seedWorker("w-usd", 5, 5, 0.30, "claude", "claude-opus-r")
	w.seedWorker("w-credits", 5, 5, 1.5, "codex", "gpt-r")
	w.seedReviewer("r-claude", 4, 4, 4, 0, 4)
	w.seedReviewer("r-gpt", 4, 4, 4, 0, 3)
}

func (w *world) discover(ctx context.Context, cfg *providers.Config, o providers.DiscoverOptions) []providers.Inventory {
	call := int(w.calls.Add(1))
	var out []providers.Inventory
	for _, h := range o.Harnesses {
		inv := providers.Inventory{Harness: h, Adapter: "fake", Scope: "scope-" + h, QueriedAt: time.Now().UTC(), Status: providers.InvComplete, Freshness: providers.ClientCatalog}
		for _, p := range cfg.Providers {
			if p.Agent == h {
				inv.Models = append(inv.Models, providers.ModelEntry{ID: p.Model})
			}
		}
		sort.Slice(inv.Models, func(i, j int) bool { return inv.Models[i].ID < inv.Models[j].ID })
		if w.invHook != nil {
			w.invHook(call, &inv)
		}
		out = append(out, inv)
	}
	return out
}

func (w *world) profileJSON() []byte {
	tp := &TaskProfile{ContractHash: contractHash(w.c), Phase: "worker", Kind: "backend", RequiredCapabilities: []string{"repository-edit", "tests"}, ExpectedContext: 50000,
		PlannedFiles: []string{"src/api/users.go"}, Rationale: []Rationale{{Path: "src/api/users.go", Reason: "adds the endpoint"}}}
	if w.profile != nil {
		w.profile(tp)
	}
	b, _ := json.Marshal(tp)
	return b
}

func (w *world) in() AutoInput {
	w.c.Profile.Selection = w.sel
	w.writeConfig()
	return AutoInput{Contract: w.c, Run: "run", TaskProfile: w.profileJSON(), ConfigPath: w.cfgPath, Timeout: 10 * time.Second, Discover: w.discover,
		MarkerPath: w.marker, Budget: w.budget, Diff: func(string, string) ([]string, error) { return w.diff, w.diffErr }}
}

func (w *world) prepare() (*AutoResult, error) { return PrepareAuto(context.Background(), w.in()) }

func (w *world) probeLog() []string {
	b, _ := os.ReadFile(w.log)
	return strings.Fields(string(b))
}

func excluded(r *AutoResult, provider string) string {
	for _, c := range r.Rounds[len(r.Rounds)-1].Candidates {
		if c.Provider == provider {
			return c.Excluded
		}
	}
	return "(not evaluated)"
}

func mustPrepare(t *testing.T, w *world) *AutoResult {
	t.Helper()
	r, err := w.prepare()
	if err != nil {
		t.Fatalf("%v\n%s", err, dump(r))
	}
	return r
}

func dump(r *AutoResult) string {
	b, _ := json.MarshalIndent(r, "", " ")
	return string(b)
}

// ---- the happy path and the objective (acceptance tests 5 and 1) ----

func TestBalancedPicksTheLowerComparableCostAndReportsEverything(t *testing.T) {
	w := newWorld(t)
	r := mustPrepare(t, w)
	d := r.Receipt
	// w-sub: subscription (known zero cash) + reviewed by a subscription reviewer; w-usd costs 0.30 USD; w-credits has no measured cash price
	if d.Provider != "w-sub" || d.Agent != "codex" || d.Model != "gpt-sub" || d.Effort != "high" || d.SelectionMode != modeScored || r.Objective != "balanced" {
		t.Fatalf("%s", dump(r))
	}
	if d.DecisionID == "" || d.Attempt == "" || !strings.HasPrefix(d.DecisionID, d.Attempt) || d.Hold == "" || d.ExpiresAt == nil {
		t.Errorf("receipt must carry decision identity and reservation: %+v", d)
	}
	for name, h := range map[string]string{"task profile": d.TaskProfileHash, "inventory": d.InventoryHash, "pricing": d.PricingHash, "quality": d.QualityHash, "policy": d.PolicyHash} {
		if h == "" {
			t.Errorf("missing %s hash", name)
		}
	}
	if got := excluded(r, "w-credits"); !strings.HasPrefix(got, "cost_basis_unknown") {
		t.Errorf("a credit price with no measured cash cost can never be the cheapest: %q", got)
	}
	if got := excluded(r, "w-usd"); got != "" {
		t.Errorf("w-usd is eligible, just costlier: %q", got)
	}
	sel := r.Selected
	if sel == nil || sel.Rank != 1 || sel.Worker == nil || sel.Worker.Successes != 5 || len(sel.ReviewerPlan) != 1 || sel.BasisValue == nil || *sel.BasisValue != 0 {
		t.Errorf("the report must show rank, evidence, the bound reviewer plan and the basis value: %s", dump(r))
	}
	if r.Class == nil || r.Class.Policy != "backend" || len(r.Rounds) != 1 || len(r.Rounds[0].Inventories) == 0 {
		t.Errorf("classification and inventories must be in the report: %s", dump(r))
	}
	if _, err := os.Stat(filepath.Join(artifactDir("task"), "report.json")); err != nil {
		t.Error("the report must be kept beside the receipt")
	}
	if got := strings.Join(w.probeLog(), ","); got != "w-sub" {
		t.Errorf("only the winner is probed, never every model: %s", got)
	}
	holds, _ := budget.HoldsFor(d.Attempt)
	if len(holds) != 1 || holds[0].State != "reserved" || holds[0].ID != d.Hold {
		t.Errorf("the winner's probe and funding stay reserved: %+v", holds)
	}
	if _, err := Validate(w.c, "run", "codex", "gpt-sub"); err != nil {
		t.Errorf("a fresh receipt must validate: %v", err)
	}
}

func TestQualityFirstOrdersByQualityBeforeCost(t *testing.T) {
	w := newWorld(t)
	w.seedWorker("w-usd", 15, 15, 0.30, "claude", "claude-opus-r") // stronger record than w-sub's 5/5
	w.sel.Objective = "quality_first"
	r := mustPrepare(t, w)
	if r.Receipt.Provider != "w-usd" || r.Objective != "quality_first" {
		t.Errorf("quality-first must prefer the higher lower-bound even though it costs more: %s", dump(r))
	}
	w2 := newWorld(t)
	w2.seedWorker("w-usd", 15, 15, 0.30, "claude", "claude-opus-r")
	if r := mustPrepare(t, w2); r.Receipt.Provider != "w-sub" {
		t.Errorf("balanced stays on the cheaper candidate: %s", r.Receipt.Provider)
	}
}

func TestTiesBreakByQualityThenLatencyThenStableID(t *testing.T) {
	items := []rankItem{
		{ID: "b", Q: 50, Latency: 5, Cost: oneCost("p", 1)},
		{ID: "a", Q: 50, Latency: 5, Cost: oneCost("p", 1)},
		{ID: "c", Q: 50, Latency: 3, Cost: oneCost("p", 1)},
		{ID: "d", Q: 60, Latency: 9, Cost: oneCost("p", 1)},
		{ID: "e", Q: 90, Latency: 1, Cost: oneCost("p", 2)},
	}
	order, dropped, err := rank("balanced", items, basis{})
	if err != nil || len(dropped) != 0 {
		t.Fatal(err, dropped)
	}
	var got []string
	for _, i := range order {
		got = append(got, items[i].ID)
	}
	if strings.Join(got, "") != "dcabe" { // cost first (all 1 before 2), then higher Q (d), then lower latency (c), then ID (a < b)
		t.Errorf("order %v", got)
	}
}

func oneCost(pool string, amt float64) costVec {
	v := newVec()
	_ = v.add(pool, "usd", amt)
	return v
}

// ---- cost comparability (acceptance test 4) ----

func TestIncomparablePoolsNeedADeclaredBasis(t *testing.T) {
	w := newWorld(t)
	w.sel.CostBasis = nil
	r, err := w.prepare()
	if ErrCode(err) != CodeCostBasis {
		t.Fatalf("subscription, usd and credit pools cannot be ranked without a declared basis: %v\n%s", err, dump(r))
	}
	if len(w.probeLog()) != 0 {
		t.Error("nothing may be probed when the choice is not defensible")
	}
	// quality-first does not need a cost basis to order by quality
	w.sel.Objective = "quality_first"
	if r, err = w.prepare(); err != nil || r.Receipt == nil {
		t.Errorf("quality-first without a basis: %v", err)
	}
}

func TestConvertedBasisIsAnExplicitPolicyEstimate(t *testing.T) {
	w := newWorld(t)
	w.sel.CostBasis = &contract.CostBasis{Mode: "converted", Unit: "usd_equiv", Conversions: map[string]contract.Conversion{
		"codex/sub/week":  {PerUnit: 0.20, Source: "owner estimate", AsOf: ago(24 * time.Hour), ValidDays: 30},
		"claude/max/5h":   {PerUnit: 0.10, Source: "owner estimate", AsOf: ago(24 * time.Hour), ValidDays: 30},
		"glm/api":         {PerUnit: 1, Source: "usd", AsOf: ago(24 * time.Hour), ValidDays: 30},
		"kiro/team/month": {PerUnit: 0.05, Source: "owner estimate", AsOf: ago(24 * time.Hour), ValidDays: 30},
	}}
	r := mustPrepare(t, w)
	// w-sub: 2*0.20 + 4*0.10 = 0.80; w-usd: 0.30 + 0.40 = 0.70; w-credits: 1.5*0.05 + 3*0.20 = 0.675 (reviewed by r-gpt)
	if r.Receipt.Provider != "w-credits" || r.Selected.BasisValue == nil || *r.Selected.BasisValue < 0.67 || *r.Selected.BasisValue > 0.68 {
		t.Errorf("%s", dump(r))
	}
	// an expired conversion is not a basis: the candidate that needs it is dropped, never assumed free
	w2 := newWorld(t)
	w2.sel.CostBasis = &contract.CostBasis{Mode: "converted", Unit: "u", Conversions: map[string]contract.Conversion{
		"codex/sub/week": {PerUnit: 0.20, Source: "old", AsOf: ago(90 * 24 * time.Hour), ValidDays: 30},
		"claude/max/5h":  {PerUnit: 0.10, Source: "x", AsOf: ago(time.Hour), ValidDays: 30},
		"glm/api":        {PerUnit: 1, Source: "x", AsOf: ago(time.Hour), ValidDays: 30},
	}}
	r = mustPrepare(t, w2)
	if r.Receipt.Provider != "w-usd" || !strings.Contains(excluded(r, "w-sub"), "expired") {
		t.Errorf("%s", dump(r))
	}
}

func TestUnknownCostNeverWins(t *testing.T) {
	w := newWorld(t)
	// w-sub keeps its quality record but loses its charges: cost evidence is unavailable, never zero
	rows, _ := ledger.LoadStrict("")
	var keep []ledger.Row
	for _, r := range rows {
		if !(r.Kind == "cost" && strings.HasPrefix(r.Attempt, "seed-w-sub-")) {
			keep = append(keep, r)
		}
	}
	os.Remove(ledger.Path())
	w.seed(keep...)
	r := mustPrepare(t, w)
	if r.Receipt.Provider == "w-sub" || !strings.HasPrefix(excluded(r, "w-sub"), "cost_unavailable") {
		t.Errorf("%s", dump(r))
	}
}

// ---- evidence gates (acceptance test 3) ----

func TestNewModelThinDataStaleAliasAndEffortChangesCannotQualify(t *testing.T) {
	cases := map[string]func(*world){
		"thin data": func(w *world) {
			os.Remove(ledger.Path())
			w.seedWorker("w-sub", 4, 4, 2, "claude", "claude-opus-r") // 4 < min 5
			w.seedWorker("w-usd", 5, 5, 0.3, "claude", "claude-opus-r")
			w.seedWorker("w-credits", 5, 5, 1.5, "codex", "gpt-r")
			w.seedReviewer("r-claude", 4, 4, 4, 0, 4)
			w.seedReviewer("r-gpt", 4, 4, 4, 0, 3)
		},
		"new model": func(w *world) {
			p := w.cfg.Providers["w-sub"]
			p.Model = "gpt-sub-next" // a successor does not inherit its predecessor's score
			w.cfg.Providers["w-sub"] = p
		},
		"effort change": func(w *world) {
			p := w.cfg.Providers["w-sub"]
			p.Effort = "low"
			w.cfg.Providers["w-sub"] = p
		},
		"suite bump": func(w *world) { w.sel.Suite = "s2" },
		"stale": func(w *world) { // every record is 40 days old, outside the 30-day evidence window
			rows, _ := ledger.LoadStrict("")
			os.Remove(ledger.Path())
			for i := range rows {
				if t, err := time.Parse(time.RFC3339, rows[i].RecordedAt); err == nil {
					rows[i].RecordedAt = t.Add(-40 * 24 * time.Hour).Format(time.RFC3339)
				}
			}
			w.seed(rows...)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			mutate(w)
			r, err := w.prepare()
			got := excluded(r, "w-sub")
			if name == "suite bump" || name == "stale" {
				if ErrCode(err) != CodeNoCandidate {
					t.Fatalf("every candidate lost its evidence: %v %s", err, dump(r))
				}
				return
			}
			if !strings.HasPrefix(got, "unqualified") && !strings.HasPrefix(got, "model_not_in_catalog") && !strings.HasPrefix(got, "no_qualified") {
				t.Errorf("w-sub must not qualify: %q\n%s", got, dump(r))
			}
			if err == nil && r.Receipt.Provider == "w-sub" {
				t.Error("selected an unqualified model")
			}
		})
	}
}

func TestMissingOutcomeFieldsAndFalseClaimsDisqualify(t *testing.T) {
	w := newWorld(t)
	w.sel.CostBasis = nil // the lone survivor needs no comparison
	rows, _ := ledger.LoadStrict("")
	os.Remove(ledger.Path())
	for i := range rows {
		if rows[i].Kind == "task" && strings.HasPrefix(rows[i].Attempt, "seed-w-sub") && rows[i].Attempt == "seed-w-sub-T1-5-0" {
			rows[i].Drift = nil // one unknown critical field: coverage drops below 100%
		}
		if rows[i].Kind == "task" && rows[i].Attempt == "seed-w-usd-T1-5-0" {
			rows[i].FalseClaims = ip(1) // one false claim exceeds the allowed 0
		}
	}
	w.seed(rows...)
	r := mustPrepare(t, w)
	if !strings.Contains(excluded(r, "w-sub"), "coverage") || !strings.Contains(excluded(r, "w-usd"), "false claim") || r.Receipt.Provider != "w-credits" {
		t.Errorf("%s", dump(r))
	}
}

func TestReviewerApprovalFrequencyIsNotQuality(t *testing.T) {
	w := newWorld(t)
	rows, _ := ledger.LoadStrict("")
	os.Remove(ledger.Path())
	var keep []ledger.Row
	for _, r := range rows {
		if r.Kind != "fixture" {
			keep = append(keep, r)
		}
	}
	w.seed(keep...) // no fixtures at all: only approval rows (the workers' reviewer field) remain
	r, err := w.prepare()
	if err == nil {
		t.Fatalf("no reviewer is qualified, so no worker may be selected: %s", dump(r))
	}
	if got := excluded(r, "w-sub"); !strings.HasPrefix(got, "no_qualified_independent_reviewers") {
		t.Errorf("a worker is eligible only with a qualified independent reviewer in the same refresh: %q", got)
	}
	// a reviewer below its floor on adjudicated fixtures does not qualify either, and the workers that depend on it lose their pair
	w2 := newWorld(t)
	w2.seedReviewer("r-gpt", 10, 1, 10, 9, 3) // judged again: misses most defects and flags most clean code
	r2 := mustPrepare(t, w2)
	if got := excluded(r2, "r-gpt"); !strings.HasPrefix(got, "unqualified: reviewer:") {
		t.Errorf("r-gpt: %q", got)
	}
	if got := excluded(r2, "w-credits"); !strings.HasPrefix(got, "no_qualified_independent_reviewers") {
		t.Errorf("an anthropic worker needs a qualified non-anthropic reviewer, and r-gpt no longer qualifies: %q", got)
	}
	if r2.Receipt.Provider == "w-credits" {
		t.Error("selected a worker without a qualified independent reviewer")
	}
}

func TestLedgerFailurePreventsTheProbeAndReleasesTheReservation(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("file permissions do not bind root")
	}
	w := newWorld(t)
	w.startRun()
	w.budget = &contract.Profile{Budget: &contract.Budget{Pools: map[string]contract.PoolCaps{"codex/sub/week": {RunCap: 100}, "claude/max/5h": {RunCap: 100}, "glm/api": {RunCap: 100}, "kiro/team/month": {RunCap: 100}}}}
	in := w.in()
	if err := os.Chmod(ledger.Path(), 0o400); err != nil { // readable evidence, but nothing can be recorded
		t.Fatal(err)
	}
	if _, err := PrepareAuto(context.Background(), in); err == nil || !strings.Contains(err.Error(), "probe ledger") {
		t.Fatalf("an unrecorded probe must not run: %v", err)
	}
	if len(w.probeLog()) != 0 {
		t.Errorf("spent a probe despite the ledger failure: %v", w.probeLog())
	}
	if out, _ := budget.Outstanding(); len(out) != 0 {
		t.Errorf("nothing ran, so nothing may stay reserved: %+v", out)
	}
	if _, e := os.Stat(filepath.Join(dir(), "decisions", "task.json")); e == nil {
		t.Error("no receipt without a recorded probe")
	}
}

func TestFailedProbeChargesItsBoundOnceAndReleasesItsFundingPlan(t *testing.T) {
	w := newWorld(t)
	w.startRun()
	w.budget = &contract.Profile{Budget: &contract.Budget{Pools: map[string]contract.PoolCaps{"codex/sub/week": {RunCap: 100}, "claude/max/5h": {RunCap: 100}, "glm/api": {RunCap: 100}, "kiro/team/month": {RunCap: 100}}}}
	w.probes["w-sub"] = "down"
	r := mustPrepare(t, w)
	if r.Receipt.Provider != "w-usd" {
		t.Fatalf("%s", dump(r))
	}
	rows, _ := ledger.LoadStrict("")
	probes := 0
	for _, x := range rows {
		if x.Kind == "cost" && x.Attempt == r.Attempt && x.Component == "probe" {
			probes++
			if x.Pool != "codex/sub/week" || *x.Amount != 0.5 || !x.Approx || x.Unit != "subscription_percent" {
				t.Errorf("the failed probe is charged at its declared bound: %+v", x)
			}
		}
	}
	if probes != 1 {
		t.Errorf("exactly one probe charge for the failed candidate, counted once: %d", probes)
	}
	holds, _ := budget.HoldsFor(r.Attempt)
	open := 0
	for _, h := range holds {
		if h.State == "reserved" {
			open++
			if h.ID != r.Receipt.Hold {
				t.Errorf("only the winner's hold stays open: %+v", h)
			}
		}
	}
	if open != 1 || len(holds) != 2 {
		t.Errorf("failed candidate's hold settled, winner's held: %+v", holds)
	}
	rem, err := budget.Remaining(w.marker, w.budget, "")
	if err != nil {
		t.Fatal(err)
	}
	// codex pool: only the failed probe's 0.5 is spent; w-usd runs on the glm pool, so the funding plan of w-sub (2) was released
	if got := rem["codex/sub/week"]; got < 99.5-0.0001 || got > 99.5+0.0001 {
		t.Errorf("codex pool remaining %v, want 99.5", got)
	}
}

// ---- policy and classification fail closed (acceptance test 2) ----

func TestMissingPolicyIsExplicitNotInvented(t *testing.T) {
	w := newWorld(t)
	w.sel.Worker.MinApprovalRate = 0
	r, err := w.prepare()
	if ErrCode(err) != CodeQualityPolicy || !strings.Contains(err.Error(), "min_approval_rate") || len(w.probeLog()) != 0 {
		t.Errorf("%v %v", err, r)
	}
	w2 := newWorld(t)
	w2.sel.AcceptFreshness = nil
	if _, err = w2.prepare(); ErrCode(err) != CodeSelectionPolicy {
		t.Errorf("the policy must declare which freshness class it accepts: %v", err)
	}
	w3 := newWorld(t)
	w3.sel = nil
	if _, err = w3.prepare(); ErrCode(err) != CodeSelectionPolicy {
		t.Errorf("%v", err)
	}
	// the quality floor works with no spending cap configured, and the legacy budget.quality_floor is reused when the policy has none
	w4 := newWorld(t)
	w4.sel.Worker.QualityFloor = contract.QualityFloor{}
	w4.c.Profile.Budget = &contract.Budget{QualityFloor: contract.QualityFloor{MinApprovalRate: 0.5, MaxFalseClaims: ip(0)}}
	w4.budget = &contract.Profile{} // no spending caps
	if _, err = w4.prepare(); err != nil {
		t.Errorf("legacy quality_floor must be reused without any pool caps: %v", err)
	}
}

func TestClassificationRefusalsStopBeforeAnyDiscovery(t *testing.T) {
	w := newWorld(t)
	w.profile = func(tp *TaskProfile) {
		tp.Kind = "mechanical"
		tp.Bounded = &Bounded{Operation: "rename", AcceptanceChecks: []string{"go build"}}
		tp.RiskFlags = []string{"secrets"}
	}
	r, err := w.prepare()
	if ErrCode(err) != CodeClassification || w.calls.Load() != 0 || len(w.probeLog()) != 0 || r.ErrorCode != CodeClassification {
		t.Errorf("T3 can never be mechanical: %v calls=%d", err, w.calls.Load())
	}
	w2 := newWorld(t)
	w2.profile = func(tp *TaskProfile) { tp.RequiredCapabilities = []string{"quantum-annealing"} }
	r, err = w2.prepare()
	if ErrCode(err) != CodeNoCandidate || !strings.HasPrefix(excluded(r, "w-sub"), "missing_capability") {
		t.Errorf("a missing required capability must fail closed: %v", err)
	}
	// the coordinator cannot name a model
	w3 := newWorld(t)
	in := w3.in()
	in.TaskProfile = []byte(strings.Replace(string(in.TaskProfile), `{`, `{"model":"gpt-sub",`, 1))
	if _, err = PrepareAuto(context.Background(), in); ErrCode(err) != CodeProfileInvalid {
		t.Errorf("%v", err)
	}
}

func TestT3TaskUsesHighRiskCandidatesAndTwoReviewerMakers(t *testing.T) {
	w := newWorld(t)
	w.c.Profile.SensitivePaths = []string{"src/api/**"}
	// risk-specific evidence: what was proven on T1 work says nothing about T3
	r, err := w.prepare()
	if ErrCode(err) != CodeNoCandidate || r.Class.Policy != "high-risk" || !strings.HasPrefix(excluded(r, "w-sub"), "unqualified") {
		t.Fatalf("T1 evidence must not qualify a model for T3: %v\n%s", err, dump(r))
	}
	w.tier = "T3"
	w.seedWorker("w-sub", 5, 5, 2, "claude", "claude-opus-r")
	w.seedReviewer("r-claude", 4, 4, 4, 0, 4)
	w.seedReviewer("r-gpt", 4, 4, 4, 0, 3)
	// the high-risk chain is {w-sub}; T3 needs two distinct reviewer makers, and r-gpt shares the worker's maker
	r, err = w.prepare()
	if ErrCode(err) != CodeNoCandidate || !strings.Contains(excluded(r, "w-sub"), "needs 2") {
		t.Fatalf("%v\n%s", err, dump(r))
	}
	// with a second independent maker qualified, the same task proceeds
	w.cfg.Providers["r-gemini"] = providers.Provider{Agent: "kiro", Model: "gemini-r2", Launch: "shell", Effort: "high", Capabilities: []string{"repository-edit", "tests"}, ContextTokens: 200000,
		Billing: &providers.Billing{Mode: "subscription", Unit: "subscription_percent", Pool: "gemini/pool"}, ProbeBound: &providers.Bound{Calls: 1, Amount: 0.1}}
	w.cfg.ReviewChains["all"] = append(w.cfg.ReviewChains["all"], "r-gemini")
	w.seedReviewer("r-gemini", 4, 4, 4, 0, 2)
	r = mustPrepare(t, w)
	if r.Receipt.Provider != "w-sub" || len(r.Selected.ReviewerPlan) != 2 || r.Receipt.Tier != "T3" {
		t.Errorf("%s", dump(r))
	}
}

// ---- refresh, retry and fallback (acceptance tests 1 and 6) ----

func TestEverySelectionRetryAndFallbackRefreshesAndOldSnapshotsAuthorizeNothing(t *testing.T) {
	w := newWorld(t)
	w.probes["w-sub"] = "down"
	// the harness of the first winner drops out of the SECOND refresh: a snapshot from the first decision must not authorize it
	w.invHook = func(call int, inv *providers.Inventory) {
		if call >= 2 && inv.Harness == "codex" {
			inv.Status, inv.Models, inv.Reason = providers.InvFailed, nil, "refresh failed"
		}
	}
	r := mustPrepare(t, w)
	if w.calls.Load() < 2 || len(r.Rounds) != 2 {
		t.Fatalf("a failed probe must start a new decision with a new refresh: calls=%d rounds=%d", w.calls.Load(), len(r.Rounds))
	}
	first, second := r.Rounds[0], r.Rounds[1]
	if first.Decision == second.Decision || second.Parent != first.Decision || first.Probed != "w-sub" || first.Outcome != "probe_down" || second.Outcome != "selected" {
		t.Errorf("decision chain: %s", dump(r))
	}
	if r.Receipt.Provider != "w-usd" || !strings.HasPrefix(excluded(r, "w-sub"), "failed_this_prepare") || !strings.HasPrefix(excluded(r, "r-gpt"), "harness_excluded") {
		t.Errorf("the failed refresh excludes the harness, and the failed probe stays excluded: %s", dump(r))
	}
	if got := strings.Join(w.probeLog(), ","); got != "w-sub,w-usd" {
		t.Errorf("probe order: %s", got)
	}
	if r.Receipt.ParentDecision != first.Decision || r.Receipt.Attempt != r.Attempt {
		t.Errorf("the receipt must name its parent decision and attempt: %+v", r.Receipt)
	}
}

func TestPartialInvalidAndUnsupportedCatalogsExcludeTheirHarness(t *testing.T) {
	for name, tc := range map[string]struct {
		hook   func(*providers.Inventory)
		prefix string
	}{
		"partial pagination": {func(i *providers.Inventory) { i.Status, i.Reason = providers.InvPartial, "page cap reached" }, "harness_excluded"},
		"failed refresh":     {func(i *providers.Inventory) { i.Status, i.Models = providers.InvFailed, nil }, "harness_excluded"},
		"unsupported": {func(i *providers.Inventory) {
			i.Status, i.Freshness, i.Models = providers.InvUnsupported, providers.FreshnessUnknown, nil
		}, "freshness_not_accepted"},
		"unaccepted class": {func(i *providers.Inventory) { i.Freshness = providers.FreshnessUnknown }, "freshness_not_accepted"},
		"model missing":    {func(i *providers.Inventory) { i.Models = nil }, "model_not_in_catalog"},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.invHook = func(_ int, inv *providers.Inventory) {
				if inv.Harness == "codex" {
					tc.hook(inv)
				}
			}
			r := mustPrepare(t, w)
			if r.Receipt.Provider == "w-sub" || !strings.HasPrefix(excluded(r, "w-sub"), tc.prefix) {
				t.Errorf("%q: %s", excluded(r, "w-sub"), dump(r))
			}
		})
	}
	// unsupported freshness is acceptable only when the policy says so; then the probe still has to prove availability
	w := newWorld(t)
	w.sel.AcceptFreshness = []string{"client_catalog", "unknown"}
	w.invHook = func(_ int, inv *providers.Inventory) {
		if inv.Harness == "claude" {
			inv.Status, inv.Freshness, inv.Models = providers.InvUnsupported, providers.FreshnessUnknown, nil
		}
	}
	if r := mustPrepare(t, w); r.Receipt.Provider != "w-sub" {
		t.Errorf("%s", dump(r))
	}
}

func TestQuotaFailureCoolsTheSharedPoolAndNoOverageOrBaselineSubstitutes(t *testing.T) {
	w := newWorld(t)
	w.probes["w-sub"] = "quota exceeded"
	w.sel.BaselineChain = "worker:backend" // an explicit baseline exists, but a failure after scoring must never fall back to it
	r := mustPrepare(t, w)
	if r.Receipt.Provider != "w-usd" || r.Receipt.SelectionMode != modeScored {
		t.Fatalf("%s", dump(r))
	}
	s, err := Status()
	if err != nil || !blocked(s, "codex") {
		t.Fatalf("the quota failure must cool the shared codex pool: %v %v", s, err)
	}
	// r-gpt shares that pool: the next preparation cannot use it for anything, as worker or reviewer
	w2 := newWorld(t)
	if err := SetCooldown(w2.in().ConfigPath, "w-sub", "quota evidence", nil, false); err != nil {
		t.Fatal(err)
	}
	r = mustPrepare(t, w2)
	if r.Receipt.Provider == "w-sub" || !strings.HasPrefix(excluded(r, "w-sub"), "pool_unavailable") || !strings.HasPrefix(excluded(r, "r-gpt"), "pool_unavailable") {
		t.Errorf("%s", dump(r))
	}
	if r.Selected.ReviewerPlan[0] != "r-claude" {
		t.Errorf("the cooled pool's reviewer must not be planned: %v", r.Selected.ReviewerPlan)
	}
}

func TestEveryCandidateFailingEndsWithoutAReceipt(t *testing.T) {
	w := newWorld(t)
	for _, n := range []string{"w-sub", "w-usd", "w-credits"} {
		w.probes[n] = "down"
	}
	r, err := w.prepare()
	if err == nil || ErrCode(err) == "" && r.Receipt != nil {
		t.Fatalf("%v", err)
	}
	if _, e := os.Stat(filepath.Join(dir(), "decisions", "task.json")); e == nil {
		t.Error("a failed preparation must leave no usable receipt")
	}
	if len(r.Rounds) < 2 {
		t.Errorf("each failure refreshes and re-ranks: %d rounds", len(r.Rounds))
	}
	if _, e := Validate(w.c, "run", "codex", "gpt-sub"); e == nil {
		t.Error("no receipt, no launch")
	}
}

// ---- baseline, probe bound, calibration ----

func TestExplicitBaselineIsLabelledAndOnlyForMissingEvidence(t *testing.T) {
	w := newWorld(t)
	w.startRun()
	w.budget = &contract.Profile{Budget: &contract.Budget{Pools: map[string]contract.PoolCaps{"codex/sub/week": {RunCap: 100}, "claude/max/5h": {RunCap: 100}, "glm/api": {RunCap: 100}, "kiro/team/month": {RunCap: 100}}}}
	w.sel.Worker.MinCompleteAttempts = 6 // thin quality evidence; bounded costs and qualified reviewers remain mandatory
	w.sel.BaselineChain = "worker:backend"
	r := mustPrepare(t, w)
	if r.SelectionMode != modeBaseline || r.Receipt.SelectionMode != modeBaseline || r.Receipt.Provider != "w-sub" || r.Receipt.DecisionID == "" {
		t.Fatalf("baseline is unscored selection and says so: %s", dump(r))
	}
	if r.Receipt.Attempt == "" || r.Receipt.Kind != "backend" || r.Receipt.ConfigFingerprint == "" || r.Selected == nil || r.Selected.BaselineReason == "" || r.Selected.Excluded != "" {
		t.Errorf("a baseline launch must still produce attributable evidence, and the report says which evidence gap kept it out of the scored ranking: %+v", r.Receipt)
	}
	// it goes through the scored path's reservation, probe bound and receipt validation, not around them
	if holds, _ := budget.HoldsFor(r.Attempt); len(holds) != 1 || r.Receipt.Hold != holds[0].ID || holds[0].State != "reserved" {
		t.Errorf("a baseline probe is reserved like any other: %+v", holds)
	}
	if rem, err := budget.Remaining(w.marker, w.budget, ""); err != nil || rem["codex/sub/week"] != 97.5 {
		t.Errorf("the probe and execution funding are held against the pool: %v %v", rem, err)
	}
	if _, err := Validate(w.c, "run", "codex", "gpt-sub"); err != nil {
		t.Errorf("a baseline receipt validates like any automatic receipt: %v", err)
	}
	// without the owner's explicit baseline there is none
	w2 := newWorld(t)
	os.Remove(ledger.Path())
	if _, err := w2.prepare(); ErrCode(err) != CodeNoCandidate {
		t.Errorf("%v", err)
	}
	// Known bad claims remain a hard failure even when another assessment is missing.
	w3 := newWorld(t)
	w3.sel.BaselineChain = "worker:backend"
	w3.sel.Worker.MinCompleteAttempts = 6
	rows, _ := ledger.LoadStrict("")
	os.Remove(ledger.Path())
	for i := range rows {
		if rows[i].Kind == "task" && rows[i].Attempt == "seed-w-sub-T1-5-0" {
			rows[i].FalseClaims = ip(3)
		}
		if rows[i].Kind == "task" && rows[i].Attempt == "seed-w-sub-T1-5-1" {
			rows[i].FalseClaims = nil
		}
	}
	w3.seed(rows...)
	r = mustPrepare(t, w3)
	if r.SelectionMode != modeBaseline || r.Receipt.Provider == "w-sub" || !strings.Contains(excluded(r, "w-sub"), "false claims") {
		t.Fatalf("incomplete coverage must not revive the known bad candidate: %s", dump(r))
	}

}

// The baseline relaxes the evidence qualification and nothing else. Every other gate still excludes what it excludes: a failed
// refresh, a missing capability, a context window that is too small, an observed exhausted quota, and a missing probe bound.
func TestBaselineCannotBypassAnyOtherGate(t *testing.T) {
	fresh := func() *world {
		w := newWorld(t)
		w.sel.Worker.MinCompleteAttempts = 6
		w.sel.BaselineChain = "worker:backend"
		return w
	}
	setP := func(w *world, name string, f func(*providers.Provider)) {
		p := w.cfg.Providers[name]
		f(&p)
		w.cfg.Providers[name] = p
	}
	cases := map[string]struct {
		prep func(*world)
		code string
	}{
		"failed catalog refresh": {func(w *world) {
			w.invHook = func(_ int, inv *providers.Inventory) {
				if inv.Harness == "codex" {
					inv.Status, inv.Models, inv.Reason = providers.InvFailed, nil, "refresh failed"
				}
			}
		}, "harness_excluded"},
		"missing capability": {func(w *world) {
			setP(w, "w-sub", func(p *providers.Provider) { p.Capabilities = []string{"repository-edit"} })
		}, "missing_capability"},
		"context too small": {func(w *world) { setP(w, "w-sub", func(p *providers.Provider) { p.ContextTokens = 1000 }) }, "context_too_small"},
		"quota observed exhausted": {func(w *world) {
			w.invHook = func(_ int, inv *providers.Inventory) {
				if inv.Harness == "codex" {
					used := 100.0
					inv.Limits = []providers.Limit{{ID: "weekly", Unit: "percent", UsedPercent: &used}}
				}
			}
		}, "quota_exhausted_observed"},
		"alias model": {func(w *world) { setP(w, "w-sub", func(p *providers.Provider) { p.Model = "openrouter/auto" }) }, "alias_model"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			w := fresh()
			tc.prep(w)
			r := mustPrepare(t, w) // another baseline candidate is selected, never the excluded one
			if r.Receipt.Provider == "w-sub" {
				t.Fatalf("the baseline selected a candidate that a hard gate excluded: %s", dump(r))
			}
			for _, n := range w.probeLog() {
				if n == "w-sub" {
					t.Errorf("an excluded candidate was probed (and so paid for)")
				}
			}
			if got := excluded(r, "w-sub"); !strings.HasPrefix(got, tc.code) {
				t.Errorf("w-sub: %q, want %s", got, tc.code)
			}
		})
	}

	t.Run("no probe bound", func(t *testing.T) {
		w := fresh()
		for _, n := range []string{"w-sub", "w-usd", "w-credits"} {
			setP(w, n, func(p *providers.Provider) { p.ProbeBound = nil })
		}
		r, err := w.prepare()
		if ErrCode(err) != CodeProbeBound || len(w.probeLog()) != 0 {
			t.Fatalf("no finite bound, no inference, baseline or not: %v probes=%v", err, w.probeLog())
		}
		if !strings.HasPrefix(excluded(r, "w-sub"), "probe_bound_required") {
			t.Errorf("%s", dump(r))
		}
	})

	t.Run("reservation must fit", func(t *testing.T) {
		w := fresh()
		w.startRun()
		w.budget = &contract.Profile{Budget: &contract.Budget{Pools: map[string]contract.PoolCaps{"codex/sub/week": {RunCap: 0.1}, "claude/max/5h": {RunCap: 100}, "glm/api": {RunCap: 0.001}, "kiro/team/month": {RunCap: 0.5}}}}
		r, err := w.prepare()
		if err == nil || r.Receipt != nil || len(w.probeLog()) != 0 {
			t.Fatalf("the baseline probe is reserved first: err=%v probes=%v\n%s", err, w.probeLog(), dump(r))
		}
		if !strings.HasPrefix(excluded(r, "w-sub"), "budget_insufficient") {
			t.Errorf("%s", dump(r))
		}
	})
}

// The baseline stays in force across the fallback rounds it started in, but is never entered after a scored candidate failed.
func TestBaselineFallsBackWithinItselfOnly(t *testing.T) {
	w := newWorld(t)
	w.sel.Worker.MinCompleteAttempts = 6
	w.sel.BaselineChain = "worker:backend"
	w.probes["w-sub"] = "down"
	r := mustPrepare(t, w)
	if r.SelectionMode != modeBaseline || r.Receipt.Provider != "w-usd" || len(r.Rounds) != 2 {
		t.Fatalf("the next baseline candidate, after a NEW refresh: %s", dump(r))
	}
	if r.Rounds[0].Outcome != "probe_down" || r.Rounds[1].Parent != r.Rounds[0].Decision || !strings.HasPrefix(excluded(r, "w-sub"), "failed_this_prepare") {
		t.Errorf("%s", dump(r))
	}
}

func TestProbeBoundIsRequiredBeforeAnyInference(t *testing.T) {
	w := newWorld(t)
	for _, n := range []string{"w-sub", "w-usd", "w-credits"} {
		p := w.cfg.Providers[n]
		p.ProbeBound = nil
		w.cfg.Providers[n] = p
	}
	r, err := w.prepare()
	if ErrCode(err) != CodeProbeBound || len(w.probeLog()) != 0 {
		t.Fatalf("no finite bound, no inference: %v probes=%v", err, w.probeLog())
	}
	if !strings.HasPrefix(excluded(r, "w-sub"), "probe_bound_required") {
		t.Errorf("%s", dump(r))
	}
	for name, b := range map[string]*providers.Bound{"zero calls": {Calls: 0, Amount: 1}, "negative": {Calls: 1, Amount: -1}} {
		w2 := newWorld(t)
		p := w2.cfg.Providers["w-sub"]
		p.ProbeBound = b
		w2.cfg.Providers["w-sub"] = p
		if r := mustPrepare(t, w2); r.Receipt.Provider == "w-sub" {
			t.Errorf("%s: an unbounded provider was selected", name)
		}
	}
}

func TestCalibrationObeysFixedCapsAndStopsAtExhaustion(t *testing.T) {
	w := newWorld(t)
	in := CalibrationInput{Contract: w.c, Run: "run", ConfigPath: w.in().ConfigPath, Provider: "w-sub", Amount: 1, Calls: 1}
	if _, err := ReserveCalibration(in); ErrCode(err) != CodeSelectionPolicy {
		t.Fatalf("discovery alone never enrolls a model in paid experiments: %v", err)
	}
	w.sel.Calibration = &contract.Calibration{Authorization: "standing order 4", MaxCalls: 2, PoolCaps: map[string]float64{"codex/sub/week": 3}, MaxCashUSD: fl(0)}
	in.ConfigPath = w.in().ConfigPath
	for i := 0; i < 2; i++ {
		if _, err := ReserveCalibration(in); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if _, err := ReserveCalibration(in); ErrCode(err) != CodeCalibrationExhausted {
		t.Errorf("the call cap must stop it: %v", err)
	}
	in.Provider, in.Amount = "w-usd", 0.01
	if _, err := ReserveCalibration(in); ErrCode(err) != CodeCalibrationExhausted {
		t.Errorf("a pool with no calibration cap is not authorized: %v", err)
	}
}

func TestReviewerCalibrationFreezesCostCohort(t *testing.T) {
	w := newWorld(t)
	w.sel.Calibration = &contract.Calibration{Authorization: "standing order", MaxCalls: 2, PoolCaps: map[string]float64{"codex/sub/week": 3}, MaxCashUSD: fl(0)}
	in := CalibrationInput{Contract: w.c, Run: "run", ConfigPath: w.in().ConfigPath, Provider: "r-gpt", Amount: 1, Calls: 2}
	hold, err := ReserveCalibration(in)
	if err != nil {
		t.Fatal(err)
	}
	item := hold.Items[0]
	p := w.cfg.Providers["r-gpt"]
	if item.Provider != "r-gpt" || item.Model != p.Model || item.Effort != p.Effort || item.Config != fingerprint(p) || item.Calls != 2 {
		t.Fatalf("review calibration lost its frozen cohort: %+v", item)
	}
}

// ---- receipts bind their evidence (acceptance test 7) ----

func TestReceiptsAreInvalidatedByAnyChangeOfTheirEvidence(t *testing.T) {
	mutations := map[string]func(*world){
		"policy": func(w *world) { w.sel.Worker.MinCompleteAttempts = 4 },
		"billing config": func(w *world) {
			w.cfg.Providers["w-sub"] = func() providers.Provider {
				p := w.cfg.Providers["w-sub"]
				p.Billing = &providers.Billing{Mode: "subscription", Unit: "subscription_percent", Pool: "codex/sub/week2"}
				return p
			}()
			w.writeConfigKeepReceipt()
		},
		"quality": func(w *world) {
			w.seedWorker("w-sub", 1, 0, 2, "claude", "claude-opus-r") // one more settled attempt for the cohort
		},
		"task profile": func(w *world) {
			os.WriteFile(filepath.Join(artifactDir("task"), "profile.json"), []byte(`{}`), 0o600)
		},
		"catalog snapshot": func(w *world) {
			os.WriteFile(filepath.Join(artifactDir("task"), "inventory.json"), []byte(`[]`), 0o600)
		},
		"contract": func(w *world) { w.c.Allow = append(w.c.Allow, "extra/**") },
		"expiry": func(w *world) {
			path, _ := receipt(w.c)
			var d Decision
			b, _ := os.ReadFile(path)
			json.Unmarshal(b, &d)
			past := time.Now().Add(-time.Minute)
			d.ExpiresAt = &past
			atomic(path, &d)
		},
		"reservation released": func(w *world) {
			path, _ := receipt(w.c)
			var d Decision
			b, _ := os.ReadFile(path)
			json.Unmarshal(b, &d)
			if _, err := budget.Settle(d.Hold, map[string]float64{"codex/sub/week": 0, "claude/max/5h": 0}); err != nil {
				w.t.Fatal(err)
			}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			r := mustPrepare(t, w)
			if _, err := Validate(w.c, "run", r.Receipt.Agent, r.Receipt.Model); err != nil {
				t.Fatalf("control: %v", err)
			}
			mutate(w)
			if _, err := Validate(w.c, "run", r.Receipt.Agent, r.Receipt.Model); err == nil {
				t.Errorf("%s change must invalidate the receipt", name)
			}
		})
	}
	// the launch itself, and unrelated tasks' evidence, must not invalidate it
	w := newWorld(t)
	r := mustPrepare(t, w)
	launch := ledger.Row{Kind: "routing_launch", Attempt: r.Attempt, Run: "run", Task: "task", Worker: &ledger.AgentModel{Agent: "codex", Model: "gpt-sub"}, Type: "backend",
		Effort: "high", Config: r.Receipt.ConfigFingerprint, Suite: "s1", Tier: "T1"}
	if err := ledger.Append(launch); err != nil {
		t.Fatal(err)
	}
	if _, err := Validate(w.c, "run", "codex", "gpt-sub"); err != nil {
		t.Errorf("recording its own launch must not invalidate the receipt: %v", err)
	}
}

func TestRatesChangeInvalidatesAnAPIBilledReceipt(t *testing.T) {
	w := newWorld(t)
	if err := SetCooldown(w.in().ConfigPath, "w-sub", "quota evidence", nil, false); err != nil { // w-usd (api billing) is next
		t.Fatal(err)
	}
	r := mustPrepare(t, w)
	if r.Receipt.Provider != "w-usd" {
		t.Fatalf("%s", dump(r))
	}
	if _, err := Validate(w.c, "run", r.Receipt.Agent, r.Receipt.Model); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(os.Getenv("PIPELINE_PRICES"), []byte(`{"opencode-go/glm-w":{"input_per_mtok":9,"output_per_mtok":9,"source":"x","as_of":"2026-01-01T00:00:00Z","valid_until":"2099-01-01T00:00:00Z"}}`), 0o600)
	if _, err := Validate(w.c, "run", r.Receipt.Agent, r.Receipt.Model); err == nil || !strings.Contains(err.Error(), "pricing") {
		t.Errorf("a changed public rate must invalidate the receipt: %v", err)
	}
	// an expired or provenance-less rate card removes the candidate: legacy blended prices are not enough
	for name, rates := range map[string]string{
		"expired":     `{"opencode-go/glm-w":{"input_per_mtok":1,"output_per_mtok":4,"source":"x","as_of":"2026-01-01T00:00:00Z","valid_until":"2026-02-01T00:00:00Z"}}`,
		"legacy only": `{"opencode-go/glm-w":{"usd_per_mtok":2}}`,
	} {
		w2 := newWorld(t)
		os.WriteFile(os.Getenv("PIPELINE_PRICES"), []byte(rates), 0o600)
		r2 := mustPrepare(t, w2)
		if r2.Receipt.Provider == "w-usd" || !strings.HasPrefix(excluded(r2, "w-usd"), "rates_") {
			t.Errorf("%s: %s", name, dump(r2))
		}
	}
}

// writeConfigKeepReceipt rewrites the provider config exactly as given (no probe injection changes the hash we care about).
func (w *world) writeConfigKeepReceipt() { w.writeConfig() }

func TestWorkerIdentityForReviewComesFromTheLedgerNotTheCoordinator(t *testing.T) {
	w := newWorld(t)
	// the worker task was launched as gpt-sub (openai); a review must be maker-independent of THAT, whatever the coordinator says
	launch := ledger.Row{Kind: "routing_launch", Attempt: "at-x", Run: "run", Task: "task", Worker: &ledger.AgentModel{Agent: "codex", Model: "gpt-sub"}, Type: "backend", Effort: "high", Config: "c", Suite: "s1", Tier: "T1"}
	if err := ledger.Append(launch); err != nil {
		t.Fatal(err)
	}
	w.profile = func(tp *TaskProfile) { tp.Phase, tp.ChangedFiles = "review", []string{"src/api/users.go"} }
	in := w.in()
	in.WorkerModel = "claude-opus-r" // a lie: claims an anthropic worker to obtain an openai reviewer
	if _, err := PrepareAuto(context.Background(), in); ErrCode(err) != CodeWorkerIdentity {
		t.Errorf("a claim that disagrees with the ledger must be refused: %v", err)
	}
	in.WorkerModel = ""
	r, err := PrepareAuto(context.Background(), in)
	if err != nil {
		t.Fatalf("%v\n%s", err, dump(r))
	}
	if r.Receipt.Provider != "r-claude" || r.Receipt.WorkerModel != "gpt-sub" || r.Receipt.Chain != "review:auto" {
		t.Errorf("only the anthropic reviewer is independent of an openai worker: %s", dump(r))
	}
	if _, err := Validate(w.c, "run", "claude", "claude-opus-r"); err != nil {
		t.Errorf("review receipt: %v", err)
	}
	// an unresolved alias worker proves nothing about independence
	launch2 := launch
	launch2.Attempt, launch2.Worker = "at-y", &ledger.AgentModel{Agent: "kiro", Model: "auto"}
	if err := ledger.Append(launch2); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareAuto(context.Background(), in); ErrCode(err) != CodeWorkerIdentity {
		t.Errorf("an alias worker with no resolved model: %v", err)
	}
	// a T3 review excludes the maker that already reviewed
	w2 := newWorld(t)
	_ = ledger.Append(launch)
	w2.profile = func(tp *TaskProfile) {
		tp.Phase, tp.ChangedFiles, tp.ExcludeMakers = "review", []string{"src/api/users.go"}, []string{"anthropic"}
	}
	r2, err := w2.prepare()
	if ErrCode(err) != CodeNoCandidate {
		t.Fatalf("the only independent maker already reviewed: %v\n%s", err, dump(r2))
	}
}

// ---- concurrency and exclusivity (acceptance test 8) ----

func (w *world) startRun() {
	w.t.Helper()
	w.marker = filepath.Join(os.Getenv("REIN_RUN_DIR"), run.MarkerFile)
	m := run.Marker{Schema: run.Schema, Run: "run", StartedAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), Root: w.t.TempDir(), SessionID: "s", PID: os.Getpid()}
	b, _ := json.Marshal(m)
	if err := os.WriteFile(w.marker, b, 0o600); err != nil {
		w.t.Fatal(err)
	}
}

func TestConcurrentDecisionsCannotSpendTheSameRemainingQuota(t *testing.T) {
	w := newWorld(t)
	w.startRun()
	// one pool with room for exactly one decision's probe + funding (w-sub: 0.5 probe + 2 worker + 4 review = 6.5; claude pool holds the review)
	w.budget = &contract.Profile{Budget: &contract.Budget{Pools: map[string]contract.PoolCaps{"codex/sub/week": {RunCap: 3}, "claude/max/5h": {RunCap: 100}, "glm/api": {RunCap: 0.001}, "kiro/team/month": {RunCap: 0.001}}}}
	base := w.in()
	var wg sync.WaitGroup
	results := make([]error, 4)
	for i := range results {
		c := *w.c
		c.Name = fmt.Sprintf("task%d", i)
		c.Worktree = filepath.Join(filepath.Dir(w.c.Worktree), c.Name)
		if err := os.MkdirAll(c.Worktree, 0o700); err != nil {
			t.Fatal(err)
		}
		in := base
		in.Contract = &c
		var tp TaskProfile
		_ = json.Unmarshal(in.TaskProfile, &tp)
		tp.ContractHash = contractHash(&c)
		in.TaskProfile, _ = json.Marshal(tp)
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[i] = PrepareAuto(context.Background(), in)
		}()
	}
	wg.Wait()
	won := 0
	for _, err := range results {
		if err == nil {
			won++
		}
	}
	// the codex pool allows 3 units: only one decision's w-sub reservation (0.5 + 2) fits; the others must fall to other candidates or fail,
	// but together they can never reserve more than the cap
	holds, err := budget.Outstanding()
	if err != nil {
		t.Fatal(err)
	}
	reserved := 0.0
	for _, h := range holds {
		for _, it := range h.Items {
			if it.Pool == "codex/sub/week" {
				reserved += it.Amount
			}
		}
	}
	if reserved > 3.0000001 {
		t.Fatalf("concurrent decisions double-reserved the codex pool: %v units held against a cap of 3 (%d won)", reserved, won)
	}
	if won != 1 {
		t.Errorf("room for exactly one reservation of the codex pool: %d decisions succeeded: %v", won, results)
	}
}

func TestSameTaskCannotBePreparedTwiceAtOnceAndLeaseNeedsProofOfExit(t *testing.T) {
	w := newWorld(t)
	unlock, err := run.LockFile(filepath.Join(dir(), "prepare", "task.lock"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.prepare(); err == nil || !strings.Contains(err.Error(), "another preparation") {
		t.Errorf("a second preparation of the same task must not run: %v", err)
	}
	unlock()
	r := mustPrepare(t, w)
	// a receipt is single-use, so every acquisition here stands for a separately prepared attempt
	acquire := func() (*Lease, error) {
		d := *r.Receipt
		d.Attempt, d.DecisionID = newID("at"), "" // isolate legacy lease liveness from automatic receipt binding
		return AcquireLease(w.c, &d)
	}
	l, err := acquire()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquire(); err == nil || !strings.Contains(err.Error(), "still running") {
		t.Errorf("two writers on one checkout: %v", err)
	}
	// a launcher that vanished without releasing: only proof that BOTH it and its provider are gone lets the next one in
	dead := exitedPid(t)
	l.Pid, l.PidStart = dead.pid, dead.start
	if err := writeLease(l); err != nil {
		t.Fatal(err)
	}
	live := os.Getpid()
	l.Child, l.ChildStart = live, 0
	if err := writeLease(l); err != nil {
		t.Fatal(err)
	}
	if _, err := acquire(); err == nil {
		t.Error("a dead launcher whose provider process may still be running keeps the task")
	}
	l.Child, l.ChildStart = dead.pid, dead.start
	if err := writeLease(l); err != nil {
		t.Fatal(err)
	}
	l2, err := acquire()
	if err != nil {
		t.Fatalf("both holders provably gone: %v", err)
	}
	l.Release() // not the holder any more: must not remove the new lease
	if _, err := acquire(); err == nil {
		t.Error("a stale holder released someone else's lease")
	}
	l2.Release()
	if _, err := acquire(); err != nil {
		t.Errorf("released: %v", err)
	}
	// an unreadable record is not guessed about
	if err := os.WriteFile(leasePath("task"), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := acquire(); err == nil {
		t.Error("unreadable lease must refuse")
	}
}

// One receipt buys one execution: the second use of the same attempt is refused, whatever happened to the first (a crash must
// not be healed by a retry that skips the new refresh, the new reservation and the evidence of the crash).
func TestReceiptIsSingleUse(t *testing.T) {
	w := newWorld(t)
	r := mustPrepare(t, w)
	l, err := AcquireLease(w.c, r.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	l.Release()
	if _, err = AcquireLease(w.c, r.Receipt); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("the same attempt launched twice after a clean exit: %v", err)
	}
	if !ReceiptUsed(r.Attempt) || ReceiptUsed("at-unused") {
		t.Error("ReceiptUsed must report exactly the consumed attempts")
	}
	// ordered-chain receipts have no attempt and keep their reusable semantics
	if err := ConsumeReceipt(&Decision{}); err != nil {
		t.Errorf("legacy receipts are not single-use: %v", err)
	}
	// the attempt ID names a file: it must not be able to leave the markers' directory
	for _, bad := range []string{"../x", "a/b", "..", "."} {
		if err := ConsumeReceipt(&Decision{Attempt: bad}); err == nil {
			t.Errorf("attempt %q accepted", bad)
		}
		if !ReceiptUsed(bad) {
			t.Errorf("an attempt that cannot be a marker name counts as used: %q", bad)
		}
	}
	// a writer that is still running keeps the receipt unused: the launch did not happen
	w2 := newWorld(t)
	r2 := mustPrepare(t, w2)
	holder, err := AcquireLease(w2.c, r2.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	other := *r2.Receipt
	other.Attempt = newID("at")
	if _, err = AcquireLease(w2.c, &other); err == nil || ReceiptUsed(other.Attempt) {
		t.Errorf("a refused launch must not consume its receipt: %v", err)
	}
	holder.Release()
}

// A worker that Orca dispatched outlives the launcher: its lease has no process to prove gone, so it is held until the attempt is
// settled; settling a worker that is still running locally is refused.
func TestOrcaDispatchedLeaseIsHeldUntilSettle(t *testing.T) {
	w := newWorld(t)
	w.startRun()
	r := mustPrepare(t, w)
	l, err := AcquireLease(w.c, r.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	if why := TaskBusy("task"); !strings.Contains(why, "still running") {
		t.Errorf("a live launcher holds the task: %q", why)
	}
	if _, err := SettleAttempt(r.Attempt); err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("settling an attempt whose launcher is alive must be refused: %v", err)
	}
	if holds, _ := budget.HoldsFor(r.Attempt); len(holds) != 1 || holds[0].State != "reserved" {
		t.Fatalf("a refused settlement must change nothing: %+v", holds)
	}
	if err := l.MarkDispatched(); err != nil {
		t.Fatal(err)
	}
	l.Release() // the launcher exits; a dispatched lease stays
	if why := TaskBusy("task"); !strings.Contains(why, "no recorded end") {
		t.Fatalf("the dispatched worker still holds the task: %q", why)
	}
	next := *r.Receipt
	next.Attempt = newID("at")
	if _, err := AcquireLease(w.c, &next); err == nil || !strings.Contains(err.Error(), "no recorded end") {
		t.Errorf("a second writer beside a dispatched worker: %v", err)
	}
	if err := EndLease("task", "another-attempt"); err != nil || TaskBusy("task") == "" {
		t.Errorf("another attempt's settlement must not end this lease: %v", err)
	}
	if _, err := SettleAttempt(r.Attempt); err != nil {
		t.Fatal(err)
	}
	if why := TaskBusy("task"); why != "" {
		t.Errorf("settling ends the dispatched lease: %q", why)
	}
	fresh := mustPrepare(t, w)
	if lease, err := AcquireLease(w.c, fresh.Receipt); err != nil {
		t.Errorf("a fresh funded attempt takes the free task: %v", err)
	} else {
		lease.Release()
	}
}

type deadProc struct {
	pid   int
	start int64
}

// exitedPid starts and reaps a short-lived process and returns its (now dead) identity.
func exitedPid(t *testing.T) deadProc {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	start, _ := run.ProcStart(cmd.Process.Pid)
	_ = cmd.Wait()
	return deadProc{cmd.Process.Pid, start}
}

// costRows are the attempt's cost rows by component and pool.
func costRows(t *testing.T, attempt string) map[string]float64 {
	t.Helper()
	rows, err := ledger.LoadStrict("")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	for _, x := range rows {
		if x.Kind == "cost" && x.Attempt == attempt {
			out[x.Component+"@"+x.Pool] += *x.Amount
		}
	}
	return out
}

func budgetOf(pools ...string) *contract.Profile {
	caps := map[string]contract.PoolCaps{}
	for _, p := range pools {
		caps[p] = contract.PoolCaps{RunCap: 100}
	}
	return &contract.Profile{Budget: &contract.Budget{Pools: caps}}
}

func TestSettleChargesWhatIsProvenAndTheBoundForWhatIsNot(t *testing.T) {
	pools := []string{"codex/sub/week", "claude/max/5h", "glm/api", "kiro/team/month"}
	prepared := func(t *testing.T) (*world, *AutoResult) {
		w := newWorld(t)
		w.startRun()
		w.budget = budgetOf(pools...)
		r := mustPrepare(t, w)
		if holds, _ := budget.HoldsFor(r.Attempt); len(holds) != 1 {
			t.Fatal(holds)
		}
		return w, r
	}

	t.Run("ran, nothing recorded", func(t *testing.T) {
		_, r := prepared(t)
		if err := ConsumeReceipt(r.Receipt); err != nil { // the worker was launched
			t.Fatal(err)
		}
		// unknown usage is charged at the reserved bound, never released as zero; the review capacity set aside for the reviews
		// that follow is NOT the worker's spend
		done, err := SettleAttempt(r.Attempt)
		if err != nil || len(done) != 1 || done[0].State != "settled" {
			t.Fatalf("%v %v", done, err)
		}
		got := costRows(t, r.Attempt)
		if got["probe@codex/sub/week"] != 0.5 || got["worker@codex/sub/week"] != 2 || len(got) != 2 {
			t.Errorf("unknown usage must be charged at the reserved bound, and nothing on the reviewer's pool: %v", got)
		}
		if again, err := SettleAttempt(r.Attempt); err != nil || len(again) != 0 {
			t.Errorf("settling is idempotent: %v %v", again, err)
		}
		if after := costRows(t, r.Attempt); len(after) != 2 || after["worker@codex/sub/week"] != 2 {
			t.Errorf("a second settlement charged again: %v", after)
		}
	})

	t.Run("measured usage", func(t *testing.T) {
		w, r := prepared(t)
		ConsumeReceipt(r.Receipt)
		ch, _ := ledger.NewChargeRow(r.Attempt, "wk", "worker", "codex/sub/week", "subscription_percent", "w-sub", "gpt-sub", 1.25)
		ch.Run, ch.Task = "run", "task"
		if err := ledger.Append(ch); err != nil {
			t.Fatal(err)
		}
		if _, err := SettleAttempt(r.Attempt); err != nil {
			t.Fatal(err)
		}
		rem, err := budget.Remaining(w.marker, w.budget, "")
		if err != nil {
			t.Fatal(err)
		}
		if got := rem["codex/sub/week"]; got > 98.25+0.0001 || got < 98.25-0.0001 {
			t.Errorf("measured 1.25 plus the winning probe 0.5 is spent, the rest of the funding plan is released: remaining %v", got)
		}
		if rem["claude/max/5h"] != 100 {
			t.Errorf("the review capacity is released, not spent: %v", rem)
		}
	})

	// a zero or an approximate charge says nothing about what was used: it must not release the funding as zero
	for name, mut := range map[string]func(*ledger.Row){"zero": func(r *ledger.Row) { z := 0.0; r.Amount = &z }, "approximate": func(r *ledger.Row) { r.Approx = true }} {
		t.Run(name+" charge is not proof", func(t *testing.T) {
			_, r := prepared(t)
			ConsumeReceipt(r.Receipt)
			ch, _ := ledger.NewChargeRow(r.Attempt, "wk", "worker", "codex/sub/week", "subscription_percent", "w-sub", "gpt-sub", 1.25)
			ch.Run, ch.Task = "run", "task"
			mut(&ch)
			if err := ledger.Append(ch); err != nil {
				t.Fatal(err)
			}
			if _, err := SettleAttempt(r.Attempt); err != nil {
				t.Fatal(err)
			}
			got := costRows(t, r.Attempt)
			if got["worker@codex/sub/week"] < 2 || got["probe@codex/sub/week"] != 0.5 {
				t.Errorf("funding was released on the word of a %s charge: %v", name, got)
			}
		})
	}

	t.Run("never launched", func(t *testing.T) {
		w, r := prepared(t)
		if _, err := SettleAttempt(r.Attempt); err != nil {
			t.Fatal(err)
		}
		// the probe ran at prepare time, so its bound is spent; nothing executed under the receipt, so nothing else is
		if got := costRows(t, r.Attempt); got["probe@codex/sub/week"] != 0.5 || len(got) != 1 {
			t.Errorf("a plan that never ran is released uncharged: %v", got)
		}
		if rem, _ := budget.Remaining(w.marker, w.budget, ""); rem["codex/sub/week"] != 99.5 || rem["claude/max/5h"] != 100 {
			t.Errorf("remaining %v", rem)
		}
	})
}

// A reviewer on another pool than the worker: review funding is the REVIEW's, reserved and charged under the review's own
// attempt. Repeating the cycle must not grow the worker's recorded cost, double-count the review, or leave anything reserved.
func TestReviewFundingIsNeverBookedAsWorkerSpendAcrossCycles(t *testing.T) {
	w := newWorld(t)
	w.startRun()
	w.budget = budgetOf("codex/sub/week", "claude/max/5h", "glm/api", "kiro/team/month")
	wp := w.cfg.Providers["w-sub"]
	const cycles = 4
	for i := 0; i < cycles; i++ {
		w.profile = nil
		worker := mustPrepare(t, w)
		if worker.Receipt.Provider != "w-sub" || len(worker.Selected.ReviewerPlan) != 1 {
			t.Fatalf("cycle %d: %s", i, dump(worker))
		}
		// the worker launches and records its own usage on its pool
		if err := ConsumeReceipt(worker.Receipt); err != nil {
			t.Fatal(err)
		}
		if err := ledger.Append(ledger.Row{Kind: "routing_launch", Attempt: worker.Attempt, Run: "run", Task: "task", Worker: &ledger.AgentModel{Agent: wp.Agent, Model: wp.Model},
			Type: "backend", Effort: wp.Effort, Config: fingerprint(wp), Suite: "s1", Tier: "T1"}); err != nil {
			t.Fatal(err)
		}
		wc, _ := ledger.NewChargeRow(worker.Attempt, "wk", "worker", "codex/sub/week", "subscription_percent", "w-sub", "gpt-sub", 2)
		wc.Run, wc.Task = "run", "task"
		if err := ledger.Append(wc); err != nil {
			t.Fatal(err)
		}
		if err := ledger.Append(ledger.Row{Kind: "task", Attempt: worker.Attempt, Run: "run", Task: "task", Source: "measured", ReviewRounds: ip(1), Minutes: fl(8), GatesPassed: yes(),
			Reviewer: &ledger.AgentModel{Agent: "claude", Model: "claude-opus-r"}, ReviewSHA: "sha", Approved: true, Blockers: ip(0), Highs: ip(0), FalseClaims: ip(0), Drift: ip(0)}); err != nil {
			t.Fatal(err)
		}

		// the review takes over the capacity the worker's plan set aside, reserves under its own attempt and pays for itself
		w.profile = func(tp *TaskProfile) { tp.Phase, tp.ChangedFiles = "review", []string{"src/api/users.go"} }
		review := mustPrepare(t, w)
		if review.Receipt.Provider != "r-claude" {
			t.Fatalf("cycle %d: %s", i, dump(review))
		}
		wholds, _ := budget.HoldsFor(worker.Attempt)
		remainingReview := 0.0
		for _, it := range wholds[0].Items {
			if it.Kind == "review_reserve" {
				remainingReview += it.Amount
			}
		}
		if remainingReview != 3.5 {
			t.Fatalf("cycle %d: future review funding after transfer of 4 + 0.5 must remain 3.5: %+v", i, wholds[0].Items)
		}
		if err := ConsumeReceipt(review.Receipt); err != nil { // route check
			t.Fatal(err)
		}
		rc, _ := ledger.NewChargeRow(review.Attempt, "rc", "review", "claude/max/5h", "subscription_percent", "r-claude", "claude-opus-r", 4)
		rc.Run, rc.Task = "run", "task"
		if err := ledger.Append(rc); err != nil {
			t.Fatal(err)
		}
		for _, a := range []string{review.Attempt, worker.Attempt} {
			if _, err := SettleAttempt(a); err != nil {
				t.Fatal(err)
			}
		}
		if out, _ := budget.Outstanding(); len(out) != 0 {
			t.Fatalf("cycle %d left %d holds reserved: %+v", i, len(out), out)
		}
		if got := costRows(t, worker.Attempt); got["worker@codex/sub/week"] != 2 || got["probe@codex/sub/week"] != 0.5 || len(got) != 2 {
			t.Fatalf("cycle %d: the worker's attempt carries only its own pool's spend: %v", i, got)
		}
		if got := costRows(t, review.Attempt); got["review@claude/max/5h"] != 4 || got["probe@claude/max/5h"] != 0.5 || len(got) != 2 {
			t.Fatalf("cycle %d: the review is charged once, measured, plus its probe bound: %v", i, got)
		}
	}
	// nothing was ever booked as WORKER spend in the reviewer's pool, so the funding plan and the pair cost do not drift
	rows, _ := ledger.LoadStrict("")
	for _, x := range rows {
		if x.Kind == "cost" && x.Pool == "claude/max/5h" && x.Component == "worker" {
			t.Fatalf("review funding was booked as worker spend: %+v", x)
		}
	}
	if rem, err := budget.Remaining(w.marker, w.budget, ""); err != nil || rem["claude/max/5h"] != 100-cycles*4.5 || rem["codex/sub/week"] != 100-cycles*2.5 {
		t.Errorf("spend is exactly what was measured plus the probe bounds: %v %v", rem, err)
	}
	w.profile = nil
	last := mustPrepare(t, w)
	if c := last.Selected.Cost; c == nil || c.Amount["claude/max/5h"] != 4 || last.Selected.Funding.Amount["claude/max/5h"] != 8 || last.Selected.Funding.Amount["codex/sub/week"] != 2.5 {
		t.Errorf("the forecast and the funding plan must not grow with the number of cycles: cost %+v funding %+v", last.Selected.Cost, last.Selected.Funding)
	}
	if wc := last.Selected.WorkerCost; wc == nil || len(wc.Totals) != 1 || wc.MaxAttempt["claude/max/5h"] != 0 || wc.Totals["claude/max/5h"] != 0 {
		t.Errorf("the worker's cost vector stays on the worker's own pool: %+v", wc)
	}
}

// A previous reservation behind a receipt that is replaced, or whose receipt could not be written, must not stay reserved.
func TestAbandonedReservationsAreReleased(t *testing.T) {
	t.Run("re-prepare releases a receipt that was never used", func(t *testing.T) {
		w := newWorld(t)
		w.startRun()
		w.budget = budgetOf("codex/sub/week", "claude/max/5h", "glm/api", "kiro/team/month")
		first := mustPrepare(t, w)
		second := mustPrepare(t, w)
		out, _ := budget.Outstanding()
		if len(out) != 1 || out[0].Attempt != second.Attempt {
			t.Fatalf("only the live receipt's reservation may remain: %+v", out)
		}
		if got := costRows(t, first.Attempt); got["probe@codex/sub/week"] != 0.5 || len(got) != 1 {
			t.Errorf("the abandoned preparation costs its probe, not its funding plan: %v", got)
		}
	})
	t.Run("a receipt that ran keeps its reservation for its own settlement", func(t *testing.T) {
		w := newWorld(t)
		w.startRun()
		w.budget = budgetOf("codex/sub/week", "claude/max/5h", "glm/api", "kiro/team/month")
		first := mustPrepare(t, w)
		if err := ConsumeReceipt(first.Receipt); err != nil {
			t.Fatal(err)
		}
		second := mustPrepare(t, w)
		out, _ := budget.Outstanding()
		if len(out) != 2 {
			t.Fatalf("a receipt that was used is settled by its owner, not discarded with the receipt: %+v (second %s)", out, second.Attempt)
		}
	})
	t.Run("a receipt that cannot be written", func(t *testing.T) {
		w := newWorld(t)
		w.startRun()
		w.budget = budgetOf("codex/sub/week", "claude/max/5h", "glm/api", "kiro/team/month")
		if err := os.MkdirAll(dir(), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir(), "artifacts"), []byte("in the way"), 0o600); err != nil { // artifacts/<task> can no longer be created
			t.Fatal(err)
		}
		r, err := w.prepare()
		if err == nil {
			t.Fatalf("the preparation cannot succeed without its artifacts: %s", dump(r))
		}
		if out, _ := budget.Outstanding(); len(out) != 0 {
			t.Fatalf("a probe that came up but produced no receipt left its reservation behind: %+v", out)
		}
		if got := costRows(t, r.Attempt); got["probe@codex/sub/week"] != 0.5 || len(got) != 1 {
			t.Errorf("the probe ran, so its bound is charged once, and the funding plan is released: %v", got)
		}
		if _, e := os.Stat(filepath.Join(dir(), "decisions", "task.json")); e == nil {
			t.Error("no receipt may exist")
		}
	})
}

// ---- remaining acceptance details ----

func TestPolicyDeclaresWhichFreshnessClassItAccepts(t *testing.T) {
	w := newWorld(t)
	w.sel.AcceptFreshness = []string{"remote_verified"}
	r, err := w.prepare()
	if ErrCode(err) != CodeNoCandidate || !strings.HasPrefix(excluded(r, "w-sub"), "freshness_not_accepted") {
		t.Fatalf("a local client answer is not proof of a remotely refreshed catalog: %v\n%s", err, dump(r))
	}
	if len(w.probeLog()) != 0 {
		t.Error("nothing is probed when no harness meets the freshness policy")
	}
	w.invHook = func(_ int, inv *providers.Inventory) { inv.Freshness = providers.RemoteVerified }
	if r := mustPrepare(t, w); r.Receipt.Provider != "w-sub" {
		t.Errorf("a remotely verified catalog meets a remote_verified policy: %s", dump(r))
	}
}

func TestAliasesAreNeverExactIdentities(t *testing.T) {
	w := newWorld(t)
	p := w.cfg.Providers["w-sub"]
	p.Model = "auto"
	w.cfg.Providers["w-sub"] = p
	r := mustPrepare(t, w)
	if r.Receipt.Provider == "w-sub" || !strings.HasPrefix(excluded(r, "w-sub"), "alias_model") {
		t.Errorf("%s", dump(r))
	}
	// the catalog itself can flag a moving alias
	w2 := newWorld(t)
	w2.invHook = func(_ int, inv *providers.Inventory) {
		for i := range inv.Models {
			if inv.Models[i].ID == "gpt-sub" {
				inv.Models[i].Alias = true
			}
		}
	}
	if r = mustPrepare(t, w2); r.Receipt.Provider == "w-sub" || !strings.HasPrefix(excluded(r, "w-sub"), "alias_model") {
		t.Errorf("%s", dump(r))
	}
}

func TestPairCostAndFundingPlanArithmetic(t *testing.T) {
	w := newWorld(t)
	r := mustPrepare(t, w)
	s := r.Selected
	// w-sub: 10 units over 5 verified attempts = 2 per success on its pool; one review round on average (4 per review on the claude pool)
	if s.Cost == nil || s.Cost.Amount["codex/sub/week"] != 2 || s.Cost.Amount["claude/max/5h"] != 4 || len(s.Cost.Amount) != 2 {
		t.Errorf("cost per verified success must include the review regime it is bound to, in native units: %+v", s.Cost)
	}
	if s.Cost.Unit["codex/sub/week"] != "subscription_percent" || s.Cost.Unit["claude/max/5h"] != "subscription_percent" {
		t.Errorf("units: %+v", s.Cost.Unit)
	}
	// worst case = the largest observed attempt (2) and, per allowed review round (2), the largest observed review charge (4)
	if s.Funding == nil || s.Funding.Amount["codex/sub/week"] != 2 || s.Funding.Amount["claude/max/5h"] != 8 {
		t.Errorf("funding plan: %+v", s.Funding)
	}
	holds, _ := budget.HoldsFor(r.Attempt)
	got := map[string]float64{}
	for _, it := range holds[0].Items {
		got[it.Kind+"@"+it.Pool] += it.Amount
	}
	// the worker's own attempt is funding; the capacity its plan sets aside for the reviews that follow is review_reserve, which the
	// review takes over and which is never charged as the worker's spend
	if got["probe@codex/sub/week"] != 0.5 || got["funding@codex/sub/week"] != 2 || got["review_reserve@claude/max/5h"] != 8 || len(got) != 3 {
		t.Errorf("the probe bound and the funding plan are reserved together: %v", got)
	}
	if r.Comparison == nil || r.Comparison.CostBasis != "monetary_marginal" || strings.Join(r.Comparison.Compared, ",") != "w-sub,w-usd" || strings.Join(r.Comparison.UnknownCost, ",") != "w-credits" ||
		!strings.Contains(r.Comparison.Claim, "not assumed more expensive") {
		t.Errorf("the report must say what the ranking does not claim: %+v", r.Comparison)
	}
	if s.Worker.Confidence != 0.95 || s.Worker.Q <= 0 || s.Worker.Coverage != 1 || s.Worker.Complete != 5 {
		t.Errorf("Q is shown with its sample count, coverage and confidence: %+v", s.Worker)
	}
}

func TestSubstitutedWorkerModelDecidesReviewerIndependence(t *testing.T) {
	w := newWorld(t)
	// launched as gpt-sub (openai) but the harness reports that claude-sub (anthropic) actually ran
	launch := ledger.Row{Kind: "routing_launch", Attempt: "at-s", Run: "run", Task: "task", Worker: &ledger.AgentModel{Agent: "codex", Model: "gpt-sub"}, Type: "backend", Effort: "high", Config: "c", Suite: "s1", Tier: "T1"}
	outcome := ledger.Row{Kind: "task", Attempt: "at-s", Run: "run", Task: "task", ResolvedModel: "claude-sub", Source: "measured"}
	if err := ledger.Append(launch); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Append(outcome); err != nil {
		t.Fatal(err)
	}
	w.profile = func(tp *TaskProfile) { tp.Phase, tp.ChangedFiles = "review", []string{"src/api/users.go"} }
	r := mustPrepare(t, w)
	if r.Receipt.WorkerModel != "claude-sub" || r.Receipt.Provider != "r-gpt" {
		t.Errorf("the reviewer's maker is judged against the model that ran (anthropic), so r-gpt is the independent one: %s", dump(r))
	}
	if !strings.HasPrefix(excluded(r, "r-claude"), "same_or_unknown_maker") {
		t.Errorf("%q", excluded(r, "r-claude"))
	}
}

func TestNoFallbackToUnqualifiedModelsOrBaselineAfterFailures(t *testing.T) {
	w := newWorld(t)
	w.cfg.Providers["w-new"] = w.cfg.Providers["w-sub"] // configured and available, but with no record of its own
	p := w.cfg.Providers["w-new"]
	p.Model = "gpt-new"
	w.cfg.Providers["w-new"] = p
	w.cfg.WorkerChains["backend"] = append(w.cfg.WorkerChains["backend"], "w-new")
	w.sel.BaselineChain = "worker:backend"
	for _, n := range []string{"w-sub", "w-usd"} {
		w.probes[n] = "down"
	}
	r, err := w.prepare()
	if err == nil || r.Receipt != nil {
		t.Fatalf("every qualified candidate failed: %v\n%s", err, dump(r))
	}
	for _, n := range w.probeLog() {
		if n == "w-new" || n == "w-credits" {
			t.Errorf("the floor must not be weakened to reach %s", n)
		}
	}
	if r.SelectionMode == modeBaseline {
		t.Error("an unscored baseline must never substitute for a failed scored candidate")
	}
	if !strings.HasPrefix(excluded(r, "w-new"), "unqualified") {
		t.Errorf("%q", excluded(r, "w-new"))
	}
}

func TestManualChainsCannotBypassTheScoredOrdering(t *testing.T) {
	w := newWorld(t)
	w.c.Profile.Selection = w.sel
	err := ManualPrepareAllowed(w.c)
	if ErrCode(err) != CodeSelectionPolicy || !strings.Contains(err.Error(), "route auto") {
		t.Fatalf("a configured policy closes the hand-picked path: %v", err)
	}
	w.sel.AllowManualChains = true
	if err := ManualPrepareAllowed(w.c); err != nil {
		t.Errorf("the owner can keep ordered chains: %v", err)
	}
	w.c.Profile.Selection = nil
	if err := ManualPrepareAllowed(w.c); err != nil {
		t.Errorf("without a policy nothing changes: %v", err)
	}
}

// The configured effort must be an effort the freshly listed model offers, and the usable context is the smaller of what the
// configuration claims and what the catalog lists: a configuration cannot promise more than the harness provides.
func TestCatalogBoundsTheConfiguredEffortAndContext(t *testing.T) {
	cases := map[string]struct {
		hook func(*providers.ModelEntry)
		mut  func(*providers.Provider)
		want string // exclusion of w-sub, "" = still eligible
	}{
		"effort not offered":         {hook: func(m *providers.ModelEntry) { m.Variants = []string{"low", "medium"} }, want: "effort_not_in_catalog"},
		"effort offered":             {hook: func(m *providers.ModelEntry) { m.Variants = []string{"low", "high"} }},
		"catalog lists no variants":  {hook: func(m *providers.ModelEntry) {}},
		"catalog window is smaller":  {hook: func(m *providers.ModelEntry) { m.ContextTokens = 10000 }, want: "context_too_small"},
		"config window is smaller":   {hook: func(m *providers.ModelEntry) { m.ContextTokens = 1000000 }, mut: func(p *providers.Provider) { p.ContextTokens = 20000 }, want: "context_too_small"},
		"catalog window is plenty":   {hook: func(m *providers.ModelEntry) { m.ContextTokens = 1000000 }},
		"config silent, catalog set": {hook: func(m *providers.ModelEntry) { m.ContextTokens = 10000 }, mut: func(p *providers.Provider) { p.ContextTokens = 0 }, want: "context_too_small"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			if tc.mut != nil {
				p := w.cfg.Providers["w-sub"]
				tc.mut(&p)
				w.cfg.Providers["w-sub"] = p
			}
			w.invHook = func(_ int, inv *providers.Inventory) {
				for i := range inv.Models {
					if inv.Models[i].ID == "gpt-sub" {
						tc.hook(&inv.Models[i])
					}
				}
			}
			r := mustPrepare(t, w)
			got := excluded(r, "w-sub")
			if tc.want == "" && got != "" || tc.want != "" && !strings.HasPrefix(got, tc.want) {
				t.Errorf("w-sub: %q, want %q", got, tc.want)
			}
		})
	}
}

// A review's tier follows what git says the checkout changed, not what the coordinator says: its list can only add to the diff.
func TestReviewTierFollowsTheActualDiffNotTheCoordinatorsList(t *testing.T) {
	launch := func(w *world) {
		if err := ledger.Append(ledger.Row{Kind: "routing_launch", Attempt: "at-w", Run: "run", Task: "task", Worker: &ledger.AgentModel{Agent: "codex", Model: "gpt-sub"}, Type: "backend", Effort: "high", Config: "c", Suite: "s1", Tier: "T1"}); err != nil {
			t.Fatal(err)
		}
	}
	review := func(w *world, claimed ...string) {
		w.profile = func(tp *TaskProfile) { tp.Phase, tp.ChangedFiles = "review", claimed }
	}

	t.Run("the list understates the change", func(t *testing.T) {
		w := newWorld(t)
		launch(w)
		review(w, "docs/readme.md")
		w.diff = []string{"docs/readme.md", "infra/secrets/prod.key"} // a sensitive path the coordinator did not mention
		r, err := w.prepare()
		if r == nil || r.Class == nil || r.Class.Tier != "T3" {
			t.Fatalf("an unlisted sensitive file in the real diff must escalate the tier: %v\n%s", err, dump(r))
		}
		if !strings.Contains(strings.Join(r.Class.Reasons, ";"), "actual changed files") {
			t.Errorf("the report says why: %v", r.Class.Reasons)
		}
	})

	t.Run("the actual diff is the profile of record", func(t *testing.T) {
		w := newWorld(t)
		launch(w)
		review(w, "docs/readme.md")
		w.diff = []string{"src/api/users.go"}
		r := mustPrepare(t, w)
		raw, err := os.ReadFile(filepath.Join(artifactDir("task"), "profile.json"))
		if err != nil || !strings.Contains(string(raw), "src/api/users.go") || !strings.Contains(string(raw), "docs/readme.md") {
			t.Fatalf("the stored profile must carry the real diff and the claim: %s %v", raw, err)
		}
		if _, err := Validate(w.c, "run", r.Receipt.Agent, r.Receipt.Model); err != nil {
			t.Errorf("a review receipt validates against the profile it was classified on: %v", err)
		}
	})

	t.Run("no diff, no review", func(t *testing.T) {
		for name, set := range map[string]func(*world){
			"git fails":    func(w *world) { w.diffErr = errors.New("fatal: bad revision 'origin/main...HEAD'") },
			"empty change": func(w *world) { w.diff = nil },
		} {
			w := newWorld(t)
			launch(w)
			review(w, "src/api/users.go")
			set(w)
			if _, err := w.prepare(); ErrCode(err) != CodeClassification || w.calls.Load() != 0 {
				t.Errorf("%s: a review whose real diff is unknown or empty stops before discovery: %v (calls %d)", name, err, w.calls.Load())
			}
		}
	})

	t.Run("a worker needs no diff", func(t *testing.T) {
		w := newWorld(t)
		w.diffErr = errors.New("must not be asked")
		mustPrepare(t, w)
	})
}

func TestGitChangedFilesSeesCommittedUncommittedAndRenamedPaths(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		// no ambient hooks or configuration: a global post-commit hook that writes into the repository would show up as a change
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "core.hooksPath=/dev/null", "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	write("keep.txt", "keep\n")
	write("moved/from.go", "package a\n")
	git("add", "-A")
	git("commit", "-qm", "base")
	git("update-ref", "refs/remotes/origin/main", "HEAD")
	git("checkout", "-qb", "work")
	write("src/new.go", "package b\n")
	git("add", "-A")
	git("commit", "-qm", "committed")
	git("mv", "moved/from.go", "elsewhere.go") // staged rename: both paths are changes
	write("untracked.txt", "u\n")
	got, err := gitChangedFiles(dir, "origin/main")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"elsewhere.go", "moved/from.go", "src/new.go", "untracked.txt"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("changed files %v, want %v", got, want)
	}
	if _, err := gitChangedFiles(dir, "origin/nope"); err == nil {
		t.Error("an unknown base must be an error, not an empty change")
	}
}

// A review can only run where advise.sh can run it: a reviewer on another harness would cost a probe and a reservation and
// then not run, so it is not a candidate however well it is qualified.
func TestReviewerHarnessMustBeLaunchable(t *testing.T) {
	w := newWorld(t)
	w.cfg.Providers["r-agy"] = providers.Provider{Agent: "antigravity", Model: "gemini-r3", Launch: "shell", Effort: "high", Capabilities: []string{"repository-edit", "tests"}, ContextTokens: 200000,
		Billing: &providers.Billing{Mode: "credits", Unit: "agy_credits", Pool: "agy/pool"}, ProbeBound: &providers.Bound{Calls: 1, Amount: 0.1}}
	w.cfg.ReviewChains["all"] = append(w.cfg.ReviewChains["all"], "r-agy")
	w.seedReviewer("r-agy", 4, 4, 4, 0, 1)
	w.profile = func(tp *TaskProfile) { tp.Phase, tp.ChangedFiles = "review", []string{"src/api/users.go"} }
	if err := ledger.Append(ledger.Row{Kind: "routing_launch", Attempt: "at-w", Run: "run", Task: "task", Worker: &ledger.AgentModel{Agent: "codex", Model: "gpt-sub"}, Type: "backend", Effort: "high", Config: "c", Suite: "s1", Tier: "T1"}); err != nil {
		t.Fatal(err)
	}
	r := mustPrepare(t, w)
	if got := excluded(r, "r-agy"); !strings.HasPrefix(got, "reviewer_not_launchable") {
		t.Errorf("r-agy: %q", got)
	}
	if r.Receipt.Provider == "r-agy" {
		t.Error("a reviewer that advise.sh cannot launch was selected")
	}
}

func TestBaselineMissingFundingRefusesBeforeProbe(t *testing.T) {
	w := newWorld(t)
	os.Remove(ledger.Path())
	w.sel.BaselineChain = "worker:backend"
	r, err := w.prepare()
	if err == nil || r.Receipt != nil || len(w.probeLog()) != 0 {
		t.Fatalf("missing execution funding must refuse before probe: err=%v result=%s", err, dump(r))
	}
}

func TestBaselineCannotRelaxReviewerQuality(t *testing.T) {
	w := newWorld(t)
	w.profile = func(tp *TaskProfile) { tp.Phase, tp.ChangedFiles = "review", []string{"src/api/users.go"} }
	w.sel.BaselineChain = "review:all"
	w.sel.Reviewer.MinDefectFixtures = 5 // cost evidence exists, but neither reviewer meets this quality floor
	if err := ledger.Append(ledger.Row{Kind: "routing_launch", Attempt: "at-w", Run: "run", Task: "task", Worker: &ledger.AgentModel{Agent: "codex", Model: "gpt-sub"}, Type: "backend", Effort: "high", Config: "c", Suite: "s1", Tier: "T1"}); err != nil {
		t.Fatal(err)
	}
	r, err := w.prepare()
	if err == nil || r.Receipt != nil || len(w.probeLog()) != 0 {
		t.Fatalf("baseline must never relax reviewer quality or probe an unqualified reviewer: err=%v result=%s probes=%v", err, dump(r), w.probeLog())
	}
}

func TestKnownFalseClaimsOverrideIncompleteCoverage(t *testing.T) {
	why, insufficient := qualifyWorker(ledger.WorkerEvidence{Settled: 2, Coverage: 0.5, FalseClaims: 3, FalseClaimsKnown: false}, contract.WorkerPolicy{QualityFloor: contract.QualityFloor{MaxFalseClaims: ip(0)}})
	if why == "" || insufficient {
		t.Fatalf("known false claims must be a hard exclusion: %q insufficient=%v", why, insufficient)
	}
}

func TestReplacedReceiptCannotAcquireExecutionLease(t *testing.T) {
	w := newWorld(t)
	first := mustPrepare(t, w)
	mustPrepare(t, w)
	if l, err := AcquireLease(w.c, first.Receipt); err == nil {
		l.Release()
		t.Fatal("stale loaded receipt executed after replacement released its hold")
	}
	if ReceiptUsed(first.Attempt) {
		t.Fatal("refused stale receipt must not be consumed")
	}
}

func TestReviewReceiptRejectsPostPrepareCheckoutChanges(t *testing.T) {
	w := newWorld(t)
	initReviewGit(t, w.c.Worktree)
	w.seed(ledger.Row{Kind: "routing_launch", Attempt: "worker-before-review", Run: "run", Task: "task", Worker: &ledger.AgentModel{Agent: "codex", Model: "gpt-sub"}})
	w.profile = func(tp *TaskProfile) { tp.Phase, tp.ChangedFiles = "review", []string{"src/api/users.go"} }
	r := mustPrepare(t, w)
	if _, err := Validate(w.c, "run", r.Receipt.Agent, r.Receipt.Model); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.c.Worktree, "src/api/users.go"), []byte("changed after preparation"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Validate(w.c, "run", r.Receipt.Agent, r.Receipt.Model); err == nil {
		t.Fatal("changed content under the same path must invalidate review")
	}
}

func initReviewGit(t *testing.T, wt string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(wt, ".git")); err == nil {
		return
	}
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "test"}} {
		if out, err := exec.Command("git", append([]string{"-C", wt}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	os.MkdirAll(filepath.Join(wt, "src/api"), 0700)
	os.WriteFile(filepath.Join(wt, "src/api/users.go"), []byte("original"), 0600)
	for _, args := range [][]string{{"add", "."}, {"commit", "-qm", "initial"}, {"update-ref", "refs/remotes/origin/main", "HEAD"}} {
		if out, err := exec.Command("git", append([]string{"-C", wt}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
}

func TestSettlementRejectsChargeIdentityContradictingHold(t *testing.T) {
	for name, identity := range map[string][2]string{"other run": {"another-run", "task"}, "other task": {"run", "another-task"}, "missing run": {"", "task"}, "missing task": {"run", ""}} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			r := mustPrepare(t, w)
			ch, err := ledger.NewChargeRow(r.Attempt, "wrong-identity", "worker", "codex/sub/week", "subscription_percent", "codex", "gpt-sub", 1)
			if err != nil {
				t.Fatal(err)
			}
			ch.Run, ch.Task = identity[0], identity[1]
			w.seed(ch)
			if _, err := SettleAttempt(r.Attempt); err == nil {
				t.Fatal("unattributed or misattributed charge released current hold")
			}
			hs, _ := budget.HoldsFor(r.Attempt)
			if len(hs) != 1 || hs[0].State != "reserved" {
				t.Fatalf("invalid evidence must preserve funding: %+v", hs)
			}
		})
	}
}

func TestReceiptConsumptionSharesPreparationLock(t *testing.T) {
	for _, review := range []bool{false, true} {
		t.Run(fmt.Sprintf("review=%v", review), func(t *testing.T) {
			w := newWorld(t)
			if review {
				w.seed(ledger.Row{Kind: "routing_launch", Attempt: "worker-proof", Run: "run", Task: "task", Worker: &ledger.AgentModel{Agent: "codex", Model: "gpt-sub"}})
				w.profile = func(tp *TaskProfile) { tp.Phase, tp.ChangedFiles = "review", []string{"src/api/users.go"} }
			}
			r := mustPrepare(t, w)
			unlock, err := lockTaskDecision(w.c.Name, 0)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				if review {
					done <- ConsumeReview(w.c, r.Receipt)
					return
				}
				l, err := AcquireLease(w.c, r.Receipt)
				if l != nil {
					l.Release()
				}
				done <- err
			}()
			select {
			case err := <-done:
				unlock()
				t.Fatalf("consumption escaped preparation lock: %v", err)
			case <-time.After(50 * time.Millisecond):
			}
			if ReceiptUsed(r.Attempt) {
				unlock()
				t.Fatal("receipt consumed while preparation owns lock")
			}
			unlock()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReplacedReviewReceiptCannotBeConsumed(t *testing.T) {
	w := newWorld(t)
	w.seed(ledger.Row{Kind: "routing_launch", Attempt: "worker-proof", Run: "run", Task: "task", Worker: &ledger.AgentModel{Agent: "codex", Model: "gpt-sub"}})
	w.profile = func(tp *TaskProfile) { tp.Phase, tp.ChangedFiles = "review", []string{"src/api/users.go"} }
	first := mustPrepare(t, w)
	next := mustPrepare(t, w)
	if err := ConsumeReview(w.c, first.Receipt); err == nil {
		t.Fatal("stale review consumed after replacement")
	}
	if err := ConsumeReview(w.c, next.Receipt); err != nil {
		t.Fatal(err)
	}
	mustPrepare(t, w) // no launch row yet: the consumed review still owns its execution funding
	hs, _ := budget.HoldsFor(next.Attempt)
	if len(hs) != 1 || hs[0].State != "reserved" {
		t.Fatalf("consumed review funding released before launch recording: %+v", hs)
	}
}

func TestReviewRefusesUntrackedNestedRepository(t *testing.T) {
	w := newWorld(t)
	nested := filepath.Join(w.c.Worktree, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	initReviewGit(t, nested)
	w.profile = func(tp *TaskProfile) { tp.Phase = "review" }
	if _, err := PrepareAuto(context.Background(), w.in()); ErrCode(err) != CodeClassification {
		t.Fatalf("nested paths cannot be silently omitted from review classification: %v", err)
	}
}

func TestPendingOrcaDispatchSurvivesLauncherCrashAndRefusesLiveSettlement(t *testing.T) {
	w := newWorld(t)
	w.startRun()
	r := mustPrepare(t, w)
	l, err := AcquireLease(w.c, r.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.MarkDispatchPending(); err != nil {
		t.Fatal(err)
	}
	l.Release()
	if _, err := SettleAttempt(r.Attempt); err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("pending live handoff settlement: %v", err)
	}
	// Even a dead launcher cannot settle while the local provider process is alive.
	l.Child, l.ChildStart, l.Pid = l.Pid, l.PidStart, 0
	if err := writeLease(l); err != nil {
		t.Fatal(err)
	}
	if _, err := SettleAttempt(r.Attempt); err == nil || !strings.Contains(err.Error(), "provider process") {
		t.Fatalf("pending live provider settlement: %v", err)
	}
	// A crashed launcher is gone, but its unknown remote worker must keep the checkout fenced.
	l.Child = 0
	if err := writeLease(l); err != nil {
		t.Fatal(err)
	}
	if why := TaskBusy("task"); why == "" {
		t.Fatal("pending dispatch lost its fence when launcher disappeared")
	}
	next := *r.Receipt
	next.Attempt = newID("at")
	if _, err := AcquireLease(w.c, &next); err == nil {
		t.Fatal("another writer acquired an uncertain pending dispatch")
	}
	// Explicit settlement is permitted once local handoff processes are gone.
	if _, err := SettleAttempt(r.Attempt); err != nil {
		t.Fatal(err)
	}
}
