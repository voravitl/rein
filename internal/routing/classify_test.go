package routing

import (
	"strings"
	"testing"

	"github.com/voravitl/rein/internal/contract"
)

func classContract() *contract.Contract {
	return &contract.Contract{Name: "task", Worktree: "/wt/task", Allow: []string{"src/**", "docs/**", "web/**"}, Deny: []string{"src/vendor/**"},
		Scope: []string{"S1"}, ReportPath: "/r.md", MaxChangedLines: 200,
		Profile: contract.Profile{SensitivePaths: []string{"src/auth/**", "migrations/prod/**"}}}
}

func profileFor(c *contract.Contract, mutate func(*TaskProfile)) *TaskProfile {
	tp := &TaskProfile{ContractHash: contractHash(c), Phase: "worker", Kind: "backend", RequiredCapabilities: []string{"tests"}, ExpectedContext: 20000,
		PlannedFiles: []string{"src/api/users.go"}, Rationale: []Rationale{{Path: "src/api/users.go", Reason: "adds the endpoint"}}}
	if mutate != nil {
		mutate(tp)
	}
	return tp
}

// classContractNarrow has allow globs that stay clear of the sensitive paths, so the planned files alone decide the tier.
func classContractNarrow() *contract.Contract {
	c := classContract()
	c.Allow = []string{"src/api/**", "src/ui/**", "docs/**", "web/**"}
	return c
}

