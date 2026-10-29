package supervisor

import (
	"bytes"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

var dagCLIViewSGR = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func dagCLIViewStatusline(t *testing.T, p Plan, view DAGStatus, o DAGStatuslineOptions) string {
	t.Helper()
	var out bytes.Buffer
	if err := RenderDAGStatusline(&out, p, view, o); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func dagCLIViewTUI(t *testing.T, p Plan, view DAGStatus, o DAGTUIOptions) string {
	t.Helper()
	var out bytes.Buffer
	if err := RenderDAGTUI(&out, p, view, o); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func dagCLIViewLines(out string) []string {
	return strings.Split(strings.TrimSuffix(out, "\n"), "\n")
}

func dagCLIViewChain() (Plan, DAGStatus) {
	deps := map[string][]string{"a": {}, "b": {"a"}, "c": {"b"}, "d": {"c"}, "e": {"d"}}
	return dagTerminalPlan(deps, "a", "b", "c", "d", "e")
}

func TestDAGStatuslineHeightAndWidthCap(t *testing.T) {
	p, view := dagCLIViewChain()
	for _, width := range []int{12, 24, 40, 80} {
		for lines := 1; lines <= 7; lines++ {
			out := dagCLIViewStatusline(t, p, view, DAGStatuslineOptions{Width: width, Lines: lines})
			got := dagCLIViewLines(out)
			if len(got) > lines {
				t.Fatalf("width %d lines %d: %d lines rendered: %q", width, lines, len(got), out)
			}
			for i, line := range got {
				if n := utf8.RuneCountInString(line); n > width {
					t.Fatalf("width %d lines %d: line %d has %d runes: %q", width, lines, i+1, n, out)
				}
			}
			if width < 40 {
				continue
			}
			// Five layers need six lines; shown nodes plus "+k more" must
			// account for every node.
			shown := 0
			for _, name := range []string{"a", "b", "c", "d", "e"} {
				if regexp.MustCompile(`(^|\s)\S ` + name + `\b`).MatchString(out) {
					shown++
				}
			}
			m := regexp.MustCompile(`\+(\d+) more`).FindStringSubmatch(out)
			if lines >= 6 {
				if m != nil || shown != 5 || len(got) != 6 {
					t.Fatalf("lines %d must show all five layers without overflow: %q", lines, out)
				}
				continue
			}
			if m == nil {
				t.Fatalf("lines %d must end with +k more: %q", lines, out)
			}
			if hidden, _ := strconv.Atoi(m[1]); hidden+shown != 5 || len(got) != lines {
				t.Fatalf("lines %d: shown %d + hidden %s must be 5 in exactly %d lines: %q", lines, shown, m[1], lines, out)
			}
		}
	}
}

func TestDAGStatuslineWindowFollowsActiveLayer(t *testing.T) {
	p, view := dagCLIViewChain()
	for i := range view.Nodes[:3] {
		view.Nodes[i].State = "done"
	}
	view.Nodes[3].State = "running"
	out := dagCLIViewStatusline(t, p, view, DAGStatuslineOptions{Width: 80, Lines: 3})
	if !strings.Contains(out, "⠋ d") || strings.Contains(out, "✓ a") || !strings.Contains(out, "+4 more") {
		t.Fatalf("the window must start at the first unfinished layer: %q", out)
	}
	if !strings.Contains(dagCLIViewLines(out)[0], "3/5") {
		t.Fatalf("header must count done nodes: %q", out)
	}
}

func TestDAGStatuslineGlyphMapping(t *testing.T) {
	names := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	deps := map[string][]string{}
	p, view := dagTerminalPlan(deps, names...)
	states := []string{"done", "failed", "blocked", "pending", "ready", "preparing", "running", "finalizing"}
	for i, state := range states {
		view.Nodes[i].State = state
	}
	for _, tc := range []struct {
		now     int64
		spinner string
	}{{0, "⠋"}, {99, "⠋"}, {100, "⠙"}, {950, "⠏"}, {1000, "⠋"}, {-100, "⠏"}} {
		out := dagCLIViewStatusline(t, p, view, DAGStatuslineOptions{Width: 200, NowMS: tc.now})
		want := []string{"✓ a", "✗ b", "⊘ c", "◌ d", "○ e", tc.spinner + " f", tc.spinner + " g", tc.spinner + " h"}
		for _, w := range want {
			if !strings.Contains(out, w) {
				t.Fatalf("now %d: missing %q: %q", tc.now, w, out)
			}
		}
		header := dagCLIViewLines(out)[0]
		if strings.ContainsAny(header, "✓✗⊘◌○⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏") {
			t.Fatalf("header must not use node glyphs: %q", header)
		}
	}
}

func TestDAGStatuslineNoColorEqualsStrippedColor(t *testing.T) {
	p, view := dagCLIViewChain()
	view.Nodes[0].State = "running"
	for _, width := range []int{10, 40, 80} {
		for _, lines := range []int{1, 2, 4, 8} {
			o := DAGStatuslineOptions{Width: width, Lines: lines, NowMS: 1234}
			plain := dagCLIViewStatusline(t, p, view, o)
			o.Color = true
			colored := dagCLIViewStatusline(t, p, view, o)
			if strings.Contains(plain, "\x1b") || !strings.Contains(colored, "38;5;") {
				t.Fatalf("color must be SGR-only and opt-in: plain=%q colored=%q", plain, colored)
			}
			if stripped := dagCLIViewSGR.ReplaceAllString(colored, ""); stripped != plain {
				t.Fatalf("width %d lines %d: colored minus SGR differs: %q vs %q", width, lines, stripped, plain)
			}
			if strings.Contains(dagCLIViewSGR.ReplaceAllString(colored, ""), "\x1b") {
				t.Fatalf("non-SGR escape emitted: %q", colored)
			}
		}
	}
}

func TestDAGStatuslineWidthSource(t *testing.T) {
	for _, tc := range []struct {
		columns string
		host    string
		want    int
	}{
		{"40", `{"terminal_width":120}`, 40},
		{"", `{"terminal_width":60}`, 60},
		{"0", `{"terminal_width":60}`, 60},
		{"-3", `{"terminal_width":61}`, 61},
		{"wide", "", 80},
		{"", "", 80},
		{"", `{"terminal_width":0}`, 80},
		{"", `{"terminal_width":"wide"}`, 80},
		{"", `{"terminal_width":72.5}`, 80},
		{"", `[1,2,3]`, 80},
		{"", `{"terminal_width":100} trailing`, 80},
		{"", "\x1b[2J\x00", 80},
		{"999999", "", dagStatuslineMaxWidth},
	} {
		if got := DAGStatuslineWidth(tc.columns, []byte(tc.host)); got != tc.want {
			t.Fatalf("COLUMNS=%q host=%q: width %d, want %d", tc.columns, tc.host, got, tc.want)
		}
	}
	if got := DAGStatuslineNowMS("1700000000123", time.Unix(5, 0)); got != 1700000000123 {
		t.Fatalf("override ignored: %d", got)
	}
	if got := DAGStatuslineNowMS("soon", time.Unix(5, 0)); got != 5000 {
		t.Fatalf("invalid override must use the clock: %d", got)
	}
}

func TestDAGStatuslineFallbackIsShortPlainText(t *testing.T) {
	for _, reason := range []string{
		"",
		"plan: open /tmp/absent.json: no such file or directory",
		"bad \x1b[31mred\x1b[0m\x07\r\n\x00 reason " + strings.Repeat("é", 200),
	} {
		got := DAGStatuslineFallback(reason)
		if !strings.HasPrefix(got, "rein:") || strings.ContainsAny(got, "\x1b\r\n\x00\x07") || utf8.RuneCountInString(got) > 80 {
			t.Fatalf("fallback for %q must be one short plain line: %q", reason, got)
		}
	}
}

func TestDAGCLIViewSanitizesLabels(t *testing.T) {
	evil := "x\x1b[2J\x07\ry"
	p, view := dagTerminalPlan(map[string][]string{evil: {}}, evil)
	view.Run = "run\x1b]0;title\x07"
	view.Status = "ok\r"
	outputs := []string{
		dagCLIViewStatusline(t, p, view, DAGStatuslineOptions{Width: 80, Color: true}),
		dagCLIViewTUI(t, p, view, DAGTUIOptions{Width: 80, Color: true}),
	}
	for _, out := range outputs {
		if rest := dagCLIViewSGR.ReplaceAllString(out, ""); strings.ContainsAny(rest, "\x1b\x07\r\x00") {
			t.Fatalf("labels must be sanitized: %q", out)
		}
		if !strings.Contains(out, `x\x1b[2J\x07\x0dy`) {
			t.Fatalf("sanitized task name must stay visible: %q", out)
		}
	}
}

func TestDAGTUIFrameWidthAndColor(t *testing.T) {
	deps := map[string][]string{"A": {}, "B": {"A"}, "C": {"A"}, "D": {"A"}, "E": {"B", "C", "D"}}
	p, view := dagTerminalPlan(deps, "A", "B", "C", "D", "E")
	view.Nodes[0].State = "done"
	view.Nodes[1].State = "running"
	elapsed := int64(3300)
	view.Nodes[1].ElapsedMS = &elapsed
	agent, model := "codex", "gpt"
	view.Nodes[1].Agent, view.Nodes[1].Model = &agent, &model
	view.Nodes[2].State = "failed"
	for _, width := range []int{80, 120} {
		o := DAGTUIOptions{Width: width, NowMS: 0, Interval: 2 * time.Second}
		plain := dagCLIViewTUI(t, p, view, o)
		o.Color = true
		colored := dagCLIViewTUI(t, p, view, o)
		if strings.Contains(plain, "\x1b") || dagCLIViewSGR.ReplaceAllString(colored, "") != plain {
			t.Fatalf("width %d: NO_COLOR must equal colored minus SGR", width)
		}
		for _, sgr := range []string{"38;5;114m", "38;5;214m", "38;5;203m", "38;5;239m"} {
			if !strings.Contains(colored, sgr) {
				t.Fatalf("width %d: palette color %s missing: %q", width, sgr, colored)
			}
		}
		for i, line := range dagCLIViewLines(plain) {
			if n := utf8.RuneCountInString(line); n > width {
				t.Fatalf("width %d: line %d has %d runes: %q", width, i+1, n, plain)
			}
		}
		for _, want := range []string{"╭", "╯", "✓ A", "⠋ B", "✗ C", "◌ D", "◌ E", "running · 3.3s", "codex/gpt · try 0/1", "← B, C, D", "1/5 done", "refresh 2s"} {
			if !strings.Contains(plain, want) {
				t.Fatalf("width %d: frame lacks %q: %s", width, want, plain)
			}
		}
		for _, seq := range []string{"\x1b[H", "\x1b[2J", "\x1b[?"} {
			if strings.Contains(colored, seq) {
				t.Fatalf("renderer must leave screen control to the caller: %q", colored)
			}
		}
	}
	// 80 columns fit two 28-column cards per row, 120 fit four, so the
	// three-node layer wraps only at 80.
	rows := func(out string) int { return strings.Count(out, "╭") }
	if a, b := rows(dagCLIViewTUI(t, p, view, DAGTUIOptions{Width: 80})), rows(dagCLIViewTUI(t, p, view, DAGTUIOptions{Width: 120})); a != 5 || b != 5 {
		t.Fatalf("every node needs one card: 80=%d 120=%d", a, b)
	}
	if a, b := len(dagCLIViewLines(dagCLIViewTUI(t, p, view, DAGTUIOptions{Width: 80}))), len(dagCLIViewLines(dagCLIViewTUI(t, p, view, DAGTUIOptions{Width: 120}))); a <= b {
		t.Fatalf("80 columns must wrap the three-card layer: 80=%d lines 120=%d lines", a, b)
	}
	clipped := dagCLIViewTUI(t, p, view, DAGTUIOptions{Width: 80, Height: 10})
	if got := dagCLIViewLines(clipped); len(got) != 9 || !strings.Contains(got[8], "more lines") {
		t.Fatalf("height 10 must clip to 9 lines with a marker: %q", clipped)
	}
	final := dagCLIViewTUI(t, p, view, DAGTUIOptions{Width: 80, Final: true})
	if !strings.Contains(final, "final snapshot") || strings.Contains(final, "ctrl-c") {
		t.Fatalf("final frame footer: %q", final)
	}
}

func TestDAGCLIViewRejectsMismatchedSnapshot(t *testing.T) {
	p, view := dagCLIViewChain()
	view.Nodes = view.Nodes[:4]
	var out bytes.Buffer
	if err := RenderDAGStatusline(&out, p, view, DAGStatuslineOptions{}); err == nil || out.Len() != 0 {
		t.Fatalf("statusline must reject a mismatched snapshot without output: %v %q", err, out.String())
	}
	if err := RenderDAGTUI(&out, p, view, DAGTUIOptions{}); err == nil || out.Len() != 0 {
		t.Fatalf("tui must reject a mismatched snapshot without output: %v %q", err, out.String())
	}
}

func TestReadDAGStatuslineHostIsBounded(t *testing.T) {
	pipe := func(t *testing.T) (*os.File, *os.File) {
		t.Helper()
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { r.Close(); w.Close() })
		return r, w
	}
	t.Run("closed", func(t *testing.T) {
		r, w := pipe(t)
		w.WriteString(`{"terminal_width":90}`)
		w.Close()
		if got := ReadDAGStatuslineHost(r, 1024, 5*time.Second); string(got) != `{"terminal_width":90}` {
			t.Fatalf("payload: %q", got)
		}
	})
	t.Run("held_open", func(t *testing.T) {
		r, w := pipe(t)
		w.WriteString(`{"a":1}`)
		start := time.Now()
		got := ReadDAGStatuslineHost(r, 1024, 100*time.Millisecond)
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("open stdin must not block past the budget: %v", elapsed)
		}
		if string(got) != `{"a":1}` {
			t.Fatalf("payload received before the budget must be kept: %q", got)
		}
	})
	t.Run("oversized", func(t *testing.T) {
		r, w := pipe(t)
		go func() {
			w.Write(bytes.Repeat([]byte("x"), 1<<20))
			w.Close()
		}()
		if got := ReadDAGStatuslineHost(r, 1024, 5*time.Second); got != nil {
			t.Fatalf("payload over the limit must be ignored: %d bytes", len(got))
		}
	})
	if got := ReadDAGStatuslineHost(nil, 1024, time.Second); got != nil {
		t.Fatalf("nil stdin: %q", got)
	}
}
