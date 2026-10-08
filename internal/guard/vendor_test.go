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

// call runs one vendor event (a verbatim-shaped fixture, with the worktree substituted) and returns what the
// vendor's hook runner would see: stdout, stderr, exit code.
func call(t *testing.T, vendor, fixture, wt string) (string, string, int) {
	t.Helper()
	// @WT@ sits inside JSON strings: insert it JSON-escaped (Windows paths carry backslashes).
	in := strings.ReplaceAll(fixture, "@WT@", strings.Trim(jstr(wt), `"`))
	var out, errb bytes.Buffer
	code := RunVendor(vendor, strings.NewReader(in), &out, &errb)
	return out.String(), errb.String(), code
}

// jstr renders a Go string as a JSON string literal for use inside a fixture.
func jstr(s string) string { b, _ := json.Marshal(s); return string(b) }

const (
	codexPre  = `{"session_id":"s","turn_id":"t","transcript_path":"/x","cwd":"@WT@","hook_event_name":"PreToolUse","model":"gpt-6-luna","permission_mode":"bypassPermissions","tool_name":%s,"tool_input":%s,"tool_use_id":"exec-1"}`
	agyPre    = `{"artifactDirectoryPath":"/a","conversationId":"c","modelName":"gemini-3.8-flash-low","stepIdx":2,"toolCall":{"args":%s,"name":%s},"transcriptPath":"/t","workspacePaths":["@WT@"]}`
	kiroPre   = `{"hook_event_name":"preToolUse","cwd":"@WT@","tool_name":%s,"tool_input":%s}`
	opencodeP = `{"tool":%s,"input":%s,"cwd":"@WT@"}`
)

func f2(format, a, b string) string {
	return strings.Replace(strings.Replace(format, "%s", a, 1), "%s", b, 1)
}

func TestCodex(t *testing.T) {
	wt, _ := setup(t)
	patch := func(body string) string {
		return f2(codexPre, `"apply_patch"`, `{"command":`+jstr("*** Begin Patch\n"+body+"\n*** End Patch")+`}`)
	}
	deny := map[string]string{
		"push":              f2(codexPre, `"Bash"`, `{"command":"git push origin HEAD"}`),
		"update outside":    patch("*** Update File: frontend/src/x.ts\n@@\n-x\n+y"),
		"add outside":       patch("*** Add File: other.txt\n+hi"),
		"delete never-edit": patch("*** Delete File: VERSION"),
		"move to outside":   patch("*** Update File: backend/Routing/A.cs\n*** Move to: frontend/src/moved.ts\n@@\n-x\n+y"),
		"second file out":   patch("*** Update File: backend/Routing/A.cs\n@@\n-x\n+y\n*** Add File: /etc/evil\n+x"),
		"no path readable":  patch("garbage"),
	}
	for name, ev := range deny {
		out, _, code := call(t, "codex", ev, wt)
		if code != 0 || !strings.Contains(out, `"permissionDecision":"deny"`) {
			t.Errorf("%s: want a Claude-style deny, got %q (exit %d)", name, out, code)
		}
	}
	for name, ev := range map[string]string{
		"owned update": patch("*** Update File: backend/Routing/A.cs\n@@\n-x\n+y"),
		"owned add":    patch("*** Add File: backend/Routing/New.cs\n+hi"),
		"read":         f2(codexPre, `"Bash"`, `{"command":"cat backend/Routing/A.cs"}`),
		"mcp tool":     f2(codexPre, `"mcp__x__y"`, `{}`),
	} {
		if out, _, code := call(t, "codex", ev, wt); out != "" || code != 0 {
			t.Errorf("%s: want allow, got %q (exit %d)", name, out, code)
		}
	}
	stop := `{"session_id":"s","cwd":"@WT@","hook_event_name":"Stop","model":"m","permission_mode":"p","stop_hook_active":false,"last_assistant_message":"ok"}`
	if out, _, _ := call(t, "codex", stop, wt); !strings.Contains(out, `"decision":"block"`) {
		t.Errorf("stop without a report must block, got %q", out)
	}
	if out, _, _ := call(t, "codex", strings.Replace(stop, `"stop_hook_active":false`, `"stop_hook_active":true`, 1), wt); out != "" {
		t.Errorf("second stop must pass, got %q", out)
	}
}

