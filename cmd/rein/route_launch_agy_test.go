package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/voravitl/rein/internal/ledger"
	"github.com/voravitl/rein/internal/supervisor"
)

func TestGraphAGYPartialPrintTimeoutIsNotSuccessfulExit(t *testing.T) {
	for _, tc := range []struct {
		name, worker string
		ok           bool
	}{
		{"native-stderr-timeout", `printf 'partial answer\n'; printf '[agy] print timeout after 40s with turn in progress; returning partial output\n' >&2; exit 0`, false},
		{"split-stderr-timeout", `printf '[agy] print timeout after ' >&2; printf '1m0s with turn in progress; returning partial output\n' >&2; exit 0`, false},
		{"same-text-on-stdout", `printf '[agy] print timeout after 40s with turn in progress; returning partial output\n'; exit 0`, true},
		{"quoted-stderr-text", `printf 'Example: [agy] print timeout after 40s with turn in progress; returning partial output\n' >&2; exit 0`, true},
		{"normal", `printf 'complete answer\n'; printf '[agy] print completed\n' >&2; exit 0`, true},
		{"stderr-observation-overflow", `printf '[agy] diagnostic\n' >&2; dd if=/dev/zero bs=1048576 count=1 2>/dev/null | tr '\000' x >&2; exit 0`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, path, _ := localGraphFixture(t, tc.worker, "true")
			out, code := capture(t, func() int { return cmdSupervisor([]string{"run", "--plan", path}) })
			if code != 0 {
				t.Fatalf("supervisor command exit=%d output=%s", code, out)
			}
			var graph supervisor.GraphResult
			if err := json.Unmarshal([]byte(out), &graph); err != nil {
				t.Fatal(err)
			}
			if (len(graph.Completed) == 1) != tc.ok {
				t.Errorf("graph completion=%v want success=%t", graph.Completed, tc.ok)
			}
			rows, err := ledger.LoadStrict("")
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, row := range rows {
				if row.Kind != "routing_exit" || row.Run != "graph" {
					continue
				}
				found = true
				if row.OK == nil || *row.OK != tc.ok {
					t.Errorf("ledger exit.OK=%v want %t", row.OK, tc.ok)
				}
				body, err := os.ReadFile(graphEvidencePath(row.Attempt, "execution"))
				if err != nil {
					t.Fatal(err)
				}
				var execution graphExecution
				if err := json.Unmarshal(body, &execution); err != nil {
					t.Fatal(err)
				}
				if (execution.ExitCode == 0) != tc.ok || !execution.Gone || !strings.Contains(execution.Output, "[agy]") {
					t.Errorf("execution=%+v want success=%t with original output and gone proof", execution, tc.ok)
				}
			}
			if !found {
				t.Fatal("missing attributed routing exit")
			}
		})
	}
}

func TestAGYPrintTimeoutDiagnostic(t *testing.T) {
	const before = "[agy] print timeout after "
	const after = " with turn in progress; returning partial output"
	for _, tc := range []struct {
		text string
		want bool
	}{
		{before + "40s" + after + "\r\n", true},
		{before + "1m0.5s" + after, true},
		{before + "not-a-duration" + after, false},
		{before + after, false},
		{"Example: " + before + "40s" + after, false},
		{before + "40s" + after + " (example)", false},
	} {
		if got := agyPrintTimedOut(tc.text); got != tc.want {
			t.Errorf("agyPrintTimedOut(%q)=%t want %t", tc.text, got, tc.want)
		}
	}
}
