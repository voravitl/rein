package providers

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

func nativeQuotaFixture(t *testing.T, now time.Time) Inventory {
	t.Helper()
	inv := Inventory{Harness: "codex", Status: InvComplete, QueriedAt: now.Add(-time.Second), Scope: "PRIVATE_SCOPE", Reason: "PRIVATE_REASON", Executable: "/PRIVATE_EXECUTABLE", Models: []ModelEntry{{ID: "PRIVATE_MODEL"}}}
	account, err := codexLimits(json.RawMessage(`{"accountId":"PRIVATE_ACCOUNT","ordinaryUsageAllowed":true,"rateLimitsByLimitId":{"codex":{"primary":{"usedPercent":12.5,"windowDurationMins":300,"resetsAt":1893459600},"secondary":{"usedPercent":97,"windowDurationMins":10080,"resetsAt":1893542400}}}}`), &inv)
	if err != nil || account != "PRIVATE_ACCOUNT" {
		t.Fatalf("fixture parser: %q %v", account, err)
	}
	return inv
}

func TestNativeQuotaParsedWindowsAndPrivacy(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	inv := nativeQuotaFixture(t, now)
	rows := nativeQuotaSnapshots(inv, now)
	if len(rows) != 5 {
		t.Fatal(rows)
	}
	for _, row := range rows {
		if row.AccountEvidence != "unverified" {
			t.Fatal(row)
		}
		if row.Provider != "codex" {
			if row.Status != "unsupported" || len(row.Windows) != 0 || row.ObservedAt != nil {
				t.Fatalf("peer quota inferred: %+v", row)
			}
			continue
		}
		if row.Status != "ok" || row.Source != nativeCodexQuotaSource || len(row.Windows) != 2 || *row.ObservedAt != inv.QueriedAt.UnixMilli() || !strings.Contains(row.Scope, "provider data age") {
			t.Fatalf("missing native evidence: %+v", row)
		}
		first, second := row.Windows[0], row.Windows[1]
		if first.Name != "codex/primary" || *first.Remaining != 87.5 || first.WindowMinutes != 300 || *first.ResetsAt != 1893459600000 || second.Name != "codex/secondary" || *second.Remaining != 3 || second.WindowMinutes != 10080 {
			t.Fatalf("window semantics changed: %+v", row.Windows)
		}
	}
	raw, err := json.Marshal(rows)
	if err != nil || strings.Contains(string(raw), "PRIVATE_") {
		t.Fatalf("private inventory data leaked: %s %v", raw, err)
	}
}

func TestNativeQuotaRejectsInvalidOrIncompleteWindows(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		change func(*Inventory)
		status string
	}{
		{"nil used", func(i *Inventory) { i.Limits[0].UsedPercent = nil }, "unknown"},
		{"negative", func(i *Inventory) { n := -1.0; i.Limits[0].UsedPercent = &n }, "invalid"},
		{"over 100", func(i *Inventory) { n := 100.1; i.Limits[0].UsedPercent = &n }, "invalid"},
		{"NaN", func(i *Inventory) { n := math.NaN(); i.Limits[0].UsedPercent = &n }, "invalid"},
		{"infinite", func(i *Inventory) { n := math.Inf(1); i.Limits[0].UsedPercent = &n }, "invalid"},
		{"units", func(i *Inventory) { i.Limits[0].Unit = "credits" }, "invalid"},
		{"missing duration", func(i *Inventory) { i.Limits[0].WindowMinutes = nil }, "unknown"},
		{"invalid duration", func(i *Inventory) { n := int64(-1); i.Limits[0].WindowMinutes = &n }, "invalid"},
		{"missing reset", func(i *Inventory) { i.Limits[0].ResetsAt = nil }, "unknown"},
		{"expired reset", func(i *Inventory) { reset := now.Add(-time.Second); i.Limits[0].ResetsAt = &reset }, "invalid"},
		{"invalid reset", func(i *Inventory) {
			reset := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			i.Limits[0].ResetsAt = &reset
		}, "invalid"},
		{"empty id", func(i *Inventory) { i.Limits[0].ID = "" }, "invalid"},
		{"duplicate id", func(i *Inventory) { i.Limits[1].ID = i.Limits[0].ID }, "invalid"},
		{"disallowed", func(i *Inventory) { allowed := false; i.Limits[0].Allowed = &allowed }, "unavailable"},
		{"future query", func(i *Inventory) { i.QueriedAt = now.Add(time.Second) }, "invalid"},
		{"missing query", func(i *Inventory) { i.QueriedAt = time.Time{} }, "unknown"},
		{"invalid query", func(i *Inventory) { i.QueriedAt = time.Unix(-1, 0) }, "invalid"},
		{"no limits", func(i *Inventory) { i.Limits = nil }, "unknown"},
		{"partial", func(i *Inventory) { i.Status = InvPartial }, "unknown"},
		{"failed", func(i *Inventory) { i.Status = InvFailed }, "unavailable"},
		{"wrong harness", func(i *Inventory) { i.Harness = "claude" }, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inv := nativeQuotaFixture(t, now)
			tc.change(&inv)
			row := nativeQuotaSnapshots(inv, now)[1]
			if row.Status != tc.status {
				t.Fatalf("status %s != %s: %+v", row.Status, tc.status, row)
			}
			for _, w := range row.Windows {
				if w.Remaining != nil {
					t.Fatalf("unusable response has positive/known balance: %+v", row)
				}
			}
		})
	}
}

func TestNativeQuotaPercentBoundariesAndAllowedFalse(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, used := range []float64{0, 100} {
		inv := nativeQuotaFixture(t, now)
		inv.Limits[0].UsedPercent = &used
		row := nativeQuotaSnapshots(inv, now)[1]
		if row.Status != "ok" || *row.Windows[0].Remaining != 100-used {
			t.Fatal(row)
		}
	}
	inv := Inventory{Harness: "codex", Status: InvComplete, QueriedAt: now}
	if _, err := codexLimits(json.RawMessage(`{"ordinaryUsageAllowed":false,"rateLimits":{"limitId":"default","primary":{"usedPercent":0,"windowDurationMins":300,"resetsAt":1893459600}}}`), &inv); err != nil {
		t.Fatal(err)
	}
	row := nativeQuotaSnapshots(inv, now)[1]
	if row.Status != "unavailable" || row.Windows[0].Remaining != nil {
		t.Fatalf("usage disallowed became positive balance: %+v", row)
	}
}

func TestNativeQuotaMalformedAndEmptyResponsesRemainUnknown(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, raw := range []string{`not json`, `null`, `{}`, `{"rateLimits":null}`, `{"rateLimits":{"primary":{"usedPercent":null}}}`} {
		inv := Inventory{Harness: "codex", Status: InvComplete, QueriedAt: now}
		_, _ = codexLimits(json.RawMessage(raw), &inv)
		row := nativeQuotaSnapshots(inv, now)[1]
		if row.Status != "unknown" || len(row.Windows) != 0 {
			t.Fatalf("malformed quota gained balance: %q %+v", raw, row)
		}
	}
}

func TestReadNativeQuotaInvalidRequestDoesNotDiscover(t *testing.T) {
	// These branches return before Discover; no native process or model is run.
	rows, err := ReadNativeQuota(context.Background(), t.TempDir(), 0)
	if err == nil || len(rows) != 5 || rows[1].Status != "unknown" {
		t.Fatalf("%+v %v", rows, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rows, err = ReadNativeQuota(ctx, t.TempDir(), time.Second)
	if err == nil || len(rows) != 5 || rows[1].Status != "unknown" {
		t.Fatalf("%+v %v", rows, err)
	}
}
