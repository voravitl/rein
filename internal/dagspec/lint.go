package dagspec

import (
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/voravitl/rein/internal/run"
)

// Stable finding codes. Callers and tests match on these; never rename one.
const (
	CodeInvalidSpec          = "invalid_spec"
	CodeUnknownField         = "unknown_field"
	CodeDuplicateID          = "duplicate_id"
	CodeInvalidID            = "invalid_id"
	CodeUnknownDependency    = "unknown_dependency"
	CodeSelfDependency       = "self_dependency"
	CodeCycle                = "cycle"
	CodeTooManyNodes         = "too_many_nodes"
	CodeWriteOverlap         = "write_overlap"
	CodeMissingVerification  = "missing_verification"
	CodeEmptyVerify          = "empty_verify"
	CodeMissingPromptSection = "missing_prompt_section"
	CodeInvalidBase          = "invalid_base"
	CodeInvalidMaxParallel   = "invalid_max_parallel"
	CodeInvalidKind          = "invalid_kind"
	CodeActionMissingImage   = "action_missing_image"

	WarnSerialGraph = "serial_graph"
)

// MaxNodes is the largest accepted graph.
const MaxNodes = 1000

// maxRunLen keeps the derived task name `<run>-<id>` within one path segment.
const maxRunLen = 64

var idRx = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// Finding is one lint refusal or warning.
type Finding struct {
	Code    string `json:"code"`
	Node    string `json:"node"`
	Message string `json:"message"`
}

// Result is the full lint outcome. Valid is true when Errors is empty;
// warnings never make a spec invalid.
type Result struct {
	Valid    bool      `json:"valid"`
	Errors   []Finding `json:"errors"`
	Warnings []Finding `json:"warnings"`
}

// Lint parses and lints raw spec bytes. The decoded spec is returned when the
// bytes were structurally decodable, even if lint refused it.
func Lint(data []byte) (*Spec, Result) {
	s, errs := Parse(data)
	var warns []Finding
	if s != nil {
		e, w := lintSpec(s)
		errs = append(errs, e...)
		warns = w
	}
	sortFindings(errs)
	sortFindings(warns)
	if errs == nil {
		errs = []Finding{}
	}
	if warns == nil {
		warns = []Finding{}
	}
	return s, Result{Valid: len(errs) == 0, Errors: errs, Warnings: warns}
}

func sortFindings(f []Finding) {
	sort.SliceStable(f, func(i, j int) bool {
		if f[i].Code != f[j].Code {
			return f[i].Code < f[j].Code
		}
		if f[i].Node != f[j].Node {
			return f[i].Node < f[j].Node
		}
		return f[i].Message < f[j].Message
	})
}

func fullRevision(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 20 && s == strings.ToLower(s)
}

