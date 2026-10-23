package providers

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"
)

// Re-execute the native test binary as Orca, as the discovery fixtures do.
func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == "orca" || filepath.Base(os.Args[0]) == "orca.exe" {
		response := os.Getenv("REIN_TEST_ORCA_RESPONSE")
		if slices.Equal(os.Args[1:], []string{"account", "list", "--json"}) {
			data, err := os.ReadFile(response)
			if err != nil {
				os.Exit(1)
			}
			fmt.Print(string(data))
			os.Exit(0)
		}
		if len(os.Args) == 3 && os.Args[1] == "probe" {
			f, err := os.OpenFile(os.Args[2], os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
			if err != nil {
				os.Exit(1)
			}
			_, err = f.WriteString("x")
			closeErr := f.Close()
			if err != nil || closeErr != nil {
				os.Exit(1)
			}
			if exhausted := os.Getenv("REIN_TEST_EXHAUSTED_JSON"); exhausted != "" {
				if err := os.WriteFile(response, []byte(exhausted), 0600); err != nil {
					os.Exit(1)
				}
				time.Sleep(5 * time.Second)
			}
			fmt.Println("OK")
			os.Exit(0)
		}
		os.Exit(2)
	}
	os.Exit(m.Run())
}

func installTestOrca(t *testing.T, response string) string {
	t.Helper()
	dir := t.TempDir()
	name := "orca"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("REIN_TEST_ORCA_RESPONSE", response)
	t.Setenv("REIN_TEST_EXHAUSTED_JSON", "")
	t.Setenv("ORCA_ENVIRONMENT", "")
	t.Setenv("ORCA_PAIRING_CODE", "")
	return path
}

func TestOrcaFixtureUsesNativeExecutable(t *testing.T) {
	path := installTestOrca(t, filepath.Join(t.TempDir(), "response"))
	resolved, err := exec.LookPath("orca")
	if err != nil || resolved != path {
		t.Fatalf("Orca fixture lookup = %q, %v; want %q", resolved, err, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(data, original) {
		t.Fatal("Orca fixture must be the native test binary, not a shebang script")
	}
}
