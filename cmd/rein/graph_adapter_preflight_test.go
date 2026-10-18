package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGraphCLIPreflightBindsCurrentInputs(t *testing.T) {
	for _, input := range []string{"matching", "contract", "config", "profile"} {
		t.Run(input, func(t *testing.T) {
			e, path, p := localGraphFixture(t, "touch worker-started", "true")
			switch input {
			case "contract":
				e.c.Scope = append(e.c.Scope, "changed")
				if _, err := e.c.Save(); err != nil {
					t.Fatal(err)
				}
			case "config":
				raw, err := os.ReadFile(e.cfgPath)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(e.cfgPath, append(raw, '\n'), 0600); err != nil {
					t.Fatal(err)
				}
			case "profile":
				var profile map[string]any
				if err := json.Unmarshal(p.Tasks[0].Profile, &profile); err != nil {
					t.Fatal(err)
				}
				profile["contract_hash"] = strings.Repeat("0", 64)
				p.Tasks[0].Profile, _ = json.Marshal(profile)
				raw, err := json.Marshal(p)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			want := 2
			commands := []string{"validate", "run"}
			if input == "matching" {
				want = 0
				commands = []string{"validate"}
			}
			for _, command := range commands {
				out, code := capture(t, func() int {
					return cmdSupervisor([]string{command, "--plan", path})
				})
				if code != want {
					t.Errorf("%s exit=%d, want %d: %s", command, code, want, out)
				}
			}
			for _, file := range []string{filepath.Join(e.wt, "worker-started"), filepath.Join(e.root, "contracts", "routes", "decisions", "task.json")} {
				if _, err := os.Stat(file); !os.IsNotExist(err) {
					t.Errorf("preflight created execution state %s: %v", file, err)
				}
			}
		})
	}
}
