package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voravitl/rein/internal/contract"
)

// TestHermesInstallWritesTheGuardPlugin checks that a Hermes worker gets a task-bound plugin, the home that
// activates it, and a launch line that says how the worker must be started.
func TestHermesInstallWritesTheGuardPlugin(t *testing.T) {
	root, wt, c := setup(t)
	bin := filepath.Join(root, "rein")
	res, _, err := Install(c, bin, []string{"hermes"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Err != nil {
		t.Fatalf("hermes install: %+v", res)
	}
	if !strings.Contains(res[0].Launch, "HERMES_ENABLE_PROJECT_PLUGINS=1") {
		t.Errorf("the launch line must name the env the plugin needs, got %q", res[0].Launch)
	}
	manifest := filepath.Join(wt, ".hermes", "plugins", "rein-guard", "plugin.yaml")
	b, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "name: rein-guard") {
		t.Errorf("the manifest must name the plugin, got %q", b)
	}
	init := filepath.Join(wt, ".hermes", "plugins", "rein-guard", "__init__.py")
	b, err = os.ReadFile(init)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--vendor", "hermes", "--task", c.Name, bin} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the plugin must bind the guard to its task and binary, missing %q in:\n%s", want, b)
		}
	}
	cfg, err := os.ReadFile(filepath.Join(wt, ".hermes", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfg), "rein-guard") {
		t.Errorf("the task home must enable the guard plugin, got %q", cfg)
	}
	if out := git(t, "-C", wt, "status", "--porcelain"); out != "" {
		t.Errorf("the Hermes hook files must be excluded from git status, got %q", out)
	}
}

// TestHermesUninstallRemovesOnlyItsOwnFiles checks that the removal takes the plugin away and refuses a
// foreign file in the same directory.
func TestHermesUninstallRemovesOnlyItsOwnFiles(t *testing.T) {
	root, wt, c := setup(t)
	if _, _, err := Install(c, filepath.Join(root, "rein"), []string{"hermes"}); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(wt, ".hermes", "plugins", "rein-guard", "notes.md")
	if err := os.WriteFile(foreign, []byte("mine"), 0644); err != nil {
		t.Fatal(err)
	}
	res, _, err := Uninstall(c, []string{"hermes"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Err == nil {
		t.Fatalf("uninstall must refuse a foreign file in the guard directory, got %+v", res)
	}
	if _, err := os.Stat(filepath.Join(wt, ".hermes", "plugins", "rein-guard", "plugin.yaml")); err != nil {
		t.Errorf("a refused uninstall must leave the guard in place, got %v", err)
	}
	if err := os.Remove(foreign); err != nil {
		t.Fatal(err)
	}
	res, _, err = Uninstall(c, []string{"hermes"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Err != nil {
		t.Fatalf("hermes uninstall: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(wt, ".hermes", "plugins", "rein-guard")); !os.IsNotExist(err) {
		t.Error("the guard directory must be gone after the uninstall")
	}
	if len(c.HooksInstalled) != 0 {
		t.Errorf("the contract must not list hermes any more, got %v", c.HooksInstalled)
	}
}

// TestHermesHomeIsNotOverwritten checks that an existing task home keeps its own settings.
func TestHermesHomeIsNotOverwritten(t *testing.T) {
	root, wt, c := setup(t)
	home := filepath.Join(wt, ".hermes")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := "model:\n  default: keep-me\n"
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(existing), 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Install(c, filepath.Join(root, "rein"), []string{"hermes"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(home, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != existing {
		t.Errorf("an existing Hermes home must be kept as it is, got %q", b)
	}
}

// TestHermesHomeIsExcludedFromGitStatus checks that the task's Hermes home cannot be staged by a worker's
// `git add -A`: the guard's own files would end up in the worktree's commit.
func TestHermesHomeIsExcludedFromGitStatus(t *testing.T) {
	root, wt, c := setup(t)
	if _, _, err := Install(c, filepath.Join(root, "rein"), []string{"hermes"}); err != nil {
		t.Fatal(err)
	}
	// a runtime file the Hermes home writes while the worker runs
	runtime := filepath.Join(wt, ".hermes", "cache", "tool_discovery_cache.json")
	if err := os.MkdirAll(filepath.Dir(runtime), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtime, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if out := git(t, "-C", wt, "status", "--porcelain"); out != "" {
		t.Errorf("the whole Hermes home must stay out of git status, got %q", out)
	}
	git(t, "-C", wt, "add", "-A")
	if out := git(t, "-C", wt, "diff", "--cached", "--name-only"); strings.Contains(out, ".hermes/") {
		t.Errorf("a worker's `git add -A` must not stage the guard's own files, got %q", out)
	}
}

// TestHermesPluginIsBoundToItsTask checks that a second task's plugin cannot judge the first one.
func TestHermesPluginIsBoundToItsTask(t *testing.T) {
	root, wt, c := setup(t)
	if _, _, err := Install(c, filepath.Join(root, "rein"), []string{"hermes"}); err != nil {
		t.Fatal(err)
	}
	init, err := os.ReadFile(filepath.Join(wt, ".hermes", "plugins", "rein-guard", "__init__.py"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(init), c.Name) {
		t.Errorf("the plugin must carry its task name, got:\n%s", init)
	}
	if _, err := contract.Load("other-task"); err == nil {
		t.Error("an unrelated task must not resolve to this contract")
	}
}
