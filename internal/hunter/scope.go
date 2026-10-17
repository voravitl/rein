package hunter

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/voravitl/rein/internal/contract"
)

// ReproducerContract is distinct from the finding's implementation task. These
// directories are reserved for reproduction tests and disposable evidence.
func ReproducerContract(name, clone string) contract.Contract {
	return contract.Contract{Name: name, Worktree: clone, Allow: []string{"hunter-tests/**", "evidence/**"}, Deny: append([]string(nil), contract.AlwaysDeny...), Scope: []string{"REPRODUCE"}, ReportPath: filepath.Join(clone, "evidence", "reproduction.json")}
}

type scopeEntry struct {
	Mode   fs.FileMode
	Digest string
}

// snapshotReproducer includes ignored/untracked files and Git metadata. It is
// an after-run integrity check for trusted commands, not an OS write sandbox.
func snapshotReproducer(root string) (map[string]scopeEntry, error) {
	entries := map[string]scopeEntry{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		e := scopeEntry{Mode: info.Mode()}
		switch {
		case info.Mode().IsRegular():
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			hash := sha256.New()
			_, copyErr := io.Copy(hash, f)
			closeErr := f.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			e.Digest = hex.EncodeToString(hash.Sum(nil))
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			e.Digest = target
		case info.IsDir():
		default:
			return fmt.Errorf("unsupported reproduction scope entry %s", rel)
		}
		entries[filepath.ToSlash(rel)] = e
		return nil
	})
	return entries, err
}

func checkReproducerScope(before, after map[string]scopeEntry) error {
	for path, e := range after {
		prior, exists := before[path]
		allowed := path == "hunter-tests" || strings.HasPrefix(path, "hunter-tests/") || path == "evidence" || strings.HasPrefix(path, "evidence/")
		if allowed && (e.Mode&os.ModeSymlink != 0 || strings.Contains("/"+path+"/", "/.git/")) {
			return fmt.Errorf("reproducer scope violation: unsafe writable path %s", path)
		}
		if exists && prior == e {
			continue
		}
		if !allowed || e.Mode&os.ModeSymlink != 0 {
			return fmt.Errorf("reproducer scope violation: changed %s", path)
		}
	}
	for path := range before {
		if _, exists := after[path]; !exists {
			if !(strings.HasPrefix(path, "hunter-tests/") || strings.HasPrefix(path, "evidence/")) {
				return fmt.Errorf("reproducer scope violation: deleted %s", path)
			}
		}
	}
	return nil
}

func writeReproducerContract(root string, c contract.Contract) (string, error) {
	dir := filepath.Join(root, "contracts")
	if err := os.Mkdir(dir, 0700); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return "", err
	}
	if err = os.WriteFile(filepath.Join(dir, c.Name+".json"), data, 0400); err != nil {
		return "", err
	}
	return dir, nil
}
