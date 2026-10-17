package orca

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type hardeningCallRunner struct {
	t        *testing.T
	commands []string
	outputs  map[string]string
}

func (r *hardeningCallRunner) Run(_ context.Context, _ string, name string, args ...string) ([]byte, error) {
	command := name + " " + strings.Join(args, " ")
	r.commands = append(r.commands, command)
	out, ok := r.outputs[command]
	if !ok {
		r.t.Errorf("unexpected command: %s", command)
		return nil, fmt.Errorf("unexpected command: %s", command)
	}
	return []byte(out), nil
}
func TestCallHardeningProtectedTargets(t *testing.T) {
	t.Setenv("ORCA_COORD", "coord")
	for _, action := range []string{"close", "send", "read"} {
		for _, terminal := range []string{"", "coord"} {
			t.Run(action+"/"+terminal, func(t *testing.T) {
				r := &hardeningCallRunner{t: t}
				SetRunner(r)
				defer SetRunner(nil)
				payload, _ := json.Marshal(CallRequest{Action: " " + strings.ToUpper(action) + " ", Args: map[string]interface{}{"terminal": terminal}})
				resp, err := CallFromJSON(context.Background(), payload)
				if err == nil || resp == nil || resp.OK {
					t.Fatalf("expected protected target refusal: %+v, %v", resp, err)
				}
				if len(r.commands) != 0 {
					t.Fatalf("guard ran commands: %v", r.commands)
				}
			})
		}
	}
}
func TestCallHardeningStatusTarget(t *testing.T) {
	for _, terminal := range []string{"", "worker"} {
		t.Run(terminal, func(t *testing.T) {
			t.Setenv("ORCA_COORD", "coord")
			target := terminal
			if target == "" {
				target = "coord"
			}
			r := &hardeningCallRunner{t: t, outputs: map[string]string{"orca status --json": `{"ok":true,"result":{"app":{"running":true}}}`, "orca terminal show --terminal " + target + " --json": `{"ok":true,"result":{"terminal":{"handle":"` + target + `"}}}`}}
			SetRunner(r)
			defer SetRunner(nil)
			resp, err := Call(context.Background(), CallRequest{Action: "status", Args: map[string]interface{}{"terminal": terminal}})
			if err != nil || !resp.OK || len(r.commands) != 2 {
				t.Fatalf("status target not queried: %+v, %v, %v", resp, err, r.commands)
			}
		})
	}
}

func TestCallHardeningCoordinatorFallback(t *testing.T) {
	t.Setenv("ORCA_COORD", "coord")
	for _, action := range []string{"wait", "drain"} {
		t.Run(action, func(t *testing.T) {
			command := "orca orchestration check --terminal coord --wait --types worker_done,escalation,question --timeout-ms 60000 --json"
			output := `{"ok":true,"result":{"messages":[{"type":"worker_done"}]}}`
			if action == "drain" {
				command = "orca orchestration check --terminal coord --json"
				output = `{"ok":true,"result":{"messages":[]}}`
			}
			r := &hardeningCallRunner{t: t, outputs: map[string]string{command: output}}
			SetRunner(r)
			defer SetRunner(nil)
			resp, err := Call(context.Background(), CallRequest{Action: action})
			if err != nil || !resp.OK || len(r.commands) != 1 {
				t.Fatalf("fallback failed: %+v %v commands=%v", resp, err, r.commands)
			}
		})
	}
}

func TestCallHardeningCloseMissingWorktree(t *testing.T) {
	r := &hardeningCallRunner{t: t}
	SetRunner(r)
	defer SetRunner(nil)
	resp, err := Call(context.Background(), CallRequest{Action: "close", Args: map[string]interface{}{"terminal": "worker"}})
	if !errors.Is(err, ErrInvalidRequest) || resp.OK || len(r.commands) != 0 {
		t.Fatalf("invalid close: %+v %v commands=%v", resp, err, r.commands)
	}
}
