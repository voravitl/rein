package hooks

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voravitl/rein/internal/contract"
)

func git(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s", args, out)
	}
	return string(out)
}

func setup(t *testing.T) (root, wt string, c *contract.Contract) {
	t.Helper()
	root = t.TempDir()
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(root, "contracts"))
	m := filepath.Join(root, "m")
	git(t, "init", "-q", m)
	git(t, "-C", m, "commit", "-q", "--allow-empty", "-m", "base")
	wt = filepath.Join(root, "wts", "w")
	git(t, "-C", m, "worktree", "add", "-q", "-b", "w", wt)
	c, err := contract.New("w", 0, filepath.Join(root, "run"), "src/**", "", "S1", filepath.Join(root, "wts"), "", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	return root, wt, c
}

func mustJSON(t *testing.T, p string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s is not valid JSON: %v\n%s", p, err, b)
	}
	return m
}

func TestInstallWritesExcludesAndRecords(t *testing.T) {
	root, wt, c := setup(t)
	bin := filepath.Join(root, "my bin", "rein") // a space: the sh command must quote it
	var first map[string]string
	for i := 0; i < 2; i++ { // idempotent
		res, saved, err := Install(c, bin, All)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range res {
			if r.Err != nil || len(r.Files) == 0 || r.Launch == "" {
				t.Fatalf("%s: %+v", r.Vendor, r)
			}
		}
		if len(saved) != 2 {
			t.Errorf("both contract copies must be updated, got %v", saved)
		}
		now := map[string]string{}
		for _, f := range []string{".codex/hooks.json", ".agents/hooks.json", ".kiro/agents/rein.json", ".opencode/plugins/rein/server.js", ".claude/settings.local.json"} {
			b, err := os.ReadFile(filepath.Join(wt, filepath.FromSlash(f)))
			if err != nil {
				t.Fatal(err)
			}
			now[f] = string(b)
		}
		if first == nil {
			first = now
		} else {
			for f, s := range now {
				if s != first[f] {
					t.Errorf("%s changed on the second install", f)
				}
			}
		}
	}
	cx := mustJSON(t, filepath.Join(wt, ".codex/hooks.json"))["hooks"].(map[string]any)
	preToolUseRaw, ok := cx["PreToolUse"].([]any)
	if !ok || len(preToolUseRaw) == 0 {
		t.Fatalf("codex PreToolUse must be a non-empty array, got %T", cx["PreToolUse"])
	}
	preToolUseGroup, ok := preToolUseRaw[0].(map[string]any)
	if !ok {
		t.Fatalf("codex PreToolUse[0] must be a map, got %T", preToolUseRaw[0])
	}
	hooksRaw, ok := preToolUseGroup["hooks"].([]any)
	if !ok || len(hooksRaw) == 0 {
		t.Fatalf("codex PreToolUse hooks must be a non-empty array, got %T", preToolUseGroup["hooks"])
	}
	hook0, ok := hooksRaw[0].(map[string]any)
	if !ok {
		t.Fatalf("codex hooks[0] must be a map, got %T", hooksRaw[0])
	}
	cmd, ok := hook0["command"].(string)
	if !ok {
		t.Fatalf("codex command must be a string, got %T", hook0["command"])
	}
	if want := "'" + bin + "' hook --vendor codex --task w"; cmd != want { // always quoted, bound to the task
		t.Errorf("codex command = %q, want %q", cmd, want)
	}
	stopRaw, ok := cx["Stop"].([]any)
	if !ok {
		t.Fatalf("codex Stop must be an array, got %T", cx["Stop"])
	}
	if len(preToolUseRaw) != 1 || len(stopRaw) != 1 {
		t.Errorf("codex groups duplicated: %v", cx)
	}
	// Check PostToolUse is present
	postToolUseRaw, ok := cx["PostToolUse"].([]any)
	if !ok {
		t.Error("codex hooks must include PostToolUse as an array")
	}
	if len(postToolUseRaw) != 1 {
		t.Errorf("codex PostToolUse should have 1 group, got %d", len(postToolUseRaw))
	}
	if len(postToolUseRaw) > 0 {
		postGroup, ok := postToolUseRaw[0].(map[string]any)
		if !ok {
			t.Fatalf("codex PostToolUse[0] must be a map, got %T", postToolUseRaw[0])
		}
		if postGroup["matcher"] != "AskUserQuestion" {
			t.Errorf("PostToolUse matcher = %q, want AskUserQuestion", postGroup["matcher"])
		}
	}
	agy := mustJSON(t, filepath.Join(wt, ".agents/hooks.json"))["rein"].(map[string]any)
	agyStopRaw, ok := agy["Stop"].([]any)
	if !ok || len(agyStopRaw) == 0 {
		t.Fatalf("agy Stop must be a non-empty array, got %T", agy["Stop"])
	}
	agyStop0, ok := agyStopRaw[0].(map[string]any)
	if !ok {
		t.Fatalf("agy Stop[0] must be a map, got %T", agyStopRaw[0])
	}
	if _, flat := agyStop0["command"]; !flat {
		t.Errorf("agy Stop must be flat: %v", agy["Stop"])
	}
	// Check Claude settings.local.json has AskUserQuestion in matcher and PostToolUse
	claude := mustJSON(t, filepath.Join(wt, ".claude/settings.local.json"))["hooks"].(map[string]any)
	claudePreToolUseRaw, ok := claude["PreToolUse"].([]any)
	if !ok || len(claudePreToolUseRaw) == 0 {
		t.Fatalf("Claude PreToolUse must be a non-empty array, got %T", claude["PreToolUse"])
	}
	claudePreToolUse, ok := claudePreToolUseRaw[0].(map[string]any)
	if !ok {
		t.Fatalf("Claude PreToolUse[0] must be a map, got %T", claudePreToolUseRaw[0])
	}
	claudeMatcher, ok := claudePreToolUse["matcher"].(string)
	if !ok {
		t.Fatalf("Claude matcher must be a string, got %T", claudePreToolUse["matcher"])
	}
	if !strings.Contains(claudeMatcher, "AskUserQuestion") {
		t.Errorf("Claude PreToolUse matcher should include AskUserQuestion, got: %q", claudeMatcher)
	}
	claudePostToolUseRaw, ok := claude["PostToolUse"].([]any)
	if !ok {
		t.Error("Claude hooks must include PostToolUse as an array")
	}
	if len(claudePostToolUseRaw) != 1 {
		t.Errorf("Claude PostToolUse should have 1 group, got %d", len(claudePostToolUseRaw))
	}
	if len(claudePostToolUseRaw) > 0 {
		claudePostGroup, ok := claudePostToolUseRaw[0].(map[string]any)
		if !ok {
			t.Fatalf("Claude PostToolUse[0] must be a map, got %T", claudePostToolUseRaw[0])
		}
		if claudePostGroup["matcher"] != "AskUserQuestion" {
			t.Errorf("Claude PostToolUse matcher = %q, want AskUserQuestion", claudePostGroup["matcher"])
		}
	}
	kiro := mustJSON(t, filepath.Join(wt, ".kiro/agents/rein.json"))
	kiroHooksRaw, ok := kiro["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("kiro hooks must be a map, got %T", kiro["hooks"])
	}
	if kiro["name"] != "rein" || kiroHooksRaw["preToolUse"] == nil {
		t.Errorf("kiro agent: %v", kiro)
	}
	js, _ := os.ReadFile(filepath.Join(wt, ".opencode/plugins/rein/server.js"))
	canon, _ := filepath.EvalSymlinks(wt)
	binLit, _ := json.Marshal(bin)
	wtLit, _ := json.Marshal(canon)
	for _, want := range []string{"const REIN = " + string(binLit) + ";", `const REIN_TASK = "w";`, "const WORKTREE = " + string(wtLit) + ";", "r.status !== 0"} {
		if !strings.Contains(string(js), want) {
			t.Errorf("plugin lacks %q:\n%s", want, js)
		}
	}
	for _, bad := range []string{"process.cwd", "ctx.location", "status === 2"} {
		if strings.Contains(string(js), bad) {
			t.Errorf("plugin must not use %q (identity is fixed at install, any nonzero exit throws)", bad)
		}
	}
	for _, f := range []string{".agents/hooks.json", ".kiro/agents/rein.json"} {
		b, _ := os.ReadFile(filepath.Join(wt, f))
		if !strings.Contains(string(b), "--task w") {
			t.Errorf("%s must bind the hook to its task: %s", f, b)
		}
	}
	if node, err := exec.LookPath("node"); err == nil { // the plugin must at least parse
		if out, err := exec.Command(node, "--check", filepath.Join(wt, ".opencode/plugins/rein/server.js")).CombinedOutput(); err != nil {
			t.Errorf("node --check: %s", out)
		}
	}
	if out := git(t, "-C", wt, "status", "--porcelain"); out != "" {
		t.Errorf("hook files must be excluded from git status, got %q", out)
	}
	for _, p := range []string{contract.PathOf("w"), c.RunCopy} {
		got := mustJSON(t, p)["hooks_installed"].([]any)
		if len(got) != 5 {
			t.Errorf("%s: hooks_installed = %v", p, got)
		}
	}
}

