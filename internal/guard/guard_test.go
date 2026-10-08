package guard

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voravitl/rein/internal/contract"
)

// testProfile is a neutral project profile: a live stack of "app-*" containers on 8080/5432.
func testProfile() *contract.Profile {
	return &contract.Profile{Name: "test", ProtectContainerPrefixes: []string{"app-"}, ProtectPorts: []int{8080, 5432},
		OwnerScripts: []string{"scripts/release.sh"},
		DenyCommands: []string{"docker compose", "docker-compose", "psql", "playwright test"},
		DenyPaths:    []string{"VERSION"}}
}

// setup makes a worktree <root>/wts/good with an owned file and a contract for it.
func setup(t *testing.T) (wt string, c *contract.Contract) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(root, "contracts"))
	t.Setenv("PIPELINE_LOGDIR", filepath.Join(root, "logs"))
	wt = filepath.Join(root, "wts", "good")
	for _, d := range []string{".git", "backend/Routing", "frontend/src", "docs/design"} {
		if err := os.MkdirAll(filepath.Join(wt, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"backend/Routing/A.cs", "frontend/src/x.ts", "VERSION", "docs/design/d.md"} {
		if err := os.WriteFile(filepath.Join(wt, f), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c, err := contract.New("good", 169, filepath.Join(root, "run"), "backend/Routing/**", "docs/design/**", "S1,S2",
		filepath.Join(root, "wts"), "", "", 0, testProfile())
	if err != nil {
		t.Fatal(err)
	}
	return contract.Real(wt), c
}

func bashCase(t *testing.T, wt, cmd string) string {
	in, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash", "cwd": wt,
		"tool_input": map[string]string{"command": cmd}})
	var out bytes.Buffer
	Run(bytes.NewReader(in), &out)
	return out.String()
}

func TestBash(t *testing.T) {
	wt, _ := setup(t)
	deny := []string{
		"git push origin HEAD",
		"git -C . push",             // codex finding: global options
		"git -c user.name=x push",   // value option before the subcommand
		"bash -c \"git push\"",      // nested shell
		"sh -lc 'git push --force'", // combined flags
		"env FOO=1 git push",        // wrapper
		"timeout 30 git push",       // wrapper with a duration
		"echo hi && git push",       // second command of a list
		"echo $(git push)",          // command substitution
		"git rebase origin/main",
		"git reset --hard HEAD~1",
		"git clean -fdx",
		"git branch -D other",
		"git checkout -- .",
		"glab mr create",
		"gh pr create",
		"docker compose up -d",
		"docker-compose down",
		"docker rm -f app-db", // codex finding
		"docker container stop app-api",
		"docker system prune -af",
		"curl http://localhost:8080/api",
		"psql -h localhost appdb",
		"npm install left-pad",
		"npx playwright test",
		"sh scripts/release.sh patch",
		"echo x > ../other-task/file.txt",   // redirect outside the worktree
		"echo 1 > VERSION",                  // never-edit file
		"sed -i 's/a/b/' frontend/src/x.ts", // in-place edit outside ownership (existing file)
		"cp backend/Routing/A.cs /Users/someone/elsewhere.cs",
		"git -C /some/other/repo commit -m x", // another repository
	}
	for _, cmd := range deny {
		if out := bashCase(t, wt, cmd); !strings.Contains(out, `"deny"`) {
			t.Errorf("expected DENY for %q, got %q", cmd, out)
		}
	}
	allow := []string{
		"git add . && git commit -m \"fix: explain why workers never git push #169\"", // codex finding: false positive
		"grep -rn \"git push\" docs", // read-only search
		"git -C . status",
		"git log --oneline -5",
		"/x/scripts/gates/backend-test.sh /w good",
		"echo ok > backend/Routing/new.txt",    // new scratch file inside the worktree (drift check judges it)
		"sed -i 's/a/b/' backend/Routing/A.cs", // owned file
		"echo log > /tmp/run.log",
		"docker ps",
		"docker logs app-api", // read-only
		"npm run build",
		"npx tsc -b",
		"echo hi > /dev/null",
	}
	for _, cmd := range allow {
		if out := bashCase(t, wt, cmd); out != "" {
			t.Errorf("expected allow for %q, got %q", cmd, out)
		}
	}
}

func TestEditAndStop(t *testing.T) {
	wt, c := setup(t)
	run := func(ev map[string]any) string {
		in, _ := json.Marshal(ev)
		var out bytes.Buffer
		Run(bytes.NewReader(in), &out)
		return out.String()
	}
	edit := func(tool, fp string) string {
		return run(map[string]any{"hook_event_name": "PreToolUse", "tool_name": tool, "cwd": wt,
			"tool_input": map[string]string{"file_path": fp}})
	}
	if out := edit("Edit", filepath.Join(wt, "backend/Routing/A.cs")); out != "" {
		t.Errorf("owned edit denied: %s", out)
	}
	for _, fp := range []string{filepath.Join(wt, "frontend/src/x.ts"), "VERSION", "/etc/hosts",
		filepath.Join(filepath.Dir(c.ReportPath), "review-good-r1.md"), filepath.Join(filepath.Dir(c.ReportPath), "good-critique.md")} {
		if out := edit("Write", fp); !strings.Contains(out, `"deny"`) {
			t.Errorf("expected DENY for write %s, got %q", fp, out)
		}
	}
	if out := edit("Write", c.ReportPath); out != "" {
		t.Errorf("own report denied: %s", out)
	}
	stop := func(active bool) string {
		return run(map[string]any{"hook_event_name": "Stop", "cwd": wt, "stop_hook_active": active})
	}
	if out := stop(false); !strings.Contains(out, `"block"`) {
		t.Errorf("stop without report not blocked: %q", out)
	}
	if out := stop(true); out != "" {
		t.Errorf("second stop must pass: %q", out)
	}
	_ = os.MkdirAll(filepath.Dir(c.ReportPath), 0o755)
	_ = os.WriteFile(c.ReportPath, []byte("# report\n"), 0o644)
	if out := stop(false); out != "" {
		t.Errorf("stop with report blocked: %q", out)
	}
}

func TestOutsideWorkerIsSilent(t *testing.T) {
	setup(t)
	other := t.TempDir()
	_ = os.MkdirAll(filepath.Join(other, ".git"), 0o755)
	for _, cmd := range []string{"git push", "docker compose up", "rm -rf /"} {
		if out := bashCase(t, other, cmd); out != "" {
			t.Errorf("non-worker session got output for %q: %q", cmd, out)
		}
	}
}

// The pipeline coordinator (and its steward) must never run as a Claude subagent: that bypasses Orca orchestration.
// The guard denies it in every session, worker or not, before any contract lookup (ADR 0002 B0, interim rule #3).
func TestCoordinatorSubagentDenied(t *testing.T) {
	wt, c := setup(t)
	plain := t.TempDir()
	_ = os.MkdirAll(filepath.Join(plain, ".git"), 0o755) // a git repo with no contract: the hook is silent there today
	run := func(ev map[string]any, cwd string) string {
		ev["hook_event_name"], ev["cwd"] = "PreToolUse", cwd
		in, _ := json.Marshal(ev)
		var out bytes.Buffer
		Run(bytes.NewReader(in), &out)
		return out.String()
	}
	agent := func(cwd, subagentType string) string {
		ti := map[string]string{"prompt": "spread the work"}
		if subagentType != "" {
			ti["subagent_type"] = subagentType
		}
		return run(map[string]any{"tool_name": "Agent", "tool_input": ti}, cwd)
	}
	for _, st := range []string{"rein:orca-swarm", "rein:orca-steward"} { // denied in every session, no contract needed
		for _, cwd := range []string{plain, wt} {
			if out := agent(cwd, st); !strings.Contains(out, "must not run as Claude subagents") ||
				!strings.Contains(out, `"permissionDecision":"deny"`) {
				t.Errorf("subagent_type %q in %s must be denied, got %q", st, cwd, out)
			}
		}
	}
	// every other Agent call, in every session, behaves exactly as today: silent
	for _, ev := range []map[string]any{
		{"tool_name": "Agent", "tool_input": map[string]string{"subagent_type": "Explore", "prompt": "look"}},
		{"tool_name": "Agent", "tool_input": map[string]string{"prompt": "work"}}, // no subagent_type
		{"tool_name": "Bash", "tool_input": map[string]string{"command": "git status"}},
	} {
		for _, cwd := range []string{plain, wt} {
			if out := run(ev, cwd); out != "" {
				t.Errorf("event %v in %s must stay silent, got %q", ev["tool_name"], cwd, out)
			}
		}
	}
	// the deny even wins over a broken contract, and over a bound hook, because it is judged before both
	if err := os.WriteFile(contract.PathOf(c.Name), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := agent(wt, "rein:orca-swarm"); !strings.Contains(out, "must not run as Claude subagents") || strings.Contains(out, "unreadable") {
		t.Errorf("the deny must precede the contract lookup, got %q", out)
	}
	bound, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Agent", "cwd": plain,
		"tool_input": map[string]string{"subagent_type": "rein:orca-steward"}})
	var out, errb bytes.Buffer
	if code := RunTask("claude", c.Name, bytes.NewReader(bound), &out, &errb); code != 0 ||
		!strings.Contains(out.String(), "must not run as Claude subagents") {
		t.Errorf("a bound hook must deny the coordinator subagent, got %q (exit %d)", out.String(), code)
	}
}

