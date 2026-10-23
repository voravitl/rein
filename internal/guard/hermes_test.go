package guard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hermesPre is the shape the rein-guard plugin pipes: the tool, its input and the worktree.
const hermesPre = `{"tool":%s,"input":%s,"cwd":"@WT@"}`

// TestHermes checks the Hermes dialect: a denial is {"action":"block","message":...} on stdout, exit 0.
func TestHermes(t *testing.T) {
	wt, _ := setup(t)
	for name, ev := range map[string]string{
		"push":          f2(hermesPre, `"terminal"`, `{"command":"git push origin HEAD"}`),
		"write outside": f2(hermesPre, `"write_file"`, `{"path":"frontend/src/x.ts","content":"x"}`),
		"never-edit":    f2(hermesPre, `"write_file"`, `{"path":"VERSION","content":"x"}`),
		"patch":         f2(hermesPre, `"patch"`, `{"path":"other.txt","patchText":"*** Begin Patch\n*** Add File: other.txt\n+x\n*** End Patch"}`),
		"no path":       f2(hermesPre, `"write_file"`, `{"content":"x"}`),
		"unknown tool":  f2(hermesPre, `"some_new_tool"`, `{"path":"frontend/src/x.ts"}`),
	} {
		out, errb, code := call(t, "hermes", ev, wt)
		if code != 0 || !strings.Contains(out, `"action":"block"`) || !strings.Contains(out, "[rein]") {
			t.Errorf("%s: want a Hermes block directive, got %q %q (exit %d)", name, out, errb, code)
		}
	}
	for name, ev := range map[string]string{
		"owned write": f2(hermesPre, `"write_file"`, `{"path":"backend/Routing/N.cs","content":"x"}`),
		"owned edit":  f2(hermesPre, `"patch"`, `{"path":"backend/Routing/A.cs","old_string":"a","new_string":"b"}`),
		"read":        f2(hermesPre, `"read_file"`, `{"path":"/etc/hosts"}`),
		"list":        f2(hermesPre, `"list_files"`, `{"path":"backend"}`),
		"plain call":  f2(hermesPre, `"web_search"`, `{"query":"x"}`),
	} {
		if out, errb, code := call(t, "hermes", ev, wt); out != "" || errb != "" || code != 0 {
			t.Errorf("%s: want allow, got %q %q (exit %d)", name, out, errb, code)
		}
	}
}

// TestHermesStop checks the Stop dialect: the report is missing, so the worker is sent back.
func TestHermesStop(t *testing.T) {
	wt, _ := setup(t)
	stop := `{"tool":"","input":{},"cwd":"@WT@","hook_event_name":"stop"}`
	if out, _, code := call(t, "hermes", stop, wt); code != 0 || !strings.Contains(out, `"action":"continue"`) {
		t.Errorf("hermes stop without a report must continue with the reason, got %q (exit %d)", out, code)
	}
}

// TestHermesPluginIsProtected checks that a worker cannot edit or delete the guard plugin.
func TestHermesPluginIsProtected(t *testing.T) {
	wt, _ := setup(t)
	dir := filepath.Join(wt, ".hermes", "plugins", "rein-guard")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "__init__.py"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{".hermes/plugins/rein-guard/__init__.py", ".hermes/plugins/rein-guard/plugin.yaml"} {
		if !protectedHook(wt, rel) {
			t.Errorf("%s must be protected", rel)
		}
	}
}

// TestHermesDotenvIsProtected checks that a worker cannot write the task home dotenv files:
// Hermes loads them with override, so one of them can switch the guard plugin off.
func TestHermesDotenvIsProtected(t *testing.T) {
	wt, _ := setup(t)
	if err := os.MkdirAll(filepath.Join(wt, ".hermes"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{".hermes/.env", ".hermes/.op.env"} {
		if !protectedHook(wt, rel) {
			t.Errorf("%s must be protected", rel)
		}
	}
}

func TestHermesConfigCannotDisableGuard(t *testing.T) {
	wt, c := setup(t)
	c.Allow = []string{"**"}
	if _, err := c.Save(); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(wt, ".hermes", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("plugins:\n  enabled: [rein-guard]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	plugin := filepath.Join(wt, ".hermes/plugins/rein-guard/__init__.py")
	if err := os.MkdirAll(filepath.Dir(plugin), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plugin, []byte("guard"), 0644); err != nil {
		t.Fatal(err)
	}
	events := []map[string]any{
		{"tool": "write_file", "input": map[string]string{"path": ".hermes/config.yaml", "content": "plugins: {}"}},
		{"tool": "patch", "input": map[string]string{"path": ".hermes/config.yaml", "old_string": "rein-guard", "new_string": "crux"}},
	}
	for _, cmd := range []string{"echo disabled > .hermes/config.yaml", "echo disabled >> .hermes/config.yaml", "rm .hermes/config.yaml", "mv .hermes/config.yaml backend/Routing/config.yaml", "mv backend/Routing/A.cs .hermes/config.yaml", "rm -rf .hermes", "mv .hermes backend/Routing/home", "rm -rf .hermes*", "rm -rf .*", "rm -rf .hermes/config.*", "rm -rf .hermes/plugins/*", "rm -rf .hermes/plugins/rein-*", "mv .hermes/plugins .hermes/disabled"} {
		events = append(events, map[string]any{"tool": "terminal", "input": map[string]string{"command": cmd}})
	}
	for _, event := range events {
		event["cwd"] = "@WT@"
		payload, _ := json.Marshal(event)
		out, errb, code := call(t, "hermes", string(payload), wt)
		if code != 0 || !strings.Contains(out, `"action":"block"`) {
			t.Errorf("guard config bypass: %s => %q %q exit %d", payload, out, errb, code)
		}
	}
}
