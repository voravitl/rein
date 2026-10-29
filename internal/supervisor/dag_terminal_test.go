package supervisor

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func dagTerminalPlan(deps map[string][]string, order ...string) (Plan, DAGStatus) {
	var p Plan
	layers := map[string]int{}
	var layer func(string) int
	layer = func(name string) int {
		if l, ok := layers[name]; ok {
			return l
		}
		l := 0
		for _, d := range deps[name] {
			l = max(l, layer(d)+1)
		}
		layers[name] = l
		return l
	}
	view := DAGStatus{Run: "R", Status: "ok"}
	for _, name := range order {
		p.Tasks = append(p.Tasks, Task{Task: name, Kind: "action", DependsOn: deps[name], MaxAttempts: 1})
		state := "pending"
		if len(deps[name]) == 0 {
			state = "ready"
		}
		view.Nodes = append(view.Nodes, DAGNodeStatus{Task: name, Kind: "action", Layer: layer(name), State: state, MaxAttempts: 1})
	}
	return p, view
}

func dagTerminalRender(t *testing.T, p Plan, view DAGStatus, width int) string {
	t.Helper()
	var out bytes.Buffer
	if err := RenderDAGGraph(&out, p, view, DAGGraphOptions{Width: width}); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func dagTerminalCard(t *testing.T, out, task string) string {
	t.Helper()
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if col := strings.Index(line, "| "+task+" "); col >= 0 {
			var card []string
			for _, next := range lines[i:] {
				if len(next) <= col || next[col] != '|' {
					break
				}
				card = append(card, strings.TrimSpace(next[col+1:min(len(next), col+dagGraphCardWidth-1)]))
			}
			return strings.Join(card, "\n")
		}
	}
	t.Fatalf("card %q missing: %s", task, out)
	return ""
}

func TestDAGGraphWidthNeverDropsNodesOrEdges(t *testing.T) {
	long := "task-" + strings.Repeat("x", 70)
	deps := map[string][]string{"A": {}, "B": {"A"}, "C": {"A"}, "D": {"B", "C"}, long: {"A", "D"}}
	p, view := dagTerminalPlan(deps, "A", "B", "C", "D", long)
	for _, width := range []int{1, 16, 40, 80, 120} {
		out := dagTerminalRender(t, p, view, width)
		limit := DAGGraphWidth(width)
		for i, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
			if len(line) > limit {
				t.Fatalf("width %d: line %d has %d columns: %q", width, i+1, len(line), out)
			}
		}
		// Wrapped labels continue on the next card line; joining the
		// whitespace-free text must still contain every full name.
		flat := strings.Join(strings.Fields(strings.NewReplacer("|", " ", "\n", " ").Replace(out)), "")
		for _, name := range []string{"A", "B", "C", "D", long} {
			if !strings.Contains(flat, name) {
				t.Fatalf("width %d dropped node %q: %s", width, name, out)
			}
		}
		if !strings.Contains(flat, "nodes5edges6") {
			t.Fatalf("width %d lost the edge count: %s", width, out)
		}
	}
	if DAGGraphWidth(0) != 80 || DAGGraphWidth(-3) != 80 || DAGGraphWidth(5) != 16 || DAGGraphWidth(132) != 132 {
		t.Fatal("width fallback changed")
	}
}

func TestDAGGraphFanoutJoinEdgesAndLayers(t *testing.T) {
	p, view := dagTerminalPlan(map[string][]string{"A": {}, "B": {"A"}, "C": {"A"}, "D": {"B", "C"}}, "A", "B", "C", "D")
	out := dagTerminalRender(t, p, view, 120)
	for task, want := range map[string]string{
		"A": "A\nready action\nattempt 0/1\n<- none\n-> B, C",
		"B": "B\npending action\nattempt 0/1\n<- A\n-> D",
		"C": "C\npending action\nattempt 0/1\n<- A\n-> D",
		"D": "D\npending action\nattempt 0/1\n<- B, C\n-> none",
	} {
		if got := dagTerminalCard(t, out, task); got != want {
			t.Fatalf("card %s:\ngot  %q\nwant %q\n%s", task, got, want, out)
		}
	}
	l0, l1, l2 := strings.Index(out, "layer 0"), strings.Index(out, "layer 1"), strings.Index(out, "layer 2")
	if l0 < 0 || l0 >= l1 || l1 >= l2 || strings.Count(out, "  v\n") != 2 {
		t.Fatalf("layers must flow top to bottom: %s", out)
	}
	// Same layers, different edges: the drawing must differ.
	q, other := dagTerminalPlan(map[string][]string{"A": {}, "B": {"A"}, "C": {"A"}, "D": {"B"}}, "A", "B", "C", "D")
	if alt := dagTerminalRender(t, q, other, 120); alt == out {
		t.Fatalf("different dependencies drew the same graph: %s", out)
	}
}

