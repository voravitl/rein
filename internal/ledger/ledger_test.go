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

func TestSuggestPrefersApprovedWork(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PIPELINE_LEDGER", filepath.Join(dir, "l.jsonl"))
	t.Setenv("PIPELINE_PRICES", filepath.Join(dir, "none.json"))
	for i := 0; i < 3; i++ {
		_ = Append(Row{Kind: "task", Task: "r", Type: "frontend", Worker: &AgentModel{"opencode2", "cheap"}, ReviewRounds: ip(1), Approved: false})
		_ = Append(Row{Kind: "task", Task: "a", Type: "frontend", Worker: &AgentModel{"claude", "sonnet"}, ReviewRounds: ip(2), Approved: true})
	}
	rows, _ := Load("")
	var b bytes.Buffer
	Suggest(&b, rows, 3)
	if strings.Contains(b.String(), "routing frontend to opencode2:cheap") {
		t.Errorf("rejected one-round work must not win:\n%s", b.String())
	}
}

func TestLoadSinceComparesInstantsNotStrings(t *testing.T) {
	t.Setenv("PIPELINE_LEDGER", filepath.Join(t.TempDir(), "l.jsonl"))
	// 08:00 at -05:00 is 13:00Z, i.e. AFTER a 12:00Z run start although "08:00" < "12:00" as text.
	// 20:00 at +09:00 is 11:00Z, i.e. BEFORE it although "20:00" > "12:00" as text.
	lines := `{"kind":"call","task":"after","recorded_at":"2026-10-09T08:00:00-05:00"}` + "\n" +
		`{"kind":"call","task":"before","recorded_at":"2026-10-09T20:00:00+09:00"}` + "\n" +
		`{"kind":"call","task":"date-only","recorded_at":"2026-10-10T00:00:00Z"}` + "\n"
	if err := os.WriteFile(Path(), []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	rows, err := LoadStrict("2026-10-09T12:00:00Z")
	if err != nil || len(rows) != 2 || rows[0].Task != "after" || rows[1].Task != "date-only" {
		t.Fatalf("instant comparison: %v %v", rows, err)
	}
	if rows, err = Load("2026-10-10"); err != nil || len(rows) != 1 || rows[0].Task != "date-only" { // bare dates keep text comparison
		t.Fatalf("bare date: %v %v", rows, err)
	}
}

func TestLoadStrictRejectsCorruptionWithoutChangingReports(t *testing.T) {
	t.Setenv("PIPELINE_LEDGER", filepath.Join(t.TempDir(), "ledger.jsonl"))
	if err := os.WriteFile(Path(), []byte(`{"kind":"call","run":"r","recorded_at":"2026-01-01T00:00:00Z"}`+"\nbroken\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if rows, err := Load(""); err != nil || len(rows) != 1 {
		t.Fatalf("legacy reporting changed: %v %v", rows, err)
	}
	if _, err := LoadStrict(""); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("strict corruption result: %v", err)
	}
}
