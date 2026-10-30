package routing

// Calibration is an explicitly authorized experiment, never a scored selection. It
// uses the ordinary receipt, single-use launch and accounting paths without a probe.
import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/voravitl/rein/internal/budget"
	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/ledger"
	"github.com/voravitl/rein/internal/providers"
)

const modeCalibration = "calibration_unscored"

// PrepareCalibration prepares one exact local worker experiment under the owner's
// fixed calibration caps. It performs no discovery, probe or provider execution.
func PrepareCalibration(in CalibrationInput, profile []byte) (_ *Decision, resultErr error) {
	if in.Contract == nil || in.Run == "" || in.Calls != 1 {
		return nil, errors.New("calibration execution requires contract, run and exactly one call")
	}
	c := in.Contract
	path, err := receipt(c)
	if err != nil {
		return nil, err
	}
	unlock, err := lockTaskDecision(c.Name, 0)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err = releaseUnused(path); err != nil {
		return nil, err
	}
	if err = os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	cfg, cp, ch, err := config(in.ConfigPath)
	if err != nil {
		return nil, err
	}
	if err = cfg.CheckModelCalls(); err != nil {
		return nil, err
	}
	sel := in.Contract.Profile.EffectiveSelection()
	if in.Owner != nil && in.Owner.EffectiveSelection() != nil {
		sel = in.Owner.EffectiveSelection()
	}
	if sel == nil || sel.Calibration == nil || len(sel.Problems()) != 0 {
		return nil, refuse(CodeSelectionPolicy, "valid owner calibration authorization and caps required")
	}
	tp, err := DecodeTaskProfile(profile)
	if err != nil {
		return nil, err
	}
	cl, err := Classify(c, in.Owner, tp)
	if err != nil {
		return nil, err
	}
	if cl.Phase != "worker" {
		return nil, errors.New("calibration execution currently supports workers only")
	}
	p, ok := cfg.Providers[in.Provider]
	if !ok {
		return nil, fmt.Errorf("unknown provider %q", in.Provider)
	}
	maker, pool, err := identity(p)
	if err != nil {
		return nil, err
	}
	if p.Launch != "shell" || ledger.IsAliasModel(p.Model) {
		return nil, errors.New("calibration requires a local launch and exact model identity")
	}
	if !slices.Contains(cfg.WorkerChains[cl.Policy], in.Provider) {
		return nil, errors.New("calibration provider outside classified worker policy")
	}
	if !hookInstalled(c, p.Agent) {
		return nil, errors.New("selected worker hook not installed")
	}
	for _, cap := range cl.Capabilities {
		if !slices.Contains(p.Capabilities, cap) {
			return nil, fmt.Errorf("calibration provider missing capability %s", cap)
		}
	}
	if p.ContextTokens < cl.Context {
		return nil, errors.New("calibration provider context insufficient or unknown")
	}
	if err = cfg.CheckQuota(in.Provider); err != nil {
		return nil, err
	}
	cool, err := state()
	if err != nil {
		return nil, err
	}
	if blocked(cool, pool) {
		return nil, errors.New("selected quota pool unavailable")
	}
	if why := billingProblem(p); why != "" {
		return nil, errors.New(why)
	}
	// Reuse the reservation gate; mark the hold with this execution's immutable decision.
	in.executionDecision = newID("calexec")
	h, err := ReserveCalibration(in)
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			if _, releaseErr := budget.Settle(h.ID, map[string]float64{p.Billing.Pool: 0}); releaseErr != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("release unused calibration hold %s: %w", h.ID, releaseErr))
			}
		}
	}()
	now := time.Now()
	exp := now.Add(receiptTTL)
	d := &Decision{Task: c.Name, Run: in.Run, Worktree: c.Worktree, ContractHash: contractHash(c), HooksGeneration: c.HooksGeneration,
		ConfigPath: cp, ConfigHash: ch, Chain: "worker:" + cl.Policy, Provider: in.Provider, Agent: p.Agent, Model: p.Model, Launch: p.Launch, Maker: maker, Pool: pool, PreparedAt: now,
		DecisionID: in.executionDecision, Attempt: h.Attempt, SelectionMode: modeCalibration, Kind: cl.Kind, Tier: cl.Tier, Suite: sel.Suite, Effort: p.Effort, ConfigFingerprint: fingerprint(p), TaskProfileHash: hashBytes(profile), PolicyHash: sel.Hash(), ExpiresAt: &exp, Hold: h.ID}
	if err = writeArtifact(c.Name, "profile.json", profile); err != nil {
		return nil, err
	}
	if err = record("routing", d); err != nil {
		return nil, err
	}
	if err = atomic(path, d); err != nil {
		return nil, err
	}
	committed = true
	return d, nil
}

