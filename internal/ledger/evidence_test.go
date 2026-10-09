package ledger

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

func bp(v bool) *bool       { return &v }
func fp(v float64) *float64 { return &v }
func stamp(d time.Duration) string {
	return t0.Add(-d).Format(time.RFC3339)
}
func testMaker(m string) string {
	switch {
	case strings.HasPrefix(m, "gpt"):
		return "openai"
	case strings.HasPrefix(m, "claude"):
		return "anthropic"
	}
	return "unknown"
}
func opts() EvidenceOptions {
	return EvidenceOptions{Now: t0, MaxAge: 30 * 24 * time.Hour, Maker: testMaker}
}

var key = CohortKey{Agent: "codex", Model: "gpt-x", Effort: "high", Config: "cfg1", Kind: "backend", Tier: "T1", Suite: "s1"}

func launch(attempt, model string) Row {
	return Row{Kind: "routing_launch", Attempt: attempt, Worker: &AgentModel{"codex", model}, Type: "backend", Effort: "high", Config: "cfg1", Suite: "s1", Tier: "T1", RecordedAt: stamp(time.Hour)}
}
func success(attempt string) Row {
	return Row{Kind: "task", Attempt: attempt, GatesPassed: bp(true), Reviewer: &AgentModel{"claude", "claude-opus"}, ReviewSHA: "abc123", Approved: true,
		Blockers: ip(0), Highs: ip(0), FalseClaims: ip(0), Drift: ip(0), ReviewRounds: ip(2), Minutes: fp(10), Source: "measured", RecordedAt: stamp(time.Minute)}
}
func failedGates(attempt string) Row {
	return Row{Kind: "task", Attempt: attempt, GatesPassed: bp(false), FalseClaims: ip(0), Source: "measured", RecordedAt: stamp(time.Minute)}
}
func pair(id string, outcome Row) []Row { return []Row{launch(id, "gpt-x"), outcome} }
func concat(parts ...[]Row) []Row {
	var all []Row
	for _, p := range parts {
		all = append(all, p...)
	}
	return all
}

func TestWilsonLowerClosedForms(t *testing.T) {
	for _, n := range []int{1, 5, 10, 100, 1000} {
		got, ok := WilsonLower(n, n)
		want := float64(n) / (float64(n) + Z95*Z95) // s == n collapses the interval to n/(n+z²)
		if !ok || math.Abs(got-want) > 1e-12 {
			t.Errorf("WilsonLower(%d,%d)=%v,%v want %v", n, n, got, ok, want)
		}
		if lo, _ := WilsonLower(0, n); lo != 0 {
			t.Errorf("WilsonLower(0,%d)=%v want 0 (no negative noise)", n, lo)
		}
	}
	for _, c := range []struct {
		n    int
		want float64
	}{{5, 0.56551}, {10, 0.72246}, {100, 0.96299}} {
		if got, _ := WilsonLower(c.n, c.n); math.Abs(got-c.want) > 1e-4 {
			t.Errorf("%d/%d = %v want ~%v", c.n, c.n, got, c.want)
		}
	}
	for _, bad := range [][2]int{{0, 0}, {1, 0}, {-1, 5}, {6, 5}} {
		if _, ok := WilsonLower(bad[0], bad[1]); ok {
			t.Errorf("WilsonLower%v must report !ok", bad)
		}
	}
	prev := -1.0
	for s := 0; s <= 20; s++ {
		lo, _ := WilsonLower(s, 20)
		if lo < prev {
			t.Fatalf("not monotonic at %d", s)
		}
		prev = lo
	}
	// the documented formula, evaluated independently
	n, s := 40.0, 33.0
	p := s / n
	z := Z95
	want := (p + z*z/(2*n) - z*math.Sqrt(p*(1-p)/n+z*z/(4*n*n))) / (1 + z*z/n)
	if got, _ := WilsonLower(33, 40); math.Abs(got-want) > 1e-12 {
		t.Errorf("formula mismatch %v vs %v", got, want)
	}
}

func TestIsAliasModel(t *testing.T) {
	for _, a := range []string{"auto", "AUTO", "default", "latest", "best", "opus", "sonnet", "haiku", "opusplan", "gpt-x-latest", "foo:latest", "claude-sonnet[1m]"} {
		if !IsAliasModel(a) {
			t.Errorf("%q must be an alias", a)
		}
	}
	// a provider prefix does not make a moving alias exact
	for _, a := range []string{"openrouter/auto", "opencode-go/latest", "vendor/model-latest", "anthropic/opus"} {
		if !IsAliasModel(a) {
			t.Errorf("%q must be an alias: the last path segment is what moves", a)
		}
	}
	for _, e := range []string{"gpt-6.1-sol", "claude-opus-5.5", "gemini-3.8-flash-high", "opencode-go/glm-5.3-flash"} {
		if IsAliasModel(e) {
			t.Errorf("%q is an exact model", e)
		}
	}
}