func TestDAGGraphDeterministicOrder(t *testing.T) {
	deps := map[string][]string{"A": {}, "B": {"A"}, "C": {"A"}, "D": {"C", "B"}}
	p, view := dagTerminalPlan(deps, "A", "B", "C", "D")
	q, permuted := dagTerminalPlan(deps, "D", "C", "A", "B")
	for _, width := range []int{40, 80, 120} {
		first := dagTerminalRender(t, p, view, width)
		if again := dagTerminalRender(t, p, view, width); again != first {
			t.Fatalf("width %d unstable output", width)
		}
		if got := dagTerminalRender(t, q, permuted, width); got != first {
			t.Fatalf("width %d: plan order changed the drawing:\n%s\n%s", width, first, got)
		}
	}
}

func TestDAGGraphSanitizesLabels(t *testing.T) {
	hostile := "evil\x1b[31m\x07\r\x00\x7f\xff\u202e\\x"
	p, view := dagTerminalPlan(map[string][]string{hostile: {}}, hostile)
	view.Run, view.Status = "run\x1b]0;title\x07", "ok\nforged"
	agent, model, effort := "a\x1b", "m\u0085", "e"
	view.Nodes[0].Agent, view.Nodes[0].Model, view.Nodes[0].Effort = &agent, &model, &effort
	var out bytes.Buffer
	if err := RenderDAGGraph(&out, p, view, DAGGraphOptions{Width: 80, Caption: "seq\x1b[2J"}); err != nil {
		t.Fatal(err)
	}
	for _, b := range out.Bytes() {
		if (b < 0x20 && b != '\n') || b >= 0x7f {
			t.Fatalf("unsafe byte %#x in %q", b, out.String())
		}
	}
	for _, want := range []string{`evil\x1b[31m\x07\x0d\x00\x7f\xff\u202e\\x`, `run\x1b]0;title\x07`, `ok\x0aforged`, `a\x1b/m\u0085:e`, `seq\x1b[2J`} {
		if !strings.Contains(strings.Join(strings.Fields(strings.ReplaceAll(out.String(), "|", " ")), ""), want) {
			t.Fatalf("visible escape %q missing: %s", want, out.String())
		}
	}
}

func TestDAGGraphRejectsMismatchedSnapshotWithoutOutput(t *testing.T) {
	p, view := dagTerminalPlan(map[string][]string{"A": {}, "B": {"A"}}, "A", "B")
	var out bytes.Buffer
	view.Nodes = view.Nodes[:1]
	if err := RenderDAGGraph(&out, p, view, DAGGraphOptions{}); err == nil || out.Len() != 0 {
		t.Fatalf("missing node must fail without output: %v %q", err, out.String())
	}
	_, view = dagTerminalPlan(map[string][]string{"A": {}, "B": {"A"}}, "A", "B")
	view.Nodes[1].Task = "Z"
	if err := RenderDAGGraph(&out, p, view, DAGGraphOptions{}); err == nil || out.Len() != 0 {
		t.Fatalf("renamed node must fail without output: %v %q", err, out.String())
	}
}

