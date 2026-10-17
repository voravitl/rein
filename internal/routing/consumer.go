package routing

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SupervisorBinding is supplied by an opted-in controller from its approved
// task/dispatch mapping. A message never supplies its own Rein attempt identity.
type SupervisorBinding struct {
	Enabled        bool
	Run            string
	Task           string
	Attempt        string
	CurrentAttempt string
	OrcaTask       string
	Dispatch       string
	Source         string
	Version        string
	ObservedAt     time.Time
}

// ConsumeSupervisorBatch records a single bound delivery before acknowledging
// it. Calling this function does not start a supervisor or claim control of a run.
// The caller must prove sole controller ownership before setting Enabled.
// ObservedAt is a local observation time, not an authoritative worker sequence;
// this consumer does not prove event ordering or authorize recovery/dispatch.
// shortcut: accepts only the check envelope/payload shape used by ocloop2.py;
// validate a live schema before wiring this consumer to a live controller.
func ConsumeSupervisorBatch(binding SupervisorBinding, raw []byte, ack func(string) error) (TaskWait, error) {
	if !binding.Enabled {
		return TaskWait{}, errors.New("supervised consumer is not enabled")
	}
	for _, s := range []string{binding.Run, binding.Task, binding.Attempt, binding.CurrentAttempt, binding.OrcaTask, binding.Dispatch, binding.Source} {
		if strings.TrimSpace(s) == "" {
			return TaskWait{}, errors.New("supervisor binding needs run, task, attempt, current attempt, Orca task, dispatch and source")
		}
	}
	if binding.ObservedAt.IsZero() || ack == nil {
		return TaskWait{}, errors.New("supervisor consumer needs observed time and acknowledgement function")
	}
	var envelope struct {
		OK     *bool           `json:"ok"`
		Error  json.RawMessage `json:"error"`
		Result *struct {
			DeliveryID string `json:"deliveryId"`
			Messages   []struct {
				ID      string          `json:"id"`
				Type    string          `json:"type"`
				Payload json.RawMessage `json:"payload"`
			} `json:"messages"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return TaskWait{}, fmt.Errorf("decode supervisor batch: %w", err)
	}
	if envelope.OK != nil && !*envelope.OK {
		return TaskWait{}, errors.New("Orca check explicitly refused")
	}
	if len(envelope.Error) != 0 && string(envelope.Error) != "null" {
		return TaskWait{}, errors.New("Orca check returned an error")
	}
	if envelope.Result == nil || strings.TrimSpace(envelope.Result.DeliveryID) == "" || len(envelope.Result.Messages) == 0 {
		return TaskWait{}, errors.New("supervisor batch needs deliveryId and messages")
	}
	// Validate the whole delivery before any event is persisted or acknowledged.
	events := make([]Event, 0, len(envelope.Result.Messages))
	seen := make(map[string]bool)
	for _, m := range envelope.Result.Messages {
		if strings.TrimSpace(m.ID) == "" || seen[m.ID] {
			return TaskWait{}, errors.New("supervisor batch needs unique message ids")
		}
		seen[m.ID] = true
		switch m.Type {
		case "worker_done", "escalation", "question":
		default:
			return TaskWait{}, fmt.Errorf("unsupported supervisor message type %q", m.Type)
		}
		payload := m.Payload
		var encoded string
		if len(payload) > 0 && payload[0] == '"' {
			if err := json.Unmarshal(payload, &encoded); err != nil {
				return TaskWait{}, fmt.Errorf("decode message payload: %w", err)
			}
			payload = []byte(encoded)
		}
		var p struct {
			TaskID     string `json:"taskId"`
			DispatchID string `json:"dispatchId"`
			Outcome    string `json:"outcome"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return TaskWait{}, fmt.Errorf("decode message payload: %w", err)
		}
		if p.TaskID != binding.OrcaTask || p.DispatchID != binding.Dispatch {
			return TaskWait{}, errors.New("message task/dispatch does not match supervisor binding")
		}
		class := ClassifyWorkerExit(0, m.Type)
		// A reported success clears only the event wait, never the writer lease,
		// acceptance checks or liveness reconciliation. Failed/unknown outcomes stay blocked.
		if m.Type == "worker_done" && p.Outcome != "worker_done" && p.Outcome != "succeeded" {
			class = ClassUnknownError
		}
		if len(events) > 0 && events[0].Class != class {
			return TaskWait{}, errors.New("conflicting batch transitions need authoritative ordering")
		}
		events = append(events, Event{
			EventID: m.ID, Run: binding.Run, Task: binding.Task, Attempt: binding.Attempt,
			Source: binding.Source, Version: binding.Version, At: binding.ObservedAt,
			Kind: KindWorker, Class: class, Dispatch: binding.Dispatch,
			Evidence: "type=" + m.Type + ";outcome=" + p.Outcome,
		})
	}
	for _, event := range events {
		if _, err := Record(event); err != nil {
			return TaskWait{}, fmt.Errorf("persist supervisor batch (not acknowledged): %w", err)
		}
	}
	persisted, err := ReadEvents()
	if err != nil {
		return TaskWait{}, fmt.Errorf("read supervisor history (not acknowledged): %w", err)
	}
	var scoped []Event
	for _, event := range persisted {
		if event.Run == binding.Run {
			scoped = append(scoped, event)
		}
	}
	wait := DeriveWait(binding.Task, binding.CurrentAttempt, scoped)
	// Retrying ACK after a durable duplicate closes the record/ACK crash window.
	if err := ack(envelope.Result.DeliveryID); err != nil {
		return wait, fmt.Errorf("acknowledge supervisor batch: %w", err)
	}
	return wait, nil
}
