package dagspec

// Glob intersection for write_overlap. The dialect matches internal/glob:
// `*` and `?` stay inside one path segment, `**` spans segments and `**/` may
// match nothing. Two globs overlap when some path matches both; this is
// decided exactly by a breadth-first search over the product of their NFAs.

const (
	clsLit      = iota // one literal byte
	clsNonSlash        // any byte except '/'
	clsAny             // any byte
)

type edge struct {
	cls  int
	lit  byte
	to   int
	free bool // epsilon edge: consumes nothing
}

type nfa struct {
	edges [][]edge
	final int
}

func compileNFA(g string) nfa {
	var n nfa
	state := func() int { n.edges = append(n.edges, nil); return len(n.edges) - 1 }
	cur := state()
	link := func(from int, e edge) { n.edges[from] = append(n.edges[from], e) }
	for i := 0; i < len(g); {
		switch {
		case len(g)-i >= 3 && g[i:i+3] == "**/":
			loop, next := state(), state()
			link(cur, edge{free: true, to: next})
			link(cur, edge{free: true, to: loop})
			link(loop, edge{cls: clsAny, to: loop})
			link(loop, edge{cls: clsLit, lit: '/', to: next})
			cur, i = next, i+3
		case len(g)-i >= 2 && g[i:i+2] == "**":
			link(cur, edge{cls: clsAny, to: cur})
			i += 2
		case g[i] == '*':
			link(cur, edge{cls: clsNonSlash, to: cur})
			i++
		case g[i] == '?':
			next := state()
			link(cur, edge{cls: clsNonSlash, to: next})
			cur, i = next, i+1
		default:
			next := state()
			link(cur, edge{cls: clsLit, lit: g[i], to: next})
			cur, i = next, i+1
		}
	}
	n.final = cur
	return n
}

// classesMeet reports whether one byte can satisfy both edges.
func classesMeet(a, b edge) bool {
	switch {
	case a.cls == clsLit && b.cls == clsLit:
		return a.lit == b.lit
	case a.cls == clsLit:
		return b.cls == clsAny || a.lit != '/'
	case b.cls == clsLit:
		return a.cls == clsAny || b.lit != '/'
	default:
		return true // NonSlash/Any share every non-slash byte
	}
}

func globsIntersect(a, b string) bool {
	na, nb := compileNFA(a), compileNFA(b)
	type pair struct{ x, y int }
	seen := map[pair]bool{}
	queue := []pair{{0, 0}}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		if seen[p] {
			continue
		}
		seen[p] = true
		if p.x == na.final && p.y == nb.final {
			return true
		}
		for _, ea := range na.edges[p.x] {
			if ea.free {
				queue = append(queue, pair{ea.to, p.y})
			}
		}
		for _, eb := range nb.edges[p.y] {
			if eb.free {
				queue = append(queue, pair{p.x, eb.to})
			}
		}
		for _, ea := range na.edges[p.x] {
			if ea.free {
				continue
			}
			for _, eb := range nb.edges[p.y] {
				if !eb.free && classesMeet(ea, eb) {
					queue = append(queue, pair{ea.to, eb.to})
				}
			}
		}
	}
	return false
}
