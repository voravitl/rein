package supervisor

import (
	"encoding/json"
	"strings"
	"testing"
)

// A calibration worker is an owner-pinned schema-3 worker: exactly one launch
// provider and a finite positive worst case. Every other placement is refused.
func TestDAGCalibrationWorkerPlanValidation(t *testing.T) {
	g, _ := graphFixture(t, nil)
	worker := g.Tasks[0]
	worker.Kind = "worker"
	worker.ProcessPolicy = "non_detaching"
	worker.Base = strings.Repeat("a", 40)
	worker.MaxAttempts = 1
	worker.Calibration = &TaskCalibration{Amount: 2}
	p, _ := dagFixture(t, 1, map[string][]string{"A": {}})
	p.Tasks = []Task{worker}
	if _, _, err := Decode(mustJSON(p)); err != nil {
		t.Fatalf("valid calibration worker refused: %v", err)
	}
	cases := map[string]func(*Plan){
		"zero amount":     func(p *Plan) { p.Tasks[0].Calibration.Amount = 0 },
		"negative amount": func(p *Plan) { p.Tasks[0].Calibration.Amount = -1 },
		"two providers": func(p *Plan) {
			for name, argv := range p.Tasks[0].Launches {
				p.Tasks[0].Launches[name+"-2"] = argv
			}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			var clone Plan
			if err := json.Unmarshal(mustJSON(p), &clone); err != nil {
				t.Fatal(err)
			}
			change(&clone)
			if _, _, err := Decode(mustJSON(clone)); err == nil || !strings.Contains(err.Error(), "calibration") {
				t.Fatalf("invalid calibration worker accepted or wrong refusal: %v", err)
			}
		})
	}

	// An action never carries a worker's calibration.
	a, _ := dagFixture(t, 1, map[string][]string{"A": {}})
	a.Tasks[0].Calibration = &TaskCalibration{Amount: 1}
	if _, _, err := Decode(mustJSON(a)); err == nil {
		t.Fatal("action with calibration accepted")
	}

	// Calibration is a schema-3 field.
	g.Tasks[0].Calibration = &TaskCalibration{Amount: 1}
	if _, _, err := Decode(mustJSON(g)); err == nil || !strings.Contains(err.Error(), "require schema 3") {
		t.Fatalf("schema 2 calibration: want schema 3 refusal, got %v", err)
	}
}