func TestAgy(t *testing.T) {
	wt, _ := setup(t)
	deny := map[string]string{
		"push":           f2(agyPre, `{"CommandLine":"git push origin HEAD","Cwd":"@WT@","WaitMsBeforeAsync":5000}`, `"run_command"`),
		"push elsewhere": f2(agyPre, `{"CommandLine":"git push","Cwd":"/"}`, `"run_command"`), // cwd outside: still this worker (workspacePaths)
		"write":          f2(agyPre, `{"TargetFile":"@WT@/frontend/src/x.ts","CodeContent":"x","Overwrite":true}`, `"write_to_file"`),
		"edit":           f2(agyPre, `{"TargetFile":"@WT@/VERSION","TargetContent":"x","ReplacementContent":"y","StartLine":1,"EndLine":1}`, `"replace_file_content"`),
		"multi edit":     f2(agyPre, `{"TargetFile":"/etc/hosts","ReplacementChunks":[]}`, `"multi_replace_file_content"`),
		"unseen writer":  f2(agyPre, `{"TargetFile":"@WT@/frontend/src/x.ts"}`, `"some_new_edit_tool"`),
		"write no path":  f2(agyPre, `{"CodeContent":"x"}`, `"write_to_file"`),
	}
	for name, ev := range deny {
		out, _, code := call(t, "agy", ev, wt)
		if code != 0 || !strings.Contains(out, `"decision":"deny"`) || !strings.Contains(out, "[rein]") {
			t.Errorf("%s: want agy deny JSON, got %q (exit %d)", name, out, code)
		}
	}
	for name, ev := range map[string]string{
		"owned edit":  f2(agyPre, `{"TargetFile":"@WT@/backend/Routing/A.cs","TargetContent":"x","ReplacementContent":"y"}`, `"replace_file_content"`),
		"owned write": f2(agyPre, `{"TargetFile":"@WT@/backend/Routing/N.cs","CodeContent":"x"}`, `"write_to_file"`),
		"view":        f2(agyPre, `{"AbsolutePath":"/etc/hosts"}`, `"view_file"`),
		"ls":          f2(agyPre, `{"CommandLine":"ls","Cwd":"@WT@"}`, `"run_command"`),
		"task status": f2(agyPre, `{"Action":"status","TaskId":"x"}`, `"manage_task"`),
	} {
		if out, _, code := call(t, "agy", ev, wt); out != "" || code != 0 {
			t.Errorf("%s: want allow, got %q (exit %d)", name, out, code)
		}
	}
	stop := `{"conversationId":"c","executionNum":0,"fullyIdle":true,"terminationReason":"NO_TOOL_CALL","error":"","modelName":"m","transcriptPath":"/t","workspacePaths":["@WT@"]}`
	if out, _, _ := call(t, "agy", stop, wt); !strings.Contains(out, `"decision":"continue"`) || !strings.Contains(out, "write your report") {
		t.Errorf("agy stop must ask to continue with the reason, got %q", out)
	}
}