// hooks/hooks.json must send Agent tool calls to the guard, or the deny in RunTask can never fire in a real session.
func TestPluginMatcherSendsAgentToTheGuard(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "hooks", "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Hooks struct {
			PreToolUse []struct {
				Matcher string `json:"matcher"`
				Hooks   []struct {
					Command string   `json:"command"`
					Args    []string `json:"args"`
				} `json:"hooks"`
			} `json:"PreToolUse"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Hooks.PreToolUse) != 1 || len(doc.Hooks.PreToolUse[0].Hooks) != 1 {
		t.Fatalf("want one PreToolUse group with one hook, got %+v", doc.Hooks.PreToolUse)
	}
	g := doc.Hooks.PreToolUse[0]
	for _, tool := range []string{"Bash", "Edit", "Write", "MultiEdit", "NotebookEdit", "Agent"} {
		if !strings.Contains(g.Matcher, tool) {
			t.Errorf("the PreToolUse matcher must cover %s, got %q", tool, g.Matcher)
		}
	}
	if g.Hooks[0].Command != "${CLAUDE_PLUGIN_DATA}/bin/rein" || len(g.Hooks[0].Args) != 1 || g.Hooks[0].Args[0] != "hook" {
		t.Errorf("the PreToolUse hook must stay the rein binary with `hook`, got %+v", g.Hooks[0])
	}
}

func TestBrokenContractFailsClosed(t *testing.T) {
	wt, _ := setup(t)
	if err := os.WriteFile(contract.PathOf("good"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := bashCase(t, wt, "ls"); !strings.Contains(out, `"deny"`) {
		t.Errorf("broken contract must fail closed, got %q", out)
	}
}

func TestProtectedDirsBeatTempAllowance(t *testing.T) {
	wt, c := setup(t) // the whole test tree lives under the OS temp dir, like a scratchpad run dir would
	for _, cmd := range []string{
		"echo x > " + filepath.Join(filepath.Dir(c.ReportPath), "review-good-r1.md"),
		"echo '{}' > " + contract.PathOf("good"),
	} {
		if out := bashCase(t, wt, cmd); !strings.Contains(out, `"deny"`) {
			t.Errorf("expected DENY for %q, got %q", cmd, out)
		}
	}
	if out := bashCase(t, wt, "echo ok > "+c.ReportPath); out != "" {
		t.Errorf("own report denied: %q", out)
	}
}

func TestGenericRulesWithoutProfile(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(root, "contracts"))
	t.Setenv("PIPELINE_LOGDIR", filepath.Join(root, "logs"))
	wt := filepath.Join(root, "wts", "plain")
	_ = os.MkdirAll(filepath.Join(wt, ".git"), 0o755)
	if _, err := contract.New("plain", 0, filepath.Join(root, "run"), "src/**", "", "S1", filepath.Join(root, "wts"), "", "", 0, nil); err != nil {
		t.Fatal(err)
	}
	wt = contract.Real(wt)
	// project rules only exist in a profile ...
	for _, cmd := range []string{"docker compose up", "psql db", "curl localhost:8080", "docker rm -f app-db"} {
		if out := bashCase(t, wt, cmd); out != "" {
			t.Errorf("%q must be allowed without a profile, got %q", cmd, out)
		}
	}
	// ... while the generic rules always apply
	for _, cmd := range []string{"git push", "gh pr create", "docker system prune -a", "npm install x"} {
		if out := bashCase(t, wt, cmd); !strings.Contains(out, `"deny"`) {
			t.Errorf("%q must be denied by the generic rules, got %q", cmd, out)
		}
	}
}

// Cases from the 2026-10-08 review of the Go port.
func TestReviewR1Cases(t *testing.T) {
	wt, c := setup(t)
	other := filepath.Join(filepath.Dir(wt), "other")
	deny := []string{
		`p=../other/report.md; echo overwritten > "$p"`, // 1: expanded write target
		`echo x > "$(pwd)/../other/f"`,                  // 1: command substitution in target
		`cd ../other; echo x > src/new.txt`,             // 2: cd changes what a relative path means
		`cd "$SOMEWHERE" && echo x > a.txt`,             // 2: unresolved cd
		`env -u FOO git push`,                           // 3: env option with a value
		`xargs -I {} git push`,                          // 3: xargs option with a value
		`timeout -s KILL 30 git push`,                   // 3: timeout signal + duration
		`env -S "git push"`,                             // 3: split-string is a script
		`rm .git`,                                       // 4: the worktree's .git file
		`rm -rf .`,                                      // 4: the worktree itself
		`rm -rf ` + filepath.Dir(wt),                    // 4: an ancestor of the worktree
		`git diff --output=../other/report.md`,          // 5: git writes a file
		`git format-patch -o ` + other + ` HEAD~1`,      // 5
		`git worktree add ../x`,                         // worktree management
		`p=` + c.ReportPath + `x; echo x > "$p"`,        // literal var resolves to a protected run-dir path
	}
	for _, cmd := range deny {
		if out := bashCase(t, wt, cmd); !strings.Contains(out, `"deny"`) {
			t.Errorf("expected DENY for %q, got %q", cmd, out)
		}
	}
	allow := []string{
		`git commit -m "doc: the API listens on localhost:8080"`, // 8: text mentioning a protected port
		`echo "see localhost:5432 in the docs"`,
		`grep -rn localhost:8080 docs`,
		`p=backend/Routing/A.cs; sed -i 's/a/b/' "$p"`, // literal var resolving inside the ownership
		`cd backend && echo x > Routing/new.txt`,       // cd to a literal path we can follow
		`echo ok > ` + c.ReportPath,                    // own report, absolute
		`xargs -n 1 echo`,
	}
	for _, cmd := range allow {
		if out := bashCase(t, wt, cmd); out != "" {
			t.Errorf("expected allow for %q, got %q", cmd, out)
		}
	}
}

func TestHookNeverPanicsOnHostileInput(t *testing.T) {
	wt, _ := setup(t)
	for _, cmd := range []string{strings.Repeat("$(", 2000), strings.Repeat("a", 1<<20), "bash -c 'bash -c \"bash -c \\\"bash -c ls\\\"\"'", "\x00\xff"} {
		_ = bashCase(t, wt, cmd) // must return, not panic or hang
	}
}

func TestGitBashToNative(t *testing.T) {
	cases := map[string]string{
		"/c/Users/a/x.txt":  `C:\Users\a\x.txt`,
		"/d":                `D:\`,
		"/tmp/run.log":      `C:\Temp\run.log`,
		"/some/other/repo":  `E:\some\other\repo`,
		"relative/path":     "relative/path",
		`C:\already\native`: `C:\already\native`,
	}
	for in, want := range cases {
		if got := gitBashToNative(in, "E:", `C:\Temp\`); got != want {
			t.Errorf("gitBashToNative(%q) = %q, want %q", in, got, want)
		}
	}
}
