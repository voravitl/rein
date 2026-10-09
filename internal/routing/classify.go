package routing

// Deterministic task classification for automatic selection (docs/ROUTING_SELECTION_DESIGN.md, "Coordinator input and
// deterministic classification"). The coordinator DESCRIBES the task; rein validates the description against the contract,
// the planned paths and the project's sensitive rules and derives the candidate policy. The coordinator can never name a
// model, provider, harness or chain, and a claim can raise the risk but never lower it.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/glob"
	"github.com/voravitl/rein/internal/ledger"
	"github.com/voravitl/rein/internal/tier"
)

// Machine-readable reasons automatic selection stops. Each is a refusal to guess.
const (
	CodeClassification  = "classification_required" // missing, ambiguous or conflicting task classification
	CodeProfileInvalid  = "profile_invalid"         // the coordinator input is not a task description
	CodeQualityPolicy   = "quality_policy_required" // a required quality floor is not configured
	CodeSelectionPolicy = "selection_policy_required"
	CodeCostBasis       = "cost_basis_required"
	CodeProbeBound      = "probe_bound_required"
	CodeNoCandidate     = "no_eligible_candidate"
	CodeWorkerIdentity  = "worker_identity_required"
)

// Error is a refusal with a stable code.
type Error struct{ Code, Detail string }

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

func refuse(code, format string, a ...any) error { return &Error{code, fmt.Sprintf(format, a...)} }

