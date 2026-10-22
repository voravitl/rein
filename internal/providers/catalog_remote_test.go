package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A provider that confirms currency with its own cache validator proves `remote_verified` freshness. A 304 is the
// provider's own answer that the stored snapshot is still current; a 200 with a fresh ETag is a fresh fetch.
func TestRevalidateCatalogProvesRemoteFreshness(t *testing.T) {
	t.Setenv("PIPELINE_CATALOGS", filepath.Join(t.TempDir(), "catalogs"))
	var seenETag string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenETag = r.Header.Get("If-None-Match")
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(`{"providers":{}}`))
	}))
	defer srv.Close()

	at, err := revalidateCatalog(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if at.IsZero() {
		t.Fatal("no evidence time")
	}
	if seenETag != "" {
		t.Errorf("the first revalidation sent a validator it never had: %q", seenETag)
	}
	// The second call revalidates conditionally: the provider answers 304 and the stored time is kept.
	second, err := revalidateCatalog(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if seenETag != `"v1"` {
		t.Errorf("the second revalidation sent %q, want the stored validator", seenETag)
	}
	if !second.Equal(at) {
		t.Errorf("a confirmed snapshot changed its evidence time: %v vs %v", second, at)
	}
}

// A 304 that confirms an ETag this process never stored is refused: it would prove nothing.
func TestRevalidateCatalogRefusesAnUnknownValidator(t *testing.T) {
	t.Setenv("PIPELINE_CATALOGS", filepath.Join(t.TempDir(), "catalogs"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()
	if _, err := revalidateCatalog(context.Background(), srv.URL); err == nil {
		t.Fatal("a 304 for an unknown validator was accepted")
	}
}

// A provider that does not confirm currency fails the refresh; the prior snapshot is never reused silently.
func TestRevalidateCatalogFailsWhenTheProviderDoesNotConfirm(t *testing.T) {
	t.Setenv("PIPELINE_CATALOGS", filepath.Join(t.TempDir(), "catalogs"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	if _, err := revalidateCatalog(context.Background(), srv.URL); err == nil {
		t.Fatal("a failed revalidation was accepted")
	}
}

// A fresh catalog with no ETag cannot be revalidated later, so it is refused rather than stored as unverifiable.
func TestRevalidateCatalogRefusesAFreshBodyWithoutAValidator(t *testing.T) {
	t.Setenv("PIPELINE_CATALOGS", filepath.Join(t.TempDir(), "catalogs"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"providers":{}}`))
	}))
	defer srv.Close()
	if _, err := revalidateCatalog(context.Background(), srv.URL); err == nil {
		t.Fatal("a fresh body without a validator was accepted")
	}
}

// The inventory reports remote_verified only when the provider confirmed the catalog; the evidence time is the
// provider's own, not the moment of the query.
func TestDiscoverReportsRemoteVerifiedOnlyAfterConfirmation(t *testing.T) {
	t.Setenv("PIPELINE_CATALOGS", filepath.Join(t.TempDir(), "catalogs"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(`{"providers":{}}`))
	}))
	defer srv.Close()

	cfg := fake(t, "opencode", "ok", "opencode v2.0.20")
	spec := cfg.Discovery["opencode"]
	spec.CatalogURL = srv.URL
	cfg.Discovery["opencode"] = spec
	inv := one(t, cfg, "opencode")
	if inv.Status != InvComplete {
		t.Fatalf("status = %s (%s)", inv.Status, inv.Reason)
	}
	if inv.Freshness != RemoteVerified {
		t.Errorf("freshness = %s, want %s", inv.Freshness, RemoteVerified)
	}
	if inv.EvidenceAt == nil {
		t.Error("evidence time is unknown")
	}

	// With the endpoint gone the refresh fails closed: the harness is excluded, not served from the old snapshot.
	srv.Close()
	inv = one(t, cfg, "opencode")
	if inv.Status != InvFailed || inv.Freshness != FreshnessUnknown {
		t.Errorf("a failed revalidation = %s %s, want failed/unknown", inv.Status, inv.Freshness)
	}
	if !strings.Contains(inv.Reason, "revalidation failed") {
		t.Errorf("reason = %q", inv.Reason)
	}
}

// The stored validator is per URL, so two catalogs never share one.
func TestCatalogCacheIsKeyedByURL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "catalogs")
	t.Setenv("PIPELINE_CATALOGS", dir)
	if err := writeCatalogCache("https://a.example/api.json", CatalogCache{ETag: `"a"`, At: time.Now().UTC().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	if err := writeCatalogCache("https://b.example/api.json", CatalogCache{ETag: `"b"`, At: time.Now().UTC().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	a, err := readCatalogCache("https://a.example/api.json")
	if err != nil {
		t.Fatal(err)
	}
	b, err := readCatalogCache("https://b.example/api.json")
	if err != nil {
		t.Fatal(err)
	}
	if a.ETag != `"a"` || b.ETag != `"b"` {
		t.Errorf("the caches collided: %q %q", a.ETag, b.ETag)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("the cache directory is missing: %v", err)
	}
}
