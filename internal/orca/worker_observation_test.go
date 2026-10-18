package orca

import (
	"strings"
	"testing"
)

func TestValidateWorkerObservationProjectionCannotReplaceFreshObservation(t *testing.T) {
	const fresh = `{"ok":true,"result":{"dispatch":{"id":"d","taskId":"t","runId":"r","assigneeHandle":"w"},"worker":{"dispatchId":"d","state":"ready","agentTerminalHandle":"w"},"observation":{"exactWorker":true,"status":"live"},"terminal":null}}`
	const projection = `{"id":"d","dispatchId":"d","taskId":"t","runId":"r","liveness":{"verdict":"live"}}`
	withProjection := func(raw, p string) string {
		return strings.Replace(raw, `"terminal":null`, `"projection":`+p+`,"terminal":null`, 1)
	}
	for _, tc := range []struct {
		name, raw string
		ok        bool
	}{
		{"missing-projection", fresh, true},
		{"null-projection", withProjection(fresh, "null"), true},
		{"live-projection", withProjection(fresh, projection), true},
		{"stale-projection", withProjection(fresh, strings.Replace(projection, `"live"`, `"unverifiable"`, 1)), true},
		{"exited-contradiction", withProjection(fresh, strings.Replace(projection, `"live"`, `"exited"`, 1)), false},
		{"foreign-projection-id", withProjection(fresh, strings.Replace(projection, `"id":"d"`, `"id":"other"`, 1)), false},
		{"foreign-projection-dispatch", withProjection(fresh, strings.Replace(projection, `"dispatchId":"d"`, `"dispatchId":"other"`, 1)), false},
		{"foreign-projection-task", withProjection(fresh, strings.Replace(projection, `"taskId":"t"`, `"taskId":"other"`, 1)), false},
		{"foreign-projection-run", withProjection(fresh, strings.Replace(projection, `"runId":"r"`, `"runId":"other"`, 1)), false},
		{"live-projection-with-inexact-observation", withProjection(strings.Replace(fresh, `"exactWorker":true`, `"exactWorker":false`, 1), projection), false},
		{"live-projection-with-nonlive-observation", withProjection(strings.Replace(fresh, `"status":"live"`, `"status":"unverifiable"`, 1), projection), false},
		{"not-addressable", strings.Replace(fresh, `"status":"live"`, `"status":"live","addressable":false`, 1), false},
		{"addressable", strings.Replace(fresh, `"status":"live"`, `"status":"live","addressable":true`, 1), true},
		{"foreign-terminal", strings.Replace(fresh, `"terminal":null`, `"terminal":{"handle":"other"}`, 1), false},
		{"error-contradiction", strings.Replace(fresh, `"ok":true`, `"ok":true,"error":{"message":"failed"}`, 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			terminal, err := ValidateWorkerObservation([]byte(tc.raw), "r", "t", "d", "w")
			if (err == nil) != tc.ok || terminal != "" {
				t.Fatalf("terminal=%q err=%v want success=%t", terminal, err, tc.ok)
			}
		})
	}
}

func TestValidateWorkerLaunchIdentityRequiresEffectiveSettings(t *testing.T) {
	good := `{"ok":true,"result":{"worker":{"startOptions":{"launch":{"effective":{"agent":"antigravity","model":"chosen","effort":"high"}}}}}}`
	for _, tc := range []struct {
		name, raw string
		ok        bool
	}{
		{"exact", good, true},
		{"wrong-agent", strings.Replace(good, `"antigravity"`, `"claude"`, 1), false},
		{"wrong-model", strings.Replace(good, `"chosen"`, `"fallback"`, 1), false},
		{"wrong-effort", strings.Replace(good, `"high"`, `"low"`, 1), false},
		{"requested-only", strings.Replace(good, `"effective"`, `"requested"`, 1), false},
		{"missing", `{"ok":true,"result":{"worker":{}}}`, false},
		{"bad-envelope", strings.Replace(good, `"ok":true`, `"ok":false`, 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateWorkerLaunchIdentity([]byte(tc.raw), "antigravity", "chosen", "high")
			if (err == nil) != tc.ok {
				t.Fatalf("%v", err)
			}
		})
	}
}
