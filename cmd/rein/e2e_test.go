package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCmdE2E_ArgValidation(t *testing.T) {
	if code := cmdE2E([]string{}); code != 2 {
		t.Fatalf("expected code 2 on empty args, got %d", code)
	}
	if code := cmdE2E([]string{"unknown-subcommand"}); code != 2 {
		t.Fatalf("expected code 2 on unknown subcommand, got %d", code)
	}
	if code := cmdE2ERun([]string{}); code != 2 {
		t.Fatalf("expected code 2 on empty e2e run, got %d", code)
	}
	if code := cmdE2ERun([]string{"non-existent-spec-file.json"}); code != 2 {
		t.Fatalf("expected code 2 on missing spec file, got %d", code)
	}
}

func TestCmdE2E_Check(t *testing.T) {
	// If orca is installed and running, check returns 0 or 1
	code := cmdE2ECheck([]string{"--json"})
	if code != 0 && code != 1 {
		t.Fatalf("expected code 0 or 1, got %d", code)
	}
}

func TestCmdE2E_SpecParsingAndRunValidation(t *testing.T) {
	tmp := t.TempDir()
	specFile := filepath.Join(tmp, "invalid-steps.json")
	if err := os.WriteFile(specFile, []byte(`{"name": "test", "steps": []}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Should fail with code 2 because spec has no steps
	code := cmdE2ERun([]string{specFile})
	if code != 2 {
		t.Fatalf("expected code 2 on empty steps spec, got %d", code)
	}
}
