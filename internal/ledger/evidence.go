package ledger

// Strict, attributable quality evidence for automatic selection (docs/ROUTING_SELECTION_DESIGN.md, "Quality score and
// eligibility"). A nil pointer or an empty string means UNKNOWN, never false or zero: unknown lowers coverage, it can never
// count as a success and it never leaves a denominator. Evidence for one CohortKey is never carried to another.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

// Z95 is the two-sided 95% normal quantile used by the Wilson interval.
const Z95 = 1.959963984540054

// WilsonLower is the lower endpoint of the two-sided 95% Wilson score interval for successes/n:
// (p + z²/2n - z*sqrt(p(1-p)/n + z²/4n²)) / (1 + z²/n). ok=false for n <= 0 or successes outside [0,n].
func WilsonLower(successes, n int) (lower float64, ok bool) {
	if n <= 0 || successes < 0 || successes > n {
		return 0, false
	}
	nf, p := float64(n), float64(successes)/float64(n)
	z2 := Z95 * Z95
	lower = (p + z2/(2*nf) - Z95*math.Sqrt(p*(1-p)/nf+z2/(4*nf*nf))) / (1 + z2/nf)
	return math.Max(lower, 0), true // float noise can leave -1e-17 at p=0
}

// IsAliasModel reports whether an ID can resolve to different models over time, so it cannot be an exact identity. A provider
// prefix does not make an alias exact: "openrouter/auto" is as moving as "auto".
func IsAliasModel(id string) bool {
	l := strings.ToLower(strings.TrimSpace(id))
	if i := strings.LastIndex(l, "/"); i >= 0 && aliasWord(l[i+1:]) {
		return true
	}
	return aliasWord(l)
}

func aliasWord(l string) bool {
	switch l {
	case "auto", "default", "latest", "best", "opus", "sonnet", "haiku", "opusplan":
		return true
	default:
		return strings.HasSuffix(l, "-latest") || strings.HasSuffix(l, ":latest") || strings.Contains(l, "[1m]")
	}
}

// CohortKey identifies evidence that may be pooled. Every field matters: a new model, an unresolved alias, another effort,
// another harness configuration, task kind, risk tier or evaluation suite starts with no evidence.
type CohortKey struct {
	Agent  string `json:"agent"`
	Model  string `json:"model"`
	Effort string `json:"effort"`
	Config string `json:"config"`
	Kind   string `json:"kind"`
	Tier   string `json:"tier"`
	Suite  string `json:"suite"`
}

// EvidenceOptions bounds which rows count and how liveness and makers are judged.
type EvidenceOptions struct {
	Now            time.Time                       // required
	MaxAge         time.Duration                   // > 0; attempts and fixtures recorded before Now-MaxAge do not count
	Alive          func(pid int, start int64) bool // is the launcher still that same live process? nil = liveness unknown
	Maker          func(model string) string       // model -> maker, "unknown" when unknown; nil = independence is never proven
	ExcludeAttempt string                          // an attempt whose own rows must not count (the decision being validated)
}

// Attempt is one settled worker attempt as the ledger can prove it.
type Attempt struct {
	ID          string    `json:"id"`
	Key         CohortKey `json:"key"`
	LaunchedAt  time.Time `json:"launched_at"`
	Settled     bool      `json:"settled"`
	Complete    bool      `json:"complete"`    // every critical field known, or the failure is certain
	Success     bool      `json:"success"`     // gates + exact-revision independent review + no high/blocker/false claim/drift
	Quarantined bool      `json:"quarantined"` // the model that ran is not the model launched (or the alias never resolved)
	FalseClaims *int      `json:"false_claims,omitempty"`
	Rounds      *int      `json:"rounds,omitempty"`
	Minutes     *float64  `json:"minutes,omitempty"`
	Reason      string    `json:"reason,omitempty"` // why incomplete or quarantined; "" when complete
}

