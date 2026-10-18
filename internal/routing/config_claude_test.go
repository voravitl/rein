package routing

import (
	"strings"
	"testing"

	"github.com/voravitl/rein/internal/providers"
)

func TestExplicitNoEffortCatalogExcludesBeforeProbe(t *testing.T) {
	w := newWorld(t)
	w.seedAll()
	w.invHook = func(_ int, inv *providers.Inventory) {
		if inv.Harness == "codex" {
			for i := range inv.Models {
				if inv.Models[i].ID == "gpt-sub" {
					inv.Models[i].SupportsEffort = no()
					inv.Models[i].Variants = nil
				}
			}
		}
	}
	result, err := w.prepare()
	if err == nil || result == nil || result.Receipt != nil {
		t.Fatalf("unsupported effort selected: %v", err)
	}
	if !strings.HasPrefix(excluded(result, "w-sub"), "effort_not_supported:") {
		t.Fatal(dump(result))
	}
	for _, p := range w.probeLog() {
		if p == "w-sub" {
			t.Fatal("unsupported effort was probed")
		}
	}
}
