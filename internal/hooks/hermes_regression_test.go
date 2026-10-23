package hooks

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHermesReviewExistingConfig(t *testing.T) {
	for _, config := range []string{"", "plugins:\n  enabled: []\n", "plugins:\n  enabled: [other]\n", "model: keep-me\n"} {
		t.Run(config, func(t *testing.T) {
			root, wt, c := setup(t)
			dir := filepath.Join(wt, ".hermes")
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
			cfg := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(cfg, []byte(config), 0644); err != nil {
				t.Fatal(err)
			}
			res, _, err := Install(c, filepath.Join(root, "rein"), []string{"hermes"})
			if err != nil {
				t.Fatal(err)
			}
			if res[0].Err == nil || len(c.HooksInstalled) != 0 {
				t.Fatalf("disabled guard recorded installed: %+v %v", res, c.HooksInstalled)
			}
			got, _ := os.ReadFile(cfg)
			if string(got) != config {
				t.Fatal("existing config changed")
			}
			if _, err := os.Stat(filepath.Join(dir, "plugins", "rein-guard", "plugin.yaml")); !os.IsNotExist(err) {
				t.Fatal("refused config left partial plugin")
			}
		})
	}
}

func TestHermesReviewSymlinkUninstall(t *testing.T) {
	for _, component := range []string{".hermes", ".hermes/plugins", ".hermes/plugins/rein-guard"} {
		t.Run(component, func(t *testing.T) {
			_, wt, c := setup(t)
			outside := t.TempDir()
			link := filepath.Join(wt, component)
			if err := os.MkdirAll(filepath.Dir(link), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, link); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(wt, ".hermes/plugins/rein-guard")
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
			f := filepath.Join(dir, "plugin.yaml")
			if err := os.WriteFile(f, []byte("foreign"), 0644); err != nil {
				t.Fatal(err)
			}
			res, _, err := Uninstall(c, []string{"hermes"})
			if err != nil {
				t.Fatal(err)
			}
			if res[0].Err == nil {
				t.Fatal("symlink uninstall accepted")
			}
			if got, err := os.ReadFile(f); err != nil || string(got) != "foreign" {
				t.Fatal("external file changed")
			}
		})
	}
}

func TestHermesReviewForeignAndModifiedArtifacts(t *testing.T) {
	for _, name := range []string{"plugin.yaml", "__init__.py"} {
		for _, installed := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "-foreign", true: "-modified"}[installed], func(t *testing.T) {
				root, wt, c := setup(t)
				bin := filepath.Join(root, "rein")
				if installed {
					res, _, err := Install(c, bin, []string{"hermes"})
					if err != nil || res[0].Err != nil {
						t.Fatalf("install: %v %+v", err, res)
					}
				}
				dir := filepath.Join(wt, ".hermes/plugins/rein-guard")
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
				dest := filepath.Join(dir, name)
				if err := os.WriteFile(dest, []byte("user customization"), 0644); err != nil {
					t.Fatal(err)
				}
				for _, operation := range []func(*testing.T){func(t *testing.T) {
					res, _, err := Install(c, bin, []string{"hermes"})
					if err != nil || res[0].Err == nil {
						t.Fatalf("install did not refuse: %v %+v", err, res)
					}
				}, func(t *testing.T) {
					res, _, err := Uninstall(c, []string{"hermes"})
					if err != nil || res[0].Err == nil {
						t.Fatalf("uninstall did not refuse: %v %+v", err, res)
					}
				}} {
					operation(t)
					got, _ := os.ReadFile(dest)
					if string(got) != "user customization" {
						t.Fatal("foreign artifact changed")
					}
				}
			})
		}
	}
}