func TestDecodeRefusesTheCoordinatorPickingAModel(t *testing.T) {
	for _, field := range []string{"model", "provider", "agent", "harness", "chain", "effort", "launch", "preferred_model", "fallback", "pool", "maker", "worker_model"} {
		_, err := DecodeTaskProfile([]byte(`{"phase":"worker","` + field + `":"x"}`))
		if ErrCode(err) != CodeProfileInvalid || !strings.Contains(err.Error(), field) {
			t.Errorf("%s: %v", field, err)
		}
	}
	for name, body := range map[string]string{"array": `[]`, "text": `nope`, "unknown field": `{"phase":"worker","surprise":1}`, "trailing": `{"phase":"worker"} {"x":1}`} {
		if _, err := DecodeTaskProfile([]byte(body)); ErrCode(err) != CodeProfileInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
	tp, err := DecodeTaskProfile([]byte(`{"phase":"worker","kind":"backend","risk_flags":["authorization"],"required_capabilities":["repository-edit","tests"],
		"rationale":[{"path":"src/auth.go","reason":"Changes authorization decisions"}]}`))
	if err != nil || tp.Kind != "backend" || tp.RiskFlags[0] != "authorization" {
		t.Errorf("the documented example must decode: %+v %v", tp, err)
	}
}

func TestClassificationMapsKindToCandidatePolicy(t *testing.T) {
	c := classContractNarrow()
	for name, tc := range map[string]struct {
		mutate func(*TaskProfile)
		policy string
	}{
		"backend":   {nil, "backend"},
		"frontend":  {func(p *TaskProfile) { p.Kind = "frontend" }, "frontend"},
		"fullstack": {func(p *TaskProfile) { p.Kind = "fullstack" }, "ordinary"},
		"docs": {func(p *TaskProfile) {
			p.Kind, p.PlannedFiles, p.Rationale = "docs", []string{"docs/guide.md"}, []Rationale{{Path: "docs/guide.md", Reason: "typo"}}
		}, "docs"},
		"mechanical": {func(p *TaskProfile) {
			p.Kind = "mechanical"
			p.Bounded = &Bounded{Operation: "rename Foo to Bar", AcceptanceChecks: []string{"go build ./...", "grep -r Foo src returns nothing"}}
		}, "mechanical"},
	} {
		t.Run(name, func(t *testing.T) {
			cl, err := Classify(c, nil, profileFor(c, tc.mutate))
			if err != nil || cl.Policy != tc.policy || cl.Tier != "T1" || cl.RequiredMakers() != 1 || cl.ProfileHash == "" || cl.Phase != "worker" {
				t.Fatalf("%+v %v", cl, err)
			}
		})
	}
}

func TestT3ScopeNeverBecomesCheap(t *testing.T) {
	c := classContract() // allow src/** reaches src/auth/** (a sensitive path)
	for _, kind := range []string{"backend", "frontend", "fullstack", "docs"} {
		cl, err := Classify(c, nil, profileFor(c, func(p *TaskProfile) {
			p.Kind = kind
			if kind == "docs" {
				p.PlannedFiles, p.Rationale = []string{"docs/a.md"}, []Rationale{{Path: "docs/a.md", Reason: "x"}}
			}
		}))
		if err != nil || cl.Tier != "T3" || cl.Policy != "high-risk" || cl.RequiredMakers() != 2 {
			t.Errorf("%s on a sensitive scope must be high-risk: %+v %v", kind, cl, err)
		}
	}
	_, err := Classify(c, nil, profileFor(c, func(p *TaskProfile) {
		p.Kind = "mechanical"
		p.Bounded = &Bounded{Operation: "rename", AcceptanceChecks: []string{"go build"}}
	}))
	if ErrCode(err) != CodeClassification || !strings.Contains(err.Error(), "T3") {
		t.Errorf("a mechanical claim on T3 scope is a conflict: %v", err)
	}

	narrow := classContractNarrow()
	// planned files that reach a sensitive path escalate even when the allow globs looked harmless
	narrow.Allow = []string{"src/auth/session.go", "docs/**"}
	cl, err := Classify(narrow, nil, profileFor(narrow, func(p *TaskProfile) {
		p.PlannedFiles, p.Rationale = []string{"src/auth/session.go"}, []Rationale{{Path: "src/auth/session.go", Reason: "x"}}
	}))
	if err != nil || cl.Policy != "high-risk" {
		t.Errorf("%+v %v", cl, err)
	}
	// the owner's profile can add sensitive paths the contract copy does not know
	narrow2 := classContractNarrow()
	owner := &contract.Profile{SensitivePaths: []string{"src/api/**"}}
	if cl, err = Classify(narrow2, owner, profileFor(narrow2, nil)); err != nil || cl.Tier != "T3" {
		t.Errorf("owner sensitive paths: %+v %v", cl, err)
	}
}

func TestClaimedRiskOnlyRaises(t *testing.T) {
	c := classContractNarrow()
	for _, flag := range tierThreeFlags {
		cl, err := Classify(c, nil, profileFor(c, func(p *TaskProfile) { p.RiskFlags = []string{flag} }))
		if err != nil || cl.Tier != "T3" || cl.Policy != "high-risk" {
			t.Errorf("%s: %+v %v", flag, cl, err)
		}
	}
	// a neutral claim cannot lower what the paths prove
	c2 := classContract()
	cl, err := Classify(c2, nil, profileFor(c2, func(p *TaskProfile) { p.RiskFlags = []string{"performance"} }))
	if err != nil || cl.Tier != "T3" {
		t.Errorf("%+v %v", cl, err)
	}
	// unknown risk is not assumed harmless, whatever the kind: the coordinator restates it in the known vocabulary. Words that
	// merely sound like the T3 flags ("auth", "payment-processing", "credentials") are exactly what must not pass as neutral.
	for _, kind := range []string{"backend", "frontend", "fullstack", "mechanical", "docs"} {
		for _, flag := range []string{"mystery", "auth", "payment-processing", "credentials"} {
			_, err = Classify(c, nil, profileFor(c, func(p *TaskProfile) {
				p.Kind, p.RiskFlags = kind, []string{flag}
				p.Bounded = &Bounded{Operation: "x", AcceptanceChecks: []string{"y"}}
				if kind == "docs" {
					p.PlannedFiles, p.Rationale = []string{"docs/a.md"}, []Rationale{{Path: "docs/a.md", Reason: "x"}}
				}
			}))
			if ErrCode(err) != CodeClassification || !strings.Contains(err.Error(), flag) {
				t.Errorf("unknown risk flag %q on a %s task must be classification_required: %v", flag, kind, err)
			}
		}
	}
}

func TestAmbiguousOrConflictingClassificationFailsClosed(t *testing.T) {
	c := classContractNarrow()
	cases := map[string]func(*TaskProfile){
		"no contract hash":    func(p *TaskProfile) { p.ContractHash = "" },
		"stale contract hash": func(p *TaskProfile) { p.ContractHash = "deadbeef" },
		"no phase":            func(p *TaskProfile) { p.Phase = "" },
		"bad phase":           func(p *TaskProfile) { p.Phase = "deploy" },
		"no kind":             func(p *TaskProfile) { p.Kind = "" },
		"review is a phase":   func(p *TaskProfile) { p.Kind = "review" },
		"unknown kind":        func(p *TaskProfile) { p.Kind = "miscellaneous" },
		"no capabilities":     func(p *TaskProfile) { p.RequiredCapabilities = []string{" "} },
		"no context":          func(p *TaskProfile) { p.ExpectedContext = 0 },
		"no planned files":    func(p *TaskProfile) { p.PlannedFiles = nil },
		"no rationale":        func(p *TaskProfile) { p.Rationale = nil },
		"absolute path":       func(p *TaskProfile) { p.PlannedFiles = []string{"/etc/passwd"} },
		"parent path":         func(p *TaskProfile) { p.PlannedFiles = []string{"src/../../x"} },
		"outside allow":       func(p *TaskProfile) { p.PlannedFiles = []string{"cmd/main.go"} },
		"denied":              func(p *TaskProfile) { p.PlannedFiles = []string{"src/vendor/x.go"} },
		"rationale elsewhere": func(p *TaskProfile) { p.Rationale = []Rationale{{Path: "src/other.go", Reason: "why"}} },
		"rationale no reason": func(p *TaskProfile) { p.Rationale = []Rationale{{Path: "src/api/users.go", Reason: " "}} },
		"docs touching code": func(p *TaskProfile) {
			p.Kind, p.PlannedFiles, p.Rationale = "docs", []string{"docs/a.md", "src/api/users.go"}, []Rationale{{Path: "docs/a.md", Reason: "x"}}
		},
		"mechanical without bounds": func(p *TaskProfile) { p.Kind = "mechanical" },
		"mechanical without checks": func(p *TaskProfile) {
			p.Kind, p.Bounded = "mechanical", &Bounded{Operation: "rename"}
		},
		"review without diff": func(p *TaskProfile) { p.Phase = "review" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cl, err := Classify(c, nil, profileFor(c, mutate))
			if ErrCode(err) != CodeClassification || cl != nil {
				t.Fatalf("must refuse with classification_required: %+v %v", cl, err)
			}
		})
	}
	unbounded := classContractNarrow()
	unbounded.MaxChangedLines = 0
	if _, err := Classify(unbounded, nil, profileFor(unbounded, func(p *TaskProfile) {
		p.Kind, p.Bounded = "mechanical", &Bounded{Operation: "rename", AcceptanceChecks: []string{"go build"}}
	})); ErrCode(err) != CodeClassification {
		t.Errorf("a mechanical task needs a size bound on the contract: %v", err)
	}
}

