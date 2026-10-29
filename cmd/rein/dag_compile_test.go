package main

import (
	"os"
	"path/filepath"
	"testing"
)

func sampleCompileSpec() string {
	return `{
  "schema": "rein_dag_spec/v1",
  "run": "compile-test",
  "repo": "/tmp/test-repo",
  "base": "0123456789abcdef0123456789abcdef01234567",
  "goal": "Test compile CLI",
  "max_parallel": 2,
  "nodes": [
    {
      "id": "node-1",
      "kind": "worker",
      "title": "Node 1",
      "write": ["internal/**"],
      "depends_on": [],
      "prompt": {
        "task": "Task 1",
        "deliverable": "Artifact 1",
        "scope": "internal",
        "verify": "go test ./...",
        "stop_when": "done"
      },
      "verify": [["go", "test", "./..."]],
      "providers": ["claude"],
      "inputs": [],
      "outputs": []
    },
    {
      "id": "verify-node",
      "kind": "action",
      "title": "Verification Node",
      "write": [],
      "depends_on": ["node-1"],
      "prompt": {
        "task": "Verify all",
        "deliverable": "Proof",
        "scope": "none",
        "verify": "/bin/true",
        "stop_when": "done"
      },
      "verify": [["/bin/true"]],
      "action": {
        "argv": ["/bin/true"],
        "image": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
        "timeout_ms": 60000
      }
    }
  ]
}`
}

func sampleCompileOwnerConfig() string {
	return `{
  "providers": {
    "claude": {
      "launch": ["claude", "--print"]
    }
  },
  "profile": {"schema": 1},
  "config_path": "/etc/rein/owner.json"
}`
}

func TestDagCmdCompileCLI(t *testing.T) {
	tmp := t.TempDir()
	specFile := filepath.Join(tmp, "spec.json")
	if err := os.WriteFile(specFile, []byte(sampleCompileSpec()), 0644); err != nil {
		t.Fatal(err)
	}
	ownerFile := filepath.Join(tmp, "owner.json")
	if err := os.WriteFile(ownerFile, []byte(sampleCompileOwnerConfig()), 0644); err != nil {
		t.Fatal(err)
	}

	outDir := filepath.Join(tmp, "out")

	// Missing args -> exit 2
	if code := cmdDag([]string{"compile"}); code != 2 {
		t.Errorf("expected exit 2 on empty args, got %d", code)
	}

	// Successful compile -> exit 0
	if code := cmdDag([]string{"compile", "--spec", specFile, "--out", outDir, "--owner-config", ownerFile}); code != 0 {
		t.Fatalf("expected exit 0 on valid compile, got %d", code)
	}

	// Verify files created
	if _, err := os.Stat(filepath.Join(outDir, "plan.json")); err != nil {
		t.Errorf("plan.json not found: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "plan.md")); err != nil {
		t.Errorf("plan.md not found: %v", err)
	}

	// Idempotent re-run -> exit 0
	if code := cmdDag([]string{"compile", "--spec", specFile, "--out", outDir, "--owner-config", ownerFile}); code != 0 {
		t.Errorf("expected exit 0 on idempotent compile, got %d", code)
	}

	// Tampered plan in outDir -> exit 1
	if err := os.WriteFile(filepath.Join(outDir, "plan.json"), []byte("tampered"), 0644); err != nil {
		t.Fatal(err)
	}
	if code := cmdDag([]string{"compile", "--spec", specFile, "--out", outDir, "--owner-config", ownerFile}); code != 1 {
		t.Errorf("expected exit 1 on tampered plan overwrite, got %d", code)
	}
}