// ErrCode extracts the code of a refusal ("" for any other error).
func ErrCode(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// TaskProfile is the coordinator's structured description of one task.
type TaskProfile struct {
	ContractHash         string      `json:"contract_hash"`         // binds the profile to the exact contract it analysed
	SpecHash             string      `json:"spec_hash,omitempty"`   // recorded verbatim for the audit trail
	Phase                string      `json:"phase"`                 // worker | review
	Kind                 string      `json:"kind"`                  // backend | frontend | fullstack | docs | mechanical
	RiskFlags            []string    `json:"risk_flags,omitempty"`  // claimed risk; it can only raise the tier
	RequiredCapabilities []string    `json:"required_capabilities"` // e.g. repository-edit, tests, browser, input:image
	ExpectedContext      int         `json:"expected_context_tokens"`
	PlannedFiles         []string    `json:"planned_files"`            // repo-relative paths the task expects to change
	ChangedFiles         []string    `json:"changed_files,omitempty"`  // review phase: the actual diff
	ExcludeMakers        []string    `json:"exclude_makers,omitempty"` // review phase: makers whose verdict for this revision already exists; narrows only
	Rationale            []Rationale `json:"rationale"`
	Bounded              *Bounded    `json:"bounded_operation,omitempty"` // mechanical only
}

// Rationale explains why a planned path is in the task (shown in the decision report).
type Rationale struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Bounded describes the finite operation a mechanical task performs and the objective checks that prove it.
type Bounded struct {
	Operation        string   `json:"operation"`
	AcceptanceChecks []string `json:"acceptance_checks"`
}

// Class is the validated classification.
type Class struct {
	Phase         string   `json:"phase"`
	Kind          string   `json:"kind"`
	Tier          string   `json:"tier"`   // T1 | T3 (a claim can only raise it)
	Policy        string   `json:"policy"` // worker candidate policy: high-risk|backend|frontend|docs|mechanical|ordinary
	Capabilities  []string `json:"capabilities"`
	Context       int      `json:"context_tokens"`
	PlannedFiles  []string `json:"planned_files"`
	ExcludeMakers []string `json:"exclude_makers,omitempty"`
	Reasons       []string `json:"reasons"`
	ContractHash  string   `json:"contract_hash"`
	SpecHash      string   `json:"spec_hash,omitempty"`
	ProfileHash   string   `json:"profile_hash"`
}

// Risk vocabulary. A flag in tierThreeFlags forces T3 whatever the paths say. An unknown flag is not assumed harmless: the
// coordinator must restate the risk in this vocabulary, whatever the kind of task.
var (
	tierThreeFlags = []string{"authentication", "authorization", "crypto", "payments", "pii", "production-data", "secrets", "security"}
	neutralFlags   = []string{"api", "concurrency", "config", "dependency", "migration", "performance", "schema", "ui"}
	docSuffixes    = []string{".md", ".mdx", ".rst", ".adoc"}
	// fields that would let the coordinator pick the model: refused by name
	forbiddenKeys = []string{"model", "models", "provider", "providers", "agent", "harness", "chain", "effort", "launch", "preferred_model",
		"preferred_provider", "fallback", "candidates", "pool", "maker", "reviewer", "worker_model"}
	// kind -> worker candidate policy (the worker_chains key that supplies the candidate SET; its order is ignored)
	policyOfKind = map[string]string{"backend": "backend", "frontend": "frontend", "docs": "docs", "mechanical": "mechanical", "fullstack": "ordinary"}
)

// DecodeTaskProfile parses coordinator input strictly: one JSON object, no unknown fields, and nothing that names a model,
// provider, harness, chain or effort.
func DecodeTaskProfile(b []byte) (*TaskProfile, error) {
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(b, &generic); err != nil {
		return nil, refuse(CodeProfileInvalid, "task profile must be one JSON object")
	}
	for _, k := range forbiddenKeys {
		if _, ok := generic[k]; ok {
			return nil, refuse(CodeProfileInvalid, "field %q is not allowed: the coordinator describes the task, rein selects the model, provider, harness and effort", k)
		}
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var tp TaskProfile
	if err := dec.Decode(&tp); err != nil {
		return nil, refuse(CodeProfileInvalid, "%v", err)
	}
	if dec.More() {
		return nil, refuse(CodeProfileInvalid, "trailing data after the task profile")
	}
	return &tp, nil
}

func norm(list []string) []string {
	out := make([]string, 0, len(list))
	for _, s := range list {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return slices.Compact(out)
}

func cleanPath(p string) (string, bool) {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	if p == "" || strings.ContainsRune(p, 0) || strings.HasPrefix(p, "/") {
		return "", false
	}
	c := path.Clean(p)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return "", false
	}
	return c, true
}

// Classify validates tp against the contract and the project's sensitive rules. owner is the policy owner's profile (may be
// nil); sensitive paths of the contract's own profile and the owner's are both honoured. Anything missing, ambiguous or
// conflicting is refused with classification_required so the coordinator must refine it; nothing is guessed.
func Classify(c *contract.Contract, owner *contract.Profile, tp *TaskProfile) (*Class, error) {
	var missing []string
	need := func(ok bool, what string) {
		if !ok {
			missing = append(missing, what)
		}
	}
	need(tp.ContractHash != "", "contract_hash")
	phase, kind := strings.ToLower(strings.TrimSpace(tp.Phase)), strings.ToLower(strings.TrimSpace(tp.Kind))
	need(phase == "worker" || phase == "review", "phase (worker|review)")
	need(validKind(kind), "kind (backend|frontend|fullstack|docs|mechanical)")
	caps := norm(tp.RequiredCapabilities)
	need(len(caps) > 0, "required_capabilities")
	need(tp.ExpectedContext > 0, "expected_context_tokens")
	need(len(tp.Rationale) > 0, "rationale")
	var planned []string
	for _, p := range tp.PlannedFiles {
		cp, ok := cleanPath(p)
		if !ok {
			return nil, refuse(CodeClassification, "planned file %q is not a clean repo-relative path", p)
		}
		planned = append(planned, cp)
	}
	sort.Strings(planned)
	planned = slices.Compact(planned)
	need(len(planned) > 0, "planned_files")
	if len(missing) > 0 {
		return nil, refuse(CodeClassification, "missing or invalid: %s", strings.Join(missing, ", "))
	}
	exclude := norm(tp.ExcludeMakers)
	if len(exclude) > 0 && phase != "review" {
		return nil, refuse(CodeClassification, "exclude_makers applies to a review only")
	}
	if want := contractHash(c); tp.ContractHash != want {
		return nil, refuse(CodeClassification, "contract_hash does not match the current contract (it changed after the profile was written; `rein contract hash %s`)", c.Name)
	}

	// every planned file must be work the contract allows, and every rationale must explain a planned file
	for _, p := range planned {
		if !glob.Match(p, c.Allow) || glob.Match(p, c.Deny) {
			return nil, refuse(CodeClassification, "planned file %q is outside the contract's allow/deny globs", p)
		}
	}
	for _, r := range tp.Rationale {
		cp, ok := cleanPath(r.Path)
		if !ok || strings.TrimSpace(r.Reason) == "" || !slices.Contains(planned, cp) {
			return nil, refuse(CodeClassification, "rationale entry %q needs a reason and must name a planned file", r.Path)
		}
	}

	// risk: the highest of what the paths prove and what the coordinator claims
	sensitive := slices.Concat(c.Profile.SensitivePaths, ownerSensitive(owner))
	if len(sensitive) == 0 {
		return nil, refuse(CodeClassification, "no sensitive_paths are configured in the owner's profile or the contract's copy: without them rein cannot tell a sensitive path from an ordinary one, so it will not choose a model automatically (declare them: examples/profile.example.json)")
	}
	t := tier.EvaluateFromAllowGlobs(c.Allow, sensitive)
	t = tier.EvaluateFromChangedFiles(planned, sensitive, t)
	var reasons []string
	if t == tier.T3 {
		reasons = append(reasons, "contract allow globs or planned files reach project-sensitive paths")
	}
	var unknownFlags []string
	for _, f := range norm(tp.RiskFlags) {
		switch {
		case slices.Contains(tierThreeFlags, f):
			t = tier.T3
			reasons = append(reasons, "risk flag "+f+" forces T3")
		case !slices.Contains(neutralFlags, f):
			unknownFlags = append(unknownFlags, f)
		}
	}
	if len(unknownFlags) > 0 {
		return nil, refuse(CodeClassification, "unknown risk flag(s) %s are not assumed harmless: restate the risk with one of %s (forces T3) or %s",
			strings.Join(unknownFlags, ", "), strings.Join(tierThreeFlags, ", "), strings.Join(neutralFlags, ", "))
	}
	if phase == "review" {
		changed := make([]string, 0, len(tp.ChangedFiles))
		for _, p := range tp.ChangedFiles {
			cp, ok := cleanPath(p)
			if !ok {
				return nil, refuse(CodeClassification, "changed file %q is not a clean repo-relative path", p)
			}
			changed = append(changed, cp)
		}
		if len(changed) == 0 {
			return nil, refuse(CodeClassification, "a review needs changed_files (the actual diff): actual changes can escalate the tier")
		}
		if after := tier.EvaluateFromChangedFiles(changed, sensitive, t); after > t {
			t = after
			reasons = append(reasons, "the actual changed files reach project-sensitive paths")
		}
	}

	switch kind {
	case "docs":
		for _, p := range planned {
			if !docLike(p) {
				return nil, refuse(CodeClassification, "kind docs conflicts with planned file %q, which is not documentation", p)
			}
		}
	case "mechanical":
		switch {
		case t == tier.T3:
			return nil, refuse(CodeClassification, "T3 scope can never be mechanical (%s)", strings.Join(reasons, "; "))
		case tp.Bounded == nil || strings.TrimSpace(tp.Bounded.Operation) == "" || len(norm(tp.Bounded.AcceptanceChecks)) == 0:
			return nil, refuse(CodeClassification, "mechanical needs bounded_operation with an operation and objective acceptance_checks")
		case c.MaxChangedLines <= 0:
			return nil, refuse(CodeClassification, "mechanical needs a bounded change size: set max_changed_lines on the contract")
		}
	}

	policy := policyOfKind[kind]
	if t == tier.T3 {
		policy = "high-risk"
	}
	reasons = append(reasons, fmt.Sprintf("kind %s, tier %s -> candidate policy %s", kind, t, policy))
	cl := &Class{Phase: phase, Kind: kind, Tier: t.String(), Policy: policy, Capabilities: caps, Context: tp.ExpectedContext, PlannedFiles: planned,
		ExcludeMakers: exclude, Reasons: reasons, ContractHash: tp.ContractHash, SpecHash: tp.SpecHash}
	if phase == "worker" && !slices.Contains(cl.Capabilities, "repository-edit") {
		cl.Capabilities = append(cl.Capabilities, "repository-edit") // a worker that cannot edit the repository is never valid
		sort.Strings(cl.Capabilities)
	}
	b, _ := json.Marshal(struct {
		C Class
		T TaskProfile
	}{*cl, *tp})
	h := sha256.Sum256(b)
	cl.ProfileHash = hex.EncodeToString(h[:])
	return cl, nil
}

func ownerSensitive(p *contract.Profile) []string {
	if p == nil {
		return nil
	}
	return p.SensitivePaths
}

// docLike: a documentation file by what it is, not by where it sits. A directory called docs holds scripts and configuration too,
// and a .txt file can be a dependency list or a build file.
func docLike(p string) bool {
	l := strings.ToLower(p)
	return slices.ContainsFunc(docSuffixes, func(s string) bool { return strings.HasSuffix(l, s) })
}

// TierOf converts a class tier back to the engine's type.
func (c *Class) TierOf() tier.Tier {
	if c.Tier == "T3" {
		return tier.T3
	}
	return tier.T1
}

// RequiredMakers is how many distinct reviewer makers the tier needs (verdict check: two for T3, one otherwise).
func (c *Class) RequiredMakers() int {
	if c.TierOf() == tier.T3 {
		return 2
	}
	return 1
}

// validKind reuses the ledger task types. "review" is a phase, not a kind: reviewing backend work keeps kind backend.
func validKind(k string) bool { return k != "review" && slices.Contains(ledger.Types, k) }