func TestReviewPhaseUsesActualChangesAndNormalisesTheProfile(t *testing.T) {
	c := classContractNarrow()
	cl, err := Classify(c, nil, profileFor(c, func(p *TaskProfile) {
		p.Phase, p.ChangedFiles = "review", []string{"src/api/users.go"}
	}))
	if err != nil || cl.Phase != "review" || cl.Tier != "T1" || cl.Policy != "backend" {
		t.Fatalf("a backend review keeps kind backend: %+v %v", cl, err)
	}
	cl, err = Classify(c, nil, profileFor(c, func(p *TaskProfile) {
		p.Phase, p.ChangedFiles = "review", []string{"src/api/users.go", "src/auth/token.go"}
	}))
	if err != nil || cl.Tier != "T3" || cl.Policy != "high-risk" {
		t.Errorf("actual changes can escalate the tier: %+v %v", cl, err)
	}
	cl, err = Classify(c, nil, profileFor(c, func(p *TaskProfile) {
		p.RequiredCapabilities = []string{" Tests ", "tests", "BROWSER"}
		p.PlannedFiles = []string{"./src/api/./users.go", "src/api/users.go"}
	}))
	if err != nil || strings.Join(cl.Capabilities, ",") != "browser,repository-edit,tests" || len(cl.PlannedFiles) != 1 {
		t.Errorf("capabilities and paths are normalised, a worker always needs repository-edit: %+v %v", cl, err)
	}
	a, _ := Classify(c, nil, profileFor(c, nil))
	b, _ := Classify(c, nil, profileFor(c, nil))
	d, _ := Classify(c, nil, profileFor(c, func(p *TaskProfile) { p.ExpectedContext++ }))
	if a.ProfileHash != b.ProfileHash || a.ProfileHash == d.ProfileHash {
		t.Error("the profile hash must be stable and bind the content")
	}
}

