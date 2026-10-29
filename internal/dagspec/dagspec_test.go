package dagspec

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

const testBase = "1a3d605635ba995c169e81a9f8079fcb34364474"
const testImage = "sha256:632dff7351799a5a9954a48dc06670ade72ee44e8c351cdc0e5a57af2fea21af"

func prompt() *Prompt {
	return &Prompt{Task: "t", Deliverable: "d", Scope: "s", Verify: "v", StopWhen: "w"}
}

func worker(id string, write []string, deps ...string) Node {
	return Node{ID: id, Kind: "worker", Title: id, Write: write, DependsOn: deps, Prompt: prompt(), Verify: [][]string{{"go", "test", "./..."}}, Providers: []string{"claude"}}
}

func action(id string, deps ...string) Node {
	n := worker(id, nil, deps...)
	n.Kind = "action"
	n.Providers = nil
	n.Action = &Action{Argv: []string{"/usr/local/go/bin/go", "test", "./..."}, Image: testImage, TimeoutMS: 600000}
	return n
}

// validSpec: two parallel producers and a verification node after both.
func validSpec() Spec {
	return Spec{Schema: SchemaV1, Run: "r1", Repo: "/abs/repo", Base: testBase, Goal: "one sentence", MaxParallel: 2,
		Nodes: []Node{worker("api", []string{"internal/api/**"}), worker("cli", []string{"cmd/rein/**"}), action("check", "api", "cli")}}
}