func TestKiro(t *testing.T) {
	wt, c := setup(t)
	for name, ev := range map[string]string{
		"push":      f2(kiroPre, `"execute_bash"`, `{"command":"git push","summary":"s"}`),
		"create":    f2(kiroPre, `"fs_write"`, `{"command":"create","path":"@WT@/frontend/src/n.ts","file_text":"x"}`),
		"replace":   f2(kiroPre, `"fs_write"`, `{"command":"str_replace","path":"@WT@/VERSION","old_str":"a","new_str":"b"}`),
		"insert":    f2(kiroPre, `"fs_write"`, `{"command":"insert","path":"/etc/hosts","new_str":"x","insert_line":1}`),
		"no path":   f2(kiroPre, `"fs_write"`, `{"command":"append"}`),
		"report r1": f2(kiroPre, `"fs_write"`, `{"command":"create","path":`+jstr(strings.ReplaceAll(c.ReportPath, "good.md", "other.md"))+`,"file_text":"x"}`),
	} {
		out, errb, code := call(t, "kiro", ev, wt)
		if code != 2 || !strings.HasPrefix(errb, "[rein] ") || out != "" {
			t.Errorf("%s: want exit 2 + reason on stderr, got stdout %q stderr %q exit %d", name, out, errb, code)
		}
	}
	for name, ev := range map[string]string{
		"owned replace": f2(kiroPre, `"fs_write"`, `{"command":"str_replace","path":"@WT@/backend/Routing/A.cs","old_str":"a","new_str":"b"}`),
		"read":          f2(kiroPre, `"fs_read"`, `{"operations":[{"mode":"Line","path":"/etc/hosts"}]}`),
		"ls":            f2(kiroPre, `"execute_bash"`, `{"command":"ls"}`),
	} {
		if out, errb, code := call(t, "kiro", ev, wt); out != "" || errb != "" || code != 0 {
			t.Errorf("%s: want allow, got %q %q (exit %d)", name, out, errb, code)
		}
	}
	stop := `{"hook_event_name":"stop","cwd":"@WT@","assistant_response":"done"}`
	if _, errb, code := call(t, "kiro", stop, wt); code != 2 || !strings.Contains(errb, "write your report") {
		t.Errorf("kiro stop warns with the reason (exit 2), got %q exit %d", errb, code)
	}
}

func TestOpencode(t *testing.T) {
	wt, _ := setup(t)
	for name, ev := range map[string]string{
		"push":    f2(opencodeP, `"shell"`, `{"command":"git push"}`),
		"edit":    f2(opencodeP, `"edit"`, `{"path":"frontend/src/x.ts","oldString":"a","newString":"b"}`),
		"write":   f2(opencodeP, `"write"`, `{"path":"/etc/evil","content":"x"}`),
		"patch":   f2(opencodeP, `"patch"`, `{"patchText":`+jstr("*** Begin Patch\n*** Add File: other.txt\n+x\n*** End Patch")+`}`),
		"no path": f2(opencodeP, `"edit"`, `{"oldString":"a"}`),
	} {
		out, errb, code := call(t, "opencode", ev, wt)
		if code != 2 || !strings.HasPrefix(errb, "[rein] ") || out != "" {
			t.Errorf("%s: want exit 2 + reason on stderr, got %q %q (exit %d)", name, out, errb, code)
		}
	}
	for name, ev := range map[string]string{
		"owned edit":  f2(opencodeP, `"edit"`, `{"path":"backend/Routing/A.cs","oldString":"a","newString":"b"}`),
		"owned write": f2(opencodeP, `"write"`, `{"path":"backend/Routing/N.cs","content":"x"}`),
		"read":        f2(opencodeP, `"read"`, `{"path":"/etc/hosts"}`),
		"code mode":   f2(opencodeP, `"execute"`, `{"code":"return 1"}`), // unclassified: allowed, logged, drift is the backstop
	} {
		if out, errb, code := call(t, "opencode", ev, wt); out != "" || errb != "" || code != 0 {
			t.Errorf("%s: want allow, got %q %q (exit %d)", name, out, errb, code)
		}
	}
}

