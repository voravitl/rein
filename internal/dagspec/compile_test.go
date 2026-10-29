package dagspec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sampleValidSpec() string {
	return `{
  "schema": "rein_dag_spec/v1",
  "run": "test-run",
  "repo": "/tmp/test-repo",
  "base": "0123456789abcdef0123456789abcdef01234567",
  "goal": "Test compilation goal",
  "max_parallel": 2,
  "nodes": [
    {
      "id": "node-a",
      "kind": "worker",
      "title": "Node A",
      "write": ["internal/a/**"],
      "depends_on": [],
      "prompt": {
        "task": "Do task A",
        "deliverable": "A artifact",
        "scope": "internal/a",
        "verify": "go test ./internal/a",
        "stop_when": "done"
      },
      "verify": [["go", "test", "./internal/a"]],
      "providers": ["claude"],
      "inputs": [],
      "outputs": []
    },
    {
      "id": "node-b",
      "kind": "action",
      "title": "Node B Action",
      "write": [],
      "depends_on": ["node-a"],
      "prompt": {
        "task": "Do action B",
        "deliverable": "B artifact",
        "scope": "none",
        "verify": "/bin/true",
        "stop_when": "done"
      },
      "verify": [["/bin/true"]],
      "action": {
        "argv": ["/bin/true"],
        "image": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
        "timeout_ms": 600000
      }
    }
  ]
}`
}

func sampleOwnerConfig() string {
	return `{
  "providers": {
    "claude": {
      "launch": ["claude", "--print"]
    },
    "codex": {
      "launch": ["codex", "run"]
    }
  },
  "profile": {"name": "test-profile"},
  "config_path": "/etc/rein/owner.json"
}`
}

func TestCompileDeterministicAndValid(t *testing.T) {
	outDir := filepath.Join(t.TempDir(), "compiled")
	specData := []byte(sampleValidSpec())
	ownerData := []byte(sampleOwnerConfig())

	c, err := Compile(specData, ownerData, outDir, "/etc/rein/owner.json")
	if err != nil {
		t.Fatalf("unexpected compile error: %v", err)
	}

	if c.Plan.Schema != 3 {
		t.Errorf("expected schema 3, got %d", c.Plan.Schema)
	}
	if len(c.Plan.Tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(c.Plan.Tasks))
	}

	// Task 0 must be node-a (topological order)
	t0 := c.Plan.Tasks[0]
	if t0.Task != "test-run-node-a" {
		t.Errorf("expected first task test-run-node-a, got %s", t0.Task)
	}
	if t0.Kind != "worker" {
		t.Errorf("expected worker kind, got %s", t0.Kind)
	}
	// Provider narrowing: node-a only asked for claude
	if len(t0.Launches) != 1 || t0.Launches["claude"] == nil {
		t.Errorf("expected launches to contain only claude, got %v", t0.Launches)
	}
	if len(t0.ContractHash) != 64 {
		t.Errorf("expected 64-hex contract hash, got %s", t0.ContractHash)
	}

	// Task 1 must be node-b
	t1 := c.Plan.Tasks[1]
	if t1.Task != "test-run-node-b" {
		t.Errorf("expected second task test-run-node-b, got %s", t1.Task)
	}
	if t1.Kind != "action" {
		t.Errorf("expected action kind, got %s", t1.Kind)
	}
	if len(t1.DependsOn) != 1 || t1.DependsOn[0] != "test-run-node-a" {
		t.Errorf("expected depends_on [test-run-node-a], got %v", t1.DependsOn)
	}

	// Markdown should include approval commands
	md := string(c.PlanMD)
	if !strings.Contains(md, "rein run start /tmp/test-repo --supervisor-plan") {
		t.Errorf("plan.md missing rein run start command: %s", md)
	}
	if !strings.Contains(md, "rein run allow /tmp/test-repo --task test-run-node-a") {
		t.Errorf("plan.md missing rein run allow command: %s", md)
	}

	// Validate in-process with supervisor decoder
	if err := c.ValidateInProcess(); err != nil {
		t.Errorf("expected supervisor in-process validation to pass, got: %v", err)
	}

	// Output writing and idempotency
	if err := c.WriteOutputs(); err != nil {
		t.Fatalf("first WriteOutputs failed: %v", err)
	}
	if err := c.WriteOutputs(); err != nil {
		t.Fatalf("second WriteOutputs should be idempotent, got: %v", err)
	}

	// Tampering detection
	planFile := filepath.Join(outDir, "plan.json")
	if err := os.WriteFile(planFile, []byte(`{"schema": 3, "tampered": true}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := c.WriteOutputs(); err == nil {
		t.Error("expected WriteOutputs to refuse differing existing file, got nil")
	}
}

func TestCompileRejectsUnknownProvider(t *testing.T) {
	outDir := filepath.Join(t.TempDir(), "compiled")
	spec := sampleValidSpec()
	spec = strings.Replace(spec, `"claude"`, `"unknown-prov"`, 1)

	_, err := Compile([]byte(spec), []byte(sampleOwnerConfig()), outDir, "/etc/rein/owner.json")
	if err == nil {
		t.Fatal("expected error for unknown provider, got nil")
	}
	if !strings.Contains(err.Error(), "unknown-prov") {
		t.Errorf("expected error mentioning unknown-prov, got: %v", err)
	}
}

func TestCompileRejectsEmptyOwnerProviders(t *testing.T) {
	outDir := filepath.Join(t.TempDir(), "compiled")
	owner := `{"providers": {}, "profile": {}, "config_path": "/etc/rein/owner.json"}`

	_, err := Compile([]byte(sampleValidSpec()), []byte(owner), outDir, "/etc/rein/owner.json")
	if err == nil {
		t.Fatal("expected error for empty providers, got nil")
	}
}
