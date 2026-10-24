package providers

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"time"
)

func collectHermes(ctx context.Context, argv []string, dir string, inv *Inventory) string {
	raw, why := runOnce(ctx, append(slices.Clone(argv), "model", "--catalog-json"), dir, false)
	if why != "" {
		inv.fail(InvFailed, "Hermes model --catalog-json unavailable: "+why)
		return ""
	}
	models, err := parseHermesModels(raw, inv.Version, time.Now().UTC())
	if err != nil {
		inv.fail(InvFailed, "invalid or incomplete Hermes bundled catalog metadata")
		return ""
	}
	inv.Models, inv.Status = models, InvComplete
	// Hash normalized model data only; observation time is a local query, never upstream evidence.
	normalized, _ := json.Marshal(models)
	inv.SourceHash = sha(normalized)
	inv.note("bundled curated client catalog; excludes virtual/nonliteral providers; entitlement, quota, context and upstream freshness unverified")
	return "hermes.model_catalog/v1/bundled_client_catalog"
}

func parseHermesModels(raw []byte, version string, now time.Time) ([]ModelEntry, error) {
	var doc struct {
		Protocol     string   `json:"protocol"`
		Schema       int      `json:"schema_version"`
		Capabilities []string `json:"capabilities"`
		Version      string   `json:"hermes_version"`
		Scope        string   `json:"scope"`
		Complete     *bool    `json:"complete"`
		Truncated    *bool    `json:"truncated"`
		Observed     string   `json:"observed_at"`
		Total        int      `json:"total_models"`
		Models       []struct {
			Provider string `json:"provider"`
			Model    string `json:"model_id"`
			Launch   string `json:"launch_model"`
			Source   string `json:"source"`
		} `json:"models"`
		Excluded *[]struct {
			Provider string `json:"provider"`
			Reason   string `json:"reason"`
		} `json:"excluded_providers"`
	}
	if json.Unmarshal(raw, &doc) != nil || doc.Protocol != "hermes.model_catalog" || doc.Schema != 1 || doc.Scope != "bundled_client_catalog" || doc.Complete == nil || !*doc.Complete || doc.Truncated == nil || *doc.Truncated || doc.Total != len(doc.Models) || doc.Total == 0 || doc.Total > 10000 || doc.Excluded == nil {
		return nil, errSchema
	}
	if versionRx.FindString(doc.Version) != version || !slices.Contains(doc.Capabilities, "provider_qualified_models") || !slices.Contains(doc.Capabilities, "bundled_catalog") {
		return nil, errSchema
	}
	observed, err := time.Parse(time.RFC3339Nano, doc.Observed)
	if err != nil || observed.UnixMilli() <= 0 || observed.After(now.Add(time.Minute)) || now.Sub(observed) > 5*time.Minute {
		return nil, errSchema
	}
	for _, excluded := range *doc.Excluded {
		if !validID(excluded.Provider) || !slices.Contains([]string{"virtual_provider", "non_literal_provider"}, excluded.Reason) {
			return nil, errSchema
		}
	}
	seen := map[string]bool{}
	models := make([]ModelEntry, 0, doc.Total)
	for _, row := range doc.Models {
		if !validID(row.Provider) || strings.ContainsAny(row.Provider, ":/") || !validID(row.Model) || !validID(row.Launch) || row.Launch != row.Provider+":"+row.Model || row.Source != "bundled_curated" || seen[row.Launch] {
			return nil, errSchema
		}
		seen[row.Launch] = true
		models = append(models, ModelEntry{ID: row.Launch, Alias: slices.Contains([]string{"auto", "default", "latest"}, row.Model)})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}
