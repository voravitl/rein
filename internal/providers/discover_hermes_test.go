package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHermesCatalogHelper(t *testing.T) {
	if os.Getenv("REIN_HERMES_CATALOG_HELPER") != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	if log := os.Getenv("REIN_HERMES_CATALOG_METHODS"); log != "" {
		f, _ := os.OpenFile(log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		fmt.Fprintln(f, strings.Join(args, " "))
		f.Close()
	}
	if len(args) == 1 && args[0] == "--version" {
		fmt.Println("Hermes Agent v0.21.5")
		os.Exit(0)
	}
	if len(args) != 2 || args[0] != "model" || args[1] != "--catalog-json" {
		os.Exit(7)
	}
	if os.Getenv("REIN_HERMES_CATALOG_HANG") == "1" {
		time.Sleep(time.Minute)
	}
	if os.Getenv("REIN_HERMES_CATALOG_EXIT") == "1" {
		fmt.Fprintln(os.Stderr, "PRIVATE_ERROR")
		os.Exit(1)
	}
	fmt.Print(os.Getenv("REIN_HERMES_CATALOG_DOCUMENT"))
	os.Exit(0)
}

func hermesCatalogDocument(now time.Time) string {
	return fmt.Sprintf(`{"protocol":"hermes.model_catalog","schema_version":1,"capabilities":["provider_qualified_models","bundled_catalog"],"hermes_version":"0.21.5+local","scope":"bundled_client_catalog","complete":true,"truncated":false,"observed_at":%q,"total_models":2,"models":[{"provider":"anthropic","model_id":"shared-model","launch_model":"anthropic:shared-model","source":"bundled_curated"},{"provider":"openrouter","model_id":"vendor/shared-model:free","launch_model":"openrouter:vendor/shared-model:free","source":"bundled_curated"}],"excluded_providers":[{"provider":"moa","reason":"virtual_provider"}],"account":"PRIVATE_ACCOUNT"}`, now.UTC().Format(time.RFC3339Nano))
}

func hermesCatalogConfig(t *testing.T, raw string) *Config {
	t.Helper()
	t.Setenv("REIN_HERMES_CATALOG_HELPER", "1")
	t.Setenv("REIN_HERMES_CATALOG_DOCUMENT", raw)
	return &Config{Discovery: map[string]DiscoverySpec{"hermes": {Command: []string{os.Args[0], "-test.run=^TestHermesCatalogHelper$", "--"}}}}
}

func TestHermesDiscoveryBundledCatalogIdentityAndPrivacy(t *testing.T) {
	methods := filepath.Join(t.TempDir(), "methods")
	t.Setenv("REIN_HERMES_CATALOG_METHODS", methods)
	inv := one(t, hermesCatalogConfig(t, hermesCatalogDocument(time.Now())), "hermes")
	if inv.Status != InvComplete || inv.Freshness != ClientCatalog || len(inv.Models) != 2 || inv.SourceHash == "" || inv.EvidenceAt != nil {
		t.Fatalf("missing qualified client catalog: %+v", inv)
	}
	if strings.Join(ids(inv), ",") != "anthropic:shared-model,openrouter:vendor/shared-model:free" {
		t.Fatalf("provider identity changed: %+v", inv.Models)
	}
	for _, model := range inv.Models {
		if model.Enabled != nil || model.SupportsEffort != nil || model.ContextTokens != 0 {
			t.Fatalf("unproven capability or entitlement: %+v", model)
		}
	}
	raw, _ := json.Marshal(inv)
	if strings.Contains(string(raw), "PRIVATE_") || !strings.Contains(inv.Reason, "bundled") {
		t.Fatalf("private or missing provenance: %s", raw)
	}
	called, _ := os.ReadFile(methods)
	if string(called) != "--version\nmodel --catalog-json\n" {
		t.Fatalf("unexpected metadata command: %s", called)
	}
}

func TestHermesDiscoveryRejectsFallbackIncompleteAndMismatchedCatalogs(t *testing.T) {
	now := time.Now()
	good := hermesCatalogDocument(now)
	for name, raw := range map[string]string{
		"ACP picker":         `{"availableModels":[{"modelId":"configured-current"}]}`,
		"not JSON":           "PRIVATE_RAW_ERROR",
		"old schema":         strings.Replace(good, `"schema_version":1`, `"schema_version":2`, 1),
		"missing complete":   strings.Replace(good, `"complete":true,`, ``, 1),
		"incomplete":         strings.Replace(good, `"complete":true`, `"complete":false`, 1),
		"truncated":          strings.Replace(good, `"truncated":false`, `"truncated":true`, 1),
		"missing truncation": strings.Replace(good, `"truncated":false,`, ``, 1),
		"wrong count":        strings.Replace(good, `"total_models":2`, `"total_models":3`, 1),
		"config fallback":    strings.Replace(good, `"source":"bundled_curated"`, `"source":"user-config"`, 1),
		"wrong provider":     strings.Replace(good, `"launch_model":"anthropic:shared-model"`, `"launch_model":"openrouter:shared-model"`, 1),
		"unqualified":        strings.Replace(good, `"launch_model":"anthropic:shared-model"`, `"launch_model":"shared-model"`, 1),
		"wrong version":      strings.Replace(good, `"hermes_version":"0.21.5+local"`, `"hermes_version":"0.21.6"`, 1),
		"stale":              hermesCatalogDocument(now.Add(-6 * time.Minute)),
		"future":             hermesCatalogDocument(now.Add(2 * time.Minute)),
	} {
		t.Run(name, func(t *testing.T) {
			inv := one(t, hermesCatalogConfig(t, raw), "hermes")
			if inv.Status != InvFailed || inv.Freshness != FreshnessUnknown || len(inv.Models) != 0 || inv.SourceHash != "" || inv.EvidenceAt != nil {
				t.Fatalf("invalid metadata gained evidence: %+v", inv)
			}
			encoded, _ := json.Marshal(inv)
			if strings.Contains(string(encoded), "PRIVATE_") {
				t.Fatalf("private response leaked: %s", encoded)
			}
		})
	}
}

func TestHermesDiscoveryMetadataFailureDoesNotUsePreviousCatalog(t *testing.T) {
	cfg := hermesCatalogConfig(t, hermesCatalogDocument(time.Now()))
	if inv := one(t, cfg, "hermes"); inv.Status != InvComplete {
		t.Fatal(inv)
	}
	t.Setenv("REIN_HERMES_CATALOG_EXIT", "1")
	if inv := one(t, cfg, "hermes"); inv.Status != InvFailed || len(inv.Models) != 0 || inv.SourceHash != "" || strings.Contains(inv.Reason, "PRIVATE_") {
		t.Fatalf("metadata failure reused or leaked evidence: %+v", inv)
	}
	t.Setenv("REIN_HERMES_CATALOG_EXIT", "")
	t.Setenv("REIN_HERMES_CATALOG_HANG", "1")
	invs := Discover(context.Background(), cfg, DiscoverOptions{Dir: t.TempDir(), Timeout: 200 * time.Millisecond, Harnesses: []string{"hermes"}})
	if invs[0].Status != InvFailed || len(invs[0].Models) != 0 || invs[0].SourceHash != "" {
		t.Fatalf("timed-out catalog gained evidence: %+v", invs)
	}
}

// Opt in with a JSON argv prefix for an isolated Hermes checkout; this runs metadata only.
func TestHermesDiscoveryCLI(t *testing.T) {
	command := os.Getenv("REIN_TEST_HERMES_METADATA_ARGV")
	if command == "" {
		t.Skip("isolated Hermes CLI not requested")
	}
	var argv []string
	if err := json.Unmarshal([]byte(command), &argv); err != nil || len(argv) == 0 {
		t.Fatal("invalid metadata argv")
	}
	cfg := &Config{Discovery: map[string]DiscoverySpec{"hermes": {Command: argv}}}
	inv := one(t, cfg, "hermes")
	if inv.Status != InvComplete || inv.Freshness != ClientCatalog || len(inv.Models) == 0 || inv.SourceHash == "" || inv.EvidenceAt != nil {
		t.Fatalf("actual CLI metadata failed: %+v", inv)
	}
	t.Logf("actual CLI: %s; version=%s models=%d source_hash=%s freshness=%s", inv.Adapter, inv.Version, len(inv.Models), inv.SourceHash, inv.Freshness)
}
