package hunter

// Evidence-based bug-hunter finding registry.
//
// PRD §10 requires every finding to carry a stable ID and a fingerprint, an
// exact baseline revision, file:line and symbol, expected/actual behavior,
// the violated requirement or invariant, trigger and impact, severity (kept
// separate from confidence), exact commands with cwd/exits/environment,
// evidence references/hashes, per-attempt reproduction results, limitations
// and a next action. Findings move through a lifecycle:
//
//	CANDIDATE -> REPRODUCED -> TRIAGED -> APPROVED_FOR_FIX
//	  -> FIX_IN_PROGRESS -> VERIFIED_FIXED
//
// with auxiliary states UNVERIFIED, DUPLICATE, REJECTED, OUT_OF_SCOPE,
// BLOCKED, FLAKY and REGRESSED. A static-analysis claim is recorded as
// STATIC_SUPPORTED evidence strength, never mixed with runtime reproduction.
// Status moves that need evidence are checked by the runner/controller —
// never self-certified by the agent that proposed the finding.
//
// This file pins the schema, the fingerprint, the dedupe rule and the
// allowed transitions. It stores findings as JSONL under the contract index
// (same durability pattern as the routing event log), append-only, so a
// duplicate keeps the original evidence. Nothing here launches a worker,
// edits application code, or approves a fix.
//

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/run"
)

// Lifecycle states. The order below is the forward path; auxiliary states
// are terminal except BLOCKED (may return to the state it came from) and
// REGRESSED (returns to FIX_IN_PROGRESS).
type State string

const (
	Candidate     State = "CANDIDATE"
	Reproduced    State = "REPRODUCED"
	Triaged       State = "TRIAGED"
	ApprovedForFX State = "APPROVED_FOR_FIX"
	FixInProgress State = "FIX_IN_PROGRESS"
	VerifiedFixed State = "VERIFIED_FIXED"

	Unverified State = "UNVERIFIED"
	Duplicate  State = "DUPLICATE"
	Rejected   State = "REJECTED"
	OutOfScope State = "OUT_OF_SCOPE"
	Blocked    State = "BLOCKED"
	Flaky      State = "FLAKY"
	Regressed  State = "REGRESSED"
)

// Evidence strength. Static analysis with reasoning is STATIC_SUPPORTED —
// real evidence, but never a runtime reproduction.
type Strength string

const (
	StaticSupported Strength = "STATIC_SUPPORTED"
	ReproducedOnce  Strength = "REPRODUCED_ONCE"
	ReproducedMulti Strength = "REPRODUCED_MULTI"
)

// ReproAttempt is one reproduction run: the exact command, where it ran,
// how it exited, and what happened.
type ReproAttempt struct {
	Attempt      string `json:"attempt,omitempty"`
	Revision     string `json:"revision,omitempty"`
	Observed     bool   `json:"observed,omitempty"`
	EvidencePath string `json:"evidence_path,omitempty"`
	EvidenceHash string `json:"evidence_hash,omitempty"`
	Command      string `json:"command"`
	Cwd          string `json:"cwd"`
	Exit         int    `json:"exit"`
	Env          string `json:"env,omitempty"`
	Outcome      string `json:"outcome"` // "reproduced" | "not-reproduced" | "setup-failure" | "scope-violation"
	At           string `json:"at"`      // RFC3339
}

// Finding is one bug-hunter report. ID is stable (assigned once); Fingerprint
// is derived from baseline+location+invariant so the same defect found twice
// dedupes to DUPLICATE instead of a second record.
type Finding struct {
	FixReview       *FixReview       `json:"fix_review,omitempty"`
	OwnerAcceptance *OwnerAcceptance `json:"owner_acceptance,omitempty"`
	ID              string           `json:"id"`
	FindingIdx      int              `json:"finding_idx"`
	Task            string           `json:"task"`
	Run             string           `json:"run"`
	Baseline        string           `json:"baseline"` // exact revision the defect was found on
	File            string           `json:"file"`
	Line            int              `json:"line"`
	Symbol          string           `json:"symbol,omitempty"`
	Expected        string           `json:"expected"`
	Actual          string           `json:"actual"`
	Invariant       string           `json:"invariant"`
	Trigger         string           `json:"trigger,omitempty"`
	Impact          string           `json:"impact,omitempty"`
	Severity        string           `json:"severity"`   // with reason, kept separate from Confidence
	Confidence      string           `json:"confidence"` // "low" | "medium" | "high"
	Strength        Strength         `json:"strength"`
	Attempts        []ReproAttempt   `json:"attempts,omitempty"`
	Evidence        []string         `json:"evidence,omitempty"` // refs/hashes
	Limitations     string           `json:"limitations,omitempty"`
	NextAction      string           `json:"next_action,omitempty"`
	State           State            `json:"state"`
	Fingerprint     string           `json:"fingerprint"`
	DuplicateOf     string           `json:"duplicate_of,omitempty"`
	BlockedFrom     State            `json:"blocked_from,omitempty"` // state before BLOCKED: resume returns here, never jumps forward
	At              string           `json:"at"`
}

