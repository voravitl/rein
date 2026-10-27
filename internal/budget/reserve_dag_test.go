package budget

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestDAGReservationDirectoryDurabilityFailure(t *testing.T) {
	dir := t.TempDir()
	want := errors.New("directory durability unavailable")
	original := syncHoldsDirectory
	syncHoldsDirectory = func(string) error { return want }
	t.Cleanup(func() { syncHoldsDirectory = original })
	if err := writeHolds(dir, filepath.Join(dir, "holds.json"), map[string]*Hold{}); !errors.Is(err, want) {
		t.Fatalf("reservation acknowledged without directory durability: %v", err)
	}
}
