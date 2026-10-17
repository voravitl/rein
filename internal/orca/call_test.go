package orca

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestCallMissingAction(t *testing.T) {
	resp, err := Call(context.Background(), CallRequest{})
	if err == nil {
		t.Fatal("expected error on missing action, got nil")
	}
	if resp.OK {
		t.Fatal("expected ok=false")
	}
}

func TestCallUnknownAction(t *testing.T) {
	resp, err := Call(context.Background(), CallRequest{Action: "foo"})
	if err == nil {
		t.Fatal("expected error on unknown action, got nil")
	}
	if resp.OK {
		t.Fatal("expected ok=false")
	}
	if !strings.Contains(resp.Error, "unknown action") {
		t.Fatalf("unexpected error message: %s", resp.Error)
	}
}

func TestCallStatus(t *testing.T) {
	fake := &mockRunner{
		handlers: map[string]func(args []string) ([]byte, error){
			"orca status": func(args []string) ([]byte, error) {
				return []byte(`[1015/081047:ERROR:mac.cc] noise
{
  "id": "local-status",
  "ok": true,
  "result": {
    "app": { "running": true, "pid": 12345 },
    "runtime": { "state": "ready", "reachable": true, "runtimeId": "run-abc" }
  }
}`), nil
			},
		},
	}
	SetRunner(fake)
	defer SetRunner(nil)

	resp, err := Call(context.Background(), CallRequest{Action: "status"})
	if err != nil {
		t.Fatalf("Call(status) unexpected error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected ok=true, got err: %s", resp.Error)
	}

	res, ok := resp.Result.(*StatusResult)
	if !ok {
		t.Fatalf("expected *StatusResult, got %T", resp.Result)
	}
	if !res.Running || res.State != "ready" || !res.Reachable || res.AppPID != 12345 {
		t.Fatalf("unexpected StatusResult: %+v", res)
	}
}

func TestCallSendAndRead(t *testing.T) {
	fake := &mockRunner{
		handlers: map[string]func(args []string) ([]byte, error){
			"orca terminal send": func(args []string) ([]byte, error) {
				return []byte(`{"ok": true, "result": {"sent": true}}`), nil
			},
			"orca terminal read": func(args []string) ([]byte, error) {
				return []byte(`{"ok": true, "result": {"content": "test output\n"}}`), nil
			},
		},
	}
	SetRunner(fake)
	defer SetRunner(nil)

	// Test Send
	sendResp, err := Call(context.Background(), CallRequest{
		Action: "send",
		Args: map[string]interface{}{
			"terminal": "term_42",
			"text":     "echo test",
			"enter":    true,
		},
	})
	if err != nil {
		t.Fatalf("Call(send) unexpected error: %v", err)
	}
	if !sendResp.OK {
		t.Fatalf("expected sendResp.OK=true, got error: %s", sendResp.Error)
	}

	// Test Read
	readResp, err := Call(context.Background(), CallRequest{
		Action: "read",
		Args: map[string]interface{}{
			"terminal": "term_42",
			"cursor":   10,
			"limit":    20,
		},
	})
	if err != nil {
		t.Fatalf("Call(read) unexpected error: %v", err)
	}
	if !readResp.OK {
		t.Fatalf("expected readResp.OK=true, got error: %s", readResp.Error)
	}
}

func TestCallTerminalsAndWorktrees(t *testing.T) {
	fake := &mockRunner{
		handlers: map[string]func(args []string) ([]byte, error){
			"orca terminal list": func(args []string) ([]byte, error) {
				return []byte(`{"ok": true, "result": {"terminals": []}}`), nil
			},
			"orca worktree ps": func(args []string) ([]byte, error) {
				return []byte(`{"ok": true, "result": {"worktrees": []}}`), nil
			},
		},
	}
	SetRunner(fake)
	defer SetRunner(nil)

	termResp, err := Call(context.Background(), CallRequest{
		Action: "terminals",
		Args: map[string]interface{}{
			"worktree": "path:/wt/test",
			"limit":    5,
		},
	})
	if err != nil {
		t.Fatalf("Call(terminals) error: %v", err)
	}
	if !termResp.OK {
		t.Fatalf("expected termResp.OK=true")
	}

	wtResp, err := Call(context.Background(), CallRequest{
		Action: "worktrees",
		Args: map[string]interface{}{
			"limit": 10,
		},
	})
	if err != nil {
		t.Fatalf("Call(worktrees) error: %v", err)
	}
	if !wtResp.OK {
		t.Fatalf("expected wtResp.OK=true")
	}
}

func TestCallFromJSON(t *testing.T) {
	fake := &mockRunner{
		handlers: map[string]func(args []string) ([]byte, error){
			"orca status": func(args []string) ([]byte, error) {
				return []byte(`{"ok": true, "result": {"app": {"running": true}, "runtime": {"state": "ready"}}}`), nil
			},
		},
	}
	SetRunner(fake)
	defer SetRunner(nil)

	jsonPayload := []byte(`{"action": "status"}`)
	resp, err := CallFromJSON(context.Background(), jsonPayload)
	if err != nil {
		t.Fatalf("CallFromJSON error: %v", err)
	}
	if !resp.OK || resp.Action != "status" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestToolSchema(t *testing.T) {
	schema := ToolSchema()
	if schema["name"] != "orca_call" {
		t.Fatalf("expected schema name orca_call, got %v", schema["name"])
	}

	rawJSON := ToolSchemaJSON()
	var roundTrip map[string]interface{}
	if err := json.Unmarshal(rawJSON, &roundTrip); err != nil {
		t.Fatalf("ToolSchemaJSON emitted invalid JSON: %v", err)
	}
	if roundTrip["name"] != "orca_call" {
		t.Fatalf("mismatched roundtrip name: %v", roundTrip["name"])
	}
}

func TestCallStructuredCloseDispatchOnly(t *testing.T) {
	t.Setenv("ORCA_COORD", "coordinator")
	root := controllerRepo(t, false)
	SetRunner(launchRunnerFunc(func(_ context.Context, _ string, name string, args ...string) ([]byte, error) {
		t.Fatalf("check-only absent worktree must not call %s %v", name, args)
		return nil, nil
	}))
	defer SetRunner(nil)
	response, err := Call(context.Background(), CallRequest{Action: "close", Args: map[string]interface{}{"dispatch": "dispatch-structured", "worktree": root + "/absent", "check_only": true}})
	if err != nil || !response.OK {
		t.Fatalf("dispatch-only close: %+v %v", response, err)
	}
	for _, action := range []string{"close", "send", "read"} {
		args := map[string]interface{}{"worktree": "/absent", "dispatch": "dispatch-structured"}
		if action == "close" {
			delete(args, "dispatch")
		}
		response, err := Call(context.Background(), CallRequest{Action: action, Args: args})
		if err == nil || response.OK {
			t.Fatalf("missing identity accepted for %s", action)
		}
	}
	response, err = Call(context.Background(), CallRequest{Action: "close", Args: map[string]interface{}{"terminal": "coordinator", "dispatch": "dispatch-structured", "worktree": "/absent", "check_only": true}})
	if err == nil || response.OK {
		t.Fatal("coordinator terminal accepted with dispatch")
	}
}
