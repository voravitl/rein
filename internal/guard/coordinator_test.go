package guard

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/run"
)

const coordSession = "sess-owner"

// coordEnv is a repo with an active run: a main checkout, an uncontracted linked worktree "side", and a marker owned
// by this test process. Nothing here runs git: the hook reads the layout the way git writes it.
type coordEnv struct {
	t        *testing.T
	root     string // main checkout
	side     string // linked worktree without a contract
	outside  string // a directory outside the repo (the run dir, notes)
	marker   string
	m        *run.Marker
	contract string // contract index dir
}

func writeFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// linkWorktree lays out <root>/.git/worktrees/<name> and <wt>/.git the way `git worktree add` does.
func linkWorktree(t *testing.T, root, wt, name string) {
	t.Helper()
	gd := filepath.Join(root, ".git", "worktrees", name)
	writeFile(t, filepath.Join(gd, "commondir"), "../..\n")
	writeFile(t, filepath.Join(gd, "gitdir"), filepath.Join(wt, ".git")+"\n")
	writeFile(t, filepath.Join(wt, ".git"), "gitdir: "+gd+"\n")
}

func newCoordEnv(t *testing.T) *coordEnv {
	t.Helper()
	base := contract.Real(t.TempDir())
	e := &coordEnv{t: t, root: filepath.Join(base, "main"), side: filepath.Join(base, "side"), outside: filepath.Join(base, "notes"),
		contract: filepath.Join(base, "contracts")}
	t.Setenv("PIPELINE_CONTRACTS", e.contract)
	t.Setenv("PIPELINE_LOGDIR", filepath.Join(base, "logs"))
	t.Setenv("REIN_RUN_DIR", "")
	t.Setenv("GIT_DIR", "")
	t.Setenv("GIT_COMMON_DIR", "")
	for _, f := range []string{"src/a.go", "docs/d.md", "README.md", "CHANGELOG.md", ".claude-plugin/plugin.json", "scripts/x.sh"} {
		writeFile(t, filepath.Join(e.root, f), "x\n")
	}
	writeFile(t, filepath.Join(e.root, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(e.side, "src", "b.go"), "x\n")
	linkWorktree(t, e.root, e.side, "side")
	if err := os.MkdirAll(e.outside, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := run.ProcStart(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	e.m = &run.Marker{Schema: run.Schema, Run: "sprint", StartedAt: "2026-10-08T00:00:00Z", Root: e.root, StartSHA: "0", BaseRef: "main",
		SessionID: coordSession, PID: os.Getpid(), StartTime: st,
		CoordinatorWritable: run.DefaultWritable, CoordinatorTools: run.DefaultTools, ReadonlyAgents: []string{"Explore"}}
	e.marker = filepath.Join(e.root, ".git", run.MarkerFile)
	e.save()

	// Create a fresh tick by default so tests don't get denied for stale tick
	tick := run.TickState{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		PID:       os.Getpid(),
		Status:    "ok",
		Workers:   map[string]run.Worker{},
	}
	tickData, _ := json.Marshal(tick)
	writeFile(t, filepath.Join(e.root, ".git", "rein-tick.json"), string(tickData))

	return e
}

func (e *coordEnv) save() {
	e.t.Helper()
	b, _ := json.Marshal(e.m)
	writeFile(e.t, e.marker, string(b))
}

// fire sends one Claude Code event and returns the hook's stdout.
func (e *coordEnv) fire(ev map[string]any) string {
	e.t.Helper()
	if _, ok := ev["session_id"]; !ok {
		ev["session_id"] = coordSession
	}
	if _, ok := ev["cwd"]; !ok {
		ev["cwd"] = e.root
	}
	b, _ := json.Marshal(ev)
	var out bytes.Buffer
	if code := RunTask("claude", "", bytes.NewReader(b), &out, io.Discard); code != 0 {
		e.t.Fatalf("exit code %d for %s", code, b)
	}
	return out.String()
}

func (e *coordEnv) tool(name string, input map[string]any, extra ...map[string]any) string {
	ev := map[string]any{"hook_event_name": "PreToolUse", "tool_name": name, "tool_input": input}
	for _, x := range extra {
		for k, v := range x {
			ev[k] = v
		}
	}
	return e.fire(ev)
}

func (e *coordEnv) bash(cmd string, extra ...map[string]any) string {
	return e.tool("Bash", map[string]any{"command": cmd}, extra...)
}

func isDeny(out string) bool { return strings.Contains(out, `"permissionDecision":"deny"`) }

func (e *coordEnv) expect(deny bool, out, what string) {
	e.t.Helper()
	if isDeny(out) != deny {
		e.t.Errorf("%s: deny=%v, want %v (output %q)", what, isDeny(out), deny, out)
	}
}

func TestCoordWritesOutsideWritableDenied(t *testing.T) {
	e := newCoordEnv(t)
	for _, tool := range []string{"Edit", "Write", "NotebookEdit", "MultiEdit"} {
		for _, f := range []string{"src/a.go", "scripts/x.sh", "src/new.go", ".git/config", ".claude-plugin/other.json"} {
			e.expect(true, e.tool(tool, map[string]any{"file_path": filepath.Join(e.root, f)}), tool+" "+f)
		}
		e.expect(true, e.tool(tool, map[string]any{"file_path": "src/a.go"}), tool+" relative src/a.go") // relative to cwd
		e.expect(true, e.tool(tool, map[string]any{"file_path": filepath.Join(e.side, "src", "b.go")}), tool+" linked worktree")
		e.expect(true, e.tool(tool, map[string]any{"file_path": filepath.Join(e.side, ".git")}), tool+" linked worktree's .git file")
		e.expect(true, e.tool(tool, map[string]any{"file_path": filepath.Join(e.root, ".git", run.MarkerFile)}), tool+" marker")
		e.expect(true, e.tool(tool, map[string]any{"file_path": filepath.Join(e.root, "src", "a.go")}, map[string]any{"cwd": e.outside}), tool+" abs path, cwd outside the repo")
		e.expect(true, e.tool(tool, map[string]any{}), tool+" without a path")
	}
	e.expect(true, e.tool("Edit", map[string]any{"notebook_path": filepath.Join(e.root, "src", "n.ipynb")}), "notebook_path")
	out := e.tool("Edit", map[string]any{"file_path": filepath.Join(e.root, "src", "a.go")})
	for _, want := range []string{"coordinator_writable", "rein contract new", "worker-start"} {
		if !strings.Contains(out, want) {
			t.Errorf("deny reason lacks %q: %s", want, out)
		}
	}
}

func TestCoordGitDirIsNeverWritableEvenWithBroadGlobs(t *testing.T) {
	e := newCoordEnv(t)
	e.m.CoordinatorWritable = []string{"**"} // a profile that allows everything still cannot touch .git
	e.save()
	e.expect(false, e.tool("Edit", map[string]any{"file_path": filepath.Join(e.side, "src", "b.go")}), "broad glob allows code")
	e.expect(true, e.tool("Edit", map[string]any{"file_path": filepath.Join(e.side, ".git")}), "linked worktree's .git file")
	e.expect(true, e.tool("Write", map[string]any{"file_path": filepath.Join(e.root, ".git", "hooks", "pre-commit")}), "hooks dir")
	e.expect(true, e.bash("echo x > "+filepath.Join(e.side, ".git")), "bash write to the .git file")
	e.expect(true, e.bash("rm -rf .git"), "rm -rf .git")
}

func TestCoordWritesInsideWritableAllowed(t *testing.T) {
	e := newCoordEnv(t)
	for _, tool := range []string{"Edit", "Write", "NotebookEdit", "MultiEdit"} {
		for _, f := range []string{"docs/d.md", "docs/new/deep.txt", "README.md", "notes/todo.md", "CHANGELOG.md", ".claude-plugin/plugin.json"} {
			e.expect(false, e.tool(tool, map[string]any{"file_path": filepath.Join(e.root, f)}), tool+" "+f)
		}
		e.expect(false, e.tool(tool, map[string]any{"file_path": filepath.Join(e.side, "docs", "x.md")}), tool+" docs in the linked worktree")
		e.expect(false, e.tool(tool, map[string]any{"file_path": filepath.Join(e.outside, "spec.md")}), tool+" run dir outside the repo")
		e.expect(false, e.tool(tool, map[string]any{"file_path": filepath.Join(e.outside, "code.go")}), tool+" any file outside the repo")
	}
	e.expect(false, e.tool("Edit", map[string]any{"file_path": "docs/d.md"}, map[string]any{"cwd": filepath.Join(e.root, "src")}), "relative to a subdirectory cwd")
}

func TestCoordWorkerWorktreeStaysContracted(t *testing.T) {
	e := newCoordEnv(t)
	wt := filepath.Join(contract.Real(filepath.Dir(e.root)), "wts", "task-x")
	linkWorktree(t, e.root, wt, "task-x")
	writeFile(t, filepath.Join(wt, "src", "w.go"), "x\n")
	if _, err := contract.New("task-x", 1, filepath.Join(e.outside, "run"), "src/**", "", "S1", filepath.Dir(wt), "", "", 0, nil); err != nil {
		t.Fatal(err)
	}
	// the worker's own rule applies inside its worktree (no coordinator message), whoever the session is
	out := e.tool("Edit", map[string]any{"file_path": filepath.Join(wt, "docs", "w.md")}, map[string]any{"cwd": wt})
	if !isDeny(out) || strings.Contains(out, "coordinator_writable") || !strings.Contains(out, "ownership of task task-x") {
		t.Errorf("worker rule expected, got %q", out)
	}
	e.expect(false, e.tool("Edit", map[string]any{"file_path": filepath.Join(wt, "src", "w.go")}, map[string]any{"cwd": wt}), "worker edit inside its allow")
	// the coordinator writing into a worker's worktree from the main checkout is denied, even docs
	out = e.tool("Write", map[string]any{"file_path": filepath.Join(wt, "docs", "x.md")})
	e.expect(true, out, "coordinator writes into a worker worktree")
	if !strings.Contains(out, "worker worktree task-x") {
		t.Errorf("reason = %s", out)
	}
}

func TestCoordBashWrites(t *testing.T) {
	e := newCoordEnv(t)
	root := e.root
	deny := []string{
		"echo x > src/a.go", "echo x >> src/a.go", "echo x > " + filepath.Join(root, "src", "a.go"),
		"sed -i 's/a/b/' src/a.go", "tee src/a.go", "touch src/new.go", "rm src/a.go", "rm -rf src", "truncate -s 0 src/a.go",
		"cp docs/d.md src/a.go", "mv src/a.go docs/", "install -m 755 docs/d.md src/a.sh", "ln -s docs/d.md src/link",
		"bash -c 'echo x > src/a.go'", "sh -c \"echo x > src/a.go\"", "env X=1 timeout 30 sh -c 'echo x > src/a.go'", "echo $(echo x > src/a.go)",
		"cd src && echo x > a.go", "cd src; sed -i s/a/b/ a.go", "echo x > " + filepath.Join(e.side, "src", "b.go"),
		"echo x > .git/config", "echo x > .git/" + run.MarkerFile, "rm .git/" + run.MarkerFile,
		"echo x > $UNSET/f", "find src -delete", "find . -name '*.go' -exec rm {} \\;", "perl -pi -e 's/a/b/' src/a.go",
		"git restore src/a.go", "git restore .", "git restore --worktree docs/d.md", "git checkout -- src/a.go", "git checkout -- .", "git checkout -f main",
		"git checkout main src/a.go", "git switch --discard-changes main",
		"git stash", "git stash push -m x", "git stash pop", "git stash apply", "git stash -u",
		"git merge feat", "git merge --no-ff feat", "git pull", "git pull --ff-only origin main", "git cherry-pick abc1234", "git revert HEAD", "git rebase main", "git am x.patch",
		"git reset --hard", "git reset --hard HEAD~1", "git reset --merge", "git clean -fd", "git apply fix.patch", "git rm src/a.go", "git mv src/a.go src/b.go",
		"git -C " + root + " restore src/a.go", "git -C " + e.side + " merge feat", "cd " + e.side + " && git checkout -- .",
		"GIT_DIR=/x git status", "export GIT_WORK_TREE=/x", "env GIT_DIR=/x git status", "git --git-dir=/x status", "git --work-tree=/x status",
		"git diff --output=src/out.patch", "echo ok && git merge feat",
	}
	for _, c := range deny {
		e.expect(true, e.bash(c), c)
	}
	allow := []string{
		"ls", "cat src/a.go", "grep -rn foo src", "git status", "git log --oneline -5", "git diff", "git show HEAD:src/a.go", "git -C . status",
		"git push origin main", "git commit -m 'fix: x'", "git add docs README.md", "git tag v1", "git branch new", "git fetch", "git worktree add ../w -b w",
		"git checkout -b new origin/main", "git checkout main", "git switch main", "git switch -c new", "git stash list", "git stash show", "git restore --staged docs/d.md",
		"git reset --soft HEAD~1", "git reset HEAD docs/d.md", "git clean -n", "git apply --check fix.patch", "git rm docs/d.md", "git mv docs/d.md docs/e.md",
		"gh pr create --title x", "glab mr list", "docker ps", "npm install", "psql -h localhost x", "curl http://localhost:8080/health",
		"echo x > docs/new.md", "echo x >> README.md", "sed -i 's/a/b/' docs/d.md", "tee docs/a.md", "touch CHANGELOG.md", "cp src/a.go docs/copy.md", "rm docs/d.md",
		"echo x > /tmp/run.log", "echo x > " + filepath.Join(e.outside, "spec.md"), "mkdir -p " + filepath.Join(e.outside, "reports"), "cd " + e.outside + " && echo x > code.go",
		"sh scripts/x.sh", "bash -c 'git status'", "git commit -m \"explain why git merge is denied\"", "grep -rn 'git merge' docs",
		"git -C " + e.outside + " merge feat", "echo x > /dev/null", "find src -name '*.go'", "find . -exec grep foo {} \\;",
		"orca orchestration check --wait", "orca terminal send --terminal t --text hi",
		"rein run start . --run x", "rein run end", "rein run audit", "rein contract new --name x --run-dir d",
	}
	for _, c := range allow {
		e.expect(false, e.bash(c), c)
	}
}

func TestCoordUserOnlyCommands(t *testing.T) {
	e := newCoordEnv(t)
	deny := []string{
		"rein run allow --task t --reason r", "rein run allow . --commit abc1234 --reason r", "/usr/local/bin/rein run allow --task t --reason r",
		"./bin/rein.exe run allow --task t --reason r", "rein run end --abandon", "rein run end --abandon --reason x", "rein run end . -abandon", "rein run end --abandon=true",
		"rein run end --reason x --abandon", "rein budget raise --reason x", "rein approve --mr 4", "rein approve prompt --mr 4",
		"env -u CLAUDE_PID rein run allow --task t --reason r", "env -u CLAUDE_CODE_SESSION_ID -u CLAUDE_PID rein approve --mr 4",
		"r=rein; $r run allow --task t --reason r", "R=/opt/rein; $R/../rein approve", "bash -c 'rein run allow --task t'", "sh -c \"echo hi && rein run end --abandon\"",
		"echo hi && rein run end --abandon", "nohup rein approve &", "timeout 5 rein budget raise", "sudo rein run allow --task t", "xargs rein approve",
		"$(which rein) run allow --task t --reason r", "\"$REIN\" approve --mr 4", "command rein run allow --task t --reason r", "time rein approve",
	}
	for _, c := range deny {
		out := e.bash(c)
		e.expect(true, out, c)
		if !strings.Contains(out, "for the user") {
			t.Errorf("%s: reason does not say the command is for the user: %s", c, out)
		}
	}
	allow := []string{
		"rein run start . --run x", "rein run end", "rein run end .", "rein run audit", "rein run resume", "rein contract new --name x --run-dir d",
		"rein drift x", "rein hooks install x", "rein version", "git commit -m 'rein run allow'", "echo rein run allow", "grep -rn 'rein approve' docs",
		"cat docs/rein-run-allow.md", "rein budget check", "rein run end --reason 'no abandon here'",
	}
	for _, c := range allow {
		e.expect(false, e.bash(c), c)
	}
}

func TestCoordToolAllowlist(t *testing.T) {
	e := newCoordEnv(t)
	for _, tool := range []string{"Read", "View", "ViewFile", "ReadFile", "Glob", "Grep", "Search", "List", "AskUserQuestion", "ask_question", "orca", "orchestration", "TodoWrite", "Skill"} {
		e.expect(false, e.tool(tool, map[string]any{}), tool)
	}
	for _, tool := range []string{"mcp__github__create_issue", "mcp__orca__worker_start", "mcp__fs__write_file", "SomeNewTool", "ExitPlanMode", "CronCreate"} {
		out := e.tool(tool, map[string]any{})
		e.expect(true, out, tool)
		if !strings.Contains(out, "coordinator_tools") {
			t.Errorf("%s: reason does not name coordinator_tools: %s", tool, out)
		}
	}
	// a profile can list MCP tools, with a glob
	e.m.CoordinatorTools = append(append([]string{}, run.DefaultTools...), "mcp__orca__*")
	e.save()
	e.expect(false, e.tool("mcp__orca__worker_start", map[string]any{}), "listed MCP tool")
	e.expect(true, e.tool("mcp__github__create_issue", map[string]any{}), "unlisted MCP tool")
}

func agentCall(e *coordEnv, subType, prompt string, extra ...map[string]any) string {
	return e.tool("Agent", map[string]any{"subagent_type": subType, "prompt": prompt, "description": "d"}, extra...)
}

func TestCoordAgentRule(t *testing.T) {
	e := newCoordEnv(t)
	out := agentCall(e, "general-purpose", "implement the feature")
	e.expect(true, out, "writing subagent without allowance")
	for _, want := range []string{"rein run allow --task", "rein-task:", "worker-start"} {
		if !strings.Contains(out, want) {
			t.Errorf("reason lacks %q: %s", want, out)
		}
	}
	e.expect(true, agentCall(e, "general-purpose", "rein-task: task-a\nimplement"), "task named but not allowed by the user")
	e.expect(true, agentCall(e, "", "no type, no task"), "no subagent type")
	e.expect(false, agentCall(e, "Explore", "find the config loader"), "read-only agent type")
	if err := os.RemoveAll(run.AgentsDir(e.marker)); err != nil {
		t.Fatal(err)
	}

	e.m.Allowed = []run.Allowance{{Kind: "task", Ref: "task-a", Reason: "user", At: "now"}}
	e.save()
	e.expect(false, agentCall(e, "general-purpose", "rein-task: task-a\nimplement"), "allowed task")
	e.expect(true, agentCall(e, "general-purpose", "rein-task: task-b\nimplement"), "another task than the allowed one")
	// the allowed task's start is still pending: a second Agent call now would pair ambiguously
	out = agentCall(e, "Explore", "look around")
	e.expect(true, out, "second Agent while a task binding is pending")
	if !strings.Contains(out, "still starting") {
		t.Errorf("reason = %s", out)
	}
	// the denied calls above did not leave a pending entry behind: after SubagentStart pairs the first one, calls work again
	e.fire(map[string]any{"hook_event_name": "SubagentStart", "agent_id": "ag-1", "agent_type": "general-purpose"})
	e.expect(false, agentCall(e, "Explore", "look around"), "after the start paired")
	// a non-owner session is not judged and leaves no binding
	e.expect(false, agentCall(e, "general-purpose", "anything", map[string]any{"session_id": "other"}), "other session")
}

func TestCoordBoundSubagentUsesTaskContract(t *testing.T) {
	e := newCoordEnv(t)
	wt := filepath.Join(contract.Real(filepath.Dir(e.root)), "wts", "task-a")
	linkWorktree(t, e.root, wt, "task-a")
	writeFile(t, filepath.Join(wt, "src", "w.go"), "x\n")
	if _, err := contract.New("task-a", 1, filepath.Join(e.outside, "run"), "src/**", "", "S1", filepath.Dir(wt), "", "", 0, nil); err != nil {
		t.Fatal(err)
	}
	e.m.Allowed = []run.Allowance{{Kind: "task", Ref: "task-a", Reason: "user", At: "now"}}
	e.save()
	e.expect(false, agentCall(e, "general-purpose", "rein-task: task-a\nimplement"), "allowed task agent")
	e.fire(map[string]any{"hook_event_name": "SubagentStart", "agent_id": "ag-7", "agent_type": "general-purpose"})
	sub := map[string]any{"agent_id": "ag-7", "agent_type": "general-purpose"}
	// the bound subagent is judged by task-a's contract: its allow glob, never push
	e.expect(false, e.tool("Edit", map[string]any{"file_path": filepath.Join(wt, "src", "w.go")}, sub), "bound subagent edits inside the contract")
	e.expect(true, e.tool("Edit", map[string]any{"file_path": filepath.Join(wt, "docs", "x.md")}, sub), "bound subagent edits outside the contract")
	e.expect(true, e.bash("git push origin HEAD", sub), "bound subagent pushes")
	e.expect(false, e.tool("Read", map[string]any{"file_path": "x"}, sub), "bound subagent reads")
	// an unbound subagent call (same session, other agent_id) falls under the coordinator rules
	other := map[string]any{"agent_id": "ag-9", "agent_type": "Explore"}
	e.expect(true, e.tool("Edit", map[string]any{"file_path": filepath.Join(e.root, "src", "a.go")}, other), "unbound subagent edits code")
	e.expect(false, e.tool("Edit", map[string]any{"file_path": filepath.Join(e.root, "docs", "d.md")}, other), "unbound subagent edits docs")
	e.expect(true, e.bash("echo x > src/a.go", other), "read-only subagent's Bash goes through the write rules")
	e.expect(true, e.bash("rein run allow --task t --reason r", other), "subagent authorizes itself")
	e.expect(true, e.tool("Agent", map[string]any{"subagent_type": "Explore", "prompt": "nested"}, other), "subagent cannot spawn nested subagent")
}

func TestCoordWorkerStart(t *testing.T) {
	e := newCoordEnv(t)
	wt := filepath.Join(contract.Real(filepath.Dir(e.root)), "wts", "task-c")
	linkWorktree(t, e.root, wt, "task-c")
	if c, err := contract.New("task-c", 1, filepath.Join(e.outside, "run"), "src/**", "", "S1", filepath.Dir(wt), "", "", 0, nil); err != nil {
		t.Fatal(err)
	} else {
		prepareGuardRouteFor(t, c, e.m.Run, "claude", "claude-sonnet", "orca", "worker:backend")
	}
	e.m.Allowed = append(e.m.Allowed, run.Allowance{Kind: "task", Ref: "task-c", Reason: "test"})
	e.save()
	start := "rein route launch --task task-c --run sprint -- orca orchestration worker-start --run sprint --agent claude --model claude-sonnet "
	for _, c := range []string{
		start + "--worktree path:" + e.root, start + "--worktree path:" + e.side, start + "--worktree=path:" + e.side,
		start + "--worktree path:" + filepath.Join(e.side, "src"), start + "--worktree current", start + "--worktree active",
		start + "--worktree id:abc::/x", start + "--worktree branch:feature", start + "--worktree name:side",
	} {
		e.expect(true, e.bash(c), c)
	}
	for _, c := range []string{
		start + "--worktree path:" + wt, start + "--worktree new-top-level --name x", start + "--worktree new-child",
		start + "--worktree path:" + e.outside, start, "orca orchestration worker-start --task t1 --agent claude",
	} {
		e.expect(strings.HasPrefix(c, "orca "), e.bash(c), c)
	}
}

func TestCoordSilence(t *testing.T) {
	e := newCoordEnv(t)
	editSrc := map[string]any{"file_path": filepath.Join(e.root, "src", "a.go")}
	e.expect(true, e.tool("Edit", editSrc), "control: the owner session is judged")

	e.expect(false, e.tool("Edit", editSrc, map[string]any{"session_id": "someone-else"}), "another session")
	e.expect(false, e.tool("Edit", editSrc, map[string]any{"session_id": ""}), "no session id")
	e.expect(true, e.tool("Edit", editSrc, map[string]any{"cwd": e.outside}), "control: cwd outside but the path inside is judged")
	e.expect(false, e.tool("Edit", map[string]any{"file_path": filepath.Join(e.outside, "x.go")}, map[string]any{"cwd": e.outside}), "everything outside the repo")
	e.expect(false, e.fire(map[string]any{"hook_event_name": "Stop"}), "Stop")
	e.expect(false, e.fire(map[string]any{"hook_event_name": "PostToolUse", "tool_name": "Edit", "tool_input": editSrc}), "PostToolUse")
	if out := e.fire(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": map[string]any{"command": "echo hi"}}); out != "" {
		t.Errorf("a harmless command must produce no output, got %q", out)
	}

	dead := *e.m
	dead.PID = 1 << 30
	b, _ := json.Marshal(dead)
	writeFile(t, e.marker, string(b))
	e.expect(false, e.tool("Edit", editSrc), "owner process dead")
	e.expect(false, e.bash("git merge feat"), "owner process dead, bash")
	e.expect(false, e.tool("mcp__x__y", map[string]any{}), "owner process dead, tool")

	reused := *e.m
	reused.StartTime++ // the pid runs, but it is another process
	b, _ = json.Marshal(reused)
	writeFile(t, e.marker, string(b))
	e.expect(false, e.tool("Edit", editSrc), "pid reused by another process")

	if err := os.Remove(e.marker); err != nil {
		t.Fatal(err)
	}
	e.expect(false, e.tool("Edit", editSrc), "no run active")
	e.expect(false, e.bash("git merge feat"), "no run active, bash")
}

func TestCoordCorruptMarkerDenies(t *testing.T) {
	e := newCoordEnv(t)
	for name, body := range map[string]string{
		"garbage":        "{nope",
		"unknown schema": `{"schema": 2, "run": "r", "root": "/x", "session_id": "s", "pid": 1}`,
		"missing fields": `{"schema": 1}`,
	} {
		writeFile(t, e.marker, body)
		out := e.bash("echo hi")
		e.expect(true, out, name)
		if !strings.Contains(out, "rein run end --abandon") {
			t.Errorf("%s: reason does not say how to clear the marker: %s", name, out)
		}
		e.expect(true, e.tool("Read", map[string]any{}, map[string]any{"session_id": "any-other-session"}), name+" (other session)")
		e.expect(false, e.fire(map[string]any{"hook_event_name": "Stop"}), name+" (Stop never denies)")
	}
}

func TestCoordGitEnvDenies(t *testing.T) {
	e := newCoordEnv(t)
	t.Setenv("GIT_DIR", filepath.Join(e.root, ".git"))
	out := e.bash("git status")
	e.expect(true, out, "GIT_DIR set in the hook environment")
	if !strings.Contains(out, "GIT_DIR") {
		t.Errorf("reason = %s", out)
	}
	e.expect(true, e.tool("Read", map[string]any{}, map[string]any{"cwd": e.outside}), "GIT_DIR points at the repo from elsewhere")
	e.expect(false, e.bash("git status", map[string]any{"session_id": "other"}), "another session")
	t.Setenv("GIT_DIR", "")
	t.Setenv("GIT_COMMON_DIR", filepath.Join(e.root, ".git"))
	e.expect(true, e.bash("git status"), "GIT_COMMON_DIR set")
}

func TestCoordSubmoduleInsideRepo(t *testing.T) {
	e := newCoordEnv(t)
	sm := filepath.Join(e.root, "vendor", "sm")
	writeFile(t, filepath.Join(e.root, ".git", "modules", "sm", "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(sm, ".git"), "gitdir: ../../.git/modules/sm\n")
	writeFile(t, filepath.Join(sm, "lib.go"), "x\n")
	e.expect(true, e.tool("Edit", map[string]any{"file_path": filepath.Join(sm, "lib.go")}, map[string]any{"cwd": sm}), "edit in a submodule of the repo")
	e.expect(true, e.bash("echo x > lib.go", map[string]any{"cwd": sm}), "bash write in a submodule")
	e.expect(false, e.tool("Edit", map[string]any{"file_path": filepath.Join(sm, "docs", "x.md")}, map[string]any{"cwd": sm}), "docs in a submodule")
}

func sessionStart(e *coordEnv, session, source string) string {
	return e.fire(map[string]any{"hook_event_name": "SessionStart", "session_id": session, "source": source})
}

func TestCoordSessionStart(t *testing.T) {
	e := newCoordEnv(t)
	t.Setenv("CLAUDE_PID", "1") // the hook does not run for the owner's process
	if out := sessionStart(e, "sess-new", "clear"); out != "" {
		t.Errorf("a session of another process must be left alone, got %q", out)
	}
	if m, _ := run.Load(e.marker); m.SessionID != coordSession {
		t.Fatalf("session rebound by a foreign process: %s", m.SessionID)
	}
	t.Setenv("CLAUDE_PID", strconv.Itoa(os.Getpid())) // the owner's own process starts a new session
	if out := sessionStart(e, "sess-new", "startup"); out != "" {
		t.Errorf("startup is not a rebind, got %q", out)
	}
	if m, _ := run.Load(e.marker); m.SessionID != coordSession {
		t.Fatalf("startup rebound the session: %s", m.SessionID)
	}
	for i, source := range []string{"clear", "compact", "resume"} {
		session := "sess-" + source
		out := sessionStart(e, session, source)
		if !strings.Contains(out, "follows this session") || !strings.Contains(out, `"hookEventName":"SessionStart"`) {
			t.Errorf("%s: output %q", source, out)
		}
		if m, _ := run.Load(e.marker); m.SessionID != session {
			t.Fatalf("%s #%d: marker session = %s", source, i, m.SessionID)
		}
		// the guard follows: the old session is no longer judged, the new one is
		e.expect(false, e.tool("Edit", map[string]any{"file_path": filepath.Join(e.root, "src", "a.go")}, map[string]any{"session_id": coordSession}), "old session after "+source)
		e.expect(true, e.tool("Edit", map[string]any{"file_path": filepath.Join(e.root, "src", "a.go")}, map[string]any{"session_id": session}), "new session after "+source)
		e.m.SessionID = coordSession // reset for the next source
		e.save()
	}
	// a dead owner: SessionStart says the run is pending
	dead := *e.m
	dead.PID = 1 << 30
	b, _ := json.Marshal(dead)
	writeFile(t, e.marker, string(b))
	out := sessionStart(e, "sess-after-restart", "startup")
	if !strings.Contains(out, "pending") || !strings.Contains(out, "rein run end") {
		t.Errorf("pending notice = %q", out)
	}
	if m, _ := run.Load(e.marker); m.SessionID != coordSession {
		t.Fatal("a dead owner's run must not be rebound")
	}
}

func TestCoordOtherVendorsAreNotJudged(t *testing.T) {
	e := newCoordEnv(t)
	in, _ := json.Marshal(map[string]any{"hook_event_name": "preToolUse", "tool_name": "fs_write", "cwd": e.root,
		"tool_input": map[string]any{"path": filepath.Join(e.root, "src", "a.go")}})
	var out bytes.Buffer
	if code := RunTask("kiro", "", bytes.NewReader(in), &out, io.Discard); code != 0 || out.Len() != 0 {
		t.Fatalf("kiro in the coordinator's repo: code %d out %q", code, out.String())
	}
}

func TestCoordTickStalenessDeniesWorkerSpawn(t *testing.T) {
	e := newCoordEnv(t)

	// Write a stale tick (> 2 minutes old)
	tick := run.TickState{
		Timestamp: "2020-01-01T00:00:00Z",
		PID:       os.Getpid(),
		Status:    "ok",
		Workers:   map[string]run.Worker{},
	}
	tickData, _ := json.Marshal(tick)
	writeFile(t, filepath.Join(e.root, ".git", "rein-tick.json"), string(tickData))

	// Try to spawn a worker via orca worker-start - should be denied
	out := e.bash("orca orchestration worker-start --worktree path:" + e.side)
	if !strings.Contains(out, "run tick is stale") {
		t.Errorf("stale tick should deny worker spawn, got: %q", out)
	}

	// Try agent call - should also be denied
	out = e.tool("Agent", map[string]any{"prompt": "rein-task: mytask"}, nil)
	if !strings.Contains(out, "run tick is stale") {
		t.Errorf("stale tick should deny agent call, got: %q", out)
	}
}

func TestCoordTickFreshAllowsWorkerSpawn(t *testing.T) {
	e := newCoordEnv(t)

	// Allow task mytask
	e.m.Allowed = append(e.m.Allowed, run.Allowance{Kind: "task", Ref: "mytask", Reason: "test"})
	e.save()

	// Write a fresh tick (< 2 minutes old)
	tick := run.TickState{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		PID:       os.Getpid(),
		Status:    "ok",
		Workers:   map[string]run.Worker{},
	}
	tickData, _ := json.Marshal(tick)
	writeFile(t, filepath.Join(e.root, ".git", "rein-tick.json"), string(tickData))

	// Agent call with fresh tick - check it's NOT denied for tick reasons
	// (it may still be denied for contract reasons, but not tick)
	out := e.tool("Agent", map[string]any{"prompt": "rein-task: mytask"}, nil)
	if strings.Contains(out, "run tick is stale") {
		t.Errorf("fresh tick should not deny for staleness, got: %q", out)
	}
}

// Tests for B4: Standing Orders & Decision Trail

func TestCoordRecordDecision(t *testing.T) {
	e := newCoordEnv(t)
	runDir := filepath.Join(e.outside, "run")
	e.m.RunDir = runDir
	e.save()

	// Simulate PostToolUse for AskUserQuestion
	toolInput := `{"question":"Deploy to production?","options":["Yes","No"]}`
	toolResponse := `{"answers":["Yes"],"free_text":"Looks good to go"}`

	ev := map[string]any{
		"hook_event_name": "PostToolUse",
		"tool_name":       "AskUserQuestion",
		"tool_input":      json.RawMessage(toolInput),
		"tool_response":   json.RawMessage(toolResponse),
		"session_id":      coordSession,
		"cwd":             e.root,
	}

	out := e.fire(ev)
	if out != "" {
		t.Errorf("expected no output, got %q", out)
	}

	// Verify decisions.tsv was created
	decisionsPath := filepath.Join(runDir, "decisions.tsv")
	content, err := os.ReadFile(decisionsPath)
	if err != nil {
		t.Fatalf("decisions.tsv not created: %v", err)
	}

	line := string(content)
	if !strings.Contains(line, "Deploy to production?") {
		t.Errorf("decision does not contain question: %q", line)
	}
	if !strings.Contains(line, "Yes") {
		t.Errorf("decision does not contain chosen option: %q", line)
	}
	if !strings.Contains(line, "Looks good to go") {
		t.Errorf("decision does not contain free text: %q", line)
	}
}

func TestCoordRecordDecisionWithSecret(t *testing.T) {
	e := newCoordEnv(t)
	runDir := filepath.Join(e.outside, "run")
	e.m.RunDir = runDir
	e.save()

	// Simulate PostToolUse with secret in free text
	toolInput := `{"question":"What's the key?","options":["Continue"]}`
	toolResponse := `{"answers":["Continue"],"free_text":"API key is AKIAIOSFODNN7EXAMPLE"}`

	ev := map[string]any{
		"hook_event_name": "PostToolUse",
		"tool_name":       "AskUserQuestion",
		"tool_input":      json.RawMessage(toolInput),
		"tool_response":   json.RawMessage(toolResponse),
		"session_id":      coordSession,
		"cwd":             e.root,
	}

	e.fire(ev)

	// Verify secret was redacted
	decisionsPath := filepath.Join(runDir, "decisions.tsv")
	content, err := os.ReadFile(decisionsPath)
	if err != nil {
		t.Fatalf("decisions.tsv not created: %v", err)
	}

	line := string(content)
	if strings.Contains(line, "AKIA") {
		t.Errorf("AWS key should be redacted, got: %q", line)
	}
	if !strings.Contains(line, "[REDACTED]") {
		t.Errorf("expected [REDACTED] in decision: %q", line)
	}
}

func TestCoordStandingMdWriteDenied(t *testing.T) {
	e := newCoordEnv(t)
	runDir := filepath.Join(e.outside, "run")
	e.m.RunDir = runDir
	e.save()

	standingPath := filepath.Join(runDir, "standing.md")

	// Try to write standing.md with Write tool
	out := e.tool("Write", map[string]any{"file_path": standingPath})
	e.expect(true, out, "Write tool to standing.md should be denied")
	if !strings.Contains(out, "user-only: standing.md cannot be written by coordinator") {
		t.Errorf("wrong denial reason: %q", out)
	}

	// Try to write standing.md with Bash
	out = e.bash("echo test > " + standingPath)
	e.expect(true, out, "Bash write to standing.md should be denied")
	if !strings.Contains(out, "user-only: standing.md cannot be written by coordinator") {
		t.Errorf("wrong denial reason: %q", out)
	}
}

func TestCoordSessionStartInjectsStandingMd(t *testing.T) {
	e := newCoordEnv(t)
	runDir := filepath.Join(e.outside, "run")
	e.m.RunDir = runDir
	e.save()

	// Create standing.md
	standingPath := filepath.Join(runDir, "standing.md")
	standingContent := "# Standing Orders\n\n1. Always verify tests pass\n2. No direct commits to main"
	writeFile(t, standingPath, standingContent)

	// Simulate SessionStart with resume source
	ev := map[string]any{
		"hook_event_name": "SessionStart",
		"session_id":      "sess-new",
		"source":          "resume",
		"cwd":             e.root,
	}

	out := e.fire(ev)
	if !strings.Contains(out, "standing orders for run") {
		t.Errorf("SessionStart should inject standing orders, got: %q", out)
	}
	if !strings.Contains(out, "Always verify tests pass") {
		t.Errorf("SessionStart should include standing.md content, got: %q", out)
	}
}

func TestCoordWorkerStartScopeRulingDenial(t *testing.T) {
	e := newCoordEnv(t)

	// A contract exists, but the scope ruling has not approved its identity.
	c := &contract.Contract{Name: "side", Worktree: e.side, Allow: []string{"**"}, Scope: []string{"S1"}, ReportPath: filepath.Join(e.outside, "reports", "side.md")}
	if _, err := c.Save(); err != nil {
		t.Fatal(err)
	}
	out := e.bash("rein route launch --task side --run sprint -- orca orchestration worker-start --task unapproved-task --worktree path:" + e.side)
	e.expect(true, out, "worker-start without approval should be denied")
	if !strings.Contains(out, "is not in the approved scope ruling") {
		t.Errorf("wrong denial reason: %q", out)
	}
	if !strings.Contains(out, "rein run allow --task") {
		t.Errorf("denial should suggest rein run allow: %q", out)
	}
}

func TestCoordWorkerStartScopeRulingAllowed(t *testing.T) {
	e := newCoordEnv(t)

	// Allow the task named "side" (matching the worktree name)
	e.m.Allowed = append(e.m.Allowed, run.Allowance{Kind: "task", Ref: "side", Reason: "test", At: "2026-10-08"})
	e.save()

	// Create a contract for the task with the same name as the worktree
	c := &contract.Contract{
		Name:       "side",
		Worktree:   e.side,
		Allow:      []string{"**"},
		Deny:       contract.AlwaysDeny,
		Scope:      []string{"S1"},
		ReportPath: filepath.Join(e.outside, "reports", "side.md"),
	}
	b, _ := json.Marshal(c)
	writeFile(t, filepath.Join(e.contract, "side.json"), string(b))

	// Now worker-start should be allowed with an audited route.
	prepareGuardRouteFor(t, c, e.m.Run, "claude", "claude-sonnet", "orca", "worker:backend")
	out := e.bash("rein route launch --task side --run sprint -- orca orchestration worker-start --run sprint --agent claude --model claude-sonnet --task side --worktree path:" + e.side)
	e.expect(false, out, "worker-start with approval should be allowed")
}
