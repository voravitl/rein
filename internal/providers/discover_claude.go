package providers

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
)

func collectClaude(ctx context.Context, argv []string, dir string, inv *Inventory) {
	args := append(slices.Clone(argv), "--setting-sources", "", "--settings", `{"disableAllHooks":true}`, "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--no-session-persistence", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--print")
	r, err := startRPC(args, dir)
	if err != nil {
		inv.fail(InvFailed, "cannot start Claude metadata session")
		return
	}
	defer r.close(ctx)
	const requestID = "rein-model-metadata"
	if err = r.send(map[string]any{"type": "control_request", "request_id": requestID, "request": map[string]string{"subtype": "initialize"}}); err != nil {
		inv.fail(InvFailed, "cannot request Claude metadata")
		return
	}
	// EOF finishes metadata-only print sessions. No user message is ever sent.
	_ = r.in.Close()
	for n := 0; n <= maxChatter; n++ {
		line, err := r.next(ctx)
		if err != nil {
			inv.failErr(ctx, err)
			return
		}
		var message struct {
			Type     string `json:"type"`
			Response struct {
				Subtype   string `json:"subtype"`
				RequestID string `json:"request_id"`
				Response  struct {
					Models json.RawMessage `json:"models"`
				} `json:"response"`
			} `json:"response"`
		}
		if json.Unmarshal(line, &message) != nil {
			inv.fail(InvFailed, "invalid Claude control schema")
			return
		}
		if message.Type == "control_request" || message.Type == "assistant" || message.Type == "user" {
			inv.fail(InvFailed, "unexpected Claude metadata protocol message")
			return
		}
		if message.Type != "control_response" {
			continue
		}
		if message.Response.RequestID != requestID || message.Response.Subtype != "success" {
			inv.fail(InvFailed, "Claude initialization response mismatch")
			return
		}
		models, err := parseClaudeModels(message.Response.Response.Models)
		if err != nil {
			inv.fail(InvFailed, "invalid or incomplete Claude resolved model catalog")
			return
		}
		inv.Models, inv.Status = models, InvComplete
		// Hash only normalized catalog data, never initialize account/session fields.
		raw, _ := json.Marshal(models)
		inv.SourceHash = sha(raw)
		inv.note("client-resolved model catalog only; entitlement, quota, billing and context limits unverified")
		return
	}
	inv.fail(InvFailed, "too much Claude metadata chatter")
}

func parseClaudeModels(raw json.RawMessage) ([]ModelEntry, error) {
	var rows []struct {
		Value          string   `json:"value"`
		Resolved       string   `json:"resolvedModel"`
		SupportsEffort *bool    `json:"supportsEffort"`
		Efforts        []string `json:"supportedEffortLevels"`
	}
	if json.Unmarshal(raw, &rows) != nil || len(rows) == 0 {
		return nil, errSchema
	}
	seen := map[string]ModelEntry{}
	for _, row := range rows {
		if !validID(row.Value) || !validID(row.Resolved) {
			return nil, errSchema
		}
		switch row.Resolved {
		case "default", "sonnet", "opus", "haiku", "fable", "opusplan", "auto":
			return nil, errSchema
		}
		efforts := slices.Clone(row.Efforts)
		sort.Strings(efforts)
		for i, e := range efforts {
			if !validID(e) || (i > 0 && efforts[i-1] == e) {
				return nil, errSchema
			}
		}
		if (row.SupportsEffort == nil && len(efforts) > 0) || (row.SupportsEffort != nil && *row.SupportsEffort != (len(efforts) > 0)) {
			return nil, errSchema
		}
		supported := row.SupportsEffort
		entry := ModelEntry{ID: row.Resolved, SupportsEffort: supported, Variants: efforts}
		if previous, ok := seen[row.Resolved]; ok {
			if (previous.SupportsEffort == nil) != (supported == nil) || (supported != nil && previous.SupportsEffort != nil && *previous.SupportsEffort != *supported) || !slices.Equal(previous.Variants, efforts) {
				return nil, errSchema
			}
		}
		seen[row.Resolved] = entry
	}
	out := make([]ModelEntry, 0, len(seen))
	for _, entry := range seen {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
