package verdict

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestNormalizeModelMaker(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{"claude-sonnet-4.5", "anthropic"},
		{"claude-opus-4.8", "anthropic"},
		{"Claude-Haiku-4.5", "anthropic"},
		{"gpt-5.6", "openai"},
		{"gpt-4o", "openai"},
		{"o1-preview", "openai"},
		{"gemini-2.5-pro", "google"},
		{"glm-5", "glm"},
		{"deepseek-3.2", "deepseek"},
		{"qwen3-coder-next", "qwen"},
		{"minimax-m2.5", "minimax"},
		{"unknown-model", "unknown"},
	}

	for _, tt := range tests {
		got := NormalizeModelMaker(tt.model)
		if got != tt.want {
			t.Errorf("NormalizeModelMaker(%q) = %q, want %q", tt.model, got, tt.want)
		}
	}
}

func TestIsEscalatedReviewer(t *testing.T) {
	tests := []struct {
		worker   string
		reviewer string
		want     bool
	}{
		{"claude-sonnet-4.5", "claude-opus-4.8", true},
		{"claude-haiku", "claude-opus-3.7", true},
		{"claude-opus-4.8", "claude-sonnet-4.5", false},
		{"claude-sonnet-4.5", "claude-sonnet-4.5", false},
		{"gpt-4o-mini", "o1", true},
		{"gpt-4o", "o3-mini", true},
		{"gpt-4o", "gpt-4o", false},
		{"deepseek-coder", "claude-opus", false}, // different makers, handled by maker check
	}

	for _, tt := range tests {
		got := IsEscalatedReviewer(tt.worker, tt.reviewer)
		if got != tt.want {
			t.Errorf("IsEscalatedReviewer(%q, %q) = %v, want %v", tt.worker, tt.reviewer, got, tt.want)
		}
	}
}

func TestComputeRevisionID(t *testing.T) {
	// Create a temporary git repo
	tmpDir := t.TempDir()

	// Initialize git repo
	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("git %v failed: %v", args, err)
		}
	}

	runGit("init")
	runGit("config", "user.email", "test@example.com")
	runGit("config", "user.name", "Test User")

	// Create initial commit
	writeFile := func(name, content string) {
		path := filepath.Join(tmpDir, name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	writeFile("file1.txt", "initial content\n")
	runGit("add", "file1.txt")
	runGit("commit", "-m", "initial")
	runGit("branch", "-M", "main")

	// Create a change
	writeFile("file1.txt", "modified content\n")
	runGit("add", "file1.txt")
	runGit("commit", "-m", "change")

	// Test with main as base and HEAD
	id, err := ComputeRevisionID(tmpDir, "main~1", "HEAD")
	if err != nil {
		t.Fatalf("ComputeRevisionID failed: %v", err)
	}
	if id == "" {
		t.Error("expected non-empty revision ID")
	}

	// Test that same changes produce same ID
	id2, err := ComputeRevisionID(tmpDir, "main~1", "HEAD")
	if err != nil {
		t.Fatalf("ComputeRevisionID failed: %v", err)
	}
	if id != id2 {
		t.Errorf("same changes should produce same ID: %q != %q", id, id2)
	}

	// Test empty diff
	idEmpty, err := ComputeRevisionID(tmpDir, "HEAD", "HEAD")
	if err != nil {
		t.Fatalf("ComputeRevisionID failed: %v", err)
	}
	if idEmpty != "" {
		t.Errorf("empty diff should produce empty ID, got %q", idEmpty)
	}
}

func TestRevisionIdentitiesMatch(t *testing.T) {
	tests := []struct {
		name string
		idA  string
		idB  string
		want bool
	}{
		{"exact match", "abc123", "abc123", true},
		{"different IDs", "abc123", "def456", false},
		{"empty idA", "", "abc123", false},
		{"empty idB", "abc123", "", false},
		{"both empty", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RevisionIdentitiesMatch(tt.idA, tt.idB, "", "", "", nil)
			if got != tt.want {
				t.Errorf("RevisionIdentitiesMatch(%q, %q) = %v, want %v",
					tt.idA, tt.idB, got, tt.want)
			}
		})
	}
}

func TestFilesIntersect(t *testing.T) {
	tests := []struct {
		name string
		a    []string
		b    []string
		want bool
	}{
		{"no intersection", []string{"a.go", "b.go"}, []string{"c.go", "d.go"}, false},
		{"one common file", []string{"a.go", "b.go"}, []string{"b.go", "c.go"}, true},
		{"empty a", []string{}, []string{"a.go"}, false},
		{"empty b", []string{"a.go"}, []string{}, false},
		{"both empty", []string{}, []string{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filesIntersect(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("filesIntersect(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestStorageDir(t *testing.T) {
	tests := []struct {
		name     string
		runDir   string
		repoPath string
		want     string
	}{
		{
			name:     "with run dir",
			runDir:   "/runs/task-1",
			repoPath: "/repo",
			want:     "/runs/task-1/verdicts",
		},
		{
			name:     "without run dir",
			runDir:   "",
			repoPath: "/repo",
			want:     "/repo/.rein/verdicts",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StorageDir(tt.runDir, tt.repoPath)
			if got != tt.want {
				t.Errorf("StorageDir(%q, %q) = %q, want %q",
					tt.runDir, tt.repoPath, got, tt.want)
			}
		})
	}
}

func TestVerdictPath(t *testing.T) {
	storageDir := "/verdicts"
	mr := 42
	want := "/verdicts/mr-42-verdicts.json"
	got := VerdictPath(storageDir, mr)
	if got != want {
		t.Errorf("VerdictPath(%q, %d) = %q, want %q", storageDir, mr, got, want)
	}
}

func TestApprovalPath(t *testing.T) {
	storageDir := "/verdicts"
	mr := 42
	want := "/verdicts/mr-42-approvals.json"
	got := ApprovalPath(storageDir, mr)
	if got != want {
		t.Errorf("ApprovalPath(%q, %d) = %q, want %q", storageDir, mr, got, want)
	}
}

func TestHashString(t *testing.T) {
	s := "test string"
	h := HashString(s)
	if h == "" {
		t.Error("HashString returned empty")
	}
	if len(h) != 64 {
		t.Errorf("HashString length = %d, want 64", len(h))
	}
	// Same input should produce same hash
	h2 := HashString(s)
	if h != h2 {
		t.Errorf("HashString not deterministic: %q != %q", h, h2)
	}
}