type attemptRows struct {
	launch, exit, outcome *Row
	launches              int      // routing_launch rows: a receipt is single-use, so more than one is a relaunch
	resolved              []string // every distinct resolved_model any outcome row of the attempt named
}

// measured reports whether a row is a measurement; rows rebuilt from memory or flagged approximate are not evidence.
func measured(r *Row) bool { return !r.Approx && r.Source != "memory" }

func inWindow(recordedAt string, o EvidenceOptions) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, recordedAt)
	if err != nil || o.MaxAge <= 0 || t.Before(o.Now.Add(-o.MaxAge)) || t.After(o.Now.Add(time.Minute)) {
		return t, false
	}
	return t, true
}

// collect groups launch, exit and outcome rows by attempt. When several rows of one kind exist the later one wins, but nothing is
// healed by a later row: relaunches are counted and every resolved model any outcome named is kept (see judge).
func collect(rows []Row, o EvidenceOptions) (order []string, by map[string]*attemptRows) {
	by = map[string]*attemptRows{}
	for i := range rows {
		r := &rows[i]
		if r.Attempt == "" || r.Attempt == o.ExcludeAttempt || !measured(r) {
			continue
		}
		var slot func(a *attemptRows) **Row
		switch r.Kind {
		case "routing_launch":
			if r.Worker == nil {
				continue
			}
			slot = func(a *attemptRows) **Row { return &a.launch }
		case "routing_exit":
			slot = func(a *attemptRows) **Row { return &a.exit }
		case "task":
			slot = func(a *attemptRows) **Row { return &a.outcome }
		default:
			continue
		}
		a := by[r.Attempt]
		if a == nil {
			a = &attemptRows{}
			by[r.Attempt] = a
			order = append(order, r.Attempt)
		}
		*slot(a) = r
		switch r.Kind {
		case "routing_launch":
			a.launches++
		case "task":
			if r.ResolvedModel != "" && !slices.Contains(a.resolved, r.ResolvedModel) {
				a.resolved = append(a.resolved, r.ResolvedModel)
			}
		}
	}
	return order, by
}

// Attempts lists every in-window, settled attempt of any cohort in launch order. An attempt with neither outcome nor exit is
// skipped while its launcher is provably still running (in flight); once the launcher is gone, or when that cannot be told,
// it counts as settled-unknown, so a crash can never remove an attempt from a denominator.
func Attempts(rows []Row, o EvidenceOptions) []Attempt {
	order, by := collect(rows, o)
	var out []Attempt
	for _, id := range order {
		a := by[id]
		if a.launch == nil {
			continue
		}
		at, ok := inWindow(a.launch.RecordedAt, o)
		if !ok {
			continue
		}
		if a.outcome == nil && a.exit == nil && o.Alive != nil && a.launch.Pid != nil && a.launch.PidStart != nil && o.Alive(*a.launch.Pid, *a.launch.PidStart) {
			continue
		}
		out = append(out, judge(id, at, a, o))
	}
	return out
}

