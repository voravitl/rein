package routing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voravitl/rein/internal/budget"
	"github.com/voravitl/rein/internal/contract"
	"github.com/voravitl/rein/internal/ledger"
)

func calibrationWorld(t *testing.T) (*world, CalibrationInput, []byte) {
	w := newWorld(t)
	w.sel.Calibration = &contract.Calibration{Authorization: "bounded worker experiment", MaxCalls: 2, PoolCaps: map[string]float64{"codex/sub/week": 10}, MaxCashUSD: fl(0)}
	a := w.in()
	return w, CalibrationInput{Contract: w.c, Run: "run", ConfigPath: a.ConfigPath, Provider: "w-sub", Amount: 3, Calls: 1}, a.TaskProfile
}

func TestCalibrationExecutionReservesWithoutProbingAndIsSingleUse(t *testing.T) {
	w, in, raw := calibrationWorld(t)
	d, err := PrepareCalibration(in, raw)
	if err != nil {
		t.Fatal(err)
	}
	if d.SelectionMode != modeCalibration || d.Attempt == "" || d.Hold == "" {
		t.Fatalf("missing execution binding: %+v", d)
	}
	holds, err := budget.HoldsFor(d.Attempt)
	if err != nil || len(holds) != 1 || !calibrationHoldMatches(holds[0], d, w.cfg.Providers[in.Provider]) {
		t.Fatalf("reservation not bound: %+v %v", holds, err)
	}
	if w.calls.Load() != 0 || len(w.probeLog()) != 0 {
		t.Fatal("calibration preparation executed discovery/probe")
	}
	if _, err = Validate(w.c, in.Run, d.Agent, d.Model); err != nil {
		t.Fatal(err)
	}
	if err = ConsumeReceipt(d); err != nil {
		t.Fatal(err)
	}
	if err = ConsumeReceipt(d); err == nil {
		t.Fatal("receipt reused")
	}
}

func TestCalibrationExecutionRejectsDrift(t *testing.T) {
	for _, name := range []string{"disabled", "policy", "profile", "expired", "settled", "config", "hook", "review"} {
		t.Run(name, func(t *testing.T) {
			w, in, raw := calibrationWorld(t)
			if name == "disabled" {
				b, _ := os.ReadFile(in.ConfigPath)
				var cfg map[string]any
				json.Unmarshal(b, &cfg)
				cfg["model_calls_enabled"] = false
				b, _ = json.Marshal(cfg)
				os.WriteFile(in.ConfigPath, b, 0600)
				if _, err := PrepareCalibration(in, raw); err == nil || !strings.Contains(err.Error(), "disabled") {
					t.Fatalf("disabled accepted: %v", err)
				}
				if len(w.probeLog()) != 0 {
					t.Fatal("probe ran")
				}
				return
			}
			if name == "review" {
				var tp TaskProfile
				json.Unmarshal(raw, &tp)
				tp.Phase = "review"
				raw, _ = json.Marshal(tp)
				if _, err := PrepareCalibration(in, raw); err == nil {
					t.Fatal("review calibration accepted")
				}
				return
			}
			d, err := PrepareCalibration(in, raw)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "policy":
				w.sel.Calibration.MaxCalls++
			case "profile":
				os.WriteFile(filepath.Join(artifactDir(w.c.Name), "profile.json"), []byte("{}"), 0600)
			case "expired":
				exp := time.Now().Add(-time.Second)
				d.ExpiresAt = &exp
				p, _ := receipt(w.c)
				atomic(p, d)
			case "settled":
				budget.Settle(d.Hold, map[string]float64{})
			case "config":
				f, _ := os.OpenFile(in.ConfigPath, os.O_APPEND|os.O_WRONLY, 0600)
				f.WriteString("\n")
				f.Close()
			case "hook":
				w.c.HooksInstalled = nil
			}
			if _, err = Validate(w.c, in.Run, d.Agent, d.Model); err == nil {
				t.Fatal("changed receipt accepted")
			}
		})
	}
}

func TestCalibrationWorkerSettlementUsesOnlyBoundAttributableMeasurement(t *testing.T) {
	for _, name := range []string{"measured", "wrong-provider", "approximate", "wrong-component", "unused", "reserve-only"} {
		t.Run(name, func(t *testing.T) {
			w, in, raw := calibrationWorld(t)
			var d *Decision
			var h *budget.Hold
			var err error
			if name == "reserve-only" {
				h, err = ReserveCalibration(in)
			} else {
				d, err = PrepareCalibration(in, raw)
				if err == nil {
					hs, _ := budget.HoldsFor(d.Attempt)
					h = &hs[0]
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if name != "unused" {
				if d != nil {
					if err = ConsumeReceipt(d); err != nil {
						t.Fatal(err)
					}
				}
				row, e := ledger.NewChargeRow(h.Attempt, "actual-worker", "worker", "codex/sub/week", "subscription_percent", in.Provider, w.cfg.Providers[in.Provider].Model, 1)
				if e != nil {
					t.Fatal(e)
				}
				row.Run, row.Task, row.Effort, row.Config = in.Run, w.c.Name, w.cfg.Providers[in.Provider].Effort, fingerprint(w.cfg.Providers[in.Provider])
				if name == "wrong-component" {
					row.Component = "review"
				}
				if name == "wrong-provider" {
					row.Provider = "other"
				}
				if name == "approximate" {
					row.Approx = true
				}
				if e = ledger.Append(row); e != nil {
					t.Fatal(e)
				}
			}
			result, e := SettleAttempt(h.Attempt)
			if e != nil || len(result) != 1 {
				t.Fatalf("settle: %+v %v", result, e)
			}
			want := 4.0
			if name == "measured" {
				want = 1
			}
			if name == "unused" {
				want = 0
			}
			if got := result[0].Used["codex/sub/week"]; got != want {
				t.Fatalf("used=%v want=%v", got, want)
			}
		})
	}
}

func TestCalibrationWorkerMeasuredSettlementStillConsumesCaps(t *testing.T) {
	w, in, raw := calibrationWorld(t)
	w.sel.Calibration.MaxCalls = 1
	raw = w.profileJSON()
	d, err := PrepareCalibration(in, raw)
	if err != nil {
		t.Fatal(err)
	}
	if err = ConsumeReceipt(d); err != nil {
		t.Fatal(err)
	}
	row, _ := ledger.NewChargeRow(d.Attempt, "worker-measured", "worker", "codex/sub/week", "subscription_percent", in.Provider, d.Model, 1)
	row.Run, row.Task, row.Effort, row.Config = in.Run, w.c.Name, d.Effort, d.ConfigFingerprint
	if err = ledger.Append(row); err != nil {
		t.Fatal(err)
	}
	if _, err = SettleAttempt(d.Attempt); err != nil {
		t.Fatal(err)
	}
	if _, err = PrepareCalibration(in, raw); ErrCode(err) != CodeCalibrationExhausted {
		t.Fatalf("settled measured worker escaped calibration call cap: %v", err)
	}
}