// FingerprintOf derives the stable fingerprint from baseline revision,
// file, line, symbol and invariant — the identity of the defect, not of the
// report. Two reports with the same fingerprint are the same finding.
func FingerprintOf(baseline, file string, line int, symbol, invariant string) string {
	h := sha256.Sum256([]byte(strings.Join([]string{
		baseline, file, fmt.Sprint(line), symbol, invariant,
	}, "\x00")))
	return hex.EncodeToString(h[:])[:16]
}

// allowed maps each state to the states it may move to. Anything else is
// refused: the lifecycle cannot skip evidence steps, and VERIFIED_FIXED is
// reachable only through FIX_IN_PROGRESS.
var allowed = map[State][]State{
	Candidate:     {Reproduced, Unverified, Rejected, OutOfScope, Blocked, Duplicate},
	Reproduced:    {Triaged, Flaky, Rejected, OutOfScope, Blocked, Duplicate},
	Triaged:       {ApprovedForFX, Rejected, OutOfScope, Blocked, Duplicate},
	ApprovedForFX: {FixInProgress, Blocked},
	FixInProgress: {VerifiedFixed, Blocked, Regressed},
	Regressed:     {FixInProgress},
	Blocked:       {Candidate, Reproduced, Triaged, ApprovedForFX, FixInProgress},
	Flaky:         {Reproduced, Triaged},
	Unverified:    {Candidate},
	VerifiedFixed: {},
	Duplicate:     {},
	Rejected:      {},
	OutOfScope:    {},
}

// needsEvidence lists the transitions that require runner/controller-checked
// evidence: the mover must name the evidence, and the registry records it.
// (The registry cannot run the evidence itself; it refuses moves that arrive
// without an evidence reference.)
var needsEvidence = map[State]bool{
	Reproduced: true, Triaged: true, ApprovedForFX: true,
	FixInProgress: true, VerifiedFixed: true,
}

func registryPath() string {
	return filepath.Join(contract.IndexDir(), "hunter", "findings.jsonl")
}

// validate checks a new finding has everything the schema requires. A setup
// failure is never a product defect: an attempt whose outcome is
// "setup-failure" cannot be the basis of REPRODUCED or beyond.
func validate(f *Finding) error {
	var missing []string
	for name, v := range map[string]string{
		"task": f.Task, "run": f.Run, "baseline": f.Baseline,
		"file": f.File, "expected": f.Expected, "actual": f.Actual,
		"invariant": f.Invariant, "severity": f.Severity, "confidence": f.Confidence,
	} {
		if strings.TrimSpace(v) == "" {
			missing = append(missing, name)
		}
	}
	if f.Line <= 0 {
		missing = append(missing, "line")
	}
	if filepath.Base(f.Task) != f.Task {
		return errors.New("invalid task in finding")
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("finding is missing required fields: %s", strings.Join(missing, ", "))
	}
	if f.State == "" {
		f.State = Candidate
	}
	// F-LIVE-2: a new report must start at CANDIDATE. A finding arriving
	// already closed (VERIFIED_FIXED/DUPLICATE/REJECTED/OUT_OF_SCOPE) or
	// mid-chain with gate-owned history is a closed claim that skipped every
	// gate — refuse it instead of persisting it.
	switch f.State {
	case Candidate:
	case Duplicate, Rejected, OutOfScope, VerifiedFixed:
		return fmt.Errorf("new findings must start at CANDIDATE, not %s (a closed claim cannot be reported, only reached through the gates)", f.State)
	default:
		return fmt.Errorf("new findings must start at CANDIDATE, not %s", f.State)
	}
	if len(f.Attempts) > 0 {
		return errors.New("new findings carry no attempts: reproductions are recorded, not reported")
	}
	if f.DuplicateOf != "" || f.BlockedFrom != "" || f.FixReview != nil || f.OwnerAcceptance != nil {
		return errors.New("new findings carry no gate-owned history (duplicate_of/blocked_from are set by moves, not reports)")
	}
	fingerprint := FingerprintOf(f.Baseline, f.File, f.Line, f.Symbol, f.Invariant)
	if f.Fingerprint != "" && f.Fingerprint != fingerprint {
		return errors.New("finding fingerprint does not match its identity")
	}
	f.Fingerprint = fingerprint
	if f.Strength != "" && f.Strength != StaticSupported {
		return errors.New("new findings cannot claim runtime reproduction strength")
	}
	f.Strength = StaticSupported
	return nil
}