// Without sensitive path rules rein cannot tell auth/payment code from ordinary code, so it must not pick a model for ANY kind of
// task (a mechanical claim on credential code would otherwise reach the cheapest candidates with one-maker review).
func TestNoSensitivePathRulesMeansNoAutomaticChoice(t *testing.T) {
	c := classContractNarrow()
	c.Profile.SensitivePaths = nil
	c.Allow = []string{"src/**"}
	for _, kind := range []string{"backend", "frontend", "fullstack", "docs", "mechanical"} {
		_, err := Classify(c, nil, profileFor(c, func(p *TaskProfile) {
			p.Kind = kind
			p.PlannedFiles = []string{"src/auth/login.go", "src/payments/charge.go"}
			p.Rationale = []Rationale{{Path: "src/auth/login.go", Reason: "x"}, {Path: "src/payments/charge.go", Reason: "y"}}
			p.Bounded = &Bounded{Operation: "rewrite", AcceptanceChecks: []string{"go test"}}
		}))
		if ErrCode(err) != CodeClassification || !strings.Contains(err.Error(), "sensitive_paths") {
			t.Errorf("%s with no sensitive path rules must be classification_required: %v", kind, err)
		}
	}
	// the owner's profile supplies them just as well as the contract's copy
	owner := &contract.Profile{SensitivePaths: []string{"src/auth/**"}}
	if _, err := Classify(c, owner, profileFor(c, func(p *TaskProfile) { p.PlannedFiles = []string{"src/api/users.go"} })); err != nil {
		t.Errorf("owner-declared sensitive paths are rules: %v", err)
	}
}

// A file is documentation by what it is: a directory called docs also holds scripts and configuration, and a .txt can be a
// dependency list or a build file.
func TestDocsMeansDocumentationFiles(t *testing.T) {
	c := classContractNarrow()
	for _, notDoc := range []string{"requirements.txt", "CMakeLists.txt", "docs/deploy.sh", "docs/conf.py", "docs/Makefile", "docs/requirements.txt"} {
		c.Allow = append(c.Allow, notDoc)
		_, err := Classify(c, nil, profileFor(c, func(p *TaskProfile) {
			p.Kind, p.PlannedFiles, p.Rationale = "docs", []string{notDoc}, []Rationale{{Path: notDoc, Reason: "x"}}
		}))
		if ErrCode(err) != CodeClassification {
			t.Errorf("%s must not pass as documentation: %v", notDoc, err)
		}
	}
	for _, doc := range []string{"docs/guide.md", "README.MD", "docs/api.rst", "docs/a.adoc", "docs/x.mdx"} {
		if !docLike(doc) {
			t.Errorf("%s is documentation", doc)
		}
	}
}
