package ledger

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func ip(v int) *int { return &v }

func TestAddReportSuggest(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PIPELINE_LEDGER", filepath.Join(dir, "l.jsonl"))
	t.Setenv("PIPELINE_PRICES", filepath.Join(dir, "prices.json"))
	_ = os.WriteFile(filepath.Join(dir, "prices.json"), []byte(`{"gpt-x":{"usd_per_mtok":10},"_doc":"x"}`), 0o644)
	for i := 0; i < 3; i++ {
		if err := Append(Row{Kind: "task", Task: "b", Type: "backend", Worker: &AgentModel{"codex", "gpt-x"}, ReviewRounds: ip(2), Approved: true, Highs: ip(1), WorkerTokens: ip(100000)}); err != nil {
			t.Fatal(err)
		}
		if err := Append(Row{Kind: "task", Task: "s", Type: "backend", Worker: &AgentModel{"claude", "sonnet"}, ReviewRounds: ip(4), Approved: true, Blockers: ip(1)}); err != nil {
			t.Fatal(err)
		}
	}
	ok := true
	_ = Append(Row{Kind: "call", Role: "critic", Provider: "kiro", Model: "claude-sonnet-5.5", Credits: func() *float64 { v := 0.5; return &v }(), OK: &ok})
	_ = os.WriteFile(Path(), append(mustRead(t, Path()), []byte("not json\n")...), 0o644) // a bad line is skipped, not fatal
	rows, err := Load("")
	if err != nil || len(rows) != 7 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	var rep, sug bytes.Buffer
	Report(&rep, rows, "")
	if !strings.Contains(rep.String(), "backend | codex:gpt-x | 3 | 100% | 2.0") || !strings.Contains(rep.String(), "| 3.00 |") {
		t.Errorf("report:\n%s", rep.String())
	}
	if !strings.Contains(rep.String(), "critic@kiro:claude-sonnet-5.5: 1 calls, 0 tokens, 0.50 credits") {
		t.Errorf("calls:\n%s", rep.String())
	}
	Suggest(&sug, rows, 3)
	if !strings.Contains(sug.String(), "consider routing backend to codex:gpt-x") {
		t.Errorf("suggest:\n%s", sug.String())
	}
	sug.Reset()
	Suggest(&sug, rows, 4)
	if !strings.Contains(sug.String(), "not enough data") {
		t.Errorf("thin data must not suggest:\n%s", sug.String())
	}
}

func TestParseAgentModel(t *testing.T) {
	if _, err := ParseAgentModel("claude:claude-fable-5-1"); err == nil {
		t.Error("fable must be rejected")
	}
	if _, err := ParseAgentModel("gpt"); err == nil {
		t.Error("missing agent must be rejected")
	}
	if am, err := ParseAgentModel("opencode2:opencode-go/glm-5.3-flash"); err != nil || am.Model != "opencode-go/glm-5.3-flash" {
		t.Errorf("got %v %v", am, err)
	}
}

func mustRead(t *testing.T, p string) []byte {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