func lintSpec(s *Spec) (errs, warns []Finding) {
	add := func(code, node, format string, a ...any) {
		errs = append(errs, Finding{Code: code, Node: node, Message: fmt.Sprintf(format, a...)})
	}
	if s.Schema != SchemaV1 {
		add(CodeInvalidSpec, "", "schema must be %q, got %q", SchemaV1, s.Schema)
	}
	if !run.ValidName(s.Run) || len(s.Run) > maxRunLen {
		add(CodeInvalidSpec, "", "run %q must be a valid run name of at most %d characters", s.Run, maxRunLen)
	}
	if !filepath.IsAbs(s.Repo) || filepath.Clean(s.Repo) != s.Repo {
		add(CodeInvalidSpec, "", "repo %q must be an absolute clean path", s.Repo)
	}
	if strings.TrimSpace(s.Goal) == "" {
		add(CodeInvalidSpec, "", "goal is required")
	}
	if !fullRevision(s.Base) {
		add(CodeInvalidBase, "", "base %q must be a 40-character lowercase hex commit", s.Base)
	}
	if s.MaxParallel < 1 || s.MaxParallel > 32 {
		add(CodeInvalidMaxParallel, "", "max_parallel %d must be within 1..32", s.MaxParallel)
	} else if len(s.Nodes) > 0 && s.MaxParallel > len(s.Nodes) {
		// Schema-3 plan validation refuses max_parallel above the task count.
		add(CodeInvalidMaxParallel, "", "max_parallel %d exceeds the node count %d", s.MaxParallel, len(s.Nodes))
	}
	if len(s.Nodes) == 0 {
		add(CodeInvalidSpec, "", "nodes must not be empty")
		return errs, nil
	}
	if len(s.Nodes) > MaxNodes {
		add(CodeTooManyNodes, "", "%d nodes exceeds the limit of %d", len(s.Nodes), MaxNodes)
		return errs, nil
	}

	// Per-node rules.
	count := map[string]int{}
	for _, n := range s.Nodes {
		count[n.ID]++
	}
	graphOK := true
	for _, n := range s.Nodes {
		if !idRx.MatchString(n.ID) {
			add(CodeInvalidID, n.ID, "id %q must match %s", n.ID, idRx)
			graphOK = false
		}
		switch n.Kind {
		case "worker":
			if n.Action != nil {
				add(CodeInvalidKind, n.ID, "worker node must not carry an action")
			}
			if n.KindProfile != "" && !validWorkerKind(n.KindProfile) {
				add(CodeInvalidKind, n.ID, "kind_profile %q must be one of backend, frontend, fullstack, docs, mechanical", n.KindProfile)
			}
			if n.ExpectedContextTokens < 0 {
				add(CodeInvalidSpec, n.ID, "expected_context_tokens must be non-negative")
			}
			for _, cap := range n.RequiredCapabilities {
				if strings.TrimSpace(cap) == "" {
					add(CodeInvalidSpec, n.ID, "required_capabilities must not contain empty strings")
				}
			}
			for _, r := range n.Rationale {
				if strings.TrimSpace(r.Path) == "" || strings.TrimSpace(r.Reason) == "" {
					add(CodeInvalidSpec, n.ID, "rationale entry must have non-empty path and reason")
				}
			}
		case "action":
			if n.Action == nil || !validImage(n.Action.Image) {
				add(CodeActionMissingImage, n.ID, "action node requires action.image as sha256:<64 lowercase hex>")
			}
			if n.KindProfile != "" || len(n.RequiredCapabilities) > 0 || n.ExpectedContextTokens > 0 || len(n.Rationale) > 0 {
				add(CodeInvalidKind, n.ID, "action node must not carry worker routing fields")
			}
		default:
			add(CodeInvalidKind, n.ID, "kind %q must be worker or action", n.Kind)
		}
		if len(n.Verify) == 0 {
			add(CodeEmptyVerify, n.ID, "verify must hold at least one command")
		}
		for i, argv := range n.Verify {
			if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
				add(CodeEmptyVerify, n.ID, "verify[%d] is an empty command", i)
			}
		}
		for _, sec := range missingSections(n.Prompt) {
			add(CodeMissingPromptSection, n.ID, "prompt section %q is missing or blank", sec)
		}
		seen := map[string]bool{}
		for _, d := range n.DependsOn {
			switch {
			case d == n.ID:
				add(CodeSelfDependency, n.ID, "node depends on itself")
			case count[d] == 0:
				add(CodeUnknownDependency, n.ID, "depends_on %q names no node", d)
				graphOK = false
			case seen[d]:
				add(CodeInvalidSpec, n.ID, "depends_on lists %q twice", d)
			}
			seen[d] = true
		}
	}
	dups := []string{}
	for id, c := range count {
		if c > 1 {
			dups = append(dups, id)
		}
	}
	sort.Strings(dups)
	for _, id := range dups {
		add(CodeDuplicateID, id, "id %q is used by %d nodes", id, count[id])
		graphOK = false
	}
	if !graphOK {
		return errs, nil // graph rules need unique, resolvable ids
	}

	g := newGraph(s.Nodes)
	if cyc := g.cycles(); len(cyc) > 0 {
		for _, c := range cyc {
			add(CodeCycle, c[0], "dependency cycle among %s", strings.Join(c, ", "))
		}
		return errs, nil
	}
	errs = append(errs, g.writeOverlaps(s.Nodes)...)
	if f := g.missingVerification(s.Nodes); f != nil {
		errs = append(errs, *f)
	}
	if n := len(s.Nodes); n >= 5 {
		if l := g.longestPath(); l*5 >= n*4 {
			warns = append(warns, Finding{Code: WarnSerialGraph, Message: fmt.Sprintf("critical path covers %d of %d nodes; little parallelism", l, n)})
		}
	}
	return errs, warns
}

func validImage(s string) bool {
	h, ok := strings.CutPrefix(s, "sha256:")
	b, err := hex.DecodeString(h)
	return ok && err == nil && len(b) == 32 && h == strings.ToLower(h)
}

