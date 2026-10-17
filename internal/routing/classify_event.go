package routing

// Worker-event error classification (Phase B slice B2). Structured signals win;
// bounded text parsing is second; anything unsure stays UNKNOWN_ERROR so a wrong
// guess can never trigger a quota failover. In particular a bare 429 with no
// configured quota signal matched is RATE_LIMIT, never QUOTA_EXHAUSTED.
//
// providers.Result is the only structured probe signal: State "quota" means a
// configured quota signal matched the probe output (see probe in
// internal/providers/providers.go), so it is trusted. State "down" carries only
// the last output line (140 chars), so the text rules below stay narrow on purpose.

import (
	"strings"

	"github.com/voravitl/rein/internal/providers"
)

// ErrorClass is one of the nine supervisor error classes. ClassNoError ("")
// means the probe reported the provider available — not an error at all.
type ErrorClass string

const (
	ClassNoError          ErrorClass = ""
	ClassRateLimit        ErrorClass = "RATE_LIMIT"
	ClassQuotaExhausted   ErrorClass = "QUOTA_EXHAUSTED"
	ClassAuthRequired     ErrorClass = "AUTH_REQUIRED"
	ClassContextLimit     ErrorClass = "CONTEXT_LIMIT"
	ClassProcessExit      ErrorClass = "PROCESS_EXIT"
	ClassWorkerDone       ErrorClass = "WORKER_DONE"
	ClassApprovalRequired ErrorClass = "APPROVAL_REQUIRED"
	ClassTransientError   ErrorClass = "TRANSIENT_ERROR"
	ClassUnknownError     ErrorClass = "UNKNOWN_ERROR"
)

// ClassifyProbe maps one probe result to an error class.
func ClassifyProbe(r providers.Result) ErrorClass {
	switch r.State {
	case "up":
		return ClassNoError
	case "quota":
		return ClassQuotaExhausted
	}
	d := strings.ToLower(r.Detail)
	switch {
	case strings.HasPrefix(d, "timeout"):
		return ClassTransientError
	case strings.Contains(d, "cli not installed"),
		strings.Contains(d, "no probe command"),
		d == "unknown provider",
		d == "no output":
		// environment or configuration failure, not a provider verdict: a setup
		// failure is never a product defect and never a quota signal.
		return ClassUnknownError
	case strings.Contains(d, "429") ||
		strings.Contains(d, "rate limit") ||
		strings.Contains(d, "rate_limit") ||
		strings.Contains(d, "too many requests"):
		// a bare 429 with no configured quota signal matched: rate limit, not
		// quota exhaustion (which needs an explicit clear, not a retry).
		return ClassRateLimit
	case strings.Contains(d, "not logged in") ||
		strings.Contains(d, "unauthorized") ||
		strings.Contains(d, "unauthorised") ||
		strings.Contains(d, "401") ||
		strings.Contains(d, "invalid api key") ||
		strings.Contains(d, "invalid_api_key") ||
		strings.Contains(d, "authentication") ||
		strings.Contains(d, "auth required"):
		return ClassAuthRequired
	case strings.Contains(d, "context_length_exceeded") ||
		strings.Contains(d, "maximum context") ||
		strings.Contains(d, "context limit") ||
		strings.Contains(d, "context window"):
		return ClassContextLimit
	default:
		return ClassUnknownError
	}
}

// ClassifyWorkerExit maps a finished worker to an error class. orcaOutcome is
// the Orca message type that reported the finish ("worker_done", "question",
// "escalation", "" when unknown); exitCode is the launcher/provider process
// exit code (0 = clean).
func ClassifyWorkerExit(exitCode int, orcaOutcome string) ErrorClass {
	switch strings.ToLower(strings.TrimSpace(orcaOutcome)) {
	case "question", "ask":
		// a worker parked on a prompt only a human can answer is waiting, not failed.
		return ClassApprovalRequired
	case "worker_done":
		if exitCode == 0 {
			return ClassWorkerDone
		}
		return ClassProcessExit
	case "escalation":
		return ClassUnknownError
	}
	if exitCode == 0 {
		return ClassWorkerDone
	}
	return ClassProcessExit
}