func judge(id string, launchedAt time.Time, a *attemptRows, o EvidenceOptions) Attempt {
	l := a.launch
	key := CohortKey{Agent: l.Worker.Agent, Model: l.Worker.Model, Effort: l.Effort, Config: l.Config, Kind: l.Type, Tier: l.Tier, Suite: l.Suite}
	at := Attempt{ID: id, Key: key, LaunchedAt: launchedAt, Settled: true}
	if a.launches > 1 { // one receipt buys one execution: a relaunch must not heal an earlier crash out of the denominator
		at.Reason = fmt.Sprintf("receipt launched %d times", a.launches)
		return at
	}
	out := a.outcome
	if out == nil {
		at.Reason = "launcher gone without outcome"
		if a.exit != nil {
			at.Reason = "exited without an outcome row"
		}
		if a.exit != nil {
			at.Minutes = a.exit.Minutes
		}
		return at
	}
	at.FalseClaims, at.Rounds, at.Minutes = out.FalseClaims, out.ReviewRounds, out.Minutes
	if at.Minutes == nil && a.exit != nil {
		at.Minutes = a.exit.Minutes
	}
	// a substitution stays on the record: a later correction that omits resolved_model cannot clear it
	for _, m := range a.resolved {
		if m != key.Model {
			at.Quarantined, at.Reason = true, "model substituted: "+m+" ran instead of "+key.Model
			return at
		}
	}
	resolved := out.ResolvedModel
	switch {
	case resolved == "" && IsAliasModel(key.Model):
		at.Quarantined, at.Reason = true, "alias unresolved: the model that ran was never recorded"
		return at
	case resolved == "":
		resolved = key.Model
	}
	if (out.Effort != "" && out.Effort != key.Effort) || (out.Config != "" && out.Config != key.Config) || (out.Suite != "" && out.Suite != key.Suite) ||
		(out.Tier != "" && out.Tier != key.Tier) || (out.Type != "" && out.Type != key.Kind) {
		at.Reason = "outcome disagrees with launch"
		return at
	}
	switch {
	case out.GatesPassed == nil:
		at.Reason = "gates_passed unknown"
		return at
	case !*out.GatesPassed:
		at.Complete = true // the failure is certain whatever else is unknown
		return at
	case out.Reviewer == nil:
		at.Reason = "reviewer unknown"
	case out.ReviewSHA == "":
		at.Reason = "review_sha unknown"
	case out.Blockers == nil:
		at.Reason = "blockers unknown"
	case out.Highs == nil:
		at.Reason = "highs unknown"
	case out.FalseClaims == nil:
		at.Reason = "false_claims unknown"
	case out.Drift == nil:
		at.Reason = "drift unknown"
	}
	if at.Reason != "" {
		return at
	}
	at.Complete = true
	at.Success = out.Approved && *out.Blockers == 0 && *out.Highs == 0 && *out.FalseClaims == 0 && *out.Drift == 0 && independent(o, out.Reviewer.Model, resolved)
	return at
}

// independent: the reviewer's maker is known, the worker's maker is known, and they differ.
func independent(o EvidenceOptions, reviewer, worker string) bool {
	if o.Maker == nil {
		return false
	}
	r, w := o.Maker(reviewer), o.Maker(worker)
	return r != "" && r != "unknown" && w != "" && w != "unknown" && r != w
}

