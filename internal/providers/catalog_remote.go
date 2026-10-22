package providers

// Remote catalog revalidation (docs/ROUTING_SELECTION_DESIGN.md, "Refresh before every selection").
//
// A local client query only ever proves what the client answered: it says nothing about whether the upstream catalog is
// current. A policy that accepts only `remote_verified` needs evidence that the catalog itself was refreshed against its
// provider. This file supplies that evidence with the provider's own cache validator: an ETag sent back as
// If-None-Match. The provider answers 304 ("your snapshot is still current") or 200 (a fresh body), and either answer
// proves the catalog current. Anything else is a failed refresh, and a failed refresh must not silently reuse the prior
// snapshot: the caller excludes the harness.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxCatalogBody = 32 << 20 // one catalog is ~6 MiB; a cap keeps a broken endpoint from exhausting memory

// CatalogCache is the stored validator state of one remote catalog URL: the ETag the provider last accepted and the
// time the body it belongs to was fetched fresh.
type CatalogCache struct {
	ETag string `json:"etag"`
	At   string `json:"at"` // RFC 3339, when the body was last fetched fresh; a 304 keeps it
}

// CatalogCacheDir is where the per-URL validator state lives. PIPELINE_CATALOGS overrides it.
func CatalogCacheDir() string {
	if d := os.Getenv("PIPELINE_CATALOGS"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "rein", "catalogs")
}

// catalogCachePath keys the state by the URL, so two configured catalogs never share a validator.
func catalogCachePath(url string) string {
	sum := sha256.Sum256([]byte(url))
	return filepath.Join(CatalogCacheDir(), hex.EncodeToString(sum[:])+".json")
}

func readCatalogCache(url string) (CatalogCache, error) {
	var c CatalogCache
	b, err := os.ReadFile(catalogCachePath(url))
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(b, &c)
	return c, err
}

func writeCatalogCache(url string, c CatalogCache) error {
	path := catalogCachePath(url)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// revalidateCatalog asks the provider's catalog endpoint whether the stored snapshot is still current. It returns the
// time the catalog body was last fetched fresh (a 304 keeps the stored time: the body is unchanged), or an error when
// the provider did not confirm currency. A 304 reuses the stored ETag only because the provider itself confirmed it is
// still valid; that is a cache validator, not a silent fallback.
func revalidateCatalog(ctx context.Context, url string) (time.Time, error) {
	stored, err := readCatalogCache(url)
	if err != nil {
		return time.Time{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return time.Time{}, err
	}
	if stored.ETag != "" {
		req.Header.Set("If-None-Match", stored.ETag)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return time.Time{}, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotModified:
		if stored.At == "" {
			return time.Time{}, errors.New("provider confirmed an ETag this process never stored")
		}
		at, err := time.Parse(time.RFC3339, stored.At)
		if err != nil {
			return time.Time{}, err
		}
		return at, nil
	case http.StatusOK:
		// Drain a bounded amount so a huge or hostile body cannot exhaust memory; the body is not used as evidence,
		// only the validator is.
		if _, err := io.Copy(io.Discard, io.LimitReader(resp.Body, maxCatalogBody)); err != nil {
			return time.Time{}, err
		}
		etag := strings.TrimSpace(resp.Header.Get("ETag"))
		if etag == "" {
			return time.Time{}, errors.New("a fresh catalog carried no ETag, so a later refresh cannot be conditional")
		}
		at := time.Now().UTC()
		if d := resp.Header.Get("Date"); d != "" {
			if parsed, err := http.ParseTime(d); err == nil {
				at = parsed
			}
		}
		if err := writeCatalogCache(url, CatalogCache{ETag: etag, At: at.Format(time.RFC3339)}); err != nil {
			return time.Time{}, err
		}
		return at, nil
	default:
		return time.Time{}, fmt.Errorf("catalog answered %s", resp.Status)
	}
}