// The coordinator-subagent deny is Claude-only: an Agent call with subagent_type rein:orca-swarm/steward from any
// other vendor must be judged exactly as before (parseClaude also serves codex, so that is the leak to watch).
func TestAgentDenyIsClaudeOnly(t *testing.T) {
	wt, _ := setup(t)
	outside := t.TempDir()
	_ = os.MkdirAll(filepath.Join(outside, ".git"), 0o755)
	denied, silent := `"Agent"`, `{"subagent_type":"rein:orca-swarm","prompt":"p"}`
	for vendor, ev := range map[string]string{
		"codex":    f2(codexPre, denied, silent),
		"kiro":     f2(kiroPre, denied, silent),
		"opencode": f2(opencodeP, denied, silent),
		"agy":      f2(agyPre, silent, denied),
	} {
		if out, errb, code := call(t, vendor, ev, wt); out != "" || errb != "" || code != 0 {
			t.Errorf("%s Agent with subagent_type rein:orca-swarm inside a worker must stay allowed, got %q %q (exit %d)", vendor, out, errb, code)
		}
	}
	if out, errb, code := call(t, "codex", f2(codexPre, denied, silent), outside); out != "" || errb != "" || code != 0 {
		t.Errorf("codex Agent with subagent_type rein:orca-swarm outside a worker must stay silent, got %q %q (exit %d)", out, errb, code)
	}
}

func TestSeenLog(t *testing.T) {
	wt, c := setup(t)
	call(t, "codex", f2(codexPre, `"Bash"`, `{"command":"ls"}`), wt)
	call(t, "kiro", f2(kiroPre, `"fs write"`, `{}`), wt)
	call(t, "agy", `{"terminationReason":"NO_TOOL_CALL","workspacePaths":["@WT@"]}`, wt)
	b, err := os.ReadFile(contract.SeenPath(c.Name))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 seen lines, got %q", lines)
	}
	for i, want := range []string{" codex pretool Bash", " kiro pretool fs_write", " agy stop -"} {
		if !strings.HasSuffix(lines[i], want) {
			t.Errorf("line %d = %q, want suffix %q", i, lines[i], want)
		}
	}
	// outside a worker nothing is logged
	other := t.TempDir()
	call(t, "codex", f2(codexPre, `"Bash"`, `{"command":"ls"}`), other)
	if b2, _ := os.ReadFile(contract.SeenPath(c.Name)); string(b2) != string(b) {
		t.Error("a call outside the worktree changed the seen log")
	}
}

func TestPatchPaths(t *testing.T) {
	p := "*** Begin Patch\r\n*** Update File: a/b.go\r\n*** Move to: c\\d.go\r\n*** Add File:  e.go \r\n*** Delete File: f.go\r\n*** End Patch"
	got := strings.Join(patchPaths(p), "|")
	if got != "a/b.go|c\\d.go|e.go|f.go" && got != "a/b.go|e.go|f.go|c\\d.go" {
		t.Errorf("patchPaths = %q", got)
	}
	if patchPaths("hello") != nil {
		t.Error("no headers must give nil")
	}
}

func TestBadVendorInput(t *testing.T) {
	for _, v := range Vendors {
		for _, in := range []string{"", "not json", "{}", `{"toolCall":null}`, `[1]`} {
			if _, _, code := call(t, v, in, t.TempDir()); code != 0 {
				t.Errorf("%s %q: hostile input must exit 0, got %d", v, in, code)
			}
		}
	}
}

// callTask runs an event through a hook bound to a task (what rein hooks install writes).
func callTask(t *testing.T, vendor, task, fixture, wt string) (string, string, int) {
	t.Helper()
	var out, errb bytes.Buffer
	code := RunTask(vendor, task, strings.NewReader(strings.ReplaceAll(fixture, "@WT@", wt)), &out, &errb)
	return out.String(), errb.String(), code
}

