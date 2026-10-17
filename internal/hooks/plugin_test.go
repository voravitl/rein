package hooks

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPluginHookCommands(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("plugin command hooks require sh")
	}
	var config struct {
		Hooks map[string][]struct {
			Hooks []struct{ Command string }
		}
	}
	b, err := os.ReadFile(filepath.Join("..", "..", "hooks", "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &config); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"plugin", "plugin data"} {
		t.Run(name, func(t *testing.T) {
			data := filepath.Join(t.TempDir(), name)
			if err := os.MkdirAll(filepath.Join(data, "bin"), 0o755); err != nil {
				t.Fatal(err)
			}
			// Command hooks execute only the command string; separate args are ignored.
			if err := os.WriteFile(filepath.Join(data, "bin", "rein"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CLAUDE_PLUGIN_DATA", data)
			for _, event := range []string{"PreToolUse", "PostToolUse", "Stop"} {
				t.Run(event, func(t *testing.T) {
					groups := config.Hooks[event]
					if len(groups) == 0 {
						t.Fatal("missing hook group")
					}
					for _, group := range groups {
						if len(group.Hooks) == 0 {
							t.Fatal("missing hook command")
						}
						for _, hook := range group.Hooks {
							out, err := exec.Command("sh", "-c", hook.Command).CombinedOutput()
							if err != nil || strings.TrimSpace(string(out)) != "hook" {
								t.Fatalf("command = %q: output %q, error %v; want hook subcommand", hook.Command, out, err)
							}
						}
					}
				})
			}
		})
	}
}
