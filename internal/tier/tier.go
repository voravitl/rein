// Package tier implements deterministic tier evaluation for task risk classification (ADR 0002 B2.1).
package tier

import (
	"path/filepath"

	"github.com/voravitl/rein/internal/glob"
)

// Tier represents task risk level.
type Tier int

const (
	T1 Tier = 1 // low risk
	T2 Tier = 2 // medium risk
	T3 Tier = 3 // high risk / sensitive
)

func (t Tier) String() string {
	switch t {
	case T1:
		return "T1"
	case T2:
		return "T2"
	case T3:
		return "T3"
	default:
		return "unknown"
	}
}

// EvaluateFromAllowGlobs computes tier from contract allow globs against sensitive paths.
// Matching any sensitive path forces T3. Cost never lowers a tier.
func EvaluateFromAllowGlobs(allowGlobs, sensitivePaths []string) Tier {
	if len(allowGlobs) == 0 {
		return T1
	}
	if len(sensitivePaths) == 0 {
		return T1
	}

	for _, allowPattern := range allowGlobs {
		for _, sensPattern := range sensitivePaths {
			if globsIntersect(allowPattern, sensPattern) {
				return T3
			}
		}
	}
	return T1
}

// EvaluateFromChangedFiles recomputes tier from actual changed files.
// Can only escalate tier (T1 -> T2/T3, never down).
func EvaluateFromChangedFiles(changedFiles, sensitivePaths []string, baseTier Tier) Tier {
	tier := baseTier
	if len(changedFiles) == 0 {
		return tier
	}

	for _, file := range changedFiles {
		if matchesSensitive(file, sensitivePaths) {
			tier = max(tier, T3)
		}
	}

	return tier
}

func matchesSensitive(file string, sensitivePaths []string) bool {
	for _, pattern := range sensitivePaths {
		matched, err := filepath.Match(pattern, file)
		if err == nil && matched {
			return true
		}
	}
	// Also check using glob.Match for more complex patterns
	return glob.Match(file, sensitivePaths)
}

// globsIntersect returns true if two glob patterns could match the same file.
// Conservative: returns true if uncertain.
func globsIntersect(a, b string) bool {
	// If either pattern is a prefix of the other, they intersect
	if len(a) > len(b) {
		a, b = b, a
	}
	// Simple heuristic: if patterns share a common non-wildcard prefix, they might intersect
	// For full correctness, we'd need to enumerate all possible matches, which is intractable.
	// Conservative approach: assume intersection unless provably disjoint.

	// Check for obvious non-intersection
	aBase := filepath.Dir(a)
	bBase := filepath.Dir(b)

	// If base directories are completely different and have no wildcards, they don't intersect
	if aBase != "." && bBase != "." && !hasWildcard(aBase) && !hasWildcard(bBase) {
		if !filepath.HasPrefix(aBase, bBase) && !filepath.HasPrefix(bBase, aBase) {
			return false
		}
	}

	// Otherwise assume possible intersection (conservative)
	return true
}

func hasWildcard(s string) bool {
	for _, c := range s {
		if c == '*' || c == '?' || c == '[' {
			return true
		}
	}
	return false
}

func max(a, b Tier) Tier {
	if a > b {
		return a
	}
	return b
}