func TestBoundHookJudgesItsOwnContract(t *testing.T) {
	wt, _ := setup(t)
	elsewhere := t.TempDir() // the event reports a directory that is not the worktree (and holds no .git)
	push := `{"tool":"shell","input":{"command":"git push"},"cwd":` + jstr(elsewhere) + `}`
	if _, errb, code := callTask(t, "opencode", "good", push, wt); code != 2 || !strings.HasPrefix(errb, "[rein] ") {
		t.Errorf("a bound hook must deny from any reported directory, got %q (exit %d)", errb, code)
	}
	if _, errb, code := callTask(t, "opencode", "good", strings.Replace(push, jstr(elsewhere), jstr(wt), 1), wt); code != 2 || !strings.Contains(errb, "workers never push") {
		t.Errorf("got %q (exit %d)", errb, code)
	}
	// agy: workspace and Cwd both elsewhere
	agyPush := f2(agyPre, `{"CommandLine":"git push","Cwd":`+jstr(elsewhere)+`}`, `"run_command"`)
	agyPush = strings.Replace(agyPush, `"@WT@"`, jstr(elsewhere), 1)
	if out, _, _ := callTask(t, "agy", "good", agyPush, wt); !strings.Contains(out, `"decision":"deny"`) {
		t.Errorf("bound agy hook must deny, got %q", out)
	}
	// fail closed: missing contract, unreadable event, empty input
	for name, in := range map[string]string{"garbage": "not json", "empty": "", "no tool": `{}`} {
		if _, errb, code := callTask(t, "kiro", "good", in, wt); code != 2 || errb == "" {
			t.Errorf("%s: a bound hook must deny what it cannot read, got %q (exit %d)", name, errb, code)
		}
	}
	if _, errb, code := callTask(t, "kiro", "no-such-task", f2(kiroPre, `"fs_read"`, `{}`), wt); code != 2 || !strings.Contains(errb, "cannot load its contract") {
		t.Errorf("a missing contract must deny, got %q (exit %d)", errb, code)
	}
	// the unbound (global Claude plugin) hook keeps today's behaviour: silent outside workers and on garbage
	if out, _, code := call(t, "claude", "not json", elsewhere); out != "" || code != 0 {
		t.Errorf("unbound hook must stay silent, got %q (exit %d)", out, code)
	}
}

func TestWorkdirIsTheCommandCwd(t *testing.T) {
	wt, _ := setup(t)
	other := filepath.Join(filepath.Dir(wt), "another-checkout") // not this task's work (and not a scratch dir)
	_ = os.MkdirAll(other+"/.git", 0o755)
	commit := func(workdir string) string {
		return `{"tool":"shell","input":{"command":"git commit -am x","workdir":` + jstr(workdir) + `},"cwd":@WTJ@}`
	}
	run := func(ev string) int {
		_, _, code := call(t, "opencode", strings.ReplaceAll(ev, "@WTJ@", jstr(wt)), wt)
		return code
	}
	if run(commit(wt)) != 0 {
		t.Fatal("a commit in the worktree must stay allowed")
	}
	if run(commit(other)) != 2 {
		t.Error("opencode workdir pointing at another checkout must be denied")
	}
	codexEv := f2(codexPre, `"Bash"`, `{"command":"git commit -am x","workdir":`+jstr(other)+`}`)
	if out, _, _ := call(t, "codex", codexEv, wt); !strings.Contains(out, `"deny"`) {
		t.Errorf("codex workdir pointing at another checkout must be denied, got %q", out)
	}
}

