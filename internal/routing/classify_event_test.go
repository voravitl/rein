package routing

import (
	"testing"

	"github.com/voravitl/rein/internal/providers"
)

func TestClassifyProbe(t *testing.T) {
	tests := []struct {
		name   string
		result providers.Result
		want   ErrorClass
	}{
		{"available is not an error", providers.Result{Name: "a", State: "up"}, ClassNoError},
		{"matched quota signal is exhaustion", providers.Result{Name: "a", State: "quota", Detail: "[quota] quota"}, ClassQuotaExhausted},
		{"bare 429 is rate limit, not quota", providers.Result{Name: "a", State: "down", Detail: "Error: 429 Too Many Requests"}, ClassRateLimit},
		{"rate limit words are rate limit", providers.Result{Name: "a", State: "down", Detail: "rate limit exceeded, retry later"}, ClassRateLimit},
		{"single timeout is transient", providers.Result{Name: "a", State: "down", Detail: "timeout 1s"}, ClassTransientError},
		{"double timeout is transient", providers.Result{Name: "a", State: "down", Detail: "timeout 1s (twice)"}, ClassTransientError},
		{"missing CLI is unknown, not quota", providers.Result{Name: "a", State: "down", Detail: "CLI not installed"}, ClassUnknownError},
		{"no probe command is unknown", providers.Result{Name: "a", State: "down", Detail: "no probe command"}, ClassUnknownError},
		{"unknown provider is unknown", providers.Result{Name: "a", State: "down", Detail: "unknown provider"}, ClassUnknownError},
		{"login failure is auth", providers.Result{Name: "a", State: "down", Detail: "Error: not logged in"}, ClassAuthRequired},
		{"401 is auth", providers.Result{Name: "a", State: "down", Detail: "request failed: 401 Unauthorized"}, ClassAuthRequired},
		{"context exhaustion is context limit", providers.Result{Name: "a", State: "down", Detail: "context_length_exceeded: max 200k tokens"}, ClassContextLimit},
		{"unparseable output stays unknown", providers.Result{Name: "a", State: "down", Detail: "something odd happened"}, ClassUnknownError},
		{"empty detail stays unknown", providers.Result{Name: "a", State: "down"}, ClassUnknownError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyProbe(tt.result); got != tt.want {
				t.Errorf("ClassifyProbe(%+v) = %q, want %q", tt.result, got, tt.want)
			}
		})
	}
}

func TestClassifyWorkerExit(t *testing.T) {
	tests := []struct {
		name        string
		exitCode    int
		orcaOutcome string
		want        ErrorClass
	}{
		{"clean worker_done is done", 0, "worker_done", ClassWorkerDone},
		{"failed worker_done is process exit", 1, "worker_done", ClassProcessExit},
		{"clean exit without outcome is done", 0, "", ClassWorkerDone},
		{"nonzero exit without outcome is process exit", 2, "", ClassProcessExit},
		{"question waits for a human", 0, "question", ClassApprovalRequired},
		{"ask waits for a human", 0, "ask", ClassApprovalRequired},
		{"escalation is unknown, never guessed", 0, "escalation", ClassUnknownError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyWorkerExit(tt.exitCode, tt.orcaOutcome); got != tt.want {
				t.Errorf("ClassifyWorkerExit(%d, %q) = %q, want %q", tt.exitCode, tt.orcaOutcome, got, tt.want)
			}
		})
	}
}