// load reads every recorded finding. A corrupt line fails the read: findings
// are evidence, and evidence is never silently skipped.
func load() ([]Finding, error) {
	b, err := os.ReadFile(registryPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(b) > 0 && b[len(b)-1] != '\n' {
		return nil, errors.New("finding registry has an incomplete final record")
	}
	var out []Finding
	seen := map[string]bool{}
	for i, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var f Finding
		if err := json.Unmarshal([]byte(line), &f); err != nil {
			return nil, fmt.Errorf("finding line %d is corrupt: %w", i+1, err)
		}
		if f.FindingIdx <= 0 || f.ID != fmt.Sprintf("H-%04d", f.FindingIdx) || seen[f.ID] || f.Fingerprint != FingerprintOf(f.Baseline, f.File, f.Line, f.Symbol, f.Invariant) {
			return nil, fmt.Errorf("finding line %d has corrupt identity", i+1)
		}
		if _, ok := allowed[f.State]; !ok {
			return nil, fmt.Errorf("finding line %d has invalid state", i+1)
		}
		seen[f.ID] = true
		out = append(out, f)
	}
	return out, nil
}

// appendFinding is called with the registry lock held.
func appendFinding(f Finding) error {
	if err := os.MkdirAll(filepath.Dir(registryPath()), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	fh, err := os.OpenFile(registryPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer fh.Close()
	if _, err = fh.Write(append(b, '\n')); err != nil {
		return err
	}
	if err := fh.Sync(); err != nil {
		return err
	}
	return syncHunterDirectory(filepath.Dir(registryPath()))
}

// rewrite replaces the whole registry atomically (temp + rename), used only
// by state transitions which must preserve every other record byte-identical.
func rewrite(all []Finding) error {
	if err := os.MkdirAll(filepath.Dir(registryPath()), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(registryPath()), ".findings-*")
	if err != nil {
		return err
	}
	for _, f := range all {
		b, err := json.Marshal(f)
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			return err
		}
		if _, err := tmp.Write(append(b, '\n')); err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			return err
		}
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), registryPath()); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return syncHunterDirectory(filepath.Dir(registryPath()))
}

// Report records a new finding. When its fingerprint already exists, the new
// report is refused as a duplicate: the caller gets the original ID and must
// reference it (DuplicateOf) instead of opening a second record. The original
// evidence is never rewritten.
func Report(f Finding) (id string, duplicate bool, err error) {
	unlock, err := run.LockFile(registryPath()+".lock", 10*time.Second)
	if err != nil {
		return "", false, err
	}
	defer unlock()
	if err := validate(&f); err != nil {
		return "", false, err
	}
	all, err := load()
	if err != nil {
		return "", false, err
	}
	for _, e := range all {
		if e.Fingerprint == f.Fingerprint {
			return e.ID, true, nil
		}
	}
	f.FindingIdx = len(all) + 1
	f.ID = fmt.Sprintf("H-%04d", f.FindingIdx)
	f.At = time.Now().UTC().Format(time.RFC3339)
	if err := appendFinding(f); err != nil {
		return "", false, err
	}
	return f.ID, false, nil
}