func TestPatchInsideShellCommand(t *testing.T) {
	wt, _ := setup(t)
	bash := func(cmd string) string { return f2(codexPre, `"Bash"`, `{"command":`+jstr(cmd)+`}`) }
	deny := []string{
		"apply_patch <<'EOF'\n*** Begin Patch\n*** Add File: docs/x.md\n+x\n*** End Patch\nEOF",
		"apply_patch <<'EOF'\n*** Begin Patch\n  *** Update File: VERSION\n@@\n-x\n+y\n*** End Patch\nEOF", // indented header
		"echo '*** Begin Patch' | apply_patch",                                                             // marker, no readable path
	}
	for _, cmd := range deny {
		if out, _, _ := call(t, "codex", bash(cmd), wt); !strings.Contains(out, `"deny"`) {
			t.Errorf("want deny for %q, got %q", cmd, out)
		}
	}
	ok := "apply_patch <<'EOF'\n*** Begin Patch\n*** Add File: backend/Routing/N.cs\n+x\n*** End Patch\nEOF"
	if out, _, _ := call(t, "codex", bash(ok), wt); out != "" {
		t.Errorf("an owned patch must pass, got %q", out)
	}
	oc := `{"tool":"bash","input":{"command":` + jstr(deny[0]) + `},"cwd":"@WT@"}`
	if _, _, code := call(t, "opencode", oc, wt); code != 2 {
		t.Error("opencode bash patch outside ownership must be denied")
	}
}

func TestKnownWritersWithoutPathAreDenied(t *testing.T) {
	wt, _ := setup(t)
	for _, tool := range []string{"Edit", "Write", "MultiEdit", "NotebookEdit", "apply_patch"} {
		if out, _, _ := call(t, "codex", f2(codexPre, jstr(tool), `{"content":"x"}`), wt); !strings.Contains(out, `"deny"`) {
			t.Errorf("codex %s with no readable target must be denied, got %q", tool, out)
		}
	}
	if out, _, _ := call(t, "codex", f2(codexPre, `"Write"`, `{"file_path":"@WT@/VERSION"}`), wt); !strings.Contains(out, `"deny"`) {
		t.Errorf("codex Write to a never-edit file must be denied, got %q", out)
	}
	if out, _, _ := call(t, "codex", f2(codexPre, `"Edit"`, `{"command":`+jstr("*** Begin Patch\n*** Add File: docs/x\n+x\n*** End Patch")+`}`), wt); !strings.Contains(out, `"deny"`) {
		t.Errorf("a patch string inside Edit must be judged, got %q", out)
	}
	// Claude's own behaviour is unchanged: a path-less Edit is not judged
	claude := `{"hook_event_name":"PreToolUse","tool_name":"Edit","cwd":"@WT@","tool_input":{}}`
	if out, _, _ := call(t, "claude", claude, wt); out != "" {
		t.Errorf("claude path-less Edit must stay as before, got %q", out)
	}
}

func TestAgyPathLikeArgs(t *testing.T) {
	wt, _ := setup(t)
	for _, args := range []string{`{"AbsolutePath":"/etc/hosts"}`, `{"FilePath":"/etc/hosts"}`, `{"Path":"/etc/hosts"}`, `{"File":"/etc/hosts"}`} {
		if out, _, _ := call(t, "agy", f2(agyPre, args, `"future_edit_tool"`), wt); !strings.Contains(out, `"deny"`) {
			t.Errorf("an unknown tool with %s must be judged as a write, got %q", args, out)
		}
	}
	for _, tool := range []string{"view_file", "view_file_outline", "list_dir", "grep_search", "find_by_name", "read_url_content"} {
		if out, _, _ := call(t, "agy", f2(agyPre, `{"AbsolutePath":"/etc/hosts","Path":"/etc"}`, jstr(tool)), wt); out != "" {
			t.Errorf("read-only tool %s must stay allowed, got %q", tool, out)
		}
	}
}

