package main

import (
	"context"
	"strings"
	"testing"

	"github.com/voravitl/rein/internal/orca"
)

type cmdMockRunner struct {
	handlers map[string]func(args []string) ([]byte, error)
}

func (m *cmdMockRunner) Run(ctx context.Context, dir string, name string, args ...string) ([]byte, error) {
	key := name + " " + strings.Join(args, " ")
	for pattern, h := range m.handlers {
		if strings.HasPrefix(key, pattern) {
			return h(args)
		}
	}
	return []byte(`{"ok": true, "result": {}}`), nil
}

func TestCmdOrcaCall_Schema(t *testing.T) {
	code := cmdOrca([]string{"call", "--schema"})
	if code != 0 {
		t.Fatalf("expected code 0 for call --schema, got %d", code)
	}

	codeSchema := cmdOrca([]string{"schema"})
	if codeSchema != 0 {
		t.Fatalf("expected code 0 for schema, got %d", codeSchema)
	}
}

func TestCmdOrcaCall_Status(t *testing.T) {
	mock := &cmdMockRunner{
		handlers: map[string]func(args []string) ([]byte, error){
			"orca status": func(args []string) ([]byte, error) {
				return []byte(`{"ok": true, "result": {"app": {"running": true}, "runtime": {"state": "ready"}}}`), nil
			},
		},
	}
	orca.SetRunner(mock)
	defer orca.SetRunner(nil)

	code := cmdOrca([]string{"call", "status"})
	if code != 0 {
		t.Fatalf("expected code 0 for call status, got %d", code)
	}
}

func TestCmdOrcaCall_RawJSON(t *testing.T) {
	mock := &cmdMockRunner{
		handlers: map[string]func(args []string) ([]byte, error){
			"orca status": func(args []string) ([]byte, error) {
				return []byte(`{"ok": true, "result": {"app": {"running": true}, "runtime": {"state": "ready"}}}`), nil
			},
		},
	}
	orca.SetRunner(mock)
	defer orca.SetRunner(nil)

	code := cmdOrca([]string{"call", `{"action": "status"}`})
	if code != 0 {
		t.Fatalf("expected code 0 for raw json, got %d", code)
	}
}

func TestCmdOrcaCall_MissingAction(t *testing.T) {
	code := cmdOrca([]string{"call"})
	if code != 2 {
		t.Fatalf("expected code 2 for missing action, got %d", code)
	}
}