func missingSections(p *Prompt) []string {
	var v Prompt
	if p != nil {
		v = *p
	}
	var out []string
	for _, s := range []struct{ name, val string }{
		{"task", v.Task}, {"deliverable", v.Deliverable}, {"scope", v.Scope}, {"verify", v.Verify}, {"stop_when", v.StopWhen},
	} {
		if strings.TrimSpace(s.val) == "" {
			out = append(out, s.name)
		}
	}
	return out
}

// graph indexes nodes by spec position; ids are unique and resolvable here.
type graph struct {
	ids   []string
	deps  [][]int // node -> direct dependencies (self edges dropped)
	reach [][]bool
}

func newGraph(nodes []Node) *graph {
	idx := map[string]int{}
	for i, n := range nodes {
		idx[n.ID] = i
	}
	g := &graph{ids: make([]string, len(nodes)), deps: make([][]int, len(nodes)), reach: make([][]bool, len(nodes))}
	for i, n := range nodes {
		g.ids[i] = n.ID
		for _, d := range n.DependsOn {
			if j := idx[d]; j != i {
				g.deps[i] = append(g.deps[i], j)
			}
		}
	}
	// reach[a][b]: a depends on b directly or transitively.
	for a := range nodes {
		r := make([]bool, len(nodes))
		stack := append([]int(nil), g.deps[a]...)
		for len(stack) > 0 {
			b := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if r[b] {
				continue
			}
			r[b] = true
			stack = append(stack, g.deps[b]...)
		}
		g.reach[a] = r
	}
	return g
}

// cycles returns each strongly connected component of size > 1, members
// sorted, components sorted by first member.
func (g *graph) cycles() [][]string {
	var out [][]string
	done := make([]bool, len(g.ids))
	for a := range g.ids {
		if done[a] || !g.reach[a][a] {
			continue
		}
		var comp []string
		for b := range g.ids {
			if b == a || (g.reach[a][b] && g.reach[b][a]) {
				done[b] = true
				comp = append(comp, g.ids[b])
			}
		}
		sort.Strings(comp)
		out = append(out, comp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

func (g *graph) ordered(a, b int) bool { return g.reach[a][b] || g.reach[b][a] }

func (g *graph) writeOverlaps(nodes []Node) []Finding {
	var out []Finding
	for a := range nodes {
		for b := a + 1; b < len(nodes); b++ {
			if g.ordered(a, b) {
				continue
			}
			for _, ga := range nodes[a].Write {
				for _, gb := range nodes[b].Write {
					if !globsIntersect(ga, gb) {
						continue
					}
					x, y, gx, gy := g.ids[a], g.ids[b], ga, gb
					if y < x {
						x, y, gx, gy = y, x, gb, ga
					}
					out = append(out, Finding{Code: CodeWriteOverlap, Node: x, Message: fmt.Sprintf("write %q overlaps %s write %q with no ordering edge between them", gx, y, gy)})
				}
			}
		}
	}
	return out
}

// missingVerification requires one node that depends, directly or
// transitively, on every producer (node with a non-empty write list).
func (g *graph) missingVerification(nodes []Node) *Finding {
	var producers []int
	for i, n := range nodes {
		if len(n.Write) > 0 {
			producers = append(producers, i)
		}
	}
	if len(producers) == 0 {
		return nil
	}
	for v := range nodes {
		all := true
		for _, p := range producers {
			if !g.reach[v][p] {
				all = false
				break
			}
		}
		if all {
			return nil
		}
	}
	return &Finding{Code: CodeMissingVerification, Message: fmt.Sprintf("no node depends on every producer (%d producers); add a final verification node", len(producers))}
}

// longestPath is the node count of the longest dependency chain (acyclic graph).
func (g *graph) longestPath() int {
	memo := make([]int, len(g.ids))
	var depth func(int) int
	depth = func(a int) int {
		if memo[a] > 0 {
			return memo[a]
		}
		best := 0
		for _, d := range g.deps[a] {
			best = max(best, depth(d))
		}
		memo[a] = best + 1
		return memo[a]
	}
	best := 0
	for a := range g.ids {
		best = max(best, depth(a))
	}
	return best
}

func validWorkerKind(k string) bool {
	switch k {
	case "backend", "frontend", "fullstack", "docs", "mechanical":
		return true
	default:
		return false
	}
}