func TestHookFilesAreProtected(t *testing.T) {
	wt, _ := setup(t)
	for _, f := range []string{".codex/hooks.json", ".agents/hooks.json", ".kiro/agents/rein.json", ".opencode/plugins/rein/server.js", ".opencode/plugins/rein/extra.js", ".claude/settings.local.json"} {
		if out, _, _ := call(t, "agy", f2(agyPre, `{"TargetFile":"@WT@/`+f+`","CodeContent":"x"}`, `"write_to_file"`), wt); !strings.Contains(out, "rein guard") {
			t.Errorf("edit of %s must be denied as a guard file, got %q", f, out)
		}
	}
	for _, f := range []string{".codex/hooks.json", ".opencode/plugins/rein/server.js"} { // dirs that hold a hook file
		_ = os.MkdirAll(filepath.Dir(filepath.Join(wt, f)), 0o755)
		_ = os.WriteFile(filepath.Join(wt, f), []byte("{}"), 0o644)
	}
	for _, cmd := range []string{
		"rm .codex/hooks.json", "rm -rf .codex", "mv .opencode /tmp/x", "echo {} > .codex/hooks.json",
		"sed -i 's/a/b/' .codex/hooks.json", "tee .agents/hooks.json", "rm -rf .opencode/plugins/rein",
	} {
		if out := bashCase(t, wt, cmd); !strings.Contains(out, `"deny"`) {
			t.Errorf("%q must be denied, got %q", cmd, out)
		}
	}
}

func TestClaudeKeepsFirstJSONValue(t *testing.T) {
	wt, _ := setup(t)
	ev := `{"hook_event_name":"PreToolUse","tool_name":"Bash","cwd":` + jstr(wt) + `,"tool_input":{"command":"git push"}} trailing junk {"x":`
	var out bytes.Buffer
	Run(strings.NewReader(ev), &out)
	if !strings.Contains(out.String(), `"deny"`) {
		t.Errorf("the first JSON value must be judged even with trailing input, got %q", out.String())
	}
}

func TestInteractiveShellIsDenied(t *testing.T) {
	wt, _ := setup(t)
	// codex write_stdin does not reach PreToolUse (probed), so text typed into a shell would never be judged
	for _, cmd := range []string{"bash", "sh -i", "zsh", "bash -s", "env bash"} {
		if out := bashCase(t, wt, cmd); !strings.Contains(out, "read commands from stdin") {
			t.Errorf("%q must be denied, got %q", cmd, out)
		}
	}
	for _, cmd := range []string{"bash -c 'ls'", "sh scripts/x.sh", "bash ./gate.sh --all"} {
		if out := bashCase(t, wt, cmd); out != "" {
			t.Errorf("%q must stay allowed, got %q", cmd, out)
		}
	}
}

func TestStdinShells(t *testing.T) {
	wt, _ := setup(t)
	deny := []string{
		"bash -s harmless", "bash -s", "sh -i x.sh", "bash -si", "bash -is", "bash",
		"printf 'git push' | bash -s harmless", "printf 'git push' | bash", "echo ls | sh", "bash < cmds.txt",
		"bash -o pipefail -s", "bash <<EOF\n$(whoami)\nEOF",
		"bash <<'EOF'\ngit push\nEOF",    // a literal heredoc is judged, and this body is a push
		"bash -s <<'EOF'\ngit push\nEOF", // same with -s
	}
	for _, cmd := range deny {
		if out := bashCase(t, wt, cmd); !strings.Contains(out, `"deny"`) {
			t.Errorf("%q must be denied, got %q", cmd, out)
		}
	}
	allow := []string{
		"bash scripts/gate.sh", "bash -c 'ls'", "bash -euo pipefail -c 'ls'", "sh -e x.sh", "bash -o pipefail x.sh arg1",
		"bash --rcfile rc.sh -c 'ls'", "bash -ilc 'ls'",
		"bash <<'EOF'\nls\nEOF", "bash -s <<'EOF'\necho hi\nEOF",
	}
	for _, cmd := range allow {
		if out := bashCase(t, wt, cmd); out != "" {
			t.Errorf("%q must stay allowed, got %q", cmd, out)
		}
	}
}