func TestInstallMergesAndRefusesTracked(t *testing.T) {
	root, wt, c := setup(t)
	// an untracked user hook file is merged, not replaced
	_ = os.MkdirAll(filepath.Join(wt, ".codex"), 0o755)
	_ = os.WriteFile(filepath.Join(wt, ".codex/hooks.json"), []byte(`{"hooks":{"PreToolUse":[{"matcher":"x","hooks":[{"type":"command","command":"mine"}]}]},"other":1}`), 0o644)
	// a tracked file is refused
	_ = os.MkdirAll(filepath.Join(wt, ".kiro/agents"), 0o755)
	_ = os.WriteFile(filepath.Join(wt, ".kiro/agents/rein.json"), []byte(`{}`), 0o644)
	git(t, "-C", wt, "add", ".kiro/agents/rein.json")
	res, _, err := Install(c, filepath.Join(root, "rein"), []string{"codex", "kiro", "bogus"})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Err != nil || res[1].Err == nil || !strings.Contains(res[1].Err.Error(), "tracked") || res[2].Err == nil {
		t.Errorf("results: %+v", res)
	}
	doc := mustJSON(t, filepath.Join(wt, ".codex/hooks.json"))
	if doc["other"] == nil || len(doc["hooks"].(map[string]any)["PreToolUse"].([]any)) != 2 {
		t.Errorf("user hook lost: %v", doc)
	}
	if got := mustJSON(t, contract.PathOf("w"))["hooks_installed"].([]any); len(got) != 1 || got[0] != "codex" {
		t.Errorf("only successful vendors are recorded, got %v", got)
	}
}

