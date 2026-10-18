package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestClaudeMetadataProcess(t *testing.T) {
	fixture := os.Getenv("REIN_CLAUDE_FIXTURE")
	if fixture == "" {
		return
	}
	if slices.Contains(os.Args, "--version") {
		fmt.Println("2.1.302")
		os.Exit(0)
	}
	for _, flag := range []string{"--setting-sources", "--settings", "--strict-mcp-config", "--no-session-persistence", "--input-format", "--output-format"} {
		if !slices.Contains(os.Args, flag) {
			os.Exit(10)
		}
	}
	input, _ := io.ReadAll(os.Stdin)
	_ = os.WriteFile(os.Getenv("REIN_CLAUDE_REQUEST"), input, 0600)
	b, _ := os.ReadFile(fixture)
	fmt.Println(string(b))
	os.Exit(0)
}

func TestClaudeMetadataDiscoveryResolvedCatalogAndPrivacy(t *testing.T) {
	dir := t.TempDir()
	fixture := filepath.Join(dir, "fixture")
	request := filepath.Join(dir, "request")
	t.Setenv("REIN_CLAUDE_FIXTURE", fixture)
	t.Setenv("REIN_CLAUDE_REQUEST", request)
	models := `[{"value":"default","resolvedModel":"claude-sonnet-5-5","supportsEffort":true,"supportedEffortLevels":["high","low"]},{"value":"sonnet","resolvedModel":"claude-sonnet-5-5","supportsEffort":true,"supportedEffortLevels":["low","high"]},{"value":"claude-haiku-fixed","resolvedModel":"claude-haiku-fixed"}]`
	good := `{"type":"control_response","response":{"subtype":"success","request_id":"rein-model-metadata","response":{"account":{"private":"` + secret + `"},"models":` + models + `}}}`
	cfg := &Config{Discovery: map[string]DiscoverySpec{"claude": {Command: []string{os.Args[0], "-test.run=^TestClaudeMetadataProcess$", "--"}}}}
	var catalogHash string
	for _, tc := range []struct {
		name, raw string
		ok        bool
	}{
		{"valid", good, true},
		{"other-account", strings.Replace(good, secret, "other-private-account", 1), true},
		{"no-resolved-id", strings.Replace(good, `"resolvedModel":"claude-haiku-fixed"`, `"resolvedModel":""`, 1), false},
		{"wrong-request", strings.Replace(good, "rein-model-metadata", "foreign", 1), false},
		{"server-request", `{"type":"control_request"}`, false},
		{"missing-models", `{"type":"control_response","response":{"subtype":"success","request_id":"rein-model-metadata","response":{}}}`, false},
		{"failure", strings.Replace(good, `"subtype":"success"`, `"subtype":"error"`, 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(fixture, []byte(tc.raw), 0600); err != nil {
				t.Fatal(err)
			}
			inv := Discover(context.Background(), cfg, DiscoverOptions{Dir: dir, Timeout: 5 * time.Second, Harnesses: []string{"claude"}})[0]
			if (inv.Status == InvComplete) != tc.ok {
				t.Fatalf("%+v", inv)
			}
			b, _ := json.Marshal(inv)
			if strings.Contains(string(b), secret) {
				t.Fatal("account data leaked")
			}
			sent, err := os.ReadFile(request)
			if err != nil {
				t.Fatal(err)
			}
			var req map[string]any
			if json.Unmarshal(sent, &req) != nil || req["type"] != "control_request" || len(strings.Split(strings.TrimSpace(string(sent)), "\n")) != 1 {
				t.Fatalf("unexpected request %s", sent)
			}
			if tc.ok {
				if inv.Models[0].SupportsEffort != nil {
					t.Fatal("missing effort support must stay unknown")
				}
				if inv.Models[0].ID != "claude-haiku-fixed" || inv.Models[1].ID != "claude-sonnet-5-5" {
					t.Fatal("resolved identity was lost", inv.Models)
				}
				if catalogHash != "" && catalogHash != inv.SourceHash {
					t.Fatal("account-only change affected catalog hash")
				}
				catalogHash = inv.SourceHash
				if inv.Freshness != ClientCatalog || len(inv.Models) != 2 || inv.SourceHash == "" || inv.Models[0].Enabled != nil || inv.Models[0].ContextTokens != 0 {
					t.Fatalf("unsupported inference: %+v", inv)
				}
				if !reflect.DeepEqual(inv.Models[1].Variants, []string{"high", "low"}) {
					t.Fatal(inv.Models)
				}
			} else if len(inv.Models) != 0 {
				t.Fatal("failed catalog exposed models")
			}
		})
	}
}

func TestClaudeCatalogRejectsContradictoryAndAliasResolution(t *testing.T) {
	for _, raw := range []string{
		`[{"value":"default","resolvedModel":"sonnet"}]`,
		`[{"value":"sonnet","resolvedModel":"claude-x","supportsEffort":true}]`,
		`[{"value":"sonnet","resolvedModel":"claude-x","supportedEffortLevels":["high"]}]`,
		`[{"value":"sonnet","resolvedModel":"claude-x"},{"value":"default","resolvedModel":"claude-x","supportsEffort":true,"supportedEffortLevels":["high"]}]`,
	} {
		if _, err := parseClaudeModels([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
