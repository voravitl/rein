package supervisor

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	dagStatuslineDefaultLines = 4
	dagStatuslineMaxWidth     = 4096
	dagStatuslineFallbackMax  = 80
	dagCLIBarMax              = 40
	dagTUICardWidth           = 26
)

// 256-color SGR parameters of the approved terminal look.
const (
	dagCLIDim    = "38;5;245"
	dagCLIFaint  = "38;5;239"
	dagCLIText   = "38;5;252"
	dagCLIBold   = "1;38;5;252"
	dagCLIAccent = "1;38;5;111"
	dagCLIOK     = "38;5;114"
	dagCLIRun    = "38;5;214"
	dagCLIFail   = "38;5;203"
	dagCLIBlock  = "38;5;176"
)

var dagCLISpinner = [...]string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// DAGStatuslineOptions controls the compact status-line view. Width is the
// column budget (zero or negative uses 80). Lines caps the total line count
// (zero or negative uses 4). Color enables 256-color SGR escapes only; the
// statusline never moves the cursor or clears the screen. NowMS (Unix ms)
// selects the spinner frame for active nodes.
type DAGStatuslineOptions struct {
	Width int
	Lines int
	Color bool
	NowMS int64
}

// DAGTUIOptions controls the full-screen card view. Width is the column
// budget (see DAGGraphWidth). Height, when positive, clips the frame to
// Height-1 lines so a redraw never scrolls. Interval is shown in the footer;
// Final marks the last frame printed after the alternate screen is left.
type DAGTUIOptions struct {
	Width    int
	Height   int
	Color    bool
	NowMS    int64
	Interval time.Duration
	Final    bool
}

type dagCLISpan struct{ text, color string }

type dagCLILine []dagCLISpan

func (l dagCLILine) width() int {
	n := 0
	for _, s := range l {
		n += utf8.RuneCountInString(s.text)
	}
	return n
}

// clamp keeps at most width runes; a cut line ends with an ellipsis.
func (l dagCLILine) clamp(width int) dagCLILine {
	if width <= 0 {
		return nil
	}
	if l.width() <= width {
		return l
	}
	var out dagCLILine
	left := width - 1
	for _, s := range l {
		if left <= 0 {
			break
		}
		r := []rune(s.text)
		if len(r) > left {
			r = r[:left]
		}
		out = append(out, dagCLISpan{string(r), s.color})
		left -= len(r)
	}
	return append(out, dagCLISpan{"…", dagCLIFaint})
}

// render emits SGR only around non-empty colored spans, so the colored
// output with SGR removed is byte-identical to the uncolored output.
func (l dagCLILine) render(color bool) string {
	var b strings.Builder
	for _, s := range l {
		if s.text == "" {
			continue
		}
		if color && s.color != "" {
			b.WriteString("\x1b[" + s.color + "m" + s.text + "\x1b[0m")
		} else {
			b.WriteString(s.text)
		}
	}
	return b.String()
}

