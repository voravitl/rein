package providers

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodexAccountMetadataEvidenceAndPrivacy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("CODEX_API_KEY", "")
	t.Setenv("OPENAI_BASE_URL", "")
	auth := []byte(`{"auth_mode":"chatgpt","tokens":{"account_id":"local-account","access_token":"private-access","refresh_token":"private-refresh"}}`)
	if err := os.WriteFile(filepath.Join(home, "auth.json"), auth, 0600); err != nil {
		t.Fatal(err)
	}
	raw := func(id string) []byte {
		return []byte(`{"ok":true,"result":{"codex":{"activeAccountId":null,"activeAccountIdsByRuntime":{"host":null},"systemDefault":{"hasAuth":true,"authKind":"oauth","providerAccountId":"` + id + `"}},"rateLimits":{"codexTarget":{"runtime":"host","wslDistro":null}}}}`)
	}
	if got := codexAccountMetadataEvidence(raw("local-account")); got != "credential_metadata_match" {
		t.Fatal(got)
	}
	if got := codexAccountMetadataEvidence(raw("other-account")); got != "credential_metadata_mismatch" {
		t.Fatal(got)
	}
	for _, data := range [][]byte{[]byte(`{`), []byte(`{"ok":false}`), []byte(`{"ok":true,"result":{}}`)} {
		if got := codexAccountMetadataEvidence(data); got != "unverified" {
			t.Fatal(got)
		}
	}
	t.Setenv("OPENAI_API_KEY", "private-key")
	if got := codexAccountMetadataEvidence(raw("local-account")); got != "unverified" {
		t.Fatal("API override inferred as OAuth match", got)
	}
	t.Setenv("OPENAI_API_KEY", "")
	var d map[string]any
	if err := json.Unmarshal(raw("local-account"), &d); err != nil {
		t.Fatal(err)
	}
	result := d["result"].(map[string]any)
	result["rateLimits"].(map[string]any)["codexTarget"] = map[string]any{"runtime": "wsl", "wslDistro": "test"}
	b, _ := json.Marshal(d)
	if got := codexAccountMetadataEvidence(b); got != "unverified" {
		t.Fatal("WSL account inferred from host file", got)
	}
	result["rateLimits"].(map[string]any)["codexTarget"] = map[string]any{"runtime": "host"}
	result["codex"].(map[string]any)["activeAccountId"] = "managed-id"
	b, _ = json.Marshal(d)
	if got := codexAccountMetadataEvidence(b); got != "unverified" {
		t.Fatal("managed selection inferred as default", got)
	}
	for _, text := range []string{codexAccountMetadataEvidence(raw("local-account")), codexAccountMetadataEvidence(raw("other-account"))} {
		for _, secret := range []string{"local-account", "other-account", "private-access", "private-refresh"} {
			if strings.Contains(text, secret) {
				t.Fatal("private account material retained")
			}
		}
	}
}

func TestCodexMetadataMismatchVetoDoesNotInvokeModel(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "codex-home")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", home)
	for _, key := range []string{"OPENAI_API_KEY", "CODEX_API_KEY", "OPENAI_BASE_URL", "ORCA_ENVIRONMENT", "ORCA_PAIRING_CODE"} {
		t.Setenv(key, "")
	}
	response := filepath.Join(dir, "response")
	marker := filepath.Join(dir, "calls")
	raw := fmt.Sprintf(`{"ok":true,"result":{"codex":{"activeAccountId":null,"activeAccountIdsByRuntime":{"host":null},"systemDefault":{"hasAuth":true,"authKind":"oauth","providerAccountId":"source-account"}},"rateLimits":{"codexTarget":{"runtime":"host"},"codex":{"status":"ok","updatedAt":%d,"weekly":{"usedPercent":10}}}}}`, time.Now().UnixMilli())
	if err := os.WriteFile(response, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	orca := installTestOrca(t, response)
	writeAuth := func(id string) {
		t.Helper()
		data := fmt.Sprintf(`{"auth_mode":"chatgpt","tokens":{"account_id":%q,"access_token":"private-token"}}`, id)
		if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := Config{RequireOrcaQuota: true, Providers: map[string]Provider{"p": {Agent: "codex", Model: "test", QuotaWindows: []string{"weekly"}, Probe: []string{orca, "probe", marker}}}}
	writeAuth("source-account")
	snapshots, err := ReadOrcaQuota()
	if err != nil {
		t.Fatal(err)
	}
	if snapshots[1].AccountEvidence != "credential_metadata_match" {
		t.Fatal(snapshots[1].AccountEvidence)
	}
	if r := Probe(&cfg, "p", time.Second); r.State != "up" {
		t.Fatal(r)
	}
	writeAuth("different-account")
	if r := Probe(&cfg, "p", time.Second); r.State != "down" || !strings.Contains(r.Detail, "metadata differ") {
		t.Fatal(r)
	}
	calls, _ := os.ReadFile(marker)
	if string(calls) != "x" {
		t.Fatalf("mismatch ran a model command: %q", calls)
	}
	b, err := json.Marshal(snapshots)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"source-account", "different-account", "private-token"} {
		if strings.Contains(string(b), secret) {
			t.Fatal("snapshot leaked credential material")
		}
	}
}

func TestCodexAccountEvidenceRejectsMissingAndOversizedCredentialData(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	raw := []byte(`{"ok":true,"result":{"codex":{"activeAccountId":null,"activeAccountIdsByRuntime":{"host":null},"systemDefault":{"hasAuth":true,"authKind":"oauth","providerAccountId":"source"}},"rateLimits":{"codexTarget":{"runtime":"host"}}}}`)
	if got := codexAccountMetadataEvidence(raw); got != "unverified" {
		t.Fatal(got)
	}
	for _, data := range []string{`{`, strings.Repeat(" ", (1<<20)+1), `{"auth_mode":"apikey","tokens":{"account_id":"source"}}`, `{"auth_mode":"chatgpt","OPENAI_API_KEY":"private-key","tokens":{"account_id":"source"}}`} {
		if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if got := codexAccountMetadataEvidence(raw); got != "unverified" {
			t.Fatal(got)
		}
	}
}
