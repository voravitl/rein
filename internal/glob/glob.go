// Package glob matches repo-relative paths against task-contract globs:
// `*` and `?` stay inside one path segment, `**` spans segments, and `**/` may match nothing.
// Paths are always slash-separated (callers convert with filepath.ToSlash), so Windows paths behave the same.
package glob

import (
	"regexp"
	"strings"
	"sync"
)

var (
	mu    sync.Mutex
	cache = map[string]*regexp.Regexp{}
)

func compile(g string) *regexp.Regexp {
	mu.Lock()
	defer mu.Unlock()
	if rx, ok := cache[g]; ok {
		return rx
	}
	var b strings.Builder
	b.WriteString(`\A`)
	for i := 0; i < len(g); {
		switch {
		case strings.HasPrefix(g[i:], "**/"):
			b.WriteString(`(?:.*/)?`)
			i += 3
		case strings.HasPrefix(g[i:], "**"):
			b.WriteString(`.*`)
			i += 2
		case g[i] == '*':
			b.WriteString(`[^/]*`)
			i++
		case g[i] == '?':
			b.WriteString(`[^/]`)
			i++
		default:
			b.WriteString(regexp.QuoteMeta(g[i : i+1]))
			i++
		}
	}
	b.WriteString(`\z`)
	rx := regexp.MustCompile(b.String())
	cache[g] = rx
	return rx
}

// Match reports whether the slash-separated relative path matches any glob.
func Match(path string, globs []string) bool {
	path = strings.TrimPrefix(path, "./") // TrimPrefix, not TrimLeft: ".github/x" keeps its dot
	for _, g := range globs {
		if compile(g).MatchString(path) {
			return true
		}
	}
	return false
}