func TestWorkerEvidenceCountsVerifiedSuccessesOnly(t *testing.T) {
	var rows []Row
	for _, id := range []string{"a1", "a2", "a3", "a4"} {
		rows = append(rows, pair(id, success(id))...)
	}
	rows = append(rows, pair("a5", failedGates("a5"))...)
	ev := EvaluateWorker(rows, key, opts())
	if ev.Settled != 5 || ev.Complete != 5 || ev.Successes != 4 || ev.Coverage != 1 || !ev.FalseClaimsKnown || ev.FalseClaims != 0 {
		t.Fatalf("%+v", ev)
	}
	lo, _ := WilsonLower(4, 5)
	if ev.Lower95 != lo || math.Abs(ev.Q-100*lo) > 1e-9 {
		t.Errorf("Q must be 100*Wilson lower: %+v want %v", ev, lo)
	}
	if ev.MeanRounds == nil || *ev.MeanRounds != 2 || ev.MeanMinutes == nil || *ev.MeanMinutes != 10 {
		t.Errorf("means: %+v", ev)
	}
	if ev.Newest.IsZero() || ev.Hash == "" {
		t.Errorf("age/hash missing: %+v", ev)
	}
	// determinism, and one more success must change the hash
	if again := EvaluateWorker(rows, key, opts()); again.Hash != ev.Hash {
		t.Error("hash not deterministic")
	}
	more := EvaluateWorker(append(rows, pair("a6", success("a6"))...), key, opts())
	if more.Hash == ev.Hash || more.Successes != 5 {
		t.Errorf("hash must bind the evidence: %+v", more)
	}
}

func TestNoDataIsNotQualityEvidence(t *testing.T) {
	ev := EvaluateWorker(nil, key, opts())
	if ev.Settled != 0 || ev.Q != 0 || ev.Coverage != 0 || ev.FalseClaimsKnown {
		t.Errorf("empty cohort must report zeros, not a perfect score: %+v", ev)
	}
}

func TestApprovalAloneIsNotSuccess(t *testing.T) {
	cases := map[string]func(*Row){
		"no review sha":          func(r *Row) { r.ReviewSHA = "" },
		"same maker reviewer":    func(r *Row) { r.Reviewer = &AgentModel{"codex", "gpt-y"} },
		"unknown maker reviewer": func(r *Row) { r.Reviewer = &AgentModel{"opencode", "mystery"} },
		"no reviewer":            func(r *Row) { r.Reviewer = nil },
		"unknown blockers":       func(r *Row) { r.Blockers = nil },
		"unknown highs":          func(r *Row) { r.Highs = nil },
		"unknown false claims":   func(r *Row) { r.FalseClaims = nil },
		"unknown drift":          func(r *Row) { r.Drift = nil },
		"unknown gates":          func(r *Row) { r.GatesPassed = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			out := success("x")
			mutate(&out)
			ev := EvaluateWorker(pair("x", out), key, opts())
			if ev.Successes != 0 || ev.Complete != 0 && name != "same maker reviewer" && name != "unknown maker reviewer" {
				t.Errorf("must not count as success/complete: %+v", ev)
			}
			if ev.Settled != 1 {
				t.Errorf("an unknown attempt must stay in the denominator: %+v", ev)
			}
		})
	}
	for name, mutate := range map[string]func(*Row){
		"blocker": func(r *Row) { r.Blockers = ip(1) }, "high": func(r *Row) { r.Highs = ip(1) },
		"false claim": func(r *Row) { r.FalseClaims = ip(1) }, "drift": func(r *Row) { r.Drift = ip(1) }, "not approved": func(r *Row) { r.Approved = false },
	} {
		out := success("x")
		mutate(&out)
		if ev := EvaluateWorker(pair("x", out), key, opts()); ev.Successes != 0 || ev.Complete != 1 {
			t.Errorf("%s: a complete rejection is a failure, not unknown: %+v", name, ev)
		}
	}
	noMaker := opts()
	noMaker.Maker = nil
	if ev := EvaluateWorker(pair("x", success("x")), key, noMaker); ev.Successes != 0 {
		t.Error("independence can never be proven without a maker function")
	}
}

