package guard

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestShellWriteOwnership(t *testing.T) {
	wt, c := setup(t)
	deny := []string{
		"rm frontend/src/x.ts",
		"touch frontend/src/new.ts",
		"echo changed > frontend/src/new.ts",
		"rm -rf frontend/src", "rmdir frontend/src", "unlink frontend/src/x.ts",
		"mv frontend/src/x.ts backend/Routing/moved.ts",
		"mv backend/Routing/A.cs frontend/src/new.ts",
		"cp backend/Routing/A.cs frontend/src/new.ts",
		"cp backend/Routing/A.cs frontend/src/x.ts",
		"tee frontend/src/new.ts", "truncate -s 0 frontend/src/new.ts",
		"git rm frontend/src/x.ts", "git mv frontend/src/x.ts backend/Routing/moved.ts",
		"git -C frontend rm src/x.ts",
		"find frontend/src -delete", "find backend -delete",
		"find frontend/src -exec rm {} +",
		"find frontend/src -exec touch backend/Routing/new.cs {} +",
		"env rm frontend/src/x.ts", "sh -c 'touch frontend/src/new.ts'",
		"rm -rf *", "touch scratch.txt", "echo build > frontend/dist/new.js",
		"rm VERSION", "touch VERSION", "echo x > docs/design/new.md",
		"rm .git", "touch .codex/hooks.json",
		"echo x > " + filepath.Join(filepath.Dir(c.ReportPath), "other.md"),
	}
	allow := []string{
		"touch backend/Routing/new.cs", "echo changed > backend/Routing/new.cs",
		"sed -i 's/x/y/' backend/Routing/A.cs", "rm backend/Routing/A.cs",
		"rm -rf backend/Routing", "rm backend/Routing/*.tmp",
		"mv backend/Routing/A.cs backend/Routing/new.cs",
		"cp frontend/src/x.ts backend/Routing/copy.ts",
		"git rm backend/Routing/A.cs", "git mv backend/Routing/A.cs backend/Routing/new.cs",
		"git -C backend/Routing rm A.cs", "find backend/Routing -delete",
		"find backend/Routing -exec rm {} +", "find frontend/src -name '*.ts'",
		"find frontend/src -exec grep x {} +",
		"echo build > backend/Routing/build.txt", "npm run build", "go test ./...",
		"touch /tmp/rein-ownership-scratch", "rm /tmp/rein-ownership-scratch",
		"echo report > " + c.ReportPath, "echo x > /dev/null",
	}
	for _, cmd := range deny {
		t.Run(cmd, func(t *testing.T) {
			if out := bashCase(t, wt, cmd); !strings.Contains(out, `"deny"`) {
				t.Errorf("expected ownership denial, got %q", out)
			}
		})
	}
	for _, cmd := range allow {
		t.Run(cmd, func(t *testing.T) {
			if out := bashCase(t, wt, cmd); out != "" {
				t.Errorf("expected allowed operation, got %q", out)
			}
		})
	}
}