// RecordAttempt appends one reproduction attempt to a finding. Attempts are
// append-only: there is no edit or delete API, so a failing run cannot be
// rewritten into a passing one and history cannot be trimmed to close a bug.
// outcome must be "reproduced", "not-reproduced" or "setup-failure"; anything
// else is refused. Recording on a terminal state (VERIFIED_FIXED, DUPLICATE,
// REJECTED, OUT_OF_SCOPE) is refused: closed findings are immutable.
// A recorded "reproduced" outcome promotes Strength deterministically:
// first reproduced attempt -> REPRODUCED_ONCE, second and beyond ->
// REPRODUCED_MULTI. Setup failures never promote and never satisfy any gate.
func RecordAttempt(id string, a ReproAttempt) error {
	unlock, err := run.LockFile(registryPath()+".lock", 10*time.Second)
	if err != nil {
		return err
	}
	defer unlock()
	if strings.TrimSpace(a.Command) == "" || strings.TrimSpace(a.Cwd) == "" {
		return fmt.Errorf("attempt needs a command and a cwd")
	}
	switch a.Outcome {
	case "reproduced", "not-reproduced", "setup-failure", "scope-violation":
	default:
		return fmt.Errorf("attempt outcome %q is not reproduced, not-reproduced, setup-failure or scope-violation", a.Outcome)
	}
	if strings.TrimSpace(a.At) == "" {
		a.At = time.Now().UTC().Format(time.RFC3339)
	}
	all, err := load()
	if err != nil {
		return err
	}
	at := -1
	for i, f := range all {
		if f.ID == id {
			at = i
			break
		}
	}
	if at < 0 {
		return fmt.Errorf("unknown finding %s", id)
	}
	switch all[at].State {
	case VerifiedFixed, Duplicate, Rejected, OutOfScope:
		return fmt.Errorf("finding %s is closed (%s): attempts are immutable", id, all[at].State)
	}
	if a.Attempt != "" {
		for _, old := range all[at].Attempts {
			if old.Attempt == a.Attempt {
				return fmt.Errorf("attempt %s already recorded", a.Attempt)
			}
		}
	}
	all[at].Attempts = append(all[at].Attempts, a)
	if a.Outcome == "reproduced" {
		n := 0
		for _, r := range all[at].Attempts {
			if r.Outcome == "reproduced" {
				n++
			}
		}
		if n >= 2 {
			all[at].Strength = ReproducedMulti
		} else {
			all[at].Strength = ReproducedOnce
		}
	}
	return rewrite(all)
}

// reproducedCount counts attempts with outcome "reproduced". Setup failures
// and not-reproduced runs never count: they are data, not proof.
func reproducedCount(f Finding) int {
	n := 0
	for _, a := range f.Attempts {
		if a.Outcome == "reproduced" {
			n++
		}
	}
	return n
}

