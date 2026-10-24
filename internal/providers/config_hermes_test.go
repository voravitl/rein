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

func TestHermesQuotaHelper(t *testing.T) {
	if os.Getenv("REIN_HERMES_QUOTA_HELPER") != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	if log := os.Getenv("REIN_HERMES_QUOTA_METHODS"); log != "" {
		f, _ := os.OpenFile(log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		fmt.Fprintln(f, strings.Join(args, " "))
		f.Close()
	}
	if len(args) == 1 && args[0] == "--version" {
		fmt.Print(os.Getenv("REIN_HERMES_QUOTA_VERSION"))
		os.Exit(0)
	}
	if len(args) != 2 || args[0] != "usage" || args[1] != "--json" {
		os.Exit(7)
	}
	if os.Getenv("REIN_HERMES_QUOTA_HANG") == "1" {
		time.Sleep(time.Minute)
	}
	fmt.Print(os.Getenv("REIN_HERMES_QUOTA_DOCUMENT"))
	os.Exit(0)
}

func hermesQuotaDocument(now time.Time) string {
	return fmt.Sprintf(`{"provider":"openai-codex","source":"usage_api","fetched_at":%q,"windows":[{"label":"Session","used_percent":25,"resets_at":%q}],"unavailable_reason":null,"plan":"PRIVATE_PLAN","details":["PRIVATE_DETAIL"],"account":"PRIVATE_ACCOUNT"}`, now.Format(time.RFC3339Nano), now.Add(time.Hour).Format(time.RFC3339Nano))
}

func TestHermesNativeQuotaMetadataOnly(t *testing.T) {
	t.Setenv("REIN_HERMES_QUOTA_HELPER", "1")
	t.Setenv("REIN_HERMES_QUOTA_VERSION", "Hermes 0.21.5")
	t.Setenv("REIN_HERMES_QUOTA_DOCUMENT", hermesQuotaDocument(time.Now()))
	methods := filepath.Join(t.TempDir(), "methods")
	t.Setenv("REIN_HERMES_QUOTA_METHODS", methods)
	argv := []string{os.Args[0], "-test.run=^TestHermesQuotaHelper$", "--"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	row := collectHermesQuota(ctx, argv, t.TempDir(), QuotaSnapshot{Provider: "hermes", AccountEvidence: "unverified", Windows: []QuotaWindow{}})
	if row.Status != "ok" || len(row.Windows) != 1 || *row.Windows[0].Remaining != 75 || row.ObservedAt == nil {
		t.Fatalf("missing measured native quota: %+v", row)
	}
	b, _ := json.Marshal(row)
	if strings.Contains(string(b), "PRIVATE_") || row.AccountEvidence != "unverified" {
		t.Fatalf("account details or launch authorization inferred: %s", b)
	}
	called, _ := os.ReadFile(methods)
	if string(called) != "--version\nusage --json\n" {
		t.Fatalf("unexpected command (no session or inference allowed): %s", called)
	}
	for _, version := range []string{"Hermes 0.20.0", "Hermes 1.0.0", "unknown"} {
		t.Setenv("REIN_HERMES_QUOTA_VERSION", version)
		if got := collectHermesQuota(ctx, argv, t.TempDir(), QuotaSnapshot{Status: "unsupported"}); got.Status != "unsupported" || len(got.Windows) != 0 {
			t.Fatalf("unsupported version gained quota: %+v", got)
		}
	}
	t.Setenv("REIN_HERMES_QUOTA_VERSION", "Hermes 0.21.5")
	t.Setenv("REIN_HERMES_QUOTA_HANG", "1")
	short, stop := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer stop()
	if got := collectHermesQuota(short, argv, t.TempDir(), QuotaSnapshot{Status: "unsupported"}); got.Status != "unavailable" || len(got.Windows) != 0 {
		t.Fatalf("timeout gained quota: %+v", got)
	}
}

func TestHermesNativeQuotaRejectsIncompleteOrUnsafeMeasurements(t *testing.T) {
	now := time.Now().UTC()
	for name, raw := range map[string]string{
		"invalid JSON":          "not JSON PRIVATE_SECRET",
		"empty":                 `{}`,
		"future timestamp":      hermesQuotaDocument(now.Add(2 * time.Minute)),
		"stale":                 hermesQuotaDocument(now.Add(-6 * time.Minute)),
		"missing used":          strings.Replace(hermesQuotaDocument(now), `"used_percent":25`, `"used_percent":null`, 1),
		"invalid used":          strings.Replace(hermesQuotaDocument(now), `"used_percent":25`, `"used_percent":101`, 1),
		"missing reset":         strings.Replace(hermesQuotaDocument(now), fmt.Sprintf(`"resets_at":%q`, now.Add(time.Hour).Format(time.RFC3339Nano)), `"resets_at":null`, 1),
		"reset expired":         strings.Replace(hermesQuotaDocument(now), now.Add(time.Hour).Format(time.RFC3339Nano), now.Add(-time.Hour).Format(time.RFC3339Nano), 1),
		"unavailable":           strings.Replace(hermesQuotaDocument(now), `"unavailable_reason":null`, `"unavailable_reason":"PRIVATE_REASON"`, 1),
		"unknown provider":      strings.Replace(hermesQuotaDocument(now), "openai-codex", "PRIVATE_PROVIDER", 1),
		"fixture source":        strings.Replace(hermesQuotaDocument(now), "usage_api", "dev-fixture", 1),
		"mismatched source":     strings.Replace(hermesQuotaDocument(now), "usage_api", "oauth_usage_api", 1),
		"duplicate label":       strings.Replace(hermesQuotaDocument(now), `}],"unavailable_reason"`, `},{"label":"Session","used_percent":5,"resets_at":"2099-01-01T00:00:00Z"}],"unavailable_reason"`, 1),
		"invalid second window": strings.Replace(hermesQuotaDocument(now), `}],"unavailable_reason"`, `},{"label":"Weekly","used_percent":101,"resets_at":"2099-01-01T00:00:00Z"}],"unavailable_reason"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			row := parseHermesQuota([]byte(raw), QuotaSnapshot{Provider: "hermes", Windows: []QuotaWindow{}}, now)
			if row.Status == "ok" || len(row.Windows) != 0 || row.ObservedAt != nil {
				t.Fatalf("invalid quota gained usable balance: %+v", row)
			}
			b, _ := json.Marshal(row)
			if strings.Contains(string(b), "PRIVATE_") {
				t.Fatalf("private response leaked: %s", b)
			}
		})
	}
}

func TestHermesQuotaRemainsUnsupportedAndVetoed(t *testing.T) {
	now := time.Now()
	for _, quota := range []string{
		`{}`,
		fmt.Sprintf(`{"hermes":{"status":"ok","updatedAt":%d,"weekly":{"usedPercent":0},"accountId":"PRIVATE_ACCOUNT","token":"PRIVATE_TOKEN"}}`, now.UnixMilli()),
		`{"hermes":"PRIVATE_MALFORMED"}`,
	} {
		rows, err := ParseOrcaQuota([]byte(`{"ok":true,"result":{"rateLimits":`+quota+`}}`), now, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		assertHermesQuotaUnsupported(t, rows, now)
		b, err := json.Marshal(rows)
		if err != nil || strings.Contains(string(b), "PRIVATE_") {
			t.Fatalf("private unrecognized quota leaked: %s %v", b, err)
		}
	}
	assertHermesQuotaUnsupported(t, nativeQuotaSnapshots(Inventory{Harness: "hermes", Status: InvComplete}, now), now)
}

func TestHermesQuotaGuardStopsProbesWithConfiguredWindows(t *testing.T) {
	for _, surface := range []string{"single", "retry", "parallel"} {
		t.Run(surface, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "started")
			t.Setenv("REIN_FAKE_MARKER", marker)
			t.Setenv("REIN_FAKE_SCENARIO", "ok")
			cfg := &Config{RequireOrcaQuota: true, Providers: map[string]Provider{"h": {
				Agent: "hermes", Model: "owner/model", QuotaWindows: []string{"weekly"},
				Probe: []string{os.Args[0], "-test.run=^TestDiscoverHelper$", "--"},
			}}}
			var r Result
			switch surface {
			case "single":
				r = Probe(cfg, "h", time.Second)
			case "retry":
				r = ProbeWithRetry(cfg, "h", time.Second)
			case "parallel":
				r = Check(cfg, []string{"h"}, time.Second)["h"]
			}
			if r.State != "down" || !strings.Contains(r.Detail, "Hermes launch quota unsupported") {
				t.Fatalf("missing explicit launch veto: %+v", r)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("unsupported quota started a model probe")
			}
		})
	}
}

func assertHermesQuotaUnsupported(t *testing.T, rows []QuotaSnapshot, now time.Time) {
	t.Helper()
	count := 0
	for _, row := range rows {
		if row.Provider != "hermes" {
			continue
		}
		count++
		if row.Status != "unsupported" || row.AccountEvidence != "unverified" || row.ObservedAt != nil || len(row.Windows) != 0 {
			t.Fatalf("Hermes gained an unproven quota: %+v", row)
		}
	}
	if count != 1 {
		t.Fatalf("want exactly one explicit Hermes quota row: %+v", rows)
	}
	p := Provider{Agent: "hermes", Model: "owner/model", QuotaWindows: []string{"weekly"}}
	if err := checkQuotaWindows(p, rows, now); err == nil {
		t.Fatal("unsupported Hermes quota must veto provider use")
	}
}