func TestMissingFieldsLowerCoverageNotDenominator(t *testing.T) {
	partial := success("p")
	partial.Drift = nil
	rows := concat(pair("s", success("s")), pair("p", partial))
	ev := EvaluateWorker(rows, key, opts())
	if ev.Settled != 2 || ev.Complete != 1 || ev.Coverage != 0.5 || ev.Successes != 1 {
		t.Errorf("%+v", ev)
	}
	// launched, exited, never recorded: still attempted
	rows = append(rows, launch("e", "gpt-x"), Row{Kind: "routing_exit", Attempt: "e", RecordedAt: stamp(time.Minute)})
	if ev = EvaluateWorker(rows, key, opts()); ev.Settled != 3 || ev.Complete != 1 {
		t.Errorf("a missing outcome must not disappear: %+v", ev)
	}
	unknownClaims := failedGates("u")
	unknownClaims.FalseClaims = nil
	ev = EvaluateWorker(pair("u", unknownClaims), key, opts())
	if ev.Complete != 1 || ev.FalseClaimsKnown {
		t.Errorf("a gate failure is certain, but unknown claim assessments stay unknown: %+v", ev)
	}
}

func TestSubstitutionAndAliasAreQuarantined(t *testing.T) {
	sub := success("s")
	sub.ResolvedModel = "gpt-other"
	rows := pair("s", sub)
	ev := EvaluateWorker(rows, key, opts())
	if ev.Settled != 1 || ev.Complete != 0 || ev.Successes != 0 {
		t.Errorf("the launched model gets an incomplete attempt, never the substitute's success: %+v", ev)
	}
	other := key
	other.Model = "gpt-other"
	if ev = EvaluateWorker(rows, other, opts()); ev.Settled != 0 {
		t.Errorf("a substitute must not inherit the evidence: %+v", ev)
	}
	for _, a := range Attempts(rows, opts()) {
		if !a.Quarantined || !strings.Contains(a.Reason, "substituted") {
			t.Errorf("%+v", a)
		}
	}
	aliasKey := key
	aliasKey.Model = "auto"
	aliasRows := []Row{launch("al", "auto"), success("al")}
	if ev = EvaluateWorker(aliasRows, aliasKey, opts()); ev.Complete != 0 || ev.Successes != 0 || ev.Settled != 1 {
		t.Errorf("an unresolved alias is never evidence: %+v", ev)
	}
	resolved := success("al")
	resolved.ResolvedModel = "auto"
	if ev = EvaluateWorker([]Row{launch("al", "auto"), resolved}, aliasKey, opts()); ev.Successes != 0 {
		t.Errorf("resolved_model equal to the alias is still an alias: %+v", ev)
	}
}

// A quarantine is sticky: a later outcome row that omits resolved_model cannot make the substitute's run count for the launched
// model, and the worker identity refuses to pick between disagreeing records.
func TestQuarantineSurvivesALaterCorrection(t *testing.T) {
	sub := success("s")
	sub.ResolvedModel = "gpt-other"
	fixed := success("s") // a "correction" that does not name the model that ran
	fixed.RecordedAt = stamp(time.Second)
	rows := []Row{launch("s", "gpt-x"), sub, fixed}
	ev := EvaluateWorker(rows, key, opts())
	if ev.Settled != 1 || ev.Complete != 0 || ev.Successes != 0 {
		t.Fatalf("a corrected outcome un-quarantined a substituted model: %+v", ev)
	}
	for _, a := range Attempts(rows, opts()) {
		if !a.Quarantined {
			t.Errorf("not quarantined: %+v", a)
		}
	}
	l := launch("s", "gpt-x")
	l.Run, l.Task = "r", "t"
	again := fixed
	again.ResolvedModel = "gpt-third"
	if _, _, err := WorkerIdentity([]Row{l, sub, again}, "r", "t"); err == nil || !strings.Contains(err.Error(), "disagree") {
		t.Errorf("outcome rows naming two different models must not yield an identity: %v", err)
	}
	same := fixed
	same.ResolvedModel = "gpt-other"
	if _, model, err := WorkerIdentity([]Row{l, sub, same}, "r", "t"); err != nil || model != "gpt-other" {
		t.Errorf("agreeing records are fine: %v %v", model, err)
	}
}

