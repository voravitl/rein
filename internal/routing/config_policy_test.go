package routing

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestConfigDisabledStopsOrderedAndAutomaticRouting(t *testing.T) {
	t.Run("ordered", func(t *testing.T) {
		c, path, log := fixture(t)
		b, _ := os.ReadFile(path)
		var cfg map[string]any
		_ = json.Unmarshal(b, &cfg)
		cfg["model_calls_enabled"] = false
		b, _ = json.Marshal(cfg)
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Prepare(c, "run", "worker:backend", "", path, false, time.Second); err == nil || !strings.Contains(err.Error(), "disabled") {
			t.Errorf("disabled routing accepted: %v", err)
		}
		if _, err := os.Stat(log); !os.IsNotExist(err) {
			t.Error("ordered prepare ran a probe")
		}
	})
	t.Run("automatic", func(t *testing.T) {
		w := newWorld(t)
		in := w.in()
		b, _ := os.ReadFile(in.ConfigPath)
		var cfg map[string]any
		_ = json.Unmarshal(b, &cfg)
		cfg["model_calls_enabled"] = false
		b, _ = json.Marshal(cfg)
		if err := os.WriteFile(in.ConfigPath, b, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := PrepareAuto(context.Background(), in); err == nil || !strings.Contains(err.Error(), "disabled") {
			t.Errorf("disabled automatic routing accepted: %v", err)
		}
		if w.calls.Load() != 0 || len(w.probeLog()) != 0 {
			t.Fatal("disabled automatic routing reached discovery/probe")
		}
	})
}