func encode(t *testing.T, s Spec) []byte {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func codes(f []Finding) []string {
	out := []string{}
	for _, x := range f {
		out = append(out, x.Code)
	}
	return out
}

func hasCode(f []Finding, code string) bool {
	for _, x := range f {
		if x.Code == code {
			return true
		}
	}
	return false
}

func TestDAGSpecValidBaseline(t *testing.T) {
	s, res := Lint(encode(t, validSpec()))
	if s == nil || !res.Valid || len(res.Errors) != 0 || len(res.Warnings) != 0 {
		t.Fatalf("valid spec refused: %+v", res)
	}
	if len(s.Nodes) != 3 || s.Nodes[2].Action.Image != testImage {
		t.Fatalf("decode lost data: %+v", s)
	}
}

// TestDAGLintRefusals gives every refusal code a negative case (the code fires)
// and a positive case (a near-identical spec that does not trigger it).
func TestDAGLintRefusals(t *testing.T) {
	type mut func(*Spec)
	cases := []struct {
		code      string
		bad, good mut
	}{
		{CodeDuplicateID,
			func(s *Spec) { s.Nodes[1].ID = "api"; s.Nodes[2].DependsOn = []string{"api"} },
			func(s *Spec) { s.Nodes[1].ID = "api2"; s.Nodes[2].DependsOn = []string{"api", "api2"} }},
		{CodeInvalidID,
			func(s *Spec) { s.Nodes[0].ID = "Api_1"; s.Nodes[2].DependsOn = []string{"Api_1", "cli"} },
			func(s *Spec) {
				s.Nodes[0].ID = "a" + strings.Repeat("-", 62)
				s.Nodes[2].DependsOn = []string{s.Nodes[0].ID, "cli"}
			}},
		{CodeInvalidID, // 64 characters exceeds the id bound
			func(s *Spec) {
				s.Nodes[0].ID = strings.Repeat("a", 64)
				s.Nodes[2].DependsOn = []string{s.Nodes[0].ID, "cli"}
			},
			func(s *Spec) {
				s.Nodes[0].ID = strings.Repeat("a", 63)
				s.Nodes[2].DependsOn = []string{s.Nodes[0].ID, "cli"}
			}},
		{CodeUnknownDependency,
			func(s *Spec) { s.Nodes[2].DependsOn = []string{"api", "cli", "ghost"} },
			func(s *Spec) { s.Nodes[2].DependsOn = []string{"api", "cli"} }},
		{CodeSelfDependency,
			func(s *Spec) { s.Nodes[2].DependsOn = []string{"api", "cli", "check"} },
			func(s *Spec) { s.Nodes[2].DependsOn = []string{"api", "cli"} }},
		{CodeCycle,
			func(s *Spec) { s.Nodes[0].DependsOn = []string{"check"} },
			func(s *Spec) { s.Nodes[1].DependsOn = []string{"api"} }},
		{CodeTooManyNodes,
			func(s *Spec) { manyNodes(s, MaxNodes+1) },
			func(s *Spec) { manyNodes(s, MaxNodes) }},
		{CodeWriteOverlap, // `**` against a nested file path, no edge
			func(s *Spec) { s.Nodes[1].Write = []string{"internal/api/v2/handler.go"} },
			func(s *Spec) {
				s.Nodes[1].Write = []string{"internal/api/v2/handler.go"}
				s.Nodes[1].DependsOn = []string{"api"}
			}},
		{CodeWriteOverlap, // single-segment stars meeting
			func(s *Spec) { s.Nodes[0].Write = []string{"docs/*.md"}; s.Nodes[1].Write = []string{"docs/a*"} },
			func(s *Spec) { s.Nodes[0].Write = []string{"docs/*.md"}; s.Nodes[1].Write = []string{"docs/sub/a.md"} }},
		{CodeMissingVerification,
			func(s *Spec) { s.Nodes[2].DependsOn = []string{"api"} },
			func(s *Spec) { s.Nodes[1].DependsOn = []string{"api"}; s.Nodes[2].DependsOn = []string{"cli"} }},
		{CodeEmptyVerify,
			func(s *Spec) { s.Nodes[1].Verify = nil },
			func(s *Spec) { s.Nodes[1].Verify = [][]string{{"true"}} }},
		{CodeEmptyVerify, // an empty argv inside verify
			func(s *Spec) { s.Nodes[1].Verify = [][]string{{"go", "vet"}, {}} },
			func(s *Spec) { s.Nodes[1].Verify = [][]string{{"go", "vet"}} }},
		{CodeMissingPromptSection,
			func(s *Spec) { s.Nodes[0].Prompt.StopWhen = "  " },
			func(s *Spec) { s.Nodes[0].Prompt.StopWhen = "receipt green" }},
		{CodeMissingPromptSection, // no prompt object at all
			func(s *Spec) { s.Nodes[0].Prompt = nil },
			func(s *Spec) { s.Nodes[0].Prompt = prompt() }},
		{CodeInvalidBase,
			func(s *Spec) { s.Base = "1a3d605" },
			func(s *Spec) { s.Base = strings.Repeat("0", 40) }},
		{CodeInvalidBase, // uppercase hex is not canonical
			func(s *Spec) { s.Base = strings.ToUpper(testBase) },
			func(s *Spec) { s.Base = testBase }},
		{CodeInvalidMaxParallel, // upper bound 32, with enough nodes that the count bound is not the cause
			func(s *Spec) { manyNodes(s, 40); s.MaxParallel = 33 },
			func(s *Spec) { manyNodes(s, 40); s.MaxParallel = 32 }},
		{CodeInvalidMaxParallel, // schema-3 plans refuse max_parallel above the task count
			func(s *Spec) { s.MaxParallel = 4 },
			func(s *Spec) { s.MaxParallel = 3 }},
		{CodeInvalidMaxParallel,
			func(s *Spec) { s.MaxParallel = 0 },
			func(s *Spec) { s.MaxParallel = 1 }},
		{CodeInvalidKind,
			func(s *Spec) { s.Nodes[0].Kind = "agent" },
			func(s *Spec) { s.Nodes[0].Kind = "worker" }},
		{CodeActionMissingImage,
			func(s *Spec) { s.Nodes[2].Action.Image = "golang:1.26" },
			func(s *Spec) { s.Nodes[2].Action.Image = testImage }},
		{CodeActionMissingImage, // action node with no action block
			func(s *Spec) { s.Nodes[2].Action = nil },
			func(s *Spec) { s.Nodes[2].Kind = "worker"; s.Nodes[2].Action = nil }},
		{CodeInvalidSpec,
			func(s *Spec) { s.Schema = "rein_dag_spec/v2" },
			func(s *Spec) { s.Schema = SchemaV1 }},
		{CodeInvalidSpec, // run name above 64 characters would overflow the derived task name
			func(s *Spec) { s.Run = strings.Repeat("r", 65) },
			func(s *Spec) { s.Run = strings.Repeat("r", 64) }},
		{CodeInvalidSpec, // the same dependency listed twice
			func(s *Spec) { s.Nodes[2].DependsOn = []string{"api", "cli", "api"} },
			func(s *Spec) { s.Nodes[2].DependsOn = []string{"api", "cli"} }},
		{CodeInvalidKind, // a worker must not carry an action block
			func(s *Spec) {
				s.Nodes[0].Action = &Action{Argv: []string{"/bin/true"}, Image: testImage, TimeoutMS: 1000}
			},
			func(s *Spec) { s.Nodes[0].Action = nil }},
		{CodeActionMissingImage, // image digests are canonical lowercase hex
			func(s *Spec) {
				s.Nodes[2].Action.Image = "sha256:" + strings.ToUpper(strings.TrimPrefix(testImage, "sha256:"))
			},
			func(s *Spec) { s.Nodes[2].Action.Image = testImage }},
	}
	for i, c := range cases {
		t.Run(fmt.Sprintf("%02d-%s", i, c.code), func(t *testing.T) {
			bad := validSpec()
			c.bad(&bad)
			_, res := Lint(encode(t, bad))
			if res.Valid || !hasCode(res.Errors, c.code) {
				t.Fatalf("negative: want %s, got %v", c.code, codes(res.Errors))
			}
			good := validSpec()
			c.good(&good)
			_, res = Lint(encode(t, good))
			if hasCode(res.Errors, c.code) || !res.Valid {
				t.Fatalf("positive: want valid, got %+v", res.Errors)
			}
		})
	}
}

// manyNodes builds n nodes: n-1 independent read-only workers plus a final
// verifier, so only the node count can refuse it.
func manyNodes(s *Spec, n int) {
	s.Nodes = nil
	var deps []string
	for i := 0; i < n-1; i++ {
		id := fmt.Sprintf("n%d", i)
		s.Nodes = append(s.Nodes, worker(id, nil))
		deps = append(deps, id)
	}
	s.Nodes = append(s.Nodes, action("verify", deps...))
}

func TestDAGSpecUnknownAndForbiddenFields(t *testing.T) {
	base := encode(t, validSpec())
	inject := func(level string, key string) []byte {
		var m map[string]any
		if err := json.Unmarshal(base, &m); err != nil {
			t.Fatal(err)
		}
		nodes := m["nodes"].([]any)
		switch level {
		case "top":
			m[key] = "x"
		case "node":
			nodes[0].(map[string]any)[key] = "x"
		case "prompt":
			nodes[0].(map[string]any)["prompt"].(map[string]any)[key] = "x"
		case "action":
			nodes[2].(map[string]any)["action"].(map[string]any)[key] = "x"
		case "output":
			nodes[0].(map[string]any)["outputs"] = []any{map[string]any{"name": "o", "path": "o.txt", "max_bytes": 1, key: "x"}}
		case "input":
			nodes[2].(map[string]any)["inputs"] = []any{map[string]any{"producer": "api", "name": "o", "as": "o", key: "x"}}
		}
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	keys := []string{"launches", "profile", "config_path", "config_digest", "process_policy", "contract_hash", "surprise", "Schema"}
	for _, level := range []string{"top", "node", "prompt", "action", "output", "input"} {
		for _, key := range keys {
			t.Run(level+"-"+key, func(t *testing.T) {
				_, res := Lint(inject(level, key))
				if res.Valid || !hasCode(res.Errors, CodeUnknownField) {
					t.Fatalf("want unknown_field, got %+v", res)
				}
				for _, f := range res.Errors {
					if f.Code == CodeUnknownField && !strings.Contains(f.Message, fmt.Sprintf("%q", key)) {
						t.Fatalf("message lacks key: %s", f.Message)
					}
				}
			})
		}
	}
	// The node is attributed for node-level keys.
	_, res := Lint(inject("prompt", "profile"))
	if res.Errors[0].Node != "api" || !strings.Contains(res.Errors[0].Message, "forbidden") {
		t.Fatalf("attribution: %+v", res.Errors)
	}
	// Positive: the untouched spec carries no unknown field.
	if _, res := Lint(base); hasCode(res.Errors, CodeUnknownField) {
		t.Fatalf("baseline flagged: %+v", res.Errors)
	}
}

func TestDAGSpecStructuralRefusals(t *testing.T) {
	good := string(encode(t, validSpec()))
	for name, c := range map[string]struct{ raw, reason string }{
		"not-json":      {`{"schema":`, "decode"},
		"array":         {`[]`, "must be a JSON object"},
		"trailing":      {good + `{}`, "trailing data after spec object"},
		"wrong-type":    {strings.Replace(good, `"max_parallel":2`, `"max_parallel":"2"`, 1), "decode"},
		"duplicate-key": {strings.Replace(good, `"run":"r1"`, `"run":"r1","run":"r2"`, 1), `duplicate key "run" at $`},
		"nested-object": {strings.Replace(good, `"verify":[["go","test","./..."]]`, `"verify":[[{"launches":1}]]`, 1), "decode"},
	} {
		s, res := Lint([]byte(c.raw))
		if s != nil || res.Valid || !reflect.DeepEqual(codes(res.Errors), []string{CodeInvalidSpec}) {
			t.Fatalf("%s: want one invalid_spec, got %+v", name, res)
		}
		// The reason pins which check refused it, so a weaker check that still
		// fails for another reason does not pass this test.
		if !strings.Contains(res.Errors[0].Message, c.reason) {
			t.Fatalf("%s: reason %q does not contain %q", name, res.Errors[0].Message, c.reason)
		}
	}
}

func TestDAGLintSerialGraphWarning(t *testing.T) {
	chain := func(n int) Spec {
		s := validSpec()
		s.Nodes = []Node{worker("n0", []string{"a/**"})}
		for i := 1; i < n; i++ {
			s.Nodes = append(s.Nodes, worker(fmt.Sprintf("n%d", i), []string{fmt.Sprintf("p%d/**", i)}, fmt.Sprintf("n%d", i-1)))
		}
		s.Nodes[n-1].Write = nil // the last node verifies every producer
		return s
	}
	// Negative: 5-node chain warns but stays valid.
	_, res := Lint(encode(t, chain(5)))
	if !res.Valid || !reflect.DeepEqual(codes(res.Warnings), []string{WarnSerialGraph}) {
		t.Fatalf("chain: %+v", res)
	}
	// Positive: 4 nodes is below the size threshold.
	if _, res := Lint(encode(t, chain(4))); !res.Valid || len(res.Warnings) != 0 {
		t.Fatalf("4-chain: %+v", res)
	}
	// Positive: 5 nodes with critical path 3 of 5 (< 80%).
	s := validSpec()
	s.Nodes = []Node{worker("a", []string{"a/**"}), worker("b", []string{"b/**"}), worker("c", []string{"c/**"}), worker("d", []string{"d/**"}, "a"), action("v", "b", "c", "d")}
	if _, res := Lint(encode(t, s)); !res.Valid || len(res.Warnings) != 0 {
		t.Fatalf("wide: %+v", res)
	}
}

func TestDAGLintDeterministicOrder(t *testing.T) {
	s := validSpec()
	s.Base = "x"
	s.MaxParallel = 99
	s.Nodes[1].Kind = "bogus"
	s.Nodes[0].Verify = nil
	s.Nodes[0].Prompt = nil
	raw := encode(t, s)
	_, first := Lint(raw)
	for i := 0; i < 20; i++ {
		if _, again := Lint(raw); !reflect.DeepEqual(first, again) {
			t.Fatalf("nondeterministic: %+v vs %+v", first, again)
		}
	}
	for i := 1; i < len(first.Errors); i++ {
		a, b := first.Errors[i-1], first.Errors[i]
		if a.Code > b.Code || (a.Code == b.Code && a.Node > b.Node) {
			t.Fatalf("unsorted at %d: %+v", i, first.Errors)
		}
	}
	if len(first.Errors) < 9 { // 5 prompt sections + verify + kind + base + max_parallel
		t.Fatalf("expected every finding reported, got %v", codes(first.Errors))
	}
}

func TestDAGSpecGlobIntersect(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"internal/api/**", "internal/api/v2/x.go", true},
		{"internal/api/**", "internal/apix/x.go", false},
		{"**/*.go", "cmd/rein/main.go", true},
		{"**/*.go", "README.md", false},
		{"**/x.go", "x.go", true},
		{"a/*.go", "a/b/c.go", false},
		{"a/*", "a/?", true},
		{"a/**", "b/**", false},
		{"a/**/b", "a/b", true},
		{"**", "anything/at/all", true},
		{"docs/*.md", "docs/a*", true},
		{"same.txt", "same.txt", true},
		{"same.txt", "other.txt", false},
	} {
		if got := globsIntersect(c.a, c.b); got != c.want {
			t.Errorf("%q ∩ %q = %v, want %v", c.a, c.b, got, c.want)
		}
		if got := globsIntersect(c.b, c.a); got != c.want {
			t.Errorf("%q ∩ %q (swapped) = %v, want %v", c.b, c.a, got, c.want)
		}
	}
}