// One receipt buys one execution. A second launch row for the same attempt must not heal an earlier crash out of the denominator.
func TestRelaunchOfOneAttemptDoesNotHealACrash(t *testing.T) {
	crashed := launch("a", "gpt-x")
	relaunch := launch("a", "gpt-x")
	relaunch.RecordedAt = stamp(30 * time.Minute)
	ev := EvaluateWorker([]Row{crashed, relaunch, success("a")}, key, opts())
	if ev.Settled != 1 || ev.Complete != 0 || ev.Successes != 0 {
		t.Fatalf("a relaunched attempt counted as complete: %+v", ev)
	}
	for _, a := range Attempts([]Row{crashed, relaunch, success("a")}, opts()) {
		if a.Success || a.Complete || !strings.Contains(a.Reason, "launched 2 times") {
			t.Errorf("%+v", a)
		}
	}
	if ev = EvaluateWorker([]Row{launch("a", "gpt-x"), success("a")}, key, opts()); ev.Successes != 1 {
		t.Errorf("a single launch must still count: %+v", ev)
	}
}

func TestCohortKeysDoNotLeak(t *testing.T) {
	rows := pair("a", success("a"))
	for name, mutate := range map[string]func(*CohortKey){
		"model": func(k *CohortKey) { k.Model = "gpt-x2" }, "effort": func(k *CohortKey) { k.Effort = "low" },
		"config": func(k *CohortKey) { k.Config = "cfg2" }, "kind": func(k *CohortKey) { k.Kind = "frontend" },
		"tier": func(k *CohortKey) { k.Tier = "T3" }, "suite": func(k *CohortKey) { k.Suite = "s2" }, "agent": func(k *CohortKey) { k.Agent = "kiro" },
	} {
		k := key
		mutate(&k)
		if ev := EvaluateWorker(rows, k, opts()); ev.Settled != 0 {
			t.Errorf("%s change must start with no evidence: %+v", name, ev)
		}
	}
	disagree := success("a")
	disagree.Effort = "low"
	if ev := EvaluateWorker(pair("a", disagree), key, opts()); ev.Complete != 0 {
		t.Errorf("an outcome disagreeing with its launch is not evidence: %+v", ev)
	}
}

func TestWindowLivenessAndRowFiltering(t *testing.T) {
	old := launch("old", "gpt-x")
	old.RecordedAt = stamp(31 * 24 * time.Hour)
	if ev := EvaluateWorker([]Row{old, success("old")}, key, opts()); ev.Settled != 0 {
		t.Errorf("stale evidence must not count: %+v", ev)
	}
	// unsettled attempts: in flight is skipped, a gone launcher counts as unknown
	l := launch("u", "gpt-x")
	l.Pid, l.PidStart = ip(4242), func() *int64 { v := int64(7); return &v }()
	alive := opts()
	alive.Alive = func(pid int, start int64) bool { return pid == 4242 && start == 7 }
	if ev := EvaluateWorker([]Row{l}, key, alive); ev.Settled != 0 {
		t.Errorf("a live launcher is in flight, not a failure: %+v", ev)
	}
	gone := opts()
	gone.Alive = func(int, int64) bool { return false }
	if ev := EvaluateWorker([]Row{l}, key, gone); ev.Settled != 1 || ev.Complete != 0 {
		t.Errorf("a crashed launcher counts as unknown: %+v", ev)
	}
	if ev := EvaluateWorker([]Row{l}, key, opts()); ev.Settled != 1 {
		t.Errorf("unknown liveness must be conservative: %+v", ev)
	}
	noPid := launch("n", "gpt-x")
	if ev := EvaluateWorker([]Row{noPid}, key, alive); ev.Settled != 1 {
		t.Errorf("a launch without a pid cannot be proven in flight: %+v", ev)
	}
	// the later outcome wins (repair), excluded attempt, approx/memory and legacy rows
	first := success("r")
	first.Approved = false
	if ev := EvaluateWorker(concat(pair("r", first), []Row{success("r")}), key, opts()); ev.Successes != 1 || ev.Settled != 1 {
		t.Errorf("repair: %+v", ev)
	}
	ex := opts()
	ex.ExcludeAttempt = "r"
	if ev := EvaluateWorker(pair("r", success("r")), key, ex); ev.Settled != 0 {
		t.Errorf("own attempt must be excluded: %+v", ev)
	}
	approx, memory := success("m"), success("m")
	approx.Approx, memory.Source = true, "memory"
	if ev := EvaluateWorker([]Row{launch("m", "gpt-x"), approx}, key, opts()); ev.Complete != 0 {
		t.Errorf("approx rows are not measurements: %+v", ev)
	}
	if ev := EvaluateWorker([]Row{launch("m", "gpt-x"), memory}, key, opts()); ev.Complete != 0 {
		t.Errorf("memory rows are not measurements: %+v", ev)
	}
	legacy := success("")
	legacy.Worker = &AgentModel{"codex", "gpt-x"}
	legacy.Type = "backend"
	if ev := EvaluateWorker([]Row{legacy, legacy}, key, opts()); ev.Settled != 0 {
		t.Errorf("legacy rows carry no attempt: %+v", ev)
	}
	if got := Attempts(pair("t", success("t")), EvidenceOptions{Now: t0, Maker: testMaker}); len(got) != 0 {
		t.Error("MaxAge <= 0 must admit nothing")
	}
}