func TestInstallNeedsWorktree(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(root, "contracts"))
	c, err := contract.New("none", 0, filepath.Join(root, "run"), "src/**", "", "S1", filepath.Join(root, "wts"), "", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Install(c, "/x/rein", Default); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("want worktree error, got %v", err)
	}
}

func TestInstallRefusesSymlinkedDestinations(t *testing.T) {
	root, wt, c := setup(t)
	global := filepath.Join(root, "global-hooks.json")
	_ = os.WriteFile(global, []byte(`{"keep":true}`), 0o644)
	_ = os.MkdirAll(filepath.Join(wt, ".agents"), 0o755)
	if err := os.Symlink(global, filepath.Join(wt, ".agents/hooks.json")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	outsideDir := filepath.Join(root, "elsewhere")
	_ = os.MkdirAll(outsideDir, 0o755)
	if err := os.Symlink(outsideDir, filepath.Join(wt, ".kiro")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	res, _, err := Install(c, filepath.Join(root, "rein"), []string{"agy", "kiro", "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Err == nil || !strings.Contains(res[0].Err.Error(), "symbolic link") || res[1].Err == nil || res[2].Err != nil {
		t.Errorf("results: %+v", res)
	}
	if b, _ := os.ReadFile(global); string(b) != `{"keep":true}` {
		t.Errorf("the file behind the symlink was rewritten: %s", b)
	}
	if ents, _ := os.ReadDir(outsideDir); len(ents) != 0 {
		t.Errorf("a file was written through the symlinked parent: %v", ents)
	}
}

func TestMergeKeepsSiblingHooksAndMetadata(t *testing.T) {
	root, wt, c := setup(t)
	_ = os.MkdirAll(filepath.Join(wt, ".codex"), 0o755)
	old := `{"hooks":{"PreToolUse":[{"matcher":"Bash","note":"mine","hooks":[{"type":"command","command":"audit.sh"},{"type":"command","command":"/old/rein hook --vendor codex"}]}]}}`
	_ = os.WriteFile(filepath.Join(wt, ".codex/hooks.json"), []byte(old), 0o644)
	for i := 0; i < 2; i++ {
		if _, _, err := Install(c, filepath.Join(root, "rein"), []string{"codex"}); err != nil {
			t.Fatal(err)
		}
	}
	groups := mustJSON(t, filepath.Join(wt, ".codex/hooks.json"))["hooks"].(map[string]any)["PreToolUse"].([]any)
	if len(groups) != 2 {
		t.Fatalf("want the user's group plus rein's, got %v", groups)
	}
	mine := groups[0].(map[string]any)
	if mine["note"] != "mine" || mine["matcher"] != "Bash" || len(mine["hooks"].([]any)) != 1 || mine["hooks"].([]any)[0].(map[string]any)["command"] != "audit.sh" {
		t.Errorf("sibling hook or group metadata lost: %v", mine)
	}
}

func TestContractCopies(t *testing.T) {
	root, wt, c := setup(t)
	// a relative --run-dir is stored absolute, so the copy is found from any directory
	if !filepath.IsAbs(c.RunCopy) {
		t.Errorf("RunCopy must be absolute, got %q", c.RunCopy)
	}
	// legacy contract: no run_copy; the copy is derived from the report path when it exists
	c.RunCopy = ""
	saved, err := c.Save()
	if err != nil || len(saved) != 2 {
		t.Errorf("derived copy must be updated: %v %v", saved, err)
	}
	// legacy contract whose run dir cannot be derived: only the index is updated, and the result says so
	c.ReportPath = filepath.Join(root, "somewhere", "report.md")
	if saved, err = c.Save(); err != nil || len(saved) != 1 {
		t.Errorf("underivable run copy must be skipped explicitly: %v %v", saved, err)
	}
	_ = wt
}

func TestCommandQuoting(t *testing.T) {
	got := command(`C:\Users\a b\rein.exe`, "kiro", "t 1")
	if want := `'C:\Users\a b\rein.exe' hook --vendor kiro --task 't 1'`; got != want {
		t.Errorf("command = %q, want %q", got, want)
	}
	if got := command("/o'k/rein", "agy", "t"); got != `'/o'\''k/rein' hook --vendor agy --task t` {
		t.Errorf("single quote not escaped: %q", got)
	}
	if !strings.Contains(CodexFlags(`C:\x\rein.exe`, "t"), `\\x\\rein.exe`) {
		t.Errorf("TOML must escape backslashes: %s", CodexFlags(`C:\x\rein.exe`, "t"))
	}
	if !strings.Contains(LaunchLine("codex", "/b/rein", "t"), "POSIX sh") {
		t.Error("launch advice must name its shell")
	}
}

func TestBinaryPath(t *testing.T) {
	data := t.TempDir()
	_ = os.MkdirAll(filepath.Join(data, "bin"), 0o755)
	name := "rein"
	if os.PathSeparator == '\\' {
		name += ".exe"
	}
	_ = os.WriteFile(filepath.Join(data, "bin", name), []byte("x"), 0o755)
	t.Setenv("CLAUDE_PLUGIN_DATA", data)
	if p, w, err := BinaryPath(); err != nil || p != filepath.Join(data, "bin", name) || w != "" {
		t.Errorf("plugin binary must win: %q %q %v", p, w, err)
	}
	t.Setenv("CLAUDE_PLUGIN_DATA", "")
	if _, w, err := BinaryPath(); err != nil || !strings.Contains(w, "temp directory") { // go test binaries live in a temp dir
		t.Errorf("a temp-dir binary must warn: %q %v", w, err)
	}
	if w := unstable("/opt/tools/rein/5.6.2/bin/rein"); !strings.Contains(w, "version-numbered") {
		t.Errorf("version dir must warn: %q", w)
	}
	if w := unstable("/usr/local/bin/rein"); w != "" {
		t.Errorf("a stable path must not warn: %q", w)
	}
}

func TestCodexLaunchAdviceOwnership(t *testing.T) {
	advice := LaunchLine("codex", "/b/rein", "task")
	for _, required := range []string{"unsupervised", "receipt", "retry", "re-install"} {
		if !strings.Contains(advice, required) {
			t.Errorf("launch advice missing %q: %s", required, advice)
		}
	}
}