// The per-task loop only catches missing or renamed nodes. A snapshot with an
// extra node or a repeated task entry still names every plan task, so only the
// count check stops it from being drawn.
func TestDAGGraphRejectsExtraOrDuplicateSnapshotNodesWithoutOutput(t *testing.T) {
	deps := map[string][]string{"A": {}, "B": {"A"}}
	p, control := dagTerminalPlan(deps, "A", "B")
	if out := dagTerminalRender(t, p, control, 80); out == "" {
		t.Fatal("control snapshot must render")
	}
	for _, tc := range []struct {
		name   string
		mutate func([]DAGNodeStatus) []DAGNodeStatus
	}{
		{name: "extra_node", mutate: func(n []DAGNodeStatus) []DAGNodeStatus {
			return append(n, DAGNodeStatus{Task: "Z", Kind: "action", Layer: 0, State: "ready", MaxAttempts: 1})
		}},
		{name: "extra_dependent_layer_node", mutate: func(n []DAGNodeStatus) []DAGNodeStatus {
			return append(n, DAGNodeStatus{Task: "Z", Kind: "action", Layer: 2, State: "pending", MaxAttempts: 1})
		}},
		{name: "duplicate_root", mutate: func(n []DAGNodeStatus) []DAGNodeStatus {
			return append(n, n[0])
		}},
		{name: "duplicate_dependent", mutate: func(n []DAGNodeStatus) []DAGNodeStatus {
			return append(n, n[1])
		}},
		{name: "duplicate_with_conflicting_state", mutate: func(n []DAGNodeStatus) []DAGNodeStatus {
			dup := n[1]
			dup.State = "failed"
			return append([]DAGNodeStatus{dup}, n...)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, view := dagTerminalPlan(deps, "A", "B")
			view.Nodes = tc.mutate(view.Nodes)
			var out bytes.Buffer
			err := RenderDAGGraph(&out, p, view, DAGGraphOptions{Width: 80})
			if err == nil || !strings.Contains(err.Error(), "does not match plan tasks") || out.Len() != 0 {
				t.Fatalf("snapshot %+v must fail without output: err=%v out=%q", view.Nodes, err, out.String())
			}
			lines, err := dagGraphLines(p, view, DAGGraphOptions{Width: 80})
			if err == nil || lines != nil {
				t.Fatalf("dagGraphLines must reject %+v: lines=%q err=%v", view.Nodes, lines, err)
			}
		})
	}
}

func TestDAGJournalPrefixCountsOnlyCompleteRows(t *testing.T) {
	raw := []byte("h\nl\nt\npartial")
	for seq, want := range map[int]string{1: "h\n", 2: "h\nl\n", 3: "h\nl\nt\n"} {
		got, ok := dagJournalPrefix(raw, seq)
		if !ok || string(got) != want {
			t.Fatalf("seq %d: %q %v", seq, got, ok)
		}
	}
	if _, ok := dagJournalPrefix(raw, 4); ok {
		t.Fatal("partial row counted as complete")
	}
}

// Each --seq frame must equal the live strict reader on that exact prefix.
func TestReadDAGStatusAtMatchesStrictPrefix(t *testing.T) {
	p, hash := dagStatusFixture(t, map[string][]string{"A": {}, "B": {"A"}})
	l := dagStatusReach(t, p, hash, "done")
	if err := os.Remove(dagTimesPath(l.path)); err != nil {
		t.Fatal(err)
	}
	full, err := os.ReadFile(l.path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.SplitAfter(full, []byte{'\n'})
	lines = lines[:len(lines)-1]
	for seq := 1; seq <= len(lines); seq++ {
		if err := os.WriteFile(l.path, bytes.Join(lines[:seq], nil), 0600); err != nil {
			t.Fatal(err)
		}
		want, err := ReadDAGStatus(context.Background(), p, hash)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(l.path, append(append([]byte(nil), full...), `{"seq":99`...), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := ReadDAGStatusAt(context.Background(), p, hash, seq)
		if err != nil || !reflect.DeepEqual(got, want) || got.Status == "changing" {
			t.Fatalf("seq %d:\ngot  %+v %v\nwant %+v", seq, got, err, want)
		}
	}
	for _, seq := range []int{0, -1, len(lines) + 1} {
		if _, err := ReadDAGStatusAt(context.Background(), p, hash, seq); err == nil {
			t.Fatalf("seq %d must be rejected", seq)
		}
	}
	if err := os.Remove(l.path); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDAGStatusAt(context.Background(), p, hash, 1); err == nil || !strings.Contains(err.Error(), "existing DAG journal") {
		t.Fatalf("missing journal must be rejected: %v", err)
	}
	if _, err := os.Stat(l.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replay created the journal: %v", err)
	}
}
