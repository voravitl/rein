package providers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestReviewCatalogConcurrentSharedURLWrites(t *testing.T) {
	t.Setenv("PIPELINE_CATALOGS", t.TempDir())
	const endpoint = "https://catalog.example/api.json"
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs <- writeCatalogCache(endpoint, CatalogCache{ETag: fmt.Sprint(i), At: time.Now().UTC().Format(time.RFC3339), Models: []string{"provider/model"}})
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	cached, err := readCatalogCache(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if cached.ETag == "" || len(cached.Models) != 1 || cached.Models[0] != "provider/model" {
		t.Fatalf("incomplete cache: %+v", cached)
	}
	leftovers, err := filepath.Glob(catalogCachePath(endpoint) + ".tmp-*")
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("temporary cache files: %v, %v", leftovers, err)
	}
}

func TestReviewCatalogCorruptCacheFetchesUnconditionally(t *testing.T) {
	t.Setenv("PIPELINE_CATALOGS", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != "" {
			t.Error("corrupt cache sent a validator")
		}
		w.Header().Set("ETag", `"new"`)
		_, _ = w.Write([]byte(`{"provider":{"models":{"model":{}}}}`))
	}))
	defer srv.Close()
	if err := os.MkdirAll(filepath.Dir(catalogCachePath(srv.URL)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalogCachePath(srv.URL), []byte(`{"etag":`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := revalidateCatalog(context.Background(), srv.URL, []ModelEntry{{ID: "provider/model"}}); err != nil {
		t.Fatal(err)
	}
	cached, err := readCatalogCache(srv.URL)
	if err != nil || cached.ETag != `"new"` {
		t.Fatalf("cache not repaired: %+v %v", cached, err)
	}
}