func fixture(id, class string, model string, flag bool) Row {
	adj := true
	r := Row{Kind: "fixture", Fixture: id, FixtureClass: class, Reviewer: &AgentModel{"claude", model}, Adjudicated: &adj, Type: "backend", Effort: "high", Config: "cfg1", Tier: "T1", Suite: "s1", RecordedAt: stamp(time.Hour)}
	if class == "defect" {
		r.Detected = &flag
	} else {
		r.FalsePositive = &flag
	}
	return r
}

var rkey = CohortKey{Agent: "claude", Model: "claude-opus", Effort: "high", Config: "cfg1", Kind: "backend", Tier: "T1", Suite: "s1"}

func TestReviewerEvidenceIsFixtureBasedNotApprovalBased(t *testing.T) {
	var rows []Row
	for i, id := range []string{"d1", "d2", "d3", "d4"} {
		rows = append(rows, fixture(id, "defect", "claude-opus", i != 3)) // 3 of 4 detected
	}
	for i, id := range []string{"c1", "c2", "c3", "c4", "c5"} {
		rows = append(rows, fixture(id, "clean", "claude-opus", i == 4)) // 1 false positive
	}
	ev := EvaluateReviewer(rows, rkey, opts())
	if ev.DefectFixtures != 4 || ev.Detected != 3 || ev.CleanFixtures != 5 || ev.CleanOK != 4 || ev.FalsePositives != 1 || ev.Coverage != 1 {
		t.Fatalf("%+v", ev)
	}
	recall, _ := WilsonLower(3, 4)
	spec, _ := WilsonLower(4, 5)
	if ev.RecallLower != recall || ev.SpecificityLower != spec || math.Abs(ev.Q-100*math.Min(recall, spec)) > 1e-9 || math.Abs(ev.FalsePositiveRate-0.2) > 1e-12 {
		t.Errorf("Q must be 100*min(recall lower, specificity lower): %+v", ev)
	}
	// a reviewer with a perfect approval record and no fixtures has no reviewer evidence at all
	var approvals []Row
	for i := 0; i < 50; i++ {
		// every cohort field matches, so only the row KIND keeps these out of the reviewer's evidence
		approvals = append(approvals, Row{Kind: "task", Attempt: "x", Reviewer: &AgentModel{"claude", "claude-opus"}, Approved: true, Type: "backend", Effort: "high", Config: "cfg1", Tier: "T1", Suite: "s1",
			Source: "measured", RecordedAt: stamp(time.Hour)})
	}
	if ev = EvaluateReviewer(approvals, rkey, opts()); ev.Rows != 0 || ev.Q != 0 || ev.DefectFixtures != 0 {
		t.Errorf("approval frequency is not reviewer quality: %+v", ev)
	}
}

func TestReviewerFixtureDedupeCoverageAndIdentity(t *testing.T) {
	missed := fixture("d1", "defect", "claude-opus", false)
	rows := []Row{missed, fixture("d1", "defect", "claude-opus", true)} // judged twice: counts once, later wins
	if ev := EvaluateReviewer(rows, rkey, opts()); ev.DefectFixtures != 1 || ev.Detected != 1 || ev.Rows != 1 {
		t.Errorf("a repeated fixture counts once: %+v", ev)
	}
	unadjudicated := fixture("d2", "defect", "claude-opus", true)
	unadjudicated.Adjudicated = nil
	unknown := fixture("c1", "clean", "claude-opus", false)
	unknown.FalsePositive = nil
	bad := fixture("x1", "weird", "claude-opus", true)
	ev := EvaluateReviewer([]Row{fixture("d1", "defect", "claude-opus", true), unadjudicated, unknown, bad}, rkey, opts())
	if ev.Rows != 4 || ev.CompleteRows != 1 || ev.Coverage != 0.25 || ev.DefectFixtures != 1 || ev.CleanFixtures != 0 {
		t.Errorf("incomplete fixtures lower coverage and never count: %+v", ev)
	}
	substituted := fixture("d9", "defect", "claude-opus", true)
	substituted.ResolvedModel = "claude-other"
	alias := fixture("d8", "defect", "auto", true)
	akey := rkey
	akey.Model = "auto"
	if ev = EvaluateReviewer([]Row{substituted}, rkey, opts()); ev.Rows != 0 {
		t.Errorf("a substituted reviewer belongs to no cohort: %+v", ev)
	}
	if ev = EvaluateReviewer([]Row{alias}, akey, opts()); ev.Rows != 0 {
		t.Errorf("auto cannot be an exact reviewer identity: %+v", ev)
	}
	other := rkey
	other.Suite = "s2"
	if ev = EvaluateReviewer([]Row{fixture("d1", "defect", "claude-opus", true)}, other, opts()); ev.Rows != 0 {
		t.Errorf("suite change starts empty: %+v", ev)
	}
	old := fixture("d1", "defect", "claude-opus", true)
	old.RecordedAt = stamp(40 * 24 * time.Hour)
	if ev = EvaluateReviewer([]Row{old}, rkey, opts()); ev.Rows != 0 {
		t.Errorf("stale fixtures expire: %+v", ev)
	}
}

