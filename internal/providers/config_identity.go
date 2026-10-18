package providers

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
)

// codexAccountMetadataEvidence compares default OAuth credential metadata only.
// Orca does not bind each quota snapshot to this identity, and a match is not
// proof that a launched worker uses these credentials. IDs and tokens never leave
// this function. Unsupported selections and auth overrides remain unverified.
func codexAccountMetadataEvidence(raw []byte) string {
	const unknown = "unverified"
	for _, key := range []string{"OPENAI_API_KEY", "CODEX_API_KEY", "OPENAI_BASE_URL", "ORCA_ENVIRONMENT", "ORCA_PAIRING_CODE"} {
		if os.Getenv(key) != "" {
			return unknown
		}
	}
	var envelope struct {
		OK     bool `json:"ok"`
		Result struct {
			Codex struct {
				Active    json.RawMessage            `json:"activeAccountId"`
				ByRuntime map[string]json.RawMessage `json:"activeAccountIdsByRuntime"`
				Default   struct {
					HasAuth bool   `json:"hasAuth"`
					Kind    string `json:"authKind"`
					Account string `json:"providerAccountId"`
				} `json:"systemDefault"`
			} `json:"codex"`
			RateLimits struct {
				Target struct {
					Runtime string  `json:"runtime"`
					WSL     *string `json:"wslDistro"`
				} `json:"codexTarget"`
			} `json:"rateLimits"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &envelope) != nil || !envelope.OK {
		return unknown
	}
	source := envelope.Result.Codex
	target := envelope.Result.RateLimits.Target
	// Require explicit system-default host selection. Missing fields cannot prove
	// this scope, and a managed/WSL selection may use another credential directory.
	if target.Runtime != "host" || target.WSL != nil || string(source.Active) != "null" || string(source.ByRuntime["host"]) != "null" || !source.Default.HasAuth || source.Default.Kind != "oauth" || source.Default.Account == "" {
		return unknown
	}
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return unknown
		}
		home = filepath.Join(userHome, ".codex")
	}
	if !filepath.IsAbs(home) {
		return unknown
	}
	path := filepath.Join(home, "auth.json")
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return unknown
	}
	file, err := os.Open(path)
	if err != nil {
		return unknown
	}
	defer file.Close()
	b, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(b) > 1<<20 {
		return unknown
	}
	var auth struct {
		Mode   string `json:"auth_mode"`
		APIKey string `json:"OPENAI_API_KEY"`
		Tokens struct {
			Account string `json:"account_id"`
		} `json:"tokens"`
	}
	if json.Unmarshal(b, &auth) != nil || auth.Mode != "chatgpt" || auth.APIKey != "" || auth.Tokens.Account == "" {
		return unknown
	}
	if auth.Tokens.Account != source.Default.Account {
		return "credential_metadata_mismatch"
	}
	return "credential_metadata_match"
}