// Move advances one finding along the lifecycle. Evidence-gated transitions
// require a non-empty evidence reference; anything else fails closed. Moving
// to DUPLICATE requires naming the original (DuplicateOf must exist).
// REPRODUCED additionally requires at least one recorded attempt with outcome
// "reproduced" (H2): an evidence string alone is not a reproduction, and
// setup failures never count (H4). VERIFIED_FIXED requires the same red
// proof plus verification evidence (H7): a bug that was never reproduced, or
// whose failing run was deleted instead of fixed, cannot be closed.
func Move(id string, to State, evidence string) error {
	unlock, err := run.LockFile(registryPath()+".lock", 10*time.Second)
	if err != nil {
		return err
	}
	defer unlock()
	all, err := load()
	if err != nil {
		return err
	}
	at := -1
	for i, f := range all {
		if f.ID == id {
			at = i
			break
		}
	}
	if at < 0 {
		return fmt.Errorf("unknown finding %s", id)
	}
	from := all[at].State
	ok := false
	for _, s := range allowed[from] {
		if s == to {
			ok = true
			break
		}
	}
	if !ok {
		return fmt.Errorf("finding %s cannot move %s -> %s", id, from, to)
	}
	if needsEvidence[to] && strings.TrimSpace(evidence) == "" {
		return fmt.Errorf("finding %s: moving to %s needs runner-checked evidence", id, to)
	}
	if to == Reproduced && reproducedCount(all[at]) == 0 {
		return fmt.Errorf("finding %s: moving to REPRODUCED needs a recorded attempt with outcome reproduced (evidence alone is not a reproduction)", id)
	}
	// FIX_IN_PROGRESS means "a fix task is working on a reproduced bug": it
	// requires the red proof on record too, including via the Blocked detour
	// (CANDIDATE -> Blocked -> FixInProgress must not skip reproduction).
	// VerifyFix checks this again at close time; this gate stops the bypass
	// at entry instead of only at close.
	if to == FixInProgress && reproducedCount(all[at]) == 0 {
		return fmt.Errorf("finding %s: moving to FIX_IN_PROGRESS needs the red proof on record (a recorded attempt with outcome reproduced; fixing an unreproduced bug)", id)
	}
	if to == VerifiedFixed && reproducedCount(all[at]) == 0 {
		return fmt.Errorf("finding %s: VERIFIED_FIXED needs the red proof first (a bug that was never reproduced cannot be closed)", id)
	}
	if to == Duplicate {
		// F-LIVE-3: the original must be a DIFFERENT finding. The old
		// condition (f.ID == evidence || fingerprint-match && f.ID != id)
		// matched the mover itself on the first clause, closing a finding
		// as its own duplicate with no separate original.
		if evidence == id {
			return fmt.Errorf("finding %s: a finding cannot be a duplicate of itself", id)
		}
		found := false
		for _, f := range all {
			if f.ID == id {
				continue
			}
			if f.ID == evidence || f.Fingerprint == all[at].Fingerprint {
				found = true
				all[at].DuplicateOf = f.ID
				break
			}
		}
		if !found {
			return fmt.Errorf("finding %s: DUPLICATE needs the original finding id", id)
		}
	} else if strings.TrimSpace(evidence) != "" {
		all[at].Evidence = append(all[at].Evidence, evidence)
	}
	// F-LIVE-1: only VerifyFix closes a finding. Move to VERIFIED_FIXED is
	// refused even from FIX_IN_PROGRESS: the direct close skips the fixed
	// revision, the HEAD match and the green-evidence structure that
	// VerifyFix checks.
	if to == VerifiedFixed {
		return fmt.Errorf("finding %s: close only through VerifyFix (Move cannot reach VERIFIED_FIXED: no fixed revision, no HEAD check, no green-evidence structure)", id)
	}
	// F-LIVE-4: BLOCKED records where it came from, and resume returns
	// there — never forward. The old table let Candidate -> Blocked ->
	// FixInProgress skip triage AND approval, so VerifyFix later assumed an
	// approval that never happened.
	if to == Blocked {
		all[at].BlockedFrom = from
	}
	if from == Blocked && all[at].BlockedFrom != "" && to != all[at].BlockedFrom {
		return fmt.Errorf("finding %s: BLOCKED resumes to %s, not forward to %s (triage and approval cannot be skipped via Blocked)", id, all[at].BlockedFrom, to)
	}
	all[at].State = to
	return rewrite(all)
}

