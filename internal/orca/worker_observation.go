package orca

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ValidateWorkerObservation confirms a ready live dispatch, never worker completion or remote termination.
func ValidateWorkerObservation(raw []byte, runID, taskID, dispatchID, handle string) (string, error) {
	for _, id := range []string{runID, taskID, dispatchID, handle} {
		if id == "" || strings.TrimSpace(id) != id {
			return "", errors.New("worker observation requires exact run/task/dispatch/handle")
		}
	}
	var observation struct {
		OK     bool            `json:"ok"`
		Error  json.RawMessage `json:"error"`
		Result struct {
			Dispatch struct {
				ID       string `json:"id"`
				Task     string `json:"taskId"`
				Run      string `json:"runId"`
				Assignee string `json:"assigneeHandle"`
			} `json:"dispatch"`
			Worker struct {
				Dispatch string `json:"dispatchId"`
				State    string `json:"state"`
				Handle   string `json:"agentTerminalHandle"`
			} `json:"worker"`
			Observation struct {
				Exact       bool   `json:"exactWorker"`
				Status      string `json:"status"`
				Addressable *bool  `json:"addressable"`
			} `json:"observation"`
			Terminal *struct {
				Handle string `json:"handle"`
			} `json:"terminal"`
			Projection *struct {
				ID       string `json:"id"`
				Dispatch string `json:"dispatchId"`
				Task     string `json:"taskId"`
				Run      string `json:"runId"`
				Liveness struct {
					Verdict string `json:"verdict"`
				} `json:"liveness"`
			} `json:"projection"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &observation); err != nil {
		return "", fmt.Errorf("parse worker dispatch observation: %w", err)
	}
	got := observation.Result
	if !observation.OK || (len(observation.Error) != 0 && string(observation.Error) != "null") || got.Dispatch.ID != dispatchID || got.Dispatch.Task != taskID || got.Dispatch.Run != runID || got.Dispatch.Assignee != handle || got.Worker.Dispatch != dispatchID || got.Worker.Handle != handle || got.Worker.State != "ready" || !got.Observation.Exact || got.Observation.Status != "live" || (got.Observation.Addressable != nil && !*got.Observation.Addressable) {
		return "", fmt.Errorf("worker dispatch observation mismatches exact task/run/handle or is not live supervised ready (task=%s dispatch=%s)", taskID, dispatchID)
	}
	if p := got.Projection; p != nil && (p.ID != dispatchID || p.Dispatch != dispatchID || p.Task != taskID || p.Run != runID || p.Liveness.Verdict == "exited") {
		return "", errors.New("worker projection contradicts the exact live dispatch")
	}
	if got.Terminal != nil {
		if got.Terminal.Handle != handle {
			return "", errors.New("worker terminal handle differs from supervised dispatch")
		}
		return handle, nil
	}
	return "", nil
}

// ValidateWorkerLaunchIdentity checks Orca's effective settings, not requested
// flags. It proves neither the provider account nor model compliance or completion.
func ValidateWorkerLaunchIdentity(raw []byte, agent, model, effort string) error {
	var envelope struct {
		OK     bool `json:"ok"`
		Result struct {
			Worker struct {
				StartOptions struct {
					Launch struct {
						Effective *struct {
							Agent  string `json:"agent"`
							Model  string `json:"model"`
							Effort string `json:"effort"`
						} `json:"effective"`
					} `json:"launch"`
				} `json:"startOptions"`
			} `json:"worker"`
		} `json:"result"`
	}
	if agent == "" || model == "" {
		return errors.New("worker launch verification requires exact agent and model")
	}
	if json.Unmarshal(raw, &envelope) != nil || !envelope.OK {
		return errors.New("worker effective launch settings unavailable")
	}
	effective := envelope.Result.Worker.StartOptions.Launch.Effective
	if effective == nil || effective.Agent != agent || effective.Model != model || effective.Effort != effort {
		return errors.New("worker effective agent/model/effort differs from prepared routing or is unverified")
	}
	return nil
}
