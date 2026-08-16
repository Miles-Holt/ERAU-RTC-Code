package statemachine

import (
	"fmt"
	"testing"
)

// daq001Path is the machine that actually runs the stand.
const daq001Path = "../../config/machines/daq001.sm"

// shippedDefaults are the defaults declared in config/channels/softchannels.chan.
// They are duplicated here on purpose: if someone changes a default, this test
// should fail and make them re-read the schedule it produces.
var shippedDefaults = map[string]float64{
	"SEQ-IGN-LEAD":   500,
	"SEQ-CUTOFF-T":   3000,
	"LIM-CPT01-HIGH": 450,
	"LIM-CPT01-LOW":  50,
}

func loadDaq001(t *testing.T) *Machine {
	t.Helper()
	prog, err := LoadFiles([]string{daq001Path}, Options{})
	if err != nil {
		t.Fatalf("load %s: %v", daq001Path, err)
	}
	m, ok := prog.Machine("fuelSeq")
	if !ok {
		t.Fatalf("machine fuelSeq missing from %s", daq001Path)
	}
	return m
}

func resolveAutoSequence(t *testing.T, values map[string]float64) *DaqStateUpdate {
	t.Helper()
	m := loadDaq001(t)
	updates, err := m.DaqStateUpdates(&mockReader{values: values})
	if err != nil {
		t.Fatalf("resolve daq_local payloads: %v", err)
	}
	for _, u := range updates["DAQ001"] {
		if u.State == "autoSequence" {
			return u
		}
	}
	t.Fatalf("no autoSequence payload for DAQ001 (got %d payload(s))", len(updates["DAQ001"]))
	return nil
}

// firstTime returns the t_ms of the first step setting refDes to value.
func firstTime(t *testing.T, steps []DaqStep, refDes string, value float64) float64 {
	t.Helper()
	for _, s := range steps {
		if s.RefDes == refDes && s.Value == value {
			return s.TMs
		}
	}
	t.Fatalf("no step sets %s = %g\nsteps: %s", refDes, value, formatSteps(steps))
	return 0
}

func formatSteps(steps []DaqStep) string {
	out := ""
	for _, s := range steps {
		out += fmt.Sprintf("\n  t=%-6g %s = %g", s.TMs, s.RefDes, s.Value)
	}
	return out
}

// TestShippedConfig_AutoSequenceSchedule pins the resolved hot-fire schedule of
// the real daq001.sm.
//
// This is the test the restructure needed and did not have: the first port of
// this machine converted the old YAML's ABSOLUTE t_ms values into sequential
// sleeps as if they were deltas, which moved the igniter from 1500 ms BEFORE
// the main valves to 500 ms AFTER them — propellant into an unlit chamber —
// and stretched the burn from 1000 ms to 3500 ms. Every unit test still passed,
// because nothing resolved the shipped config.
func TestShippedConfig_AutoSequenceSchedule(t *testing.T) {
	p := resolveAutoSequence(t, shippedDefaults)

	want := []DaqStep{
		{TMs: 0, RefDes: "OV-01-CMD", Value: 0},    // LOX vent close
		{TMs: 0, RefDes: "FV-01-CMD", Value: 0},    // Fuel vent close
		{TMs: 0, RefDes: "NV-03-CMD", Value: 1},    // LOX press open
		{TMs: 0, RefDes: "NV-04-CMD", Value: 1},    // Fuel press open
		{TMs: 500, RefDes: "IG-01-CMD", Value: 1},  // Igniter fire   (t = IGN_LEAD)
		{TMs: 2000, RefDes: "OV-05-CMD", Value: 1}, // LOX main open
		{TMs: 2000, RefDes: "FV-03-CMD", Value: 1}, // Fuel main open
		{TMs: 3000, RefDes: "OV-05-CMD", Value: 0}, // LOX main close (t = CUTOFF_T)
		{TMs: 3000, RefDes: "FV-03-CMD", Value: 0}, // Fuel main close
		{TMs: 3000, RefDes: "IG-01-CMD", Value: 0}, // Igniter off
	}
	if len(p.EntrySequence) != len(want) {
		t.Fatalf("entry_sequence has %d step(s), want %d%s",
			len(p.EntrySequence), len(want), formatSteps(p.EntrySequence))
	}
	for i, w := range want {
		if got := p.EntrySequence[i]; got != w {
			t.Errorf("entry step %d: got t=%g %s=%g, want t=%g %s=%g",
				i, got.TMs, got.RefDes, got.Value, w.TMs, w.RefDes, w.Value)
		}
	}

	// The abort sequence the node runs locally must close the mains, kill the
	// igniter and shut the press valves — all at t=0, no waiting.
	wantExit := []DaqStep{
		{TMs: 0, RefDes: "OV-05-CMD", Value: 0},
		{TMs: 0, RefDes: "FV-03-CMD", Value: 0},
		{TMs: 0, RefDes: "IG-01-CMD", Value: 0},
		{TMs: 0, RefDes: "NV-03-CMD", Value: 0},
		{TMs: 0, RefDes: "NV-04-CMD", Value: 0},
	}
	if len(p.ExitSequence) != len(wantExit) {
		t.Fatalf("exit_sequence has %d step(s), want %d%s",
			len(p.ExitSequence), len(wantExit), formatSteps(p.ExitSequence))
	}
	for i, w := range wantExit {
		if got := p.ExitSequence[i]; got != w {
			t.Errorf("exit step %d: got t=%g %s=%g, want t=%g %s=%g",
				i, got.TMs, got.RefDes, got.Value, w.TMs, w.RefDes, w.Value)
		}
	}

	wantRules := []DaqAbortRule{
		{If: "CPT-01 > 450", TMsOn: 0, TMsOff: 20000},
		{If: "CPT-01 < 50", TMsOn: 500, TMsOff: 3000},
	}
	if len(p.AbortRules) != len(wantRules) {
		t.Fatalf("abort_rules: got %d, want %d: %+v", len(p.AbortRules), len(wantRules), p.AbortRules)
	}
	for i, w := range wantRules {
		if got := p.AbortRules[i]; got != w {
			t.Errorf("abort rule %d: got %+v, want %+v", i, got, w)
		}
	}
}