func mkCharge(attempt, id, component, pool, unit string, amount float64) Row {
	r, err := NewChargeRow(attempt, id, component, pool, unit, "codex", "gpt-x", amount)
	if err != nil {
		panic(err)
	}
	r.RecordedAt = stamp(time.Minute)
	return r
}

func TestCostPerSuccessCountsEveryAttemptOnce(t *testing.T) {
	rows := concat(
		pair("a1", success("a1")), pair("a2", failedGates("a2")),
		[]Row{
			mkCharge("a1", "w", "worker", "openai/api", "usd", 2),
			mkCharge("a2", "w", "worker", "openai/api", "usd", 1),
			mkCharge("a2", "rep", "repair", "openai/api", "usd", 0.5),
			mkCharge("a1", "p", "probe", "openai/api", "usd", 0.25),
			mkCharge("a1", "p", "probe", "openai/api", "usd", 0.25), // the same charge recorded twice counts once
			mkCharge("a1", "rv", "review", "claude/max/5h", "subscription_percent", 4),
			mkCharge("a1", "sh", "shared", "openai/api", "usd", 9),
			mkCharge("a1", "cal", "calibration", "openai/api", "usd", 9),
		})
	ev := EvaluateWorkerCost(rows, key, opts())
	if !ev.Available || ev.Attempts != 2 || ev.Successes != 1 || ev.Totals["openai/api"] != 3.75 || ev.PerSuccess["openai/api"] != 3.75 {
		t.Fatalf("failed attempts and probes are part of cost per success: %+v", ev)
	}
	if ev.MaxAttempt["openai/api"] != 2.25 || ev.Units["openai/api"] != "usd" || len(ev.Totals) != 1 {
		t.Errorf("review/shared/calibration must stay out of cohort totals: %+v", ev)
	}
	totals, units := SharedOverhead(rows, "a1")
	if totals["openai/api"] != 9 || units["openai/api"] != "usd" || len(totals) != 1 {
		t.Errorf("shared overhead is reported separately: %v %v", totals, units)
	}
	bound := mkCharge("a1", "w", "worker", "openai/api", "usd", 2)
	bound.Approx = true
	if got := EvaluateWorkerCost(concat(pair("a1", success("a1")), []Row{bound}), key, opts()); !got.Approx || !got.Available {
		t.Errorf("a bound-charged amount counts and is disclosed: %+v", got)
	}
}

func TestUnavailableCostIsNeverZero(t *testing.T) {
	ok := concat(pair("a1", success("a1")), []Row{mkCharge("a1", "w", "worker", "p", "usd", 1)})
	cases := map[string][]Row{
		"no attempts":      nil,
		"zero successes":   concat(pair("f", failedGates("f")), []Row{mkCharge("f", "w", "worker", "p", "usd", 1)}),
		"no worker charge": concat(pair("a1", success("a1")), []Row{mkCharge("a1", "r", "repair", "p", "usd", 1)}),
		"partial coverage": concat(ok, pair("a2", success("a2"))), // a2 has no charge
		"unit conflict":    concat(ok, pair("a2", success("a2")), []Row{mkCharge("a2", "w", "worker", "p", "kiro_credits", 1)}),
		"conflicting duplicate": concat(ok, []Row{
			mkCharge("a1", "w", "worker", "p", "usd", 5)}), // same ChargeID "w", different amount
	}
	for name, rows := range cases {
		ev := EvaluateWorkerCost(rows, key, opts())
		if ev.Available || ev.PerSuccess != nil || ev.Reason == "" {
			t.Errorf("%s must be unavailable with a reason: %+v", name, ev)
		}
	}
	if ev := EvaluateWorkerCost(ok, key, opts()); !ev.Available {
		t.Errorf("control: %+v", ev)
	}
	// invalid charge rows never count
	junk := ok[len(ok)-1]
	neg := -1.0
	junk.Amount, junk.ChargeID = &neg, "neg"
	badUnit := mkCharge("a1", "bu", "worker", "p", "usd", 1)
	badUnit.Unit = "dollars"
	if ev := EvaluateWorkerCost(concat(ok, []Row{junk, badUnit}), key, opts()); ev.Totals["p"] != 1 {
		t.Errorf("invalid charges must be ignored: %+v", ev)
	}
}

