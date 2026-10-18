package providers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfigDisabledModelCallsNeverStartProbe(t *testing.T) {
	for _, surface := range []string{"single", "retry", "parallel"} {
		t.Run(surface, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "called")
			raw, _ := json.Marshal(map[string]any{"model_calls_enabled": false, "providers": map[string]Provider{"p": {Agent: "codex", Model: "test", Probe: []string{"sh", "-c", `printf called > "$1"; printf 'OK\n'`, "probe", marker}}}})
			var cfg Config
			if err := json.Unmarshal(raw, &cfg); err != nil {
				t.Fatal(err)
			}
			var result Result
			switch surface {
			case "single":
				result = Probe(&cfg, "p", time.Second)
			case "retry":
				result = ProbeWithRetry(&cfg, "p", time.Second)
			case "parallel":
				result = Check(&cfg, []string{"p"}, time.Second)["p"]
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Error("disabled config started a model probe")
			}
			if result.State != "down" || !strings.Contains(result.Detail, "disabled") {
				t.Errorf("missing refusal: %+v", result)
			}
		})
	}
}
