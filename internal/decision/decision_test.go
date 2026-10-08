package decision

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanSecrets(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "AWS key",
			input: "My AWS key is AKIAIOSFODNN7EXAMPLE",
			want:  "My AWS key is [REDACTED]",
		},
		{
			name:  "GitHub token ghp",
			input: "Token: ghp_" + strings.Repeat("a", 36),
			want:  "Token: [REDACTED]",
		},
		{
			name:  "GitHub token gho",
			input: "gho_" + strings.Repeat("b", 36),
			want:  "[REDACTED]",
		},
		{
			name:  "GitLab token",
			input: "glpat-" + strings.Repeat("c", 20),
			want:  "[REDACTED]",
		},
		{
			name:  "Bearer token",
			input: "Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9",
			want:  "Authorization: [REDACTED]",
		},
		{
			name:  "OpenAI API key",
			input: "sk-" + strings.Repeat("d", 20),
			want:  "[REDACTED]",
		},
		{
			name:  "Password assignment colon",
			input: "password: secret123",
			want:  "[REDACTED]",
		},
		{
			name:  "Password assignment equals",
			input: "PASSWORD=secret123",
			want:  "[REDACTED]",
		},
		{
			name:  "API key assignment",
			input: "api_key=abcd1234efgh5678",
			want:  "[REDACTED]",
		},
		{
			name:  "Multiple secrets",
			input: "AWS: AKIAIOSFODNN7EXAMPLE, GitHub: ghp_" + strings.Repeat("e", 36),
			want:  "AWS: [REDACTED], GitHub: [REDACTED]",
		},
		{
			name:  "Safe text",
			input: "This is just normal text with no secrets",
			want:  "This is just normal text with no secrets",
		},
		{
			name:  "Safe words that sound like secrets",
			input: "I need to password-protect this file",
			want:  "I need to password-protect this file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ScanSecrets(tt.input)
			if got != tt.want {
				t.Errorf("ScanSecrets() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSanitizeTSVField(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "tabs replaced with spaces",
			input: "foo\tbar\tbaz",
			want:  "foo bar baz",
		},
		{
			name:  "newlines replaced with spaces",
			input: "foo\nbar\nbaz",
			want:  "foo bar baz",
		},
		{
			name:  "carriage returns replaced",
			input: "foo\r\nbar\r\nbaz",
			want:  "foo bar baz",
		},
		{
			name:  "multiple spaces collapsed",
			input: "foo    bar     baz",
			want:  "foo bar baz",
		},
		{
			name:  "mixed whitespace",
			input: "foo\t\nbar  \n\t  baz",
			want:  "foo bar baz",
		},
		{
			name:  "clean text",
			input: "foo bar baz",
			want:  "foo bar baz",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeTSVField(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeTSVField() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAppend(t *testing.T) {
	tmpDir := t.TempDir()

	tests := []struct {
		name          string
		question      string
		chosenOption  string
		freeText      string
		wantErr       bool
		checkRedacted bool
	}{
		{
			name:         "basic decision",
			question:     "Which approach?",
			chosenOption: "Option A",
			freeText:     "This seems like the best choice",
			wantErr:      false,
		},
		{
			name:          "decision with secret in free text",
			question:      "Deploy to production?",
			chosenOption:  "Yes",
			freeText:      "Using API key: AKIAIOSFODNN7EXAMPLE",
			wantErr:       false,
			checkRedacted: true,
		},
		{
			name:         "decision with tabs and newlines",
			question:     "What\tdo\tyou\tthink?",
			chosenOption: "Option\nB",
			freeText:     "I think\nthis is\tgood",
			wantErr:      false,
		},
		{
			name:         "empty free text",
			question:     "Proceed?",
			chosenOption: "Yes",
			freeText:     "",
			wantErr:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runDir := filepath.Join(tmpDir, tt.name)

			err := Append(runDir, tt.question, tt.chosenOption, tt.freeText)
			if (err != nil) != tt.wantErr {
				t.Errorf("Append() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				// Verify file was created
				path := filepath.Join(runDir, "decisions.tsv")
				content, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("failed to read decisions.tsv: %v", err)
				}

				line := string(content)
				// Verify format: timestamp\tquestion\toption\tnotes\n
				// Note: when notes is empty, the line ends with \t\n
				fields := strings.Split(strings.TrimSuffix(line, "\n"), "\t")
				if len(fields) != 4 {
					t.Errorf("expected 4 TSV fields, got %d: %q (fields: %#v)", len(fields), line, fields)
				}

				// Verify no tabs or newlines in fields (except field separators)
				for i, field := range fields {
					if strings.Contains(field, "\t") {
						t.Errorf("field %d contains tab: %q", i, field)
					}
					if strings.Contains(field, "\n") {
						t.Errorf("field %d contains newline: %q", i, field)
					}
					if strings.Contains(field, "\r") {
						t.Errorf("field %d contains carriage return: %q", i, field)
					}
				}

				// Verify exactly one line
				lines := strings.Split(string(content), "\n")
				if len(lines) != 2 { // one line + trailing newline = 2 elements after split
					t.Errorf("expected exactly one decision line, got %d lines", len(lines)-1)
				}

				// Check for redacted secrets
				if tt.checkRedacted {
					if !strings.Contains(line, "[REDACTED]") {
						t.Errorf("expected secret to be redacted, got: %q", line)
					}
					if strings.Contains(line, "AKIA") {
						t.Errorf("AWS key should be redacted, got: %q", line)
					}
				}
			}
		})
	}
}

func TestAppendMultiple(t *testing.T) {
	tmpDir := t.TempDir()
	runDir := filepath.Join(tmpDir, "test-run")

	// Append multiple decisions
	decisions := []struct {
		question string
		option   string
		notes    string
	}{
		{"Question 1", "Option A", "Notes 1"},
		{"Question 2", "Option B", "Notes 2"},
		{"Question 3", "Option C", "Notes 3"},
	}

	for _, d := range decisions {
		if err := Append(runDir, d.question, d.option, d.notes); err != nil {
			t.Fatalf("Append() failed: %v", err)
		}
	}

	// Verify file has exactly 3 lines
	path := filepath.Join(runDir, "decisions.tsv")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read decisions.tsv: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) != 3 {
		t.Errorf("expected 3 decisions, got %d", len(lines))
	}

	// Verify each line is well-formed
	for i, line := range lines {
		fields := strings.Split(line, "\t")
		if len(fields) != 4 {
			t.Errorf("line %d: expected 4 fields, got %d: %q", i+1, len(fields), line)
		}
	}
}
