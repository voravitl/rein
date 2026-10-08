package glob

import "testing"

func TestMatch(t *testing.T) {
	cases := []struct {
		path  string
		globs []string
		want  bool
	}{
		{"backend/App.Api/Routing/A.cs", []string{"backend/App.Api/Routing/**"}, true},
		{"backend/App.Api/RoutingX/A.cs", []string{"backend/App.Api/Routing/**"}, false},
		{"frontend/node_modules/x/y.js", []string{"**/node_modules/**"}, true},
		{"node_modules/x", []string{"**/node_modules/**"}, true},
		{"VERSION", []string{"VERSION"}, true},
		{"a/VERSION", []string{"VERSION"}, false},
		{".github/ci.yml", []string{".github/**"}, true},
		{"./src/a.ts", []string{"src/*.ts"}, true},
		{"src/sub/a.ts", []string{"src/*.ts"}, false},
		{"docs/design/x.md", []string{"docs/design/**"}, true},
		{"a+b(c).cs", []string{"a+b(c).cs"}, true}, // regex metacharacters are literal
		{"x.cs", nil, false},
	}
	for _, c := range cases {
		if got := Match(c.path, c.globs); got != c.want {
			t.Errorf("Match(%q, %v) = %v, want %v", c.path, c.globs, got, c.want)
		}
	}
}