func TestReviewCostPerReviewer(t *testing.T) {
	rc := func(id, model string, amount float64) Row {
		r := mkCharge("a1", id, "review", "claude/max/5h", "subscription_percent", amount)
		r.Model, r.Provider = model, "claude"
		return r
	}
	rows := []Row{rc("r1", "claude-opus", 2), rc("r2", "claude-opus", 4), rc("r3", "claude-sonnet", 100)}
	ev := EvaluateReviewCost(rows, "claude", "claude-opus", opts())
	if !ev.Available || ev.Reviews != 2 || ev.PerReview["claude/max/5h"] != 3 || ev.MaxReview["claude/max/5h"] != 4 {
		t.Errorf("%+v", ev)
	}
	if ev = EvaluateReviewCost(rows, "kiro", "claude-opus", opts()); ev.Available {
		t.Errorf("another provider's reviewer must not borrow the charges: %+v", ev)
	}
	// a charge that names no provider could belong to any harness or effort: it prices nobody
	anon := rc("r9", "claude-opus", 50)
	anon.Provider = ""
	if ev = EvaluateReviewCost([]Row{anon}, "claude", "claude-opus", opts()); ev.Available {
		t.Errorf("an unattributed review charge was used: %+v", ev)
	}
	// two providers with the same model (two efforts) keep separate review costs
	hi, lo := rc("h1", "claude-opus", 10), rc("l1", "claude-opus", 1)
	hi.Provider, lo.Provider = "claude-high", "claude-low"
	if ev = EvaluateReviewCost([]Row{hi, lo}, "claude-low", "claude-opus", opts()); ev.PerReview["claude/max/5h"] != 1 {
		t.Errorf("effort variants were pooled: %+v", ev)
	}
	if ev = EvaluateReviewCost(nil, "claude", "claude-opus", opts()); ev.Available || ev.Reason == "" {
		t.Errorf("no review charges is unavailable, not free: %+v", ev)
	}
	mixed := append(rows, mkCharge("a1", "r4", "review", "claude/max/5h", "usd", 1))
	mixed[3].Model, mixed[3].Provider = "claude-opus", "claude"
	if ev = EvaluateReviewCost(mixed, "claude", "claude-opus", opts()); ev.Available {
		t.Errorf("units must not merge: %+v", ev)
	}
}

func TestNewChargeRowValidation(t *testing.T) {
	good, err := NewChargeRow("a", "", "worker", "pool", "kiro_credits", "kiro", "m", 1.5)
	if err != nil || good.Kind != "cost" || good.ChargeID == "" || *good.Amount != 1.5 {
		t.Fatalf("%+v %v", good, err)
	}
	if again, _ := NewChargeRow("a", "", "worker", "pool", "kiro_credits", "kiro", "m", 1.5); again.ChargeID == good.ChargeID {
		t.Error("random ids must differ")
	}
	for _, c := range []struct {
		attempt, component, pool, unit string
		amount                         float64
	}{{"", "worker", "p", "usd", 1}, {"a", "worker", "", "usd", 1}, {"a", "bogus", "p", "usd", 1}, {"a", "worker", "p", "eur", 1},
		{"a", "worker", "p", "usd", -1}, {"a", "worker", "p", "usd", math.NaN()}, {"a", "worker", "p", "usd", math.Inf(1)}} {
		if _, err := NewChargeRow(c.attempt, "x", c.component, c.pool, c.unit, "", "", c.amount); err == nil {
			t.Errorf("accepted %+v", c)
		}
	}
}