// VerifyFix retains validation for legacy callers but refuses text-only closure.
// Runtime observations, independent review and owner approval are separate gates.
func VerifyFix(id, fixedRevision, head, greenEvidence string) error {
	unlock, err := run.LockFile(registryPath()+".lock", 10*time.Second)
	if err != nil {
		return err
	}
	defer unlock()
	if strings.TrimSpace(fixedRevision) == "" {
		return fmt.Errorf("finding %s: VERIFIED_FIXED needs the fixed revision", id)
	}
	if strings.TrimSpace(greenEvidence) == "" {
		return fmt.Errorf("finding %s: VERIFIED_FIXED needs runner-checked green evidence", id)
	}
	all, err := load()
	if err != nil {
		return err
	}
	at := -1
	for i, f := range all {
		if f.ID == id {
			at = i
			break
		}
	}
	if at < 0 {
		return fmt.Errorf("unknown finding %s", id)
	}
	if all[at].State != FixInProgress {
		return fmt.Errorf("finding %s cannot close from %s: VERIFIED_FIXED is reachable only through FIX_IN_PROGRESS", id, all[at].State)
	}
	if reproducedCount(all[at]) == 0 {
		return fmt.Errorf("finding %s: VERIFIED_FIXED needs the red proof first (a bug that was never reproduced cannot be closed)", id)
	}
	// Green-evidence structure (not a substring match): the evidence must
	// carry the full red-before/green-after chain, bound to THIS finding —
	// fixed=<fixedRevision>, FAIL@<this finding's baseline>, PASS@<fixedRevision>,
	// and existing-pass (the suite still passes). A bare revision, a FAIL on
	// some other baseline, a PASS at some other revision, or a missing
	// regression result is a claim, not proof.
	for _, want := range []string{
		"fixed=" + fixedRevision,
		"FAIL@" + all[at].Baseline,
		"PASS@" + fixedRevision,
		"existing-pass",
	} {
		if !strings.Contains(greenEvidence, want) {
			return fmt.Errorf("finding %s: green evidence lacks %q — need fixed=%s with FAIL@%s (this finding's baseline), PASS@%s and existing-pass, not a bare revision", id, want, fixedRevision, all[at].Baseline, fixedRevision)
		}
	}
	if head == "" || head != fixedRevision {
		return fmt.Errorf("finding %s: checkout HEAD %q does not match the fixed revision %q — re-verify at the current HEAD before closing", id, head, fixedRevision)
	}
	return errors.New("text-only fix claims cannot close findings; run hunter verify and obtain independent review and owner approval")
}

// RequireFresh refuses to treat a finding's evidence as current when the
// checkout HEAD no longer matches the finding's baseline revision (H8).
// Evidence recorded at one revision is not evidence about another: after a
// HEAD move the runner must re-verify (new attempt at the new HEAD) instead
// of closing on stale proof. The registry itself never execs git — the caller
// supplies head — so this stays a pure check: same revision means fresh,
// anything else (including empty) means stale.
func RequireFresh(id, head string) error {
	f, err := Get(id)
	if err != nil {
		return err
	}
	if head != "" && f.Baseline == head {
		return nil
	}
	return fmt.Errorf("finding %s is stale: evidence is at baseline %s, checkout HEAD is %s — re-verify at the new HEAD before closing", id, f.Baseline, head)
}

// E2ERequest is what an E2E investigation must present before it may run
// (H10): an explicit URL, allowed hosts/actions, the environment, authorized
// account references, and teardown. Anything missing blocks — the hunter
// never guesses a production URL, and a persona is not an authorization.
type E2ERequest struct {
	URL          string
	AllowedHosts []string
	Actions      []string
	Env          string
	AccountRef   string
	Teardown     string
}

// AuthorizeE2E checks an E2E request is complete. It checks presence, not
// the live environment: running a browser is the runner's job, and this gate
// only decides whether the request may reach one.
func AuthorizeE2E(r E2ERequest) error {
	var missing []string
	if strings.TrimSpace(r.URL) == "" {
		missing = append(missing, "explicit URL")
	}
	if len(r.AllowedHosts) == 0 {
		missing = append(missing, "allowed hosts")
	}
	if len(r.Actions) == 0 {
		missing = append(missing, "allowed actions")
	}
	if strings.TrimSpace(r.Env) == "" {
		missing = append(missing, "environment")
	}
	if strings.TrimSpace(r.AccountRef) == "" {
		missing = append(missing, "authorized account reference")
	}
	if strings.TrimSpace(r.Teardown) == "" {
		missing = append(missing, "teardown")
	}
	if len(missing) > 0 {
		return fmt.Errorf("E2E blocked: missing %s — no URL/account authorization means no run", strings.Join(missing, ", "))
	}
	return nil
}

// Get returns one finding by ID.
func Get(id string) (Finding, error) {
	unlock, err := run.LockFile(registryPath()+".lock", 10*time.Second)
	if err != nil {
		return Finding{}, err
	}
	defer unlock()
	all, err := load()
	if err != nil {
		return Finding{}, err
	}
	for _, f := range all {
		if f.ID == id {
			return f, nil
		}
	}
	return Finding{}, fmt.Errorf("unknown finding %s", id)
}

// List returns every finding, oldest first.
func List() ([]Finding, error) {
	unlock, err := run.LockFile(registryPath()+".lock", 10*time.Second)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return load()
}