func hashJSON(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// WorkerEvidence is what the ledger records about one worker cohort. It carries no verdict: the caller applies its policy.
type WorkerEvidence struct {
	Key              CohortKey `json:"key"`
	Settled          int       `json:"settled"`
	Complete         int       `json:"complete"`
	Successes        int       `json:"successes"`
	FalseClaims      int       `json:"false_claims"`       // sum over attempts whose count is known
	FalseClaimsKnown bool      `json:"false_claims_known"` // every settled attempt has a known count (false when there are none)
	Coverage         float64   `json:"coverage"`           // Complete/Settled
	Lower95          float64   `json:"lower95"`            // WilsonLower(Successes, Complete)
	Q                float64   `json:"q"`                  // 100*Lower95
	Confidence       float64   `json:"confidence"`         // the interval's two-sided level, 0.95
	Newest           time.Time `json:"newest"`
	Oldest           time.Time `json:"oldest"`
	MeanMinutes      *float64  `json:"mean_minutes,omitempty"` // over complete attempts that recorded minutes
	MeanRounds       *float64  `json:"mean_rounds,omitempty"`  // over successful attempts that recorded rounds
	Hash             string    `json:"hash"`
}

// EvaluateWorker pools exactly the attempts of key. Insufficient data is reported as numbers (Settled, Complete, Q=0), not hidden.
func EvaluateWorker(rows []Row, key CohortKey, o EvidenceOptions) WorkerEvidence {
	ev := WorkerEvidence{Key: key, Confidence: 0.95}
	var minutes, rounds []float64
	known := true
	for _, a := range Attempts(rows, o) {
		if a.Key != key {
			continue
		}
		ev.Settled++
		if ev.Oldest.IsZero() || a.LaunchedAt.Before(ev.Oldest) {
			ev.Oldest = a.LaunchedAt
		}
		if a.LaunchedAt.After(ev.Newest) {
			ev.Newest = a.LaunchedAt
		}
		if a.FalseClaims == nil {
			known = false
		} else {
			ev.FalseClaims += *a.FalseClaims
		}
		if a.Complete {
			ev.Complete++
			if a.Minutes != nil {
				minutes = append(minutes, *a.Minutes)
			}
		}
		if a.Success {
			ev.Successes++
			if a.Rounds != nil {
				rounds = append(rounds, float64(*a.Rounds))
			}
		}
	}
	ev.FalseClaimsKnown = known && ev.Settled > 0
	if ev.Settled > 0 {
		ev.Coverage = float64(ev.Complete) / float64(ev.Settled)
	}
	if lo, ok := WilsonLower(ev.Successes, ev.Complete); ok {
		ev.Lower95, ev.Q = lo, 100*lo
	}
	ev.MeanMinutes, ev.MeanRounds = mean(minutes), mean(rounds)
	ev.Hash = hashJSON(ev)
	return ev
}

// ReviewerEvidence is a reviewer's record on frozen red (known-defect) and clean fixtures. Approval frequency on real tasks
// is deliberately not an input.
type ReviewerEvidence struct {
	Key               CohortKey `json:"key"`
	DefectFixtures    int       `json:"defect_fixtures"` // distinct COMPLETE defect fixtures
	Detected          int       `json:"detected"`
	CleanFixtures     int       `json:"clean_fixtures"` // distinct COMPLETE clean fixtures
	CleanOK           int       `json:"clean_ok"`
	FalsePositives    int       `json:"false_positives"`
	Rows              int       `json:"rows"`          // distinct in-window fixtures of the cohort, complete or not
	CompleteRows      int       `json:"complete_rows"` // of which every critical field is known and adjudicated
	Coverage          float64   `json:"coverage"`
	RecallLower       float64   `json:"recall_lower"`
	SpecificityLower  float64   `json:"specificity_lower"`
	Q                 float64   `json:"q"`                   // 100*min(RecallLower, SpecificityLower)
	Confidence        float64   `json:"confidence"`          // the intervals' two-sided level, 0.95
	FalsePositiveRate float64   `json:"false_positive_rate"` // FalsePositives/CleanFixtures
	Newest            time.Time `json:"newest"`
	Oldest            time.Time `json:"oldest"`
	Hash              string    `json:"hash"`
}

// EvaluateReviewer counts the cohort's fixture rows (Kind "fixture"); a fixture judged twice counts once, the later row winning.
func EvaluateReviewer(rows []Row, key CohortKey, o EvidenceOptions) ReviewerEvidence {
	ev := ReviewerEvidence{Key: key, Confidence: 0.95}
	final := map[string]*Row{}
	var order []string
	for i := range rows {
		r := &rows[i]
		if r.Kind != "fixture" || r.Reviewer == nil || !measured(r) || (o.ExcludeAttempt != "" && r.Attempt == o.ExcludeAttempt) {
			continue
		}
		if r.ResolvedModel != "" && r.ResolvedModel != r.Reviewer.Model {
			continue // another model answered: it belongs to no cohort
		}
		if r.ResolvedModel == "" && IsAliasModel(r.Reviewer.Model) {
			continue // an alias is not an exact reviewer identity
		}
		if (CohortKey{Agent: r.Reviewer.Agent, Model: r.Reviewer.Model, Effort: r.Effort, Config: r.Config, Kind: r.Type, Tier: r.Tier, Suite: r.Suite}) != key {
			continue
		}
		if _, ok := inWindow(r.RecordedAt, o); !ok {
			continue
		}
		id := r.Fixture
		if id == "" {
			id = fmt.Sprintf("#%d", i) // an unnamed fixture cannot be deduplicated: it stays a distinct, incomplete row
		}
		if final[id] == nil {
			order = append(order, id)
		}
		final[id] = r
	}
	for _, id := range order {
		r := final[id]
		t, _ := time.Parse(time.RFC3339, r.RecordedAt)
		if ev.Oldest.IsZero() || t.Before(ev.Oldest) {
			ev.Oldest = t
		}
		if t.After(ev.Newest) {
			ev.Newest = t
		}
		ev.Rows++
		if r.Adjudicated == nil || !*r.Adjudicated {
			continue
		}
		switch {
		case r.FixtureClass == "defect" && r.Detected != nil:
			ev.DefectFixtures++
			if *r.Detected {
				ev.Detected++
			}
		case r.FixtureClass == "clean" && r.FalsePositive != nil:
			ev.CleanFixtures++
			if *r.FalsePositive {
				ev.FalsePositives++
			} else {
				ev.CleanOK++
			}
		default:
			continue
		}
		ev.CompleteRows++
	}
	if ev.Rows > 0 {
		ev.Coverage = float64(ev.CompleteRows) / float64(ev.Rows)
	}
	recall, okR := WilsonLower(ev.Detected, ev.DefectFixtures)
	spec, okS := WilsonLower(ev.CleanOK, ev.CleanFixtures)
	if okR {
		ev.RecallLower = recall
	}
	if okS {
		ev.SpecificityLower = spec
	}
	if okR && okS {
		ev.Q = 100 * math.Min(recall, spec)
	}
	if ev.CleanFixtures > 0 {
		ev.FalsePositiveRate = float64(ev.FalsePositives) / float64(ev.CleanFixtures)
	}
	ev.Hash = hashJSON(ev)
	return ev
}

// LaunchRow is the routing_launch row of an attempt (the later one if there are several).
func LaunchRow(rows []Row, attempt string) (Row, bool) {
	var found *Row
	for i := range rows {
		if rows[i].Kind == "routing_launch" && rows[i].Attempt == attempt && rows[i].Worker != nil {
			found = &rows[i]
		}
	}
	if found == nil {
		return Row{}, false
	}
	return *found, true
}

func latestLaunch(rows []Row, run, task string) *Row {
	var launch *Row
	for i := range rows {
		if r := &rows[i]; r.Kind == "routing_launch" && r.Run == run && r.Task == task && r.Worker != nil {
			launch = r
		}
	}
	return launch
}

// WorkerAttempt is the attempt of the latest attributable launch of (run, task): the work a review is about.
func WorkerAttempt(rows []Row, run, task string) (attempt string, ok bool) {
	if l := latestLaunch(rows, run, task); l != nil && l.Attempt != "" {
		return l.Attempt, true
	}
	return "", false
}

// WorkerIdentity is the exact worker model of the latest launch of (run, task): the model the outcome says ran, else the
// launched model unless that is an alias. A reviewer's maker must be judged against this, never against the model that was asked for.
func WorkerIdentity(rows []Row, run, task string) (agent, model string, err error) {
	launch := latestLaunch(rows, run, task)
	if launch == nil {
		return "", "", fmt.Errorf("no routing launch recorded for run %q task %q", run, task)
	}
	model = launch.Worker.Model
	if launch.Attempt != "" {
		resolved := ""
		for i := range rows {
			if r := &rows[i]; r.Kind == "task" && r.Attempt == launch.Attempt && r.ResolvedModel != "" {
				if resolved != "" && resolved != r.ResolvedModel {
					return "", "", fmt.Errorf("outcome rows of attempt %s disagree on the model that ran (%s, %s)", launch.Attempt, resolved, r.ResolvedModel)
				}
				resolved = r.ResolvedModel
			}
		}
		if resolved != "" {
			model = resolved
		}
	}
	if IsAliasModel(model) {
		return "", "", fmt.Errorf("alias unresolved: record resolved_model (launched as %q)", model)
	}
	return launch.Worker.Agent, model, nil
}
