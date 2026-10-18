package supervisor

import (
	"reflect"
	"testing"

	"github.com/voravitl/rein/internal/routing"
)

func TestGraphOpenCodeModelEffortPlaceholder(t *testing.T) {
	argv := []string{"opencode2", "run", "--standalone", "-m", "{model}#{effort}", "prompt"}
	if err := validateArgv(argv, true); err != nil {
		t.Fatalf("exact composite placeholder rejected: %v", err)
	}
	task := Task{Launches: map[string][]string{"p": argv}}
	d := &routing.Decision{Provider: "p", Model: "provider/family/model", Effort: "high"}
	out, err := expandLaunch(task, d, "r")
	want := []string{"opencode2", "run", "--standalone", "-m", "provider/family/model#high", "prompt"}
	if err != nil || !reflect.DeepEqual(out, want) {
		t.Fatalf("argv=%v error=%v", out, err)
	}
	for _, bad := range []string{"--model={model}#{effort}", "{model}#high", "prefix{model}#{effort}", "{model}#{effort}suffix"} {
		if err := validateArgv([]string{"opencode2", bad}, true); err == nil {
			t.Errorf("accepted partial placeholder %q", bad)
		}
	}
	for _, invalid := range []routing.Decision{{Provider: "p", Model: "provider/model"}, {Provider: "p", Effort: "high"}, {Provider: "p", Model: "provider/model#low", Effort: "high"}, {Provider: "p", Model: "provider/model", Effort: "high#low"}, {Provider: "p", Model: "model", Effort: "high"}} {
		if _, err := expandLaunch(task, &invalid, "r"); err == nil {
			t.Errorf("accepted invalid composite values %+v", invalid)
		}
	}
}
