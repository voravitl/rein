package supervisor

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	dagGraphDefaultWidth = 80
	dagGraphMinWidth     = 16
	dagGraphCardWidth    = 30
)

// DAGGraphOptions controls the plain-text terminal graph. Width is the column
// budget (zero or negative uses 80; values below 16 render at 16). Caption is
// an optional extra line, sanitized like every other label. The graph never
// emits color or cursor escapes; callers own any TTY redraw sequence.
type DAGGraphOptions struct {
	Width   int
	Caption string
}

// DAGGraphWidth returns the column budget the renderer uses for width.
func DAGGraphWidth(width int) int {
	if width <= 0 {
		return dagGraphDefaultWidth
	}
	if width < dagGraphMinWidth {
		return dagGraphMinWidth
	}
	return width
}

// DAGTerminalColumns picks the graph width: the kernel-reported size of a
// terminal out (Linux/macOS), then a positive integer COLUMNS value, then 80.
func DAGTerminalColumns(out *os.File, columns string) int {
	if out != nil {
		if n := dagTerminalColumns(out); n > 0 {
			return n
		}
	}
	if n, err := strconv.Atoi(strings.TrimSpace(columns)); err == nil && n > 0 {
		return n
	}
	return dagGraphDefaultWidth
}

// RenderDAGGraph draws the status snapshot as layered cards, top to bottom.
// Every node appears exactly once with its state, kind and attempt counter;
// every plan dependency appears on both cards as "<- dependency" and
// "-> dependent". Layers are ordered by number and cards by task name, so an
// unchanged snapshot always renders identical bytes. Output is ASCII only and
// no line exceeds DAGGraphWidth(options.Width) columns; long labels wrap
// instead of being truncated. Nothing is written when the snapshot does not
// match the plan.
func RenderDAGGraph(w io.Writer, p Plan, view DAGStatus, options DAGGraphOptions) error {
	lines, err := dagGraphLines(p, view, options)
	if err != nil {
		return err
	}
	var b strings.Builder
	for _, line := range lines {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	_, err = io.WriteString(w, b.String())
	return err
}

func dagGraphLines(p Plan, view DAGStatus, options DAGGraphOptions) ([]string, error) {
	width := DAGGraphWidth(options.Width)
	nodes := make(map[string]DAGNodeStatus, len(view.Nodes))
	for _, n := range view.Nodes {
		nodes[n.Task] = n
	}
	if len(view.Nodes) != len(p.Tasks) || len(nodes) != len(p.Tasks) {
		return nil, errors.New("status snapshot does not match plan tasks")
	}
	deps := make(map[string][]string, len(p.Tasks))
	dependents := make(map[string][]string, len(p.Tasks))
	edges := 0
	for _, t := range p.Tasks {
		if _, ok := nodes[t.Task]; !ok {
			return nil, errors.New("status snapshot does not match plan tasks")
		}
		d := append([]string(nil), t.DependsOn...)
		sort.Strings(d)
		for _, dep := range d {
			if _, ok := nodes[dep]; !ok {
				return nil, fmt.Errorf("plan dependency %s is not a status node", dagTerminalText(dep))
			}
			dependents[dep] = append(dependents[dep], t.Task)
			edges++
		}
		deps[t.Task] = d
	}
	for name := range dependents {
		sort.Strings(dependents[name])
	}
	layers := map[int][]string{}
	var order []int
	for _, n := range view.Nodes {
		if _, ok := layers[n.Layer]; !ok {
			order = append(order, n.Layer)
		}
		layers[n.Layer] = append(layers[n.Layer], n.Task)
	}
	sort.Ints(order)

	header := fmt.Sprintf("Run %s status %s nodes %d edges %d", dagTerminalText(view.Run), dagTerminalText(view.Status), len(view.Nodes), edges)
	if view.InvocationActive {
		header += " invocation active"
	}
	lines := dagGraphWrap(header, width)
	if options.Caption != "" {
		lines = append(lines, dagGraphWrap(dagTerminalText(options.Caption), width)...)
	}
	cardWidth := min(dagGraphCardWidth, width)
	cols := max(1, (width+1)/(cardWidth+1))
	blank := "|" + strings.Repeat(" ", cardWidth-2) + "|"
	for i, layer := range order {
		names := layers[layer]
		sort.Strings(names)
		if i > 0 {
			lines = append(lines, "  |", "  v")
		}
		lines = append(lines, dagGraphWrap(fmt.Sprintf("layer %d", layer), width)...)
		for start := 0; start < len(names); start += cols {
			end := min(start+cols, len(names))
			var cards [][]string
			height := 0
			for _, name := range names[start:end] {
				card := dagGraphCard(nodes[name], deps[name], dependents[name], cardWidth)
				cards = append(cards, card)
				height = max(height, len(card))
			}
			for row := 0; row < height; row++ {
				parts := make([]string, len(cards))
				for k, card := range cards {
					switch {
					case row == height-1:
						parts[k] = card[len(card)-1]
					case row < len(card)-1:
						parts[k] = card[row]
					default:
						parts[k] = blank
					}
				}
				lines = append(lines, strings.Join(parts, " "))
			}
		}
	}
	return lines, nil
}

func dagGraphCard(n DAGNodeStatus, deps, next []string, width int) []string {
	inner := width - 4
	var body []string
	body = append(body, dagGraphFill("", "", []string{dagTerminalText(n.Task)}, inner)...)
	body = append(body, dagGraphFill("", "", []string{dagTerminalText(n.State), dagTerminalText(n.Kind)}, inner)...)
	body = append(body, dagGraphFill("", "", []string{"attempt", fmt.Sprintf("%d/%d", n.Attempt, n.MaxAttempts)}, inner)...)
	if n.Agent != nil && n.Model != nil {
		model := *n.Agent + "/" + *n.Model
		if n.Effort != nil && *n.Effort != "" {
			model += ":" + *n.Effort
		}
		body = append(body, dagGraphFill("", "", []string{dagTerminalText(model)}, inner)...)
	}
	body = append(body, dagGraphList("<- ", deps, inner)...)
	body = append(body, dagGraphList("-> ", next, inner)...)
	border := "+" + strings.Repeat("-", width-2) + "+"
	card := []string{border}
	for _, line := range body {
		card = append(card, "| "+line+strings.Repeat(" ", inner-len(line))+" |")
	}
	return append(card, border)
}

// dagGraphList renders one edge direction; "none" marks a root or a leaf.
func dagGraphList(prefix string, names []string, width int) []string {
	if len(names) == 0 {
		return dagGraphFill(prefix, strings.Repeat(" ", len(prefix)), []string{"none"}, width)
	}
	tokens := make([]string, len(names))
	for i, name := range names {
		tokens[i] = dagTerminalText(name)
		if i < len(names)-1 {
			tokens[i] += ","
		}
	}
	return dagGraphFill(prefix, strings.Repeat(" ", len(prefix)), tokens, width)
}

func dagGraphWrap(text string, width int) []string {
	return dagGraphFill("", "  ", strings.Fields(text), width)
}

// dagGraphFill greedily packs space-separated ASCII tokens into lines of at
// most width bytes. Tokens longer than a line are split, never dropped.
func dagGraphFill(prefix, indent string, tokens []string, width int) []string {
	if len(indent) >= width {
		indent = ""
	}
	if len(prefix) >= width {
		prefix = ""
	}
	var lines []string
	line, fresh := prefix, true
	for _, token := range tokens {
		if token == "" {
			continue
		}
		sep := " "
		if fresh {
			sep = ""
		}
		if len(line)+len(sep)+len(token) <= width {
			line += sep + token
			fresh = false
			continue
		}
		if !fresh {
			lines = append(lines, line)
			line, fresh = indent, true
		}
		for len(line)+len(token) > width {
			n := width - len(line)
			line += token[:n]
			token = token[n:]
			lines = append(lines, line)
			line = indent
		}
		line += token
		fresh = false
	}
	return append(lines, line)
}

// dagTerminalText renders labels as visible ASCII: control bytes, DEL and
// invalid UTF-8 become \xNN, other non-ASCII runes become \uNNNN or
// \UNNNNNNNN, and backslash is doubled so escapes stay unambiguous.
func dagTerminalText(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size <= 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case r == '\\':
			b.WriteString(`\\`)
		case r >= 0x20 && r < 0x7f:
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r <= 0xffff:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			fmt.Fprintf(&b, `\U%08x`, r)
		}
		i += size
	}
	return b.String()
}