// TestShippedConfig_IgnitionOrdering asserts the ordering invariants that make
// the sequence survivable, across the operator-settable range rather than only
// at the defaults. These must hold for ANY setpoints the operator dials in.
func TestShippedConfig_IgnitionOrdering(t *testing.T) {
	// min/max come from config/channels/softchannels.chan.
	cases := []struct{ ignLead, cutoff float64 }{
		{500, 3000},  // defaults
		{500, 10000}, // longest burn
		{500, 2500},  // shortest sane burn
		{0, 3000},    // igniter at t=0
		{2000, 3000}, // igniter simultaneous with mains (boundary)
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("lead=%g_cutoff=%g", c.ignLead, c.cutoff), func(t *testing.T) {
			vals := map[string]float64{
				"SEQ-IGN-LEAD":   c.ignLead,
				"SEQ-CUTOFF-T":   c.cutoff,
				"LIM-CPT01-HIGH": 450,
				"LIM-CPT01-LOW":  50,
			}
			p := resolveAutoSequence(t, vals)
			steps := p.EntrySequence

			ignOn := firstTime(t, steps, "IG-01-CMD", 1)
			loxOpen := firstTime(t, steps, "OV-05-CMD", 1)
			fuelOpen := firstTime(t, steps, "FV-03-CMD", 1)
			loxClose := firstTime(t, steps, "OV-05-CMD", 0)
			fuelClose := firstTime(t, steps, "FV-03-CMD", 0)
			ignOff := firstTime(t, steps, "IG-01-CMD", 0)

			// The igniter must be lit no later than the propellant arriving.
			if ignOn > loxOpen || ignOn > fuelOpen {
				t.Errorf("igniter at t=%g fires AFTER mains (LOX %g, fuel %g): "+
					"propellant would enter an unlit chamber", ignOn, loxOpen, fuelOpen)
			}
			// Both mains open together, and close together at cutoff.
			if loxOpen != fuelOpen {
				t.Errorf("mains open at different times: LOX %g, fuel %g", loxOpen, fuelOpen)
			}
			if loxClose != fuelClose {
				t.Errorf("mains close at different times: LOX %g, fuel %g", loxClose, fuelClose)
			}
			// Cutoff is absolute, and the burn has positive length.
			if loxClose != c.cutoff {
				t.Errorf("cutoff at t=%g, want the absolute SEQ-CUTOFF-T %g", loxClose, c.cutoff)
			}
			if loxClose <= loxOpen {
				t.Errorf("burn length is %g ms (open %g, close %g): not a positive burn",
					loxClose-loxOpen, loxOpen, loxClose)
			}
			// The igniter is not left energised past cutoff.
			if ignOff > loxClose {
				t.Errorf("igniter off at t=%g, after cutoff %g", ignOff, loxClose)
			}
		})
	}
}

// TestShippedConfig_CutoffBeforeMainsRefused guards the send-time ordering
// check: a cutoff earlier than the main-valve opening would need a negative
// sleep, which must be refused rather than silently reordering the burn.
func TestShippedConfig_CutoffBeforeMainsRefused(t *testing.T) {
	m := loadDaq001(t)
	_, err := m.DaqStateUpdates(&mockReader{values: map[string]float64{
		"SEQ-IGN-LEAD":   500,
		"SEQ-CUTOFF-T":   1500, // before the mains open at 2000
		"LIM-CPT01-HIGH": 450,
		"LIM-CPT01-LOW":  50,
	}})
	if err == nil {
		t.Fatal("a cutoff before the mains open resolved successfully; " +
			"the negative-sleep guard did not fire")
	}
}