// CalibrationResult wraps a calibration execution receipt in the result shape
// the supervised graph journals for a prepared worker. It carries no ranking,
// comparison or exclusions: nothing was scored.
func CalibrationResult(d *Decision) *AutoResult {
	if d == nil {
		return nil
	}
	return &AutoResult{Attempt: d.Attempt, Decision: d.DecisionID, SelectionMode: d.SelectionMode, Receipt: d, Launch: launchOf(d), Rounds: []*Round{}}
}

func validateCalibration(c *contract.Contract, d *Decision, cfg *providers.Config) error {
	if d.SelectionMode != modeCalibration || d.DecisionID == "" || d.Attempt == "" || d.Hold == "" || d.ExpiresAt == nil || time.Now().After(*d.ExpiresAt) {
		return errors.New("incomplete or expired calibration receipt")
	}
	sel, owner, err := resolvePolicy(c)
	if err != nil {
		return err
	}
	if sel.Calibration == nil || sel.Hash() != d.PolicyHash || sel.Suite != d.Suite {
		return errors.New("calibration policy changed")
	}
	raw, err := os.ReadFile(filepath.Join(artifactDir(c.Name), "profile.json"))
	if err != nil || hashBytes(raw) != d.TaskProfileHash {
		return errors.New("calibration profile changed or missing")
	}
	tp, err := DecodeTaskProfile(raw)
	if err != nil {
		return err
	}
	cl, err := Classify(c, owner, tp)
	if err != nil {
		return err
	}
	p, ok := cfg.Providers[d.Provider]
	if !ok || cl.Phase != "worker" || d.Chain != "worker:"+cl.Policy || cl.Kind != d.Kind || cl.Tier != d.Tier || d.Effort != p.Effort || d.ConfigFingerprint != fingerprint(p) || p.Launch != "shell" || !slices.Contains(cfg.WorkerChains[cl.Policy], d.Provider) {
		return errors.New("calibration identity or classification changed")
	}
	holds, err := budget.HoldsFor(d.Attempt)
	if err != nil {
		return err
	}
	for _, h := range holds {
		if calibrationHoldMatches(h, d, p) && h.State == "reserved" {
			return nil
		}
	}
	return errors.New("calibration reservation missing, changed or settled")
}

func calibrationHoldMatches(h budget.Hold, d *Decision, p providers.Provider) bool {
	if h.ID != d.Hold || h.Attempt != d.Attempt || h.Decision != d.DecisionID || h.Task != d.Task || h.Run != d.Run || len(h.Items) != 1 || p.Billing == nil {
		return false
	}
	it := h.Items[0]
	return it.Kind == "calibration" && it.Calls == 1 && it.Provider == d.Provider && it.Model == d.Model && it.Effort == d.Effort && it.Config == d.ConfigFingerprint && it.Pool == p.Billing.Pool && it.Unit == p.Billing.Unit
}

// Only an execution receipt recorded before launch can authorize worker evidence
// for a calibration hold. Reserve-only reviewer/legacy holds keep their old rules.
func workerCalibration(h budget.Hold, rows []ledger.Row) *Decision {
	for _, r := range rows {
		if r.Kind != "routing" || r.Task != h.Task || r.Run != h.Run {
			continue
		}
		var d Decision
		if json.Unmarshal([]byte(r.Notes), &d) != nil || d.SelectionMode != modeCalibration || d.Hold != h.ID || d.DecisionID == "" || d.DecisionID != h.Decision || d.Attempt != h.Attempt || d.Task != h.Task || d.Run != h.Run || d.Launch != "shell" || len(h.Items) != 1 {
			continue
		}
		it := h.Items[0]
		if it.Kind == "calibration" && it.Provider == d.Provider && it.Model == d.Model && it.Effort == d.Effort && it.Config == d.ConfigFingerprint {
			return &d
		}
	}
	return nil
}
