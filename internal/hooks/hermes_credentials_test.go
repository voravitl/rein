package hooks

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHermesCredentialsReferenceIsNarrowAndStable(t *testing.T) {
	wt, source := t.TempDir(), t.TempDir()
	t.Setenv("HERMES_HOME", source)
	if out, err := exec.Command("git", "init", wt).CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", out, err)
	}
	if err := os.Mkdir(filepath.Join(wt, ".hermes"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(wt, ".hermes/config.yaml")
	if err := os.WriteFile(path, []byte(hermesConfigYAML), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, ".env"), []byte("SECRET=fake-only"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := BindHermesCredentials(wt); err != nil {
		t.Fatal(err)
	}
	bound, _ := os.ReadFile(path)
	if !strings.Contains(string(bound), source) || strings.Contains(string(bound), "fake-only") {
		t.Fatal("reference missing or copied secret")
	}
	if _, err := os.Stat(filepath.Join(wt, ".hermes/.env")); !os.IsNotExist(err) {
		t.Fatal("copied credentials")
	}
	if err := BindHermesCredentials(wt); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERMES_HOME", t.TempDir())
	if err := BindHermesCredentials(wt); err == nil {
		t.Fatal("rebound source")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(bound) {
		t.Fatal("modified foreign reference")
	}
}

func TestHermesCredentialsRejectForeignAndLinkedConfig(t *testing.T) {
	t.Setenv("HERMES_HOME", t.TempDir())
	for _, foreign := range []string{"plugins: {enabled: [foreign]}\n", hermesConfigYAML + "credentials:\n  home: relative\n"} {
		wt := t.TempDir()
		os.Mkdir(filepath.Join(wt, ".hermes"), 0700)
		path := filepath.Join(wt, ".hermes/config.yaml")
		os.WriteFile(path, []byte(foreign), 0600)
		if err := BindHermesCredentials(wt); err == nil {
			t.Fatal("accepted foreign config")
		}
		after, _ := os.ReadFile(path)
		if string(after) != foreign {
			t.Fatal("modified foreign config")
		}
	}
	wt, target := t.TempDir(), filepath.Join(t.TempDir(), "config")
	os.Mkdir(filepath.Join(wt, ".hermes"), 0700)
	os.WriteFile(target, []byte(hermesConfigYAML), 0600)
	os.Symlink(target, filepath.Join(wt, ".hermes/config.yaml"))
	if err := BindHermesCredentials(wt); err == nil {
		t.Fatal("accepted symlink config")
	}
}

func TestHermesCredentialsReinstallAndUninstall(t *testing.T) {
	root, wt, c := setup(t)
	t.Setenv("HERMES_HOME", t.TempDir())
	bin := filepath.Join(root, "rein")
	results, _, err := Install(c, bin, []string{"hermes"})
	if err != nil || results[0].Err != nil {
		t.Fatalf("install: %v %v", err, results)
	}
	if err := BindHermesCredentials(wt); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(wt, ".hermes/config.yaml")
	before, _ := os.ReadFile(path)
	results, _, err = Install(c, bin, []string{"hermes"})
	if err != nil || results[0].Err != nil {
		t.Fatalf("reinstall: %v %v", err, results)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("reinstall changed credential reference")
	}
	results, _, err = Uninstall(c, []string{"hermes"})
	if err != nil || results[0].Err != nil {
		t.Fatalf("uninstall: %v %v", err, results)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("generated credential config retained after uninstall")
	}
}