func TestLaunchRowAndWorkerIdentity(t *testing.T) {
	l1 := launch("a1", "gpt-x")
	l1.Run, l1.Task = "r", "t"
	l2 := launch("a2", "auto")
	l2.Run, l2.Task = "r", "t"
	if got, ok := LaunchRow([]Row{l1, l2}, "a2"); !ok || got.Worker.Model != "auto" {
		t.Errorf("%+v %v", got, ok)
	}
	if _, ok := LaunchRow([]Row{l1}, "nope"); ok {
		t.Error("unknown attempt")
	}
	agent, model, err := WorkerIdentity([]Row{l1}, "r", "t")
	if err != nil || agent != "codex" || model != "gpt-x" {
		t.Errorf("%v %v %v", agent, model, err)
	}
	if _, _, err = WorkerIdentity([]Row{l1, l2}, "r", "t"); err == nil || !strings.Contains(err.Error(), "alias unresolved") {
		t.Errorf("the latest launch is an unresolved alias: %v", err)
	}
	out := Row{Kind: "task", Attempt: "a2", ResolvedModel: "gpt-resolved"}
	if _, model, err = WorkerIdentity([]Row{l1, l2, out}, "r", "t"); err != nil || model != "gpt-resolved" {
		t.Errorf("the recorded resolved model wins: %v %v", model, err)
	}
	if _, _, err = WorkerIdentity([]Row{l1}, "r", "other"); err == nil {
		t.Error("no launch must be an error")
	}
}

func f64(v float64) *float64 { return &v }

func TestRatesLoadValidatePrice(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "prices.json")
	if m, err := LoadRates(p); m != nil || err != nil {
		t.Errorf("missing file: %v %v", m, err)
	}
	body := `{"_doc":"x","legacy":{"usd_per_mtok":10},"note":"text",
	 "m":{"input_per_mtok":2,"output_per_mtok":10,"cache_read_per_mtok":0.2,"tiers":[{"context_over":200000,"input_per_mtok":4,"output_per_mtok":15}],
	      "source":"https://example.com/pricing","as_of":"2026-10-01T00:00:00Z","valid_until":"2026-11-01T00:00:00Z"}}`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := LoadRates(p)
	if err != nil || len(m) != 1 || m["m"].Source == "" {
		t.Fatalf("legacy and doc entries are skipped: %v %v", m, err)
	}
	r := m["m"]
	if err := r.Validate(t0); err != nil {
		t.Fatal(err)
	}
	// base rates, cache read, and the tier above 200k context (tier input/output replace, cache falls back to base)
	got, err := r.PriceUSD(Usage{InputTokens: 1_000_000, OutputTokens: 500_000, CacheReadTokens: 1_000_000, ContextTokens: 1000})
	if err != nil || math.Abs(got-(2+5+0.2)) > 1e-9 {
		t.Errorf("base: %v %v", got, err)
	}
	got, err = r.PriceUSD(Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000, CacheReadTokens: 1_000_000, ContextTokens: 250000})
	if err != nil || math.Abs(got-(4+15+0.2)) > 1e-9 {
		t.Errorf("tier: %v %v", got, err)
	}
	if _, err = r.PriceUSD(Usage{CacheWriteTokens: 10}); err == nil {
		t.Error("a missing cache-write rate must not price as zero")
	}
	if got, err = r.PriceUSD(Usage{}); err != nil || got != 0 {
		t.Errorf("no tokens, no cost: %v %v", got, err)
	}
	if _, err = r.PriceUSD(Usage{InputTokens: -1}); err == nil {
		t.Error("negative tokens")
	}
	for name, mutate := range map[string]func(*Rates){
		"no source": func(r *Rates) { r.Source = "" }, "bad as_of": func(r *Rates) { r.AsOf = "yesterday" }, "bad valid_until": func(r *Rates) { r.ValidUntil = "" },
		"future as_of": func(r *Rates) { r.AsOf = "2026-12-01T00:00:00Z" }, "expired": func(r *Rates) { r.ValidUntil = "2026-10-05T00:00:00Z" },
		"no output": func(r *Rates) { r.OutputPerMTok = nil }, "negative": func(r *Rates) { r.InputPerMTok = f64(-1) },
		"tier order": func(r *Rates) { r.Tiers = append(r.Tiers, RateTier{ContextOver: 100}) }, "nan": func(r *Rates) { r.CacheReadPerMTok = f64(math.NaN()) },
	} {
		c := r
		c.Tiers = append([]RateTier(nil), r.Tiers...)
		mutate(&c)
		if err := c.Validate(t0); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if r.Hash() != m["m"].Hash() || r.Hash() == "" {
		t.Error("hash unstable")
	}
	changed := r
	changed.InputPerMTok = f64(3)
	if changed.Hash() == r.Hash() {
		t.Error("hash must change with the rate")
	}
	all := map[string]Rates{"m": r}
	h1, h2 := RatesHash(all, []string{"m", "x"}), RatesHash(all, []string{"x", "m"})
	if h1 != h2 || RatesHash(all, []string{"m"}) == h1 {
		t.Error("RatesHash must be order independent and bind absent keys")
	}
	if err := os.WriteFile(p, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadRates(p); err == nil {
		t.Error("bad json")
	}
}