func TestDAGLintOptionalWorkerFields(t *testing.T) {
	good := validSpec()
	good.Nodes[0].KindProfile = "backend"
	good.Nodes[0].RequiredCapabilities = []string{"read", "write"}
	good.Nodes[0].ExpectedContextTokens = 4000
	good.Nodes[0].Rationale = []RationaleEntry{{Path: "internal/api/x.go", Reason: "add endpoint"}}
	_, res := Lint(encode(t, good))
	if !res.Valid {
		t.Fatalf("expected valid with optional worker fields, got errors: %+v", res.Errors)
	}

	// Invalid kind_profile
	b1 := validSpec()
	b1.Nodes[0].KindProfile = "invalid-kind"
	_, res1 := Lint(encode(t, b1))
	if res1.Valid || !hasCode(res1.Errors, CodeInvalidKind) {
		t.Fatalf("want invalid_kind, got: %+v", res1)
	}

	// Action node carrying worker routing fields
	b2 := validSpec()
	b2.Nodes[2].KindProfile = "backend"
	_, res2 := Lint(encode(t, b2))
	if res2.Valid || !hasCode(res2.Errors, CodeInvalidKind) {
		t.Fatalf("want invalid_kind on action carrying worker routing, got: %+v", res2)
	}

	// Empty capability
	b3 := validSpec()
	b3.Nodes[0].RequiredCapabilities = []string{"read", " "}
	_, res3 := Lint(encode(t, b3))
	if res3.Valid || !hasCode(res3.Errors, CodeInvalidSpec) {
		t.Fatalf("want invalid_spec for empty capability, got: %+v", res3)
	}

	// Empty rationale
	b4 := validSpec()
	b4.Nodes[0].Rationale = []RationaleEntry{{Path: "", Reason: "something"}}
	_, res4 := Lint(encode(t, b4))
	if res4.Valid || !hasCode(res4.Errors, CodeInvalidSpec) {
		t.Fatalf("want invalid_spec for empty rationale path, got: %+v", res4)
	}
}
