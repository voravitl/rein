package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestGraphInvocationCLIReadOnlyExactInvocation(t *testing.T) {
	id := strings.Repeat("a", 64)
	t.Setenv("REIN_SUPERVISOR_INVOCATION", id)
	_, path, _ := localGraphFixture(t, "printf 'fixed\\n' > src/a.go", "true")
	out, code := capture(t, func() int { return cmdSupervisor([]string{"run", "--plan", path}) })
	if code != 0 {
		t.Fatalf("graph exit=%d output=%s", code, out)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		out, code = capture(t, func() int { return cmdGraphInvocation([]string{"--plan", path, "--invocation", id}) })
		if code != 0 {
			t.Fatalf("read exit=%d output=%s", code, out)
		}
		var result struct {
			Start struct {
				InvocationID string `json:"invocation_id"`
			} `json:"start"`
			Return struct {
				Outcome string `json:"outcome"`
			} `json:"return"`
		}
		if err := json.Unmarshal([]byte(out), &result); err != nil || result.Start.InvocationID != id || result.Return.Outcome != "graph_returned" {
			t.Fatalf("unexpected telemetry %s (%v)", out, err)
		}
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("invocation read changed plan")
	}
	_, code = capture(t, func() int {
		return cmdGraphInvocation([]string{"--plan", path, "--invocation", strings.Repeat("b", 64)})
	})
	if code != 1 {
		t.Fatalf("foreign invocation exit=%d", code)
	}
}

func TestGraphInvocationCLIRequiresExplicitIdentity(t *testing.T) {
	for _, args := range [][]string{nil, {"--plan", "x"}, {"--invocation", "x"}, {"--plan", "x", "--invocation", "x", "extra"}} {
		if _, code := capture(t, func() int { return cmdGraphInvocation(args) }); code != 2 {
			t.Fatalf("args %v exit=%d", args, code)
		}
	}
}
