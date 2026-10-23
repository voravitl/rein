package hooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const hermesCredentialsPrefix = "credentials:\n  home: "

// splitHermesCredentials recognizes only the exact coordinator-generated reference.
func splitHermesCredentials(config string) (string, string, error) {
	index := strings.Index(config, hermesCredentialsPrefix)
	if index < 0 {
		return config, "", nil
	}
	base, encoded := config[:index], strings.TrimSuffix(config[index+len(hermesCredentialsPrefix):], "\n")
	var home string
	if json.Unmarshal([]byte(encoded), &home) != nil || !filepath.IsAbs(home) || strings.Contains(home, "$") {
		return "", "", fmt.Errorf("foreign Hermes credential reference; preserve existing config")
	}
	encodedHome, _ := json.Marshal(home)
	if encoded != string(encodedHome) || config != base+hermesCredentialsPrefix+encoded+"\n" {
		return "", "", fmt.Errorf("foreign Hermes credential reference; preserve existing config")
	}
	return base, home, nil
}

func hermesCredentialsConfig(base, home string) string {
	if home == "" {
		return base
	}
	encoded, _ := json.Marshal(home)
	return base + hermesCredentialsPrefix + string(encoded) + "\n"
}

// CheckHermesTaskHome refuses a task home that could switch the guard off.
// Hermes loads the task home's dotenv files with override, so a .env there can set
// HERMES_SAFE_MODE or HERMES_IGNORE_USER_CONFIG and skip the plugin entirely.
func CheckHermesTaskHome(wt string) error {
	for _, name := range []string{".env", ".op.env"} {
		path := filepath.Join(wt, ".hermes", name)
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("Hermes task home has %s; it can disable the guard plugin, so remove it or use a fresh task Hermes home", name)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// BindHermesCredentials records the authenticated source before the worker's home is isolated.
// It stores only a path; credential refreshes keep writing to the original auth store.
func BindHermesCredentials(wt string) error {
	cfg, err := prepare(wt, ".hermes/config.yaml")
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(cfg)
	if err != nil {
		return err
	}
	base, existing, err := splitHermesCredentials(string(raw))
	if err != nil {
		return err
	}
	if base != hermesConfigYAML && base != hermesCombinedConfigYAML {
		return fmt.Errorf("cannot bind credentials to foreign Hermes config")
	}
	source := os.Getenv("HERMES_HOME")
	if source == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		suffix := os.Getenv("HERMES_DATA_DIR_SUFFIX")
		source = filepath.Join(home, ".hermes"+suffix)
		if runtime.GOOS == "windows" {
			parent := os.Getenv("LOCALAPPDATA")
			if parent == "" {
				parent = filepath.Join(home, "AppData", "Local")
			}
			source = filepath.Join(parent, "hermes"+suffix)
		}
	}
	if !filepath.IsAbs(source) || strings.Contains(source, "$") {
		return fmt.Errorf("Hermes credential source must be a literal absolute path")
	}
	source = filepath.Clean(source)
	if source == filepath.Join(wt, ".hermes") {
		return fmt.Errorf("Hermes credential source cannot be the task home")
	}
	if existing != "" && existing != source {
		return fmt.Errorf("Hermes credential source differs from the installed reference; preserve existing config")
	}
	if existing != "" {
		return nil
	}
	return os.WriteFile(cfg, []byte(hermesCredentialsConfig(base, source)), 0600)
}
