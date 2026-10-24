package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeZcodeFile(wt, content string) error {
	if err := os.MkdirAll(filepath.Join(wt, ".zcode"), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(wt, ".zcode/config.json"), []byte(content), 0o644)
}

func readZcodeFile(t *testing.T, wt string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(wt, ".zcode/config.json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func zcodeEvents(t *testing.T, wt string) map[string]any {
	t.Helper()
	doc := mustJSON(t, filepath.Join(wt, ".zcode/config.json"))
	hooks, ok := doc["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("hooks object missing: %v", doc)
	}
	if v, ok := hooks["enabled"].(bool); !ok || !v {
		t.Errorf("hooks.enabled = %v, want true (ZCode runs configuration-file hooks only when enabled)", hooks["enabled"])
	}
	events, ok := hooks["events"].(map[string]any)
	if !ok {
		t.Fatalf("hooks.events missing: %v", hooks)
	}
	return events
}

func zcodeGroupCount(events map[string]any, event string) int {
	groups, _ := events[event].([]any)
	return len(groups)
}

func TestZcodeInstallShape(t *testing.T) {
	root, wt, c := setup(t)
	res, _, err := Install(c, filepath.Join(root, "rein"), []string{"zcode"})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Err != nil {
		t.Fatalf("zcode install: %v", res[0].Err)
	}
	events := zcodeEvents(t, wt)
	for _, event := range []string{"PreToolUse", "PostToolUse", "Stop"} {
		if zcodeGroupCount(events, event) != 1 {
			t.Errorf("%s groups = %d, want 1: %v", event, zcodeGroupCount(events, event), events[event])
		}
	}
	pre, _ := events["PreToolUse"].([]any)
	group, _ := pre[0].(map[string]any)
	if m, _ := group["matcher"].(string); !strings.Contains(m, "Bash") || !strings.Contains(m, "ApplyPatch") {
		t.Errorf("PreToolUse matcher = %q, want the tool surface including Bash and ApplyPatch", m)
	}
	cmd, _ := group["hooks"].([]any)[0].(map[string]any)["command"].(string)
	if !strings.Contains(cmd, " hook --vendor zcode --task w'") && !strings.Contains(cmd, ` hook --vendor zcode --task w`) {
		t.Errorf("guard command = %q, want a task-bound zcode hook", cmd)
	}
}

func TestZcodeInstallIsIdempotent(t *testing.T) {
	root, wt, c := setup(t)
	for i := 0; i < 2; i++ {
		if _, _, err := Install(c, filepath.Join(root, "rein"), []string{"zcode"}); err != nil {
			t.Fatal(err)
		}
	}
	events := zcodeEvents(t, wt)
	for _, event := range []string{"PreToolUse", "PostToolUse", "Stop"} {
		if zcodeGroupCount(events, event) != 1 {
			t.Errorf("after reinstall %s groups = %d, want 1", event, zcodeGroupCount(events, event))
		}
	}
}

func TestZcodeInstallPreservesForeignConfig(t *testing.T) {
	root, wt, c := setup(t)
	if err := writeZcodeFile(wt, `{"model":"x","hooks":{"enabled":true,"events":{"PreToolUse":[{"matcher":"Mine","hooks":[{"type":"command","command":"mine"}]}]}}}`); err != nil {
		t.Fatal(err)
	}
	res, _, err := Install(c, filepath.Join(root, "rein"), []string{"zcode"})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Err != nil {
		t.Fatalf("install over foreign config: %v", res[0].Err)
	}
	doc := mustJSON(t, filepath.Join(wt, ".zcode/config.json"))
	if doc["model"] != "x" {
		t.Errorf("foreign top-level key lost: %v", doc)
	}
	events := zcodeEvents(t, wt)
	if got := zcodeGroupCount(events, "PreToolUse"); got != 2 {
		t.Errorf("PreToolUse groups = %d, want 2 (foreign + rein): %v", got, events["PreToolUse"])
	}
	foundForeign, foundRein := false, false
	for _, g := range events["PreToolUse"].([]any) {
		m, _ := g.(map[string]any)["matcher"].(string)
		if m == "Mine" {
			foundForeign = true
		}
		if strings.Contains(m, "Bash") {
			foundRein = true
		}
	}
	if !foundForeign || !foundRein {
		t.Errorf("foreign=%v rein=%v, want both", foundForeign, foundRein)
	}
}

func TestZcodeInstallRefusesDisabledHooks(t *testing.T) {
	root, wt, c := setup(t)
	if err := writeZcodeFile(wt, `{"hooks":{"enabled":false,"events":{}}}`); err != nil {
		t.Fatal(err)
	}
	res, _, err := Install(c, filepath.Join(root, "rein"), []string{"zcode"})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Err == nil || !strings.Contains(res[0].Err.Error(), "hooks.enabled is false") {
		t.Errorf("want refusal over user-disabled hooks, got %+v", res[0])
	}
	if b := readZcodeFile(t, wt); strings.Contains(b, "--vendor zcode") {
		t.Error("refused install must not write the guard")
	}
}

func TestZcodeUninstallRemovesOnlyRein(t *testing.T) {
	root, wt, c := setup(t)
	if _, _, err := Install(c, filepath.Join(root, "rein"), []string{"zcode"}); err != nil {
		t.Fatal(err)
	}
	// a foreign group joins after install; uninstall must keep it and the enablement
	doc := mustJSON(t, filepath.Join(wt, ".zcode/config.json"))
	hooks := doc["hooks"].(map[string]any)
	events := hooks["events"].(map[string]any)
	events["Stop"] = append(events["Stop"].([]any), map[string]any{"matcher": "Mine", "hooks": []any{map[string]any{"type": "command", "command": "mine"}}})
	hooks["events"] = events
	doc["hooks"] = hooks
	if err := writeJSON(filepath.Join(wt, ".zcode/config.json"), doc); err != nil {
		t.Fatal(err)
	}
	results, _, err := Uninstall(c, []string{"zcode"})
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Err != nil {
		t.Fatalf("uninstall: %v", results[0].Err)
	}
	after := mustJSON(t, filepath.Join(wt, ".zcode/config.json"))
	hooks = after["hooks"].(map[string]any)
	events = hooks["events"].(map[string]any)
	if _, ok := events["PreToolUse"]; ok {
		t.Errorf("rein PreToolUse group survived: %v", events)
	}
	stop := events["Stop"].([]any)
	if len(stop) != 1 {
		t.Fatalf("Stop groups = %d, want the foreign one only: %v", len(stop), stop)
	}
	if v, ok := hooks["enabled"].(bool); !ok || !v {
		t.Errorf("enabled must stay true while foreign hooks remain, got %v", hooks["enabled"])
	}

	// removing the foreign group too leaves no events: the enablement goes with them
	delete(events, "Stop")
	hooks["events"] = events
	after["hooks"] = hooks
	if err := writeJSON(filepath.Join(wt, ".zcode/config.json"), after); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Uninstall(c, []string{"zcode"}); err != nil {
		t.Fatal(err)
	}
	final := mustJSON(t, filepath.Join(wt, ".zcode/config.json"))
	if _, ok := final["hooks"]; ok {
		t.Errorf("an empty hooks object should be removed, got %v", final["hooks"])
	}
}