func dagCLIWrite(w io.Writer, lines []dagCLILine, width int, color bool) error {
	var b strings.Builder
	for _, line := range lines {
		b.WriteString(line.clamp(width).render(color))
		b.WriteByte('\n')
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// dagCLIFrame maps Unix milliseconds to one of ten spinner frames, advancing
// every 100 ms.
func dagCLIFrame(nowMS int64) int {
	f := (nowMS / 100) % int64(len(dagCLISpinner))
	if f < 0 {
		f += int64(len(dagCLISpinner))
	}
	return int(f)
}

// dagCLIGlyph returns the node glyph and its color. The glyphs ✓ ✗ ⊘ ◌ ○ and
// the Braille spinner are reserved for nodes; headers never use them.
func dagCLIGlyph(state string, frame int) (string, string) {
	switch state {
	case "done":
		return "✓", dagCLIOK
	case "failed":
		return "✗", dagCLIFail
	case "blocked":
		return "⊘", dagCLIBlock
	case "pending":
		return "◌", dagCLIDim
	case "ready":
		return "○", dagCLIText
	case "preparing", "running", "finalizing":
		return dagCLISpinner[frame%len(dagCLISpinner)], dagCLIRun
	case "retry":
		return "↻", dagCLIRun
	case "wait":
		return "◇", dagCLIRun
	case "cancelled":
		return "⊗", dagCLIBlock
	}
	return "?", dagCLIDim
}

func dagCLIActive(state string) bool {
	return state == "preparing" || state == "running" || state == "finalizing"
}

func dagCLIStatusColor(status string) string {
	switch status {
	case "done":
		return dagCLIOK
	case "partial", "reconciliation":
		return dagCLIFail
	}
	return dagCLIRun
}

// dagCLITruncate keeps at most width runes, ending a cut string with "…".
func dagCLITruncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	return string(r[:width-1]) + "…"
}

func dagCLIPad(s string, width int) string {
	s = dagCLITruncate(s, width)
	return s + strings.Repeat(" ", max(0, width-utf8.RuneCountInString(s)))
}

type dagCLIModel struct {
	layers [][]DAGNodeStatus
	deps   map[string][]string
	done   int
	total  int
}

// newDAGCLIModel groups the snapshot by layer (ascending) and task name, so an
// unchanged snapshot always renders identical bytes. It rejects a snapshot
// that does not match the plan, like RenderDAGGraph.
func newDAGCLIModel(p Plan, view DAGStatus) (dagCLIModel, error) {
	m := dagCLIModel{deps: make(map[string][]string, len(p.Tasks)), total: len(view.Nodes)}
	nodes := make(map[string]DAGNodeStatus, len(view.Nodes))
	for _, n := range view.Nodes {
		nodes[n.Task] = n
	}
	if len(view.Nodes) != len(p.Tasks) || len(nodes) != len(p.Tasks) {
		return m, errors.New("status snapshot does not match plan tasks")
	}
	for _, t := range p.Tasks {
		if _, ok := nodes[t.Task]; !ok {
			return m, errors.New("status snapshot does not match plan tasks")
		}
		d := append([]string(nil), t.DependsOn...)
		sort.Strings(d)
		for _, dep := range d {
			if _, ok := nodes[dep]; !ok {
				return m, fmt.Errorf("plan dependency %s is not a status node", dagTerminalText(dep))
			}
		}
		m.deps[t.Task] = d
	}
	byLayer := map[int][]DAGNodeStatus{}
	var order []int
	for _, n := range view.Nodes {
		if _, ok := byLayer[n.Layer]; !ok {
			order = append(order, n.Layer)
		}
		byLayer[n.Layer] = append(byLayer[n.Layer], n)
		if n.State == "done" {
			m.done++
		}
	}
	sort.Ints(order)
	for _, layer := range order {
		row := byLayer[layer]
		sort.Slice(row, func(i, j int) bool { return row[i].Task < row[j].Task })
		m.layers = append(m.layers, row)
	}
	return m, nil
}

// DAGStatuslineWidth picks the statusline width: a positive integer COLUMNS
// value, then a positive integer "terminal_width" from the host's JSON object
// on stdin, then 80. Any other host payload is ignored.
func DAGStatuslineWidth(columns string, host []byte) int {
	if n, err := strconv.Atoi(strings.TrimSpace(columns)); err == nil && n > 0 {
		return min(n, dagStatuslineMaxWidth)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(host, &fields) == nil {
		var n int
		if raw, ok := fields["terminal_width"]; ok && json.Unmarshal(raw, &n) == nil && n > 0 {
			return min(n, dagStatuslineMaxWidth)
		}
	}
	return dagGraphDefaultWidth
}

// DAGStatuslineNowMS returns the integer Unix-millisecond override when it
// parses, else now in Unix milliseconds.
func DAGStatuslineNowMS(override string, now time.Time) int64 {
	if ms, err := strconv.ParseInt(strings.TrimSpace(override), 10, 64); err == nil {
		return ms
	}
	return now.UnixMilli()
}

// DAGStatuslineFallback formats the single failure line a status-line host
// shows instead of going blank: "rein: <reason>", visible ASCII only, at most
// 80 runes, without a trailing newline.
func DAGStatuslineFallback(reason string) string {
	line := "rein: " + dagTerminalText(strings.Join(strings.Fields(reason), " "))
	if len(line) > dagStatuslineFallbackMax {
		line = line[:dagStatuslineFallbackMax-3] + "..."
	}
	return line
}

// RenderDAGStatusline draws a compact, height-bounded DAG for agent CLI status
// lines: one header line (run, status, a width-sized progress bar, done/total)
// and one row per layer of "<glyph> <task>" items. At most Lines lines are
// written; on overflow the last line reads "+k more" (k hidden nodes) and the
// window starts at the first layer that is not fully done. No line exceeds
// Width runes. Nothing is written when the snapshot does not match the plan.
func RenderDAGStatusline(w io.Writer, p Plan, view DAGStatus, o DAGStatuslineOptions) error {
	m, err := newDAGCLIModel(p, view)
	if err != nil {
		return err
	}
	width := o.Width
	if width <= 0 {
		width = dagGraphDefaultWidth
	}
	limit := o.Lines
	if limit <= 0 {
		limit = dagStatuslineDefaultLines
	}
	frame := dagCLIFrame(o.NowMS)
	var lines []dagCLILine
	switch {
	case 1+len(m.layers) <= limit:
		lines = append(lines, dagStatuslineHeader(view, m, width, ""))
		for _, row := range m.layers {
			lines = append(lines, dagStatuslineRow(row, frame, width))
		}
	case limit == 1:
		lines = append(lines, dagStatuslineHeader(view, m, width, fmt.Sprintf("+%d more", m.total)))
	default:
		// Window from the first layer that is not fully done (the last
		// layers when everything is done), kept inside the layer list.
		keep := limit - 2
		start := len(m.layers)
		for i, row := range m.layers {
			if !dagCLIRowDone(row) {
				start = i
				break
			}
		}
		start = min(start, len(m.layers)-keep)
		hidden := m.total
		lines = append(lines, dagStatuslineHeader(view, m, width, ""))
		for _, row := range m.layers[start : start+keep] {
			lines = append(lines, dagStatuslineRow(row, frame, width))
			hidden -= len(row)
		}
		lines = append(lines, dagCLILine{{fmt.Sprintf("+%d more", hidden), dagCLIDim}})
	}
	return dagCLIWrite(w, lines, width, o.Color)
}

func dagCLIRowDone(row []DAGNodeStatus) bool {
	for _, n := range row {
		if n.State != "done" {
			return false
		}
	}
	return true
}

// dagStatuslineHeader keeps the done/total count and any extra marker
// visible: the run name is shortened first, then the bar is dropped.
func dagStatuslineHeader(view DAGStatus, m dagCLIModel, width int, extra string) dagCLILine {
	run, status := dagTerminalText(view.Run), dagTerminalText(view.Status)
	count := fmt.Sprintf("%d/%d", m.done, m.total)
	var tail dagCLILine
	tail = append(tail, dagCLISpan{"  ", ""}, dagCLISpan{count, dagCLIDim})
	if extra != "" {
		tail = append(tail, dagCLISpan{"  ", ""}, dagCLISpan{extra, dagCLIDim})
	}
	fixed := utf8.RuneCountInString("rein ·  · ") + utf8.RuneCountInString(status)
	if room := width - fixed - tail.width(); utf8.RuneCountInString(run) > room {
		run = dagCLITruncate(run, max(1, room))
	}
	line := dagCLILine{
		{"rein", dagCLIAccent}, {" · ", dagCLIFaint}, {run, dagCLIText},
		{" · ", dagCLIFaint}, {status, dagCLIStatusColor(view.Status)},
	}
	if bar := min(dagCLIBarMax, width-line.width()-2-tail.width()); bar >= 4 {
		line = append(line, dagCLISpan{"  ", ""})
		line = append(line, dagCLIBar(m.done, m.total, bar)...)
	}
	line = append(line, tail...)
	if line.width() > width {
		// Too narrow for run and status: keep the counts visible.
		line = append(dagCLILine{{"rein", dagCLIAccent}}, tail...)
	}
	return line
}

func dagCLIBar(done, total, width int) dagCLILine {
	fill := 0
	if total > 0 {
		fill = done * width / total
	}
	return dagCLILine{{strings.Repeat("━", fill), dagCLIOK}, {strings.Repeat("━", width-fill), dagCLIFaint}}
}

func dagStatuslineItem(n DAGNodeStatus, frame int) dagCLILine {
	glyph, color := dagCLIGlyph(n.State, frame)
	nameColor := dagCLIText
	if n.State == "pending" || n.State == "blocked" || n.State == "cancelled" {
		nameColor = dagCLIDim
	}
	return dagCLILine{{glyph, color}, {" ", ""}, {dagTerminalText(n.Task), nameColor}}
}

// dagStatuslineRow packs as many items as fit, followed by "+k more" for the
// rest. If not even the first item fits it is shown truncated.
func dagStatuslineRow(row []DAGNodeStatus, frame, width int) dagCLILine {
	build := func(k int) dagCLILine {
		var line dagCLILine
		for i, n := range row[:k] {
			if i > 0 {
				line = append(line, dagCLISpan{"  ", ""})
			}
			line = append(line, dagStatuslineItem(n, frame)...)
		}
		if k < len(row) {
			line = append(line, dagCLISpan{"  ", ""}, dagCLISpan{fmt.Sprintf("+%d more", len(row)-k), dagCLIDim})
		}
		return line
	}
	for k := len(row); k >= 1; k-- {
		if line := build(k); line.width() <= width {
			return line
		}
	}
	return build(len(row)).clamp(width)
}

// RenderDAGTUI draws the snapshot as rounded, state-colored cards grouped by
// layer, with a header, a progress bar and a footer. Lines never exceed
// DAGGraphWidth(o.Width) runes. It emits only SGR color escapes (none when
// Color is false); callers own alternate-screen and cursor sequences.
func RenderDAGTUI(w io.Writer, p Plan, view DAGStatus, o DAGTUIOptions) error {
	m, err := newDAGCLIModel(p, view)
	if err != nil {
		return err
	}
	width := DAGGraphWidth(o.Width)
	frame := dagCLIFrame(o.NowMS)
	status := dagTerminalText(view.Status)
	header := dagCLILine{
		{"  ", ""}, {"rein", dagCLIAccent}, {" · ", dagCLIFaint}, {dagTerminalText(view.Run), dagCLIText},
		{" · ", dagCLIFaint}, {status, dagCLIStatusColor(view.Status)},
	}
	if elapsed, ok := dagTUIElapsedMax(view); ok {
		header = append(header, dagCLISpan{"  ", ""}, dagCLISpan{elapsed, dagCLIDim})
	}
	if view.InvocationActive {
		header = append(header, dagCLISpan{" · ", dagCLIFaint}, dagCLISpan{"invocation active", dagCLIDim})
	}
	count := fmt.Sprintf("%d/%d done", m.done, m.total)
	progress := dagCLILine{{"  ", ""}}
	if bar := min(dagCLIBarMax, width-6-utf8.RuneCountInString(count)); bar >= 4 {
		progress = append(progress, dagCLIBar(m.done, m.total, bar)...)
		progress = append(progress, dagCLISpan{"  ", ""})
	}
	progress = append(progress, dagCLISpan{count, dagCLIDim})
	lines := []dagCLILine{nil, header, progress, nil}

	cardW := min(dagTUICardWidth, max(8, width-6))
	perLine := max(1, width/(cardW+4))
	for i, row := range m.layers {
		if i > 0 {
			indent := dagCLISpan{strings.Repeat(" ", 2+cardW/2), ""}
			lines = append(lines, dagCLILine{indent, {"│", dagCLIFaint}}, dagCLILine{indent, {"▼", dagCLIFaint}})
		}
		for start := 0; start < len(row); start += perLine {
			chunk := row[start:min(start+perLine, len(row))]
			rows := make([]dagCLILine, 6)
			for k, n := range chunk {
				card := dagTUICard(n, m.deps[n.Task], cardW, frame)
				for r := range rows {
					if k > 0 {
						rows[r] = append(rows[r], dagCLISpan{"  ", ""})
					}
					rows[r] = append(rows[r], card[r]...)
				}
			}
			for r := range rows {
				rows[r] = append(dagCLILine{{"  ", ""}}, rows[r]...)
			}
			lines = append(lines, rows...)
		}
	}
	footer := fmt.Sprintf("ctrl-c quit · refresh %s · spinner 100ms", o.Interval)
	if o.Interval <= 0 {
		footer = "ctrl-c quit · spinner 100ms"
	}
	if o.Final {
		footer = "final snapshot · " + status
	}
	lines = append(lines, nil, dagCLILine{{"  ", ""}, {footer, dagCLIFaint}})

	if o.Height > 0 && len(lines) > o.Height-1 {
		keep := max(0, o.Height-2)
		hidden := len(lines) - keep
		lines = append(lines[:keep:keep], dagCLILine{{"  ", ""}, {fmt.Sprintf("… %d more lines; enlarge the terminal", hidden), dagCLIFaint}})
	}
	return dagCLIWrite(w, lines, width, o.Color)
}

// dagTUICard returns the six lines of one rounded card: border, glyph and
// task, state and elapsed time, agent/model (or kind) and attempts,
// dependencies, border. Each line is exactly cardW+2 runes wide.
func dagTUICard(n DAGNodeStatus, deps []string, cardW, frame int) []dagCLILine {
	glyph, color := dagCLIGlyph(n.State, frame)
	border := dagCLIFaint
	if dagCLIActive(n.State) {
		border = dagCLIRun
	}
	elapsed := "—"
	if n.ElapsedMS != nil {
		elapsed = dagTUIDuration(*n.ElapsedMS)
	}
	who := dagTerminalText(n.Kind)
	if n.Agent != nil && n.Model != nil {
		who = *n.Agent + "/" + *n.Model
		if n.Effort != nil && *n.Effort != "" {
			who += ":" + *n.Effort
		}
		who = dagTerminalText(who)
	}
	who += fmt.Sprintf(" · try %d/%d", n.Attempt, n.MaxAttempts)
	edge := "root"
	if len(deps) > 0 {
		names := make([]string, len(deps))
		for i, d := range deps {
			names[i] = dagTerminalText(d)
		}
		edge = "← " + strings.Join(names, ", ")
	}
	side := dagCLISpan{"│", border}
	body := func(text, textColor string) dagCLILine {
		return dagCLILine{side, {" ", ""}, {dagCLIPad(text, cardW-1), textColor}, side}
	}
	return []dagCLILine{
		{{"╭" + strings.Repeat("─", cardW) + "╮", border}},
		{side, {" ", ""}, {glyph, color}, {" ", ""}, {dagCLIPad(dagTerminalText(n.Task), cardW-3), dagCLIBold}, side},
		body(dagTerminalText(n.State)+" · "+elapsed, color),
		body(who, dagCLIDim),
		body(edge, dagCLIFaint),
		{{"╰" + strings.Repeat("─", cardW) + "╯", border}},
	}
}

func dagTUIDuration(ms int64) string {
	if ms < 60_000 {
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	}
	return (time.Duration(ms) * time.Millisecond).Round(time.Second).String()
}

func dagTUIElapsedMax(view DAGStatus) (string, bool) {
	var best int64
	found := false
	for _, n := range view.Nodes {
		if n.ElapsedMS != nil && (!found || *n.ElapsedMS > best) {
			best, found = *n.ElapsedMS, true
		}
	}
	if !found {
		return "", false
	}
	return dagTUIDuration(best), true
}
