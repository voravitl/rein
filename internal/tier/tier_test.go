package tier

import "testing"

func TestTierString(t *testing.T) {
	tests := []struct {
		tier Tier
		want string
	}{
		{T1, "T1"},
		{T2, "T2"},
		{T3, "T3"},
		{Tier(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.tier.String(); got != tt.want {
			t.Errorf("Tier(%d).String() = %q, want %q", tt.tier, got, tt.want)
		}
	}
}

func TestEvaluateFromAllowGlobs(t *testing.T) {
	tests := []struct {
		name           string
		allowGlobs     []string
		sensitivePaths []string
		want           Tier
	}{
		{
			name:           "empty allow globs",
			allowGlobs:     []string{},
			sensitivePaths: []string{"secrets/**"},
			want:           T1,
		},
		{
			name:           "no sensitive paths",
			allowGlobs:     []string{"src/**"},
			sensitivePaths: []string{},
			want:           T1,
		},
		{
			name:           "no intersection",
			allowGlobs:     []string{"src/utils/**"},
			sensitivePaths: []string{"secrets/**", "internal/auth/**"},
			want:           T1,
		},
		{
			name:           "intersection with sensitive path",
			allowGlobs:     []string{"src/**", "internal/auth/**"},
			sensitivePaths: []string{"internal/auth/**"},
			want:           T3,
		},
		{
			name:           "allow overlaps multiple sensitive",
			allowGlobs:     []string{"**/*.go"},
			sensitivePaths: []string{"secrets/**", "internal/auth/**"},
			want:           T3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EvaluateFromAllowGlobs(tt.allowGlobs, tt.sensitivePaths)
			if got != tt.want {
				t.Errorf("EvaluateFromAllowGlobs(%v, %v) = %v, want %v",
					tt.allowGlobs, tt.sensitivePaths, got, tt.want)
			}
		})
	}
}

func TestEvaluateFromChangedFiles(t *testing.T) {
	tests := []struct {
		name           string
		changedFiles   []string
		sensitivePaths []string
		baseTier       Tier
		want           Tier
	}{
		{
			name:           "no files changed",
			changedFiles:   []string{},
			sensitivePaths: []string{"secrets/**"},
			baseTier:       T1,
			want:           T1,
		},
		{
			name:           "non-sensitive files",
			changedFiles:   []string{"src/utils/helper.go", "README.md"},
			sensitivePaths: []string{"secrets/**", "internal/auth/**"},
			baseTier:       T1,
			want:           T1,
		},
		{
			name:           "sensitive file matches exact glob",
			changedFiles:   []string{"secrets/api_key.txt"},
			sensitivePaths: []string{"secrets/**"},
			baseTier:       T1,
			want:           T3,
		},
		{
			name:           "escalate from T2 to T3",
			changedFiles:   []string{"internal/auth/login.go"},
			sensitivePaths: []string{"internal/auth/**"},
			baseTier:       T2,
			want:           T3,
		},
		{
			name:           "cannot downgrade from T3",
			changedFiles:   []string{"README.md"},
			sensitivePaths: []string{"secrets/**"},
			baseTier:       T3,
			want:           T3,
		},
		{
			name:           "mixed files with one sensitive",
			changedFiles:   []string{"src/main.go", "internal/auth/middleware.go", "docs/api.md"},
			sensitivePaths: []string{"internal/auth/**"},
			baseTier:       T1,
			want:           T3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EvaluateFromChangedFiles(tt.changedFiles, tt.sensitivePaths, tt.baseTier)
			if got != tt.want {
				t.Errorf("EvaluateFromChangedFiles(%v, %v, %v) = %v, want %v",
					tt.changedFiles, tt.sensitivePaths, tt.baseTier, got, tt.want)
			}
		})
	}
}

func TestGlobsIntersect(t *testing.T) {
	tests := []struct {
		a    string
		b    string
		want bool
	}{
		{"src/**", "src/utils/**", true},
		{"internal/auth/**", "secrets/**", false},
		{"**/*.go", "internal/auth/**", true},
		{"src/a/**", "src/b/**", false},
	}

	for _, tt := range tests {
		got := globsIntersect(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("globsIntersect(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}
