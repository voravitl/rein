package providers

// Remote catalog revalidation (docs/ROUTING_SELECTION_DESIGN.md, "Refresh before every selection").
//
// A local client query only proves what the client answered. A policy that accepts only `remote_verified` needs
// evidence that the returned models belong to a catalog refreshed against its provider, using:
// an ETag sent back as If-None-Match. A 304 means the stored snapshot is still current; a 200 with a new ETag is a
// fresh body. Anything else is a failed refresh, and a failed refresh must not silently reuse the prior snapshot.

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
	"slices"
	"strings"
	"time"
)

const maxCatalogBody = 32 << 20 // one catalog is ~6 MiB; a cap keeps a broken endpoint from exhausting memory

// CatalogCache is the stored validator state of one remote catalog URL: the ETag the provider last accepted and the
// time the body it belongs to was fetched fresh.
type CatalogCache struct {
	ETag   string   `json:"etag"`
	At     string   `json:"at"`     // RFC 3339, when the body was last fetched fresh; a 304 keeps it
	Models []string `json:"models"` // provider-qualified IDs parsed from the body bound to ETag
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
	if json.Unmarshal(b, &c) != nil {
		// A corrupt derived cache is a miss, never a partially decoded validator.
		return CatalogCache{}, nil
	}
	return c, nil
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
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(b); err != nil {
		return errors.Join(err, f.Close())
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// revalidateCatalog asks the provider's catalog endpoint whether the stored snapshot is still current. It returns the
// time the catalog body was last fetched fresh (a 304 keeps the stored time: the body is unchanged), or an error when
// the provider did not confirm currency.
func revalidateCatalog(ctx context.Context, url string, models []ModelEntry) (time.Time, error) {
	stored, err := readCatalogCache(url)
	if err != nil {
		return time.Time{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return time.Time{}, err
	}
	if stored.ETag != "" && stored.Models != nil {
		req.Header.Set("If-None-Match", stored.ETag)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return time.Time{}, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotModified:
		if req.Header.Get("If-None-Match") == "" || stored.At == "" || stored.Models == nil {
			return time.Time{}, errors.New("provider confirmed an ETag this process never stored")
		}
		at, err := time.Parse(time.RFC3339, stored.At)
		if err != nil {
			return time.Time{}, err
		}
		return at, catalogContains(stored.Models, models)
	case http.StatusOK:
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxCatalogBody+1))
		if err != nil {
			return time.Time{}, err
		}
		if len(body) > maxCatalogBody {
			return time.Time{}, errors.New("remote catalog exceeds size limit")
		}
		var providers map[string]struct {
			Models map[string]json.RawMessage `json:"models"`
		}
		if err := json.Unmarshal(body, &providers); err != nil {
			return time.Time{}, err
		}
		if providers == nil {
			return time.Time{}, errors.New("remote catalog must be a provider map")
		}
		ids := []string{}
		for provider, entry := range providers {
			if provider == "" || strings.Contains(provider, "/") || entry.Models == nil {
				return time.Time{}, errors.New("remote catalog provider has no model map")
			}
			for model, body := range entry.Models {
				var metadata map[string]json.RawMessage
				if err := json.Unmarshal(body, &metadata); err != nil || metadata == nil || model == "" {
					return time.Time{}, errors.New("remote catalog model must be a named object")
				}
				id := provider + "/" + model
				if !idRx.MatchString(id) {
					return time.Time{}, errors.New("remote catalog carries an invalid model ID")
				}
				ids = append(ids, id)
			}
		}
		slices.Sort(ids)
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
		if err := writeCatalogCache(url, CatalogCache{ETag: etag, At: at.Format(time.RFC3339), Models: ids}); err != nil {
			return time.Time{}, err
		}
		return at, catalogContains(ids, models)
	default:
		return time.Time{}, fmt.Errorf("catalog answered %s", resp.Status)
	}
}

// A validator proves only the model IDs in its own body, never an unrelated local client list.
func catalogContains(ids []string, models []ModelEntry) error {
	for _, model := range models {
		if !slices.Contains(ids, model.ID) {
			return fmt.Errorf("local model %q is absent from the remote catalog", model.ID)
		}
	}
	return nil
}
