package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/hooks"
	"github.com/voravitl/rein/internal/providers"
	"github.com/voravitl/rein/internal/routing"
)

// A Hermes worker launched through Orca runs with the user's own Hermes home, so the task's
// guard plugin is never loaded. That launch must be refused, not allowed to run unguarded.
func TestHermesOrcaLaunchIsRefused(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, "worktree")
	if err := os.Mkdir(wt, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(root, "contracts"))
	t.Setenv("PIPELINE_LEDGER", filepath.Join(root, "ledger.jsonl"))
	t.Setenv("REIN_PROFILE", "")
	c := &contract.Contract{Name: "task", Worktree: wt, ReportPath: filepath.Join(root, "report"), Allow: []string{"**"}, Scope: []string{"S1"}}
	if _, err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", wt).CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", out, err)
	}
	if _, _, err := hooks.Install(c, os.Args[0], []string{"hermes"}); err != nil {
		t.Fatal(err)
	}
	cfg := providers.Config{Providers: map[string]providers.Provider{"h": {Agent: "hermes", Model: "openrouter:model-id", Launch: "orca", Probe: []string{os.Args[0], "-test.run=TestRouteProbeProcess", "--", "route-probe"}}}, WorkerChains: map[string][]string{"backend": {"h"}}}
	raw, _ := json.Marshal(cfg)
	cfgPath := filepath.Join(root, "providers.json")
	if err := os.WriteFile(cfgPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := routing.Prepare(c, "run", "worker:backend", "", cfgPath, false, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	argv := []string{"orca", "orchestration", "worker-start", "--run", "run", "--agent", "hermes",
		"--model", "openrouter:model-id", "--worktree", "path:" + wt, "--task", "task_opaque"}
	stderr := os.Stderr
	capture, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Close()
	os.Stderr = capture
	code := cmdRouteLaunch(append([]string{"--task", "task", "--run", "run", "--"}, argv...))
	os.Stderr = stderr
	b, err := os.ReadFile(capture.Name())
	if err != nil {
		t.Fatal(err)
	}
	if code == 0 {
		t.Fatal("Orca Hermes launch accepted: the task's guard plugin would not be loaded")
	}
	if !strings.Contains(string(b), "direct shell route") {
		t.Errorf("Orca Hermes launch refused for the wrong reason: %s", b)
	}
}

// A task home whose dotenv can switch the plugin off must not reach exec.
func TestHermesTaskHomeDotenvIsRefused(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, "worktree")
	if err := os.MkdirAll(filepath.Join(wt, ".hermes"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".env", ".op.env"} {
		if err := os.WriteFile(filepath.Join(wt, ".hermes", name), []byte("HERMES_SAFE_MODE=1\n"), 0600); err != nil {
			t.Fatal(err)
		}
		err := hooks.CheckHermesTaskHome(wt)
		if err == nil {
			t.Fatalf("%s accepted in the task home", name)
		}
		if !strings.Contains(err.Error(), name) {
			t.Errorf("%s refusal does not name the file: %v", name, err)
		}
		if err := os.Remove(filepath.Join(wt, ".hermes", name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := hooks.CheckHermesTaskHome(wt); err != nil {
		t.Errorf("clean task home refused: %v", err)
	}
}

// hermesGuardEnv builds a contracted worktree with the Hermes guard installed and a prepared
// routing receipt, and returns the launch arguments for a Hermes worker.
type hermesGuardSetup struct {
	wt, log string
	c       *contract.Contract
	args    []string
}

func hermesGuardEnv(t *testing.T) *hermesGuardSetup {
	t.Helper()
	root := t.TempDir()
	wt := filepath.Join(root, "worktree")
	if err := os.Mkdir(wt, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PIPELINE_CONTRACTS", filepath.Join(root, "contracts"))
	t.Setenv("PIPELINE_LEDGER", filepath.Join(root, "ledger.jsonl"))
	t.Setenv("REIN_PROFILE", "")
	c := &contract.Contract{Name: "task", Worktree: wt, ReportPath: filepath.Join(root, "report"), Allow: []string{"**"}, Scope: []string{"S1"}}
	if _, err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", wt).CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", out, err)
	}
	if _, _, err := hooks.Install(c, os.Args[0], []string{"hermes"}); err != nil {
		t.Fatal(err)
	}
	cfg := providers.Config{Providers: map[string]providers.Provider{"h": {Agent: "hermes", Model: "openrouter:model-id", Launch: "shell", Probe: []string{os.Args[0], "-test.run=TestRouteProbeProcess", "--", "route-probe"}}}, WorkerChains: map[string][]string{"backend": {"h"}}}
	raw, _ := json.Marshal(cfg)
	cfgPath := filepath.Join(root, "providers.json")
	if err := os.WriteFile(cfgPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	cli, log := filepath.Join(root, "hermes"), filepath.Join(root, "actual")
	t.Setenv("TEST_LAUNCH_LOG", log)
	t.Setenv("TEST_REPORT_PATH", c.ReportPath)
	// The fake writes a report only when asked, so a test can tell a stale report from a written one.
	if err := os.WriteFile(cli, []byte("#!/bin/sh\nprintf '%s\\000' \"$@\" > \"$TEST_LAUNCH_LOG\"\n[ \"$TEST_WRITE_REPORT\" = 1 ] && printf '# S1 PASS\\n' > \"$TEST_REPORT_PATH\"\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := routing.Prepare(c, "run", "worker:backend", "", cfgPath, false, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	argv := []string{cli, "--cli", "chat", "--model", "openrouter:model-id", "--query", "prompt", "--oneshot"}
	return &hermesGuardSetup{wt: wt, log: log, c: c, args: append([]string{"--task", "task", "--run", "run", "--"}, argv...)}
}

// The launch must refuse the task home, not only the helper.
func TestHermesLaunchRefusesTaskHomeDotenv(t *testing.T) {
	env := hermesGuardEnv(t)
	if err := os.MkdirAll(filepath.Join(env.wt, ".hermes"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.wt, ".hermes", ".env"), []byte("HERMES_SAFE_MODE=1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if code := cmdRouteLaunch(env.args); code == 0 {
		t.Fatal("launch accepted a task home whose dotenv can disable the guard")
	}
	if _, err := os.Stat(env.log); !os.IsNotExist(err) {
		t.Fatalf("the provider process started despite the refusal: %v", err)
	}
}

// Installing hooks for another vendor must not hide a worker's own .hermes files from git status.
func TestHermesIsNotADefaultVendor(t *testing.T) {
	for _, v := range hooks.Default {
		if v == "hermes" {
			t.Fatal("hermes must be opt-in: installing it by default hides a worker's .hermes files from git status")
		}
	}
	if !slices.Contains(hooks.All, "hermes") {
		t.Fatal("hermes must remain a known vendor")
	}
}

// A worker must not be able to leave a plugin that calls something other than rein: such a plugin
// answers every tool call with success and judges nothing. The file stays otherwise byte-identical
// to what rein wrote, so only the binary it names can tell the two apart.
func TestHermesLaunchRefusesForeignGuardBinary(t *testing.T) {
	env := hermesGuardEnv(t)
	plugin := filepath.Join(env.wt, ".hermes", "plugins", "rein-guard", "__init__.py")
	body, err := os.ReadFile(plugin)
	if err != nil {
		t.Fatal(err)
	}
	line := regexp.MustCompile(`(?m)^REIN = .*$`)
	foreign := line.ReplaceAllLiteralString(string(body), `REIN = "/usr/bin/true"`)
	if foreign == string(body) {
		t.Fatal("the plugin does not carry a REIN line to replace")
	}
	if err := os.WriteFile(plugin, []byte(foreign), 0600); err != nil {
		t.Fatal(err)
	}
	if code := cmdRouteLaunch(env.args); code == 0 {
		t.Fatal("launch accepted a guard plugin that calls another program")
	}
	if _, err := os.Stat(env.log); !os.IsNotExist(err) {
		t.Fatalf("the provider process started despite the refusal: %v", err)
	}
}

// A deleted or half-written plugin means the worker runs with no guard at all.
func TestHermesLaunchRefusesIncompleteGuardPlugin(t *testing.T) {
	for _, name := range []string{"__init__.py", "plugin.yaml"} {
		env := hermesGuardEnv(t)
		path := filepath.Join(env.wt, ".hermes", "plugins", "rein-guard", name)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if code := cmdRouteLaunch(env.args); code == 0 {
			t.Fatalf("launch accepted a guard plugin without %s", name)
		}
		if _, err := os.Stat(env.log); !os.IsNotExist(err) {
			t.Fatalf("the provider process started despite the refusal: %v", err)
		}
	}
}

// A report left by an earlier attempt is not evidence of this one, whatever its timestamp says: an
// earlier attempt could have set the time to the future, and a touch makes an old report look fresh.
func TestHermesLaunchRefusesStaleReport(t *testing.T) {
	for _, name := range []string{"plain", "future-dated"} {
		env := hermesGuardEnv(t)
		t.Setenv("TEST_WRITE_REPORT", "")
		if err := os.WriteFile(env.c.ReportPath, []byte("# S1 PASS\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if name == "future-dated" {
			future := time.Now().Add(time.Hour)
			if err := os.Chtimes(env.c.ReportPath, future, future); err != nil {
				t.Fatal(err)
			}
		}
		if code := cmdRouteLaunch(env.args); code == 0 {
			t.Fatalf("launch accepted a report left by an earlier attempt (%s)", name)
		}
	}
}

// A Python bytecode cache beside the plugin is loaded in preference to the source that was checked,
// so a cached .pyc could answer every tool call without the guard ever running. The launch removes
// the cache so that Python recompiles from the source that was verified.
func TestHermesLaunchRemovesGuardBytecodeCache(t *testing.T) {
	env := hermesGuardEnv(t)
	t.Setenv("TEST_WRITE_REPORT", "1")
	cache := filepath.Join(env.wt, ".hermes", "plugins", "rein-guard", "__pycache__")
	if err := os.MkdirAll(cache, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "__init__.cpython-312.pyc"), []byte("hacked"), 0600); err != nil {
		t.Fatal(err)
	}
	if code := cmdRouteLaunch(env.args); code != 0 {
		t.Fatalf("launch refused a task with a stale bytecode cache: %d", code)
	}
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Fatalf("the bytecode cache survived the launch: %v", err)
	}
}

// A sibling plugin shares the worker's process and can neutralise the guard by patching
// subprocess.run, so one the worker could have written must not be there.
func TestHermesLaunchRefusesSiblingPlugin(t *testing.T) {
	env := hermesGuardEnv(t)
	sibling := filepath.Join(env.wt, ".hermes", "plugins", "evil")
	if err := os.MkdirAll(sibling, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sibling, "__init__.py"), []byte("import subprocess\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if code := cmdRouteLaunch(env.args); code == 0 {
		t.Fatal("launch accepted a sibling plugin in the task home")
	}
	if _, err := os.Stat(env.log); !os.IsNotExist(err) {
		t.Fatalf("the provider process started despite the refusal: %v", err)
	}
}