func TestHermesReviewImportReinstallUninstall(t *testing.T) {
	root, wt, c := setup(t)
	bin := filepath.Join(root, "rein")
	res, _, err := Install(c, bin, []string{"hermes"})
	if err != nil || res[0].Err != nil {
		t.Fatalf("install: %v %+v", err, res)
	}
	dir := filepath.Join(wt, ".hermes/plugins/rein-guard")
	cmd := exec.Command("python3", "-c", `import importlib.util,sys;sys.dont_write_bytecode=False;sys.pycache_prefix=None;s=importlib.util.spec_from_file_location("guard",sys.argv[1]);m=importlib.util.module_from_spec(s);s.loader.exec_module(m)`, filepath.Join(dir, "__init__.py"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("import: %v %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "__pycache__")); err != nil {
		t.Fatal(err)
	}
	res, _, err = Install(c, bin, []string{"hermes"})
	if err != nil || res[0].Err != nil {
		t.Fatalf("reinstall: %v %+v", err, res)
	}
	res, _, err = Uninstall(c, []string{"hermes"})
	if err != nil || res[0].Err != nil {
		t.Fatalf("uninstall: %v %+v", err, res)
	}
	for _, name := range []string{"plugin.yaml", "__init__.py"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("plugin remains: %s", name)
		}
	}
}

func TestHermesReviewReportGateExceptions(t *testing.T) {
	root, wt, c := setup(t)
	res, _, err := Install(c, filepath.Join(root, "missing-rein"), []string{"hermes"})
	if err != nil || res[0].Err != nil {
		t.Fatalf("install: %v %+v", err, res)
	}
	script := `import importlib.util,sys,subprocess
s=importlib.util.spec_from_file_location("guard",sys.argv[1]);m=importlib.util.module_from_spec(s);s.loader.exec_module(m)
for exc in (FileNotFoundError("missing"),PermissionError("denied"),subprocess.TimeoutExpired("rein",10)):
 def fail(*a,**kw): raise exc
 m.subprocess.run=fail
 result=m._report_gate()
 assert result["action"]=="block" and result["message"], result
print("all subprocess failures keep report gate active")
`
	cmd := exec.Command("python3", "-B", "-c", script, filepath.Join(wt, ".hermes/plugins/rein-guard/__init__.py"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("gate: %v %s", err, out)
	}
}

func TestHermesReviewTrackedArtifact(t *testing.T) {
	root, wt, c := setup(t)
	bin := filepath.Join(root, "rein")
	res, _, err := Install(c, bin, []string{"hermes"})
	if err != nil || res[0].Err != nil {
		t.Fatalf("install: %v %+v", err, res)
	}
	git(t, "-C", wt, "add", "-f", ".hermes/plugins/rein-guard/__init__.py")
	res, _, err = Uninstall(c, []string{"hermes"})
	if err != nil || res[0].Err == nil {
		t.Fatalf("tracked uninstall accepted: %v %+v", err, res)
	}
	if _, err := os.Stat(filepath.Join(wt, ".hermes/plugins/rein-guard/plugin.yaml")); err != nil {
		t.Fatal("tracked refusal partially removed plugin")
	}
}

func TestHermesReviewLegacyPluginPreserved(t *testing.T) {
	root, wt, c := setup(t)
	bin := filepath.Join(root, "rein")
	res, _, err := Install(c, bin, []string{"hermes"})
	if err != nil || res[0].Err != nil {
		t.Fatalf("install: %v %+v", err, res)
	}
	path := filepath.Join(wt, ".hermes/plugins/rein-guard/__init__.py")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(body), "    try:\n        r = subprocess.run")
	end := strings.Index(string(body)[start:], "    try:\n        out =") + start
	legacy := string(body[:start]) + `    r = subprocess.run([REIN, "hook", "--vendor", "hermes", "--task", REIN_TASK],
                       input=json.dumps(payload), capture_output=True, text=True, timeout=10)
` + string(body[end:])
	if err := os.WriteFile(path, []byte(legacy), 0644); err != nil {
		t.Fatal(err)
	}
	res, _, err = Install(c, bin, []string{"hermes"})
	if err != nil || res[0].Err == nil || !strings.Contains(res[0].Err.Error(), "older") {
		t.Fatalf("legacy overwrite not refused clearly: %v %+v", err, res)
	}
	res, _, err = Uninstall(c, []string{"hermes"})
	if err != nil || res[0].Err == nil {
		t.Fatalf("legacy removal not refused: %v %+v", err, res)
	}
	got, _ := os.ReadFile(path)
	if string(got) != legacy {
		t.Fatal("legacy plugin modified")
	}
}

func TestHermesCruxConfigLifecycle(t *testing.T) {
	const crux = "plugins:\n  enabled:\n    - crux\n"
	const combined = "plugins:\n  enabled:\n    - rein-guard\n    - crux\n"
	for _, initial := range []string{crux, combined} {
		t.Run(initial, func(t *testing.T) {
			root, wt, c := setup(t)
			bin := filepath.Join(root, "rein")
			cfg := filepath.Join(wt, ".hermes/config.yaml")
			if err := os.MkdirAll(filepath.Dir(cfg), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(cfg, []byte(initial), 0644); err != nil {
				t.Fatal(err)
			}
			foreign := filepath.Join(wt, ".hermes/plugins/crux/notes.txt")
			if err := os.MkdirAll(filepath.Dir(foreign), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(foreign, []byte("keep bytes"), 0644); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				res, _, err := Install(c, bin, []string{"hermes"})
				if err != nil || res[0].Err != nil {
					t.Fatalf("coinstall %d: %v %+v", i, err, res)
				}
				if got, _ := os.ReadFile(cfg); string(got) != combined {
					t.Fatalf("coinstall config: %q", got)
				}
			}
			res, _, err := Uninstall(c, []string{"hermes"})
			if err != nil || res[0].Err != nil {
				t.Fatalf("uninstall: %v %+v", err, res)
			}
			if got, _ := os.ReadFile(cfg); string(got) != crux {
				t.Fatalf("Crux config lost: %q", got)
			}
			if !strings.Contains(strings.Join(res[0].Files, "\n"), ".hermes/config.yaml") {
				t.Fatal("uninstall did not report changed config")
			}
			if got, _ := os.ReadFile(foreign); string(got) != "keep bytes" {
				t.Fatal("Crux file modified")
			}
			res, _, err = Install(c, bin, []string{"hermes"})
			if err != nil || res[0].Err != nil {
				t.Fatalf("reinstall: %v %+v", err, res)
			}
		})
	}
}

func TestHermesUninstallPreservesForeignConfig(t *testing.T) {
	root, wt, c := setup(t)
	res, _, err := Install(c, filepath.Join(root, "rein"), []string{"hermes"})
	if err != nil || res[0].Err != nil {
		t.Fatalf("install: %v %+v", err, res)
	}
	cfg := filepath.Join(wt, ".hermes/config.yaml")
	const foreign = "# owner settings\nplugins:\n  enabled: [rein-guard, crux, other]\nmodel: keep-me\n"
	if err := os.WriteFile(cfg, []byte(foreign), 0644); err != nil {
		t.Fatal(err)
	}
	res, _, err = Uninstall(c, []string{"hermes"})
	if err != nil || res[0].Err != nil {
		t.Fatalf("uninstall: %v %+v", err, res)
	}
	if got, _ := os.ReadFile(cfg); string(got) != foreign {
		t.Fatal("foreign config bytes changed")
	}
}

func TestHermesConfigOwnershipRefusals(t *testing.T) {
	for _, kind := range []string{"tracked", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			root, wt, c := setup(t)
			bin := filepath.Join(root, "rein")
			res, _, err := Install(c, bin, []string{"hermes"})
			if err != nil || res[0].Err != nil {
				t.Fatalf("install: %v %+v", err, res)
			}
			cfg := filepath.Join(wt, ".hermes/config.yaml")
			if kind == "tracked" {
				git(t, "-C", wt, "add", "-f", ".hermes/config.yaml")
			} else {
				external := filepath.Join(t.TempDir(), "config.yaml")
				if err := os.WriteFile(external, []byte(hermesConfigYAML), 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(cfg); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(external, cfg); err != nil {
					t.Fatal(err)
				}
			}
			res, _, err = Install(c, bin, []string{"hermes"})
			if err != nil || res[0].Err == nil {
				t.Fatalf("%s install accepted: %v %+v", kind, err, res)
			}
			res, _, err = Uninstall(c, []string{"hermes"})
			if err != nil || res[0].Err == nil {
				t.Fatalf("%s uninstall accepted: %v %+v", kind, err, res)
			}
			if got, _ := os.ReadFile(cfg); string(got) != hermesConfigYAML {
				t.Fatal("protected config modified")
			}
			if _, err := os.Stat(filepath.Join(wt, ".hermes/plugins/rein-guard/plugin.yaml")); err != nil {
				t.Fatal("refused uninstall partially removed plugin")
			}
		})
	}
}
