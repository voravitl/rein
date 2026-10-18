package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigInitOfflineDisabledAndNonDestructive(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "setup")
	if err := initProviderConfig(dir, configModels{"codex": "chosen-model"}); err != nil {
		t.Fatal(err)
	}
	check, err := checkProviderConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !check.Valid || check.ModelCallsEnabled || !check.QuotaGuardRequired || len(check.Providers) != 4 {
		t.Fatal(check)
	}
	for _, p := range check.Providers {
		if p.ProbeConfigured {
			t.Fatal("draft setup became callable")
		}
	}
	before, _ := os.ReadFile(filepath.Join(dir, "fallback-chain.json"))
	if err := initProviderConfig(dir, nil); err == nil {
		t.Fatal("overwrote setup")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "fallback-chain.json"))
	if string(before) != string(after) {
		t.Fatal("existing config changed")
	}
	for _, p := range []string{dir, filepath.Join(dir, "fallback-chain.json"), filepath.Join(dir, "profile.json")} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm()&0077 != 0 {
			t.Fatalf("public permissions: %s", p)
		}
	}
	link := filepath.Join(t.TempDir(), "symlink")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if err := initProviderConfig(link, nil); err == nil {
		t.Fatal("accepted symlink")
	}
}
func TestConfigModelsRequireExplicitValidMapping(t *testing.T) {
	for _, s := range []string{"codex", "codex=", "other=model"} {
		if err := (configModels{}).Set(s); err == nil {
			t.Fatalf("accepted %q", s)
		}
	}
	m := configModels{}
	if err := m.Set("codex=chosen"); err != nil {
		t.Fatal(err)
	}
	if err := m.Set("codex=another"); err == nil {
		t.Fatal("duplicate accepted")
	}
}