func TestHookFilesVsGlobsAndFind(t *testing.T) {
	wt, _ := setup(t)
	for _, f := range []string{".codex/hooks.json", ".opencode/plugins/rein/server.js", ".agents/hooks.json"} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(wt, f)), 0o755)
		_ = os.WriteFile(filepath.Join(wt, f), []byte("{}"), 0o644)
	}
	deny := []string{
		"rm .codex/*", "rm -rf .*", "rm -rf .[a-z]*", "rm -rf .co*", "mv .agents/* /tmp/x", "echo {} > .codex/h*.json",
		"sed -i 's/a/b/' .codex/*.json", "tee .agents/hook?.json", "truncate -s0 .codex/hooks.js*",
		"find . -delete", "find .opencode -exec rm {} +", "find . -name hooks.json -execdir rm {} ;", "find .codex -ok rm {} ;",
		"find -delete", "git stash -a", "git stash --all", "git stash push --include-untracked", "git stash -u",
	}
	for _, cmd := range deny {
		if out := bashCase(t, wt, cmd); !strings.Contains(out, `"deny"`) {
			t.Errorf("%q must be denied, got %q", cmd, out)
		}
	}
	allow := []string{"rm backend/Routing/*.tmp", "rm -rf *", "find . -name '*.go'", "find backend -delete", "git stash", "git stash pop", "ls .codex/*", "cat .*"}
	for _, cmd := range allow {
		if out := bashCase(t, wt, cmd); out != "" {
			t.Errorf("%q must stay allowed, got %q", cmd, out)
		}
	}
}

func TestBeginPatchOnlyWhenApplyPatchRuns(t *testing.T) {
	wt, _ := setup(t)
	for _, cmd := range []string{"grep -F '*** Begin Patch' backend/Routing/A.cs", "echo '*** Begin Patch' > backend/Routing/note.txt", "git log --grep='*** Begin Patch'"} {
		if out := bashCase(t, wt, cmd); out != "" {
			t.Errorf("%q only mentions the marker and must be allowed, got %q", cmd, out)
		}
	}
	body := " <<'EOF'\n*** Begin Patch\n*** Add File: docs/x.md\n+x\n*** End Patch\nEOF"
	for _, call := range []string{"apply_patch", "/tmp/tools/apply_patch", "./applypatch", `"apply_patch"`, "bash -c 'apply_patch", "env X=1 /opt/bin/apply_patch"} {
		cmd := call + body
		if strings.HasPrefix(call, "bash -c '") {
			cmd = call + body + "'"
		}
		if out := bashCase(t, wt, cmd); !strings.Contains(out, `"deny"`) {
			t.Errorf("%q runs apply_patch and must be judged as a patch, got %q", call, out)
		}
	}
	if out := bashCase(t, wt, "apply_patch<<'EOF'\n*** Begin Patch\n*** Add File: docs/x.md\n+x\n*** End Patch\nEOF"); !strings.Contains(out, `"deny"`) {
		t.Errorf("apply_patch glued to its heredoc must be judged, got %q", out)
	}
}

func TestGitInTempScratchDirs(t *testing.T) {
	wt, _ := setup(t)
	scratch := t.TempDir()
	run := func(cwd, cmd string) string {
		in, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash", "cwd": wt, "tool_input": map[string]string{"command": "cd " + cwd + " && " + cmd}})
		var out bytes.Buffer
		Run(bytes.NewReader(in), &out)
		return out.String()
	}
	if out := run(scratch, "git init -q && git commit -q --allow-empty -m x"); out != "" {
		t.Errorf("git in a temp scratch dir must be allowed, got %q", out)
	}
	sibling := filepath.Join(filepath.Dir(wt), "other-task") // another task's worktree area (also under temp)
	_ = os.MkdirAll(sibling, 0o755)
	if out := run(sibling, "git commit -q --allow-empty -m x"); !strings.Contains(out, `"deny"`) {
		t.Errorf("git in another task's worktree area must stay denied, got %q", out)
	}
	if out := run("/usr", "git commit -q --allow-empty -m x"); !strings.Contains(out, `"deny"`) {
		t.Errorf("git outside temp and worktree must stay denied, got %q", out)
	}
}
