package statemachine

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"
)

// standMachinePath is the machine that actually runs the stand. It is live
// config, not a fixture: these tests exist to make a change to the real firing
// sequence fail loudly rather than ship quietly. That coupling is deliberate,
// but it does mean renaming the file breaks the suite — which is exactly what
// happened when daq001.sm became engineControl.sm. Grammar-level coverage
// belongs in dsl/testdata/smokeTest.sm instead, so this file can stay about
// the sequence's behaviour rather than the parser's.
const standMachinePath = "../../config/machines/engineControl.sm"

// shippedDefaults are the defaults declared in config/channels/engineChannels.chan.
// They are duplicated here on purpose: if someone changes a default, this test
// should fail and make them re-read the schedule it produces.  Values are in
// the DSL's base time unit (seconds), matching engineChannels.chan.
var shippedDefaults = map[string]float64{
	"SEQ-IGN-LEAD":   0.5,
	"SEQ-CUTOFF-T":   3.0,
	"LIM-CPT01-HIGH": 450,
	"LIM-CPT01-LOW":  50,
}

func loadFiringSequence(t *testing.T) *Machine {
	t.Helper()
	prog, err := LoadFiles([]string{standMachinePath}, Options{})
	if err != nil {
		t.Fatalf("load %s: %v", standMachinePath, err)
	}
	m, ok := prog.Machine("firingSequence")
	if !ok {
		t.Fatalf("machine firingSequence missing from %s", standMachinePath)
	}
	return m
}

// timedWrite is one recorded channel write, timestamped in engine-elapsed
// seconds — the DSL's base time unit.  (There is no DAQ wire payload to
// inspect any more: autoSequence runs entirely control-node side, sequenced
// by the engine's own tick loop, so this is the closest analogue to the old
// daq_local payload's cumulative t_ms.)
type timedWrite struct {
	TSec   float64
	RefDes string
	Value  float64
}

func formatTimedWrites(ws []timedWrite) string {
	out := ""
	for _, w := range ws {
		out += fmt.Sprintf("\n  t=%-6g %s = %g", w.TSec, w.RefDes, w.Value)
	}
	return out
}

// runFiringSequenceResult carries everything a test needs out of one drive of
// the real daq001.sm through the engine.
type runFiringSequenceResult struct {
	writes []timedWrite
	states []string // "state" entries in order (initial state included)
}

// driveFiringSequence loads the real daq001.sm, starts it on an engine ticking
// at tickHz, commands autoSequence, and runs until postTest is reached or
// maxTicks elapse — whichever first. values seeds the channel space (soft
// channel operator settings + CPT-01); CYCLE_TIME is provided at 1/tickHz, the
// same value main.go's RegisterCycleTimeChannel publishes in production.
func driveFiringSequence(t *testing.T, values map[string]float64, tickHz, maxTicks int) runFiringSequenceResult {
	t.Helper()
	loadFiringSequence(t) // fails fast with a clear message if the machine is missing/renamed

	prog, err := LoadFiles([]string{standMachinePath}, Options{})
	if err != nil {
		t.Fatalf("load %s: %v", standMachinePath, err)
	}

	seed := map[string]float64{
		"CYCLE_TIME": 1.0 / float64(tickHz),
		"CPT-01":     200, // between LIM-CPT01-LOW and LIM-CPT01-HIGH: no abort
	}
	for k, v := range values {
		seed[k] = v
	}
	space := newFakeSpace(seed, nil)

	log := &stateLog{}
	clock := NewManualClock()
	eng, err := New(Config{
		Program:       prog,
		Reader:        space,
		Writer:        space,
		Clock:         clock,
		TickHz:        tickHz,
		OnStateChange: log.record,
	})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		eng.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		clock.Stop()
		<-done
	})

	// The gate graph is safe -> manualControl -> autoSequence: an operator
	// cannot command autoSequence directly from safe.
	if err := eng.RequestTarget("firingSequence", "manualControl"); err != nil {
		t.Fatalf("RequestTarget(manualControl): %v", err)
	}
	waitForState(t, eng, "firingSequence", "manualControl")
	// Discard every write made before autoSequence starts (the "safe" state's
	// own safing sequence, entered at engine startup): only autoSequence's and
	// postTest's writes matter to the schedule under test, and T-TIME itself
	// only starts counting from this point (autoSequence's sequence resets it
	// to 0 on entry).  The baseline MUST be taken before the transition is
	// requested: autoSequence's leading assignments (before its first
	// wait_until) need no tick at all and can complete before a poll notices
	// the state change, so capturing "seen" after waitForState can race and
	// silently swallow them.
	var recorded []timedWrite
	seen := len(space.snapshotWrites())

	if err := eng.RequestTarget("firingSequence", "autoSequence"); err != nil {
		t.Fatalf("RequestTarget(autoSequence): %v", err)
	}
	waitForState(t, eng, "firingSequence", "autoSequence")
	for i := 0; i < maxTicks; i++ {
		clock.Tick()
		// Give the sequence goroutine, woken by this tick's poke, a chance to
		// run before the next tick advances the clock further.
		runtime.Gosched()
		time.Sleep(50 * time.Microsecond)

		elapsed := float64(i+1) / float64(tickHz)
		ws := space.snapshotWrites()
		for _, w := range ws[seen:] {
			recorded = append(recorded, timedWrite{TSec: elapsed, RefDes: w.RefDes, Value: w.Value})
		}
		seen = len(ws)

		if cur, _ := eng.State("firingSequence"); cur == "postTest" {
			// A couple more ticks lets postTest's own (t=0) entry sequence land.
			for j := 0; j < 3; j++ {
				clock.Tick()
				runtime.Gosched()
				time.Sleep(50 * time.Microsecond)
			}
			ws = space.snapshotWrites()
			for _, w := range ws[seen:] {
				recorded = append(recorded, timedWrite{TSec: elapsed, RefDes: w.RefDes, Value: w.Value})
			}
			seen = len(ws)
			break
		}
	}

	return runFiringSequenceResult{writes: recorded, states: log.snapshot()}
}

// waitForState polls until the machine reaches want, or fails the test.
func waitForState(t *testing.T, eng *Engine, machine, want string) {
	t.Helper()
	for i := 0; i < 2000; i++ {
		if cur, _ := eng.State(machine); cur == want {
			return
		}
		runtime.Gosched()
		time.Sleep(50 * time.Microsecond)
	}
	cur, _ := eng.State(machine)
	t.Fatalf("machine %q: state %q not reached within the poll budget (currently %q)", machine, want, cur)
}

// firstTime returns the elapsed seconds of the first write setting refDes to
// value.
func firstTime(t *testing.T, ws []timedWrite, refDes string, value float64) float64 {
	t.Helper()
	for _, w := range ws {
		if w.RefDes == refDes && w.Value == value {
			return w.TSec
		}
	}
	t.Fatalf("no write sets %s = %g\nwrites: %s", refDes, value, formatTimedWrites(ws))
	return 0
}

// approxEqual compares two seconds values with a tolerance of a couple of
// tick periods, since a write is only observable at tick granularity.
func approxEqual(a, b, tol float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= tol
}

// TestShippedConfig_AutoSequenceSchedule pins the resolved hot-fire schedule of
// the real daq001.sm, run through the actual engine (autoSequence has no
// daq_local any more — the sequence executes control-node side, gated by
// `controller` if-statements instead of a DAQ-side abort_rule).
//
// This is the test the restructure needed and did not have: the first port of
// this machine converted the old YAML's ABSOLUTE t_ms values into sequential
// sleeps as if they were deltas, which moved the igniter from 1500 ms BEFORE
// the main valves to 500 ms AFTER them — propellant into an unlit chamber —
// and stretched the burn from 1000 ms to 3500 ms. Every unit test still passed,
// because nothing resolved the shipped config.
func TestShippedConfig_AutoSequenceSchedule(t *testing.T) {
	const tickHz = 1000 // 1 ms resolution
	res := driveFiringSequence(t, shippedDefaults, tickHz, 3200)

	const tol = 3.0 / tickHz // a few tick periods of slack

	check := func(refDes string, value, wantSec float64) {
		t.Helper()
		got := firstTime(t, res.writes, refDes, value)
		if !approxEqual(got, wantSec, tol) {
			t.Errorf("%s = %g first written at t=%g s, want ~%g s", refDes, value, got, wantSec)
		}
	}

	// t = 0: vents shut, press valves open.
	check("OV-01-CMD", 0, 0) // LOX vent close
	check("FV-01-CMD", 0, 0) // Fuel vent close
	check("NV-03-CMD", 1, 0) // LOX press open
	check("NV-04-CMD", 1, 0) // Fuel press open

	// t = SEQ-IGN-LEAD: igniter fires.
	check("IG-01-CMD", 1, 0.5)

	// t = 2 s: mains open.
	check("OV-05-CMD", 1, 2)
	check("FV-03-CMD", 1, 2)

	// t = SEQ-CUTOFF-T: everything off.
	check("OV-05-CMD", 0, 3)
	check("FV-03-CMD", 0, 3)
	check("IG-01-CMD", 0, 3)

	// The nominal run completes into postTest, not straight back to safe.
	if len(res.states) == 0 || res.states[len(res.states)-1] != "firingSequence:postTest" {
		t.Fatalf("state changes: got %v, want the last entry to be firingSequence:postTest", res.states)
	}

	// postTest closes the press valves (they have been open since t=0 and
	// would otherwise keep pressurising the tanks indefinitely).
	check("NV-03-CMD", 0, 3) // approx: right after cutoff, on postTest entry
	check("NV-04-CMD", 0, 3)
}

// TestShippedConfig_IgnitionOrdering asserts the ordering invariants that make
// the sequence survivable, across the operator-settable range rather than only
// at the defaults. These must hold for ANY setpoints the operator dials in.
func TestShippedConfig_IgnitionOrdering(t *testing.T) {
	// min/max come from config/channels/engineChannels.chan (seconds).
	cases := []struct{ ignLead, cutoff float64 }{
		{0.5, 3.0},  // defaults
		{0.5, 10.0}, // longest burn
		{0.5, 2.5},  // shortest sane burn
		{0, 3.0},    // igniter at t=0
		{2.0, 3.0},  // igniter simultaneous with mains (boundary)
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("lead=%g_cutoff=%g", c.ignLead, c.cutoff), func(t *testing.T) {
			const tickHz = 1000
			vals := map[string]float64{
				"SEQ-IGN-LEAD":   c.ignLead,
				"SEQ-CUTOFF-T":   c.cutoff,
				"LIM-CPT01-HIGH": 450,
				"LIM-CPT01-LOW":  50,
			}
			maxTicks := int(c.cutoff*float64(tickHz)) + 500
			res := driveFiringSequence(t, vals, tickHz, maxTicks)
			ws := res.writes

			ignOn := firstTime(t, ws, "IG-01-CMD", 1)
			loxOpen := firstTime(t, ws, "OV-05-CMD", 1)
			fuelOpen := firstTime(t, ws, "FV-03-CMD", 1)
			loxClose := firstTime(t, ws, "OV-05-CMD", 0)
			fuelClose := firstTime(t, ws, "FV-03-CMD", 0)
			ignOff := firstTime(t, ws, "IG-01-CMD", 0)

			const tol = 3.0 / tickHz

			// The igniter must be lit no later than the propellant arriving.
			if ignOn > loxOpen+tol || ignOn > fuelOpen+tol {
				t.Errorf("igniter at t=%g fires AFTER mains (LOX %g, fuel %g): "+
					"propellant would enter an unlit chamber", ignOn, loxOpen, fuelOpen)
			}
			// Both mains open together, and close together at cutoff.
			if !approxEqual(loxOpen, fuelOpen, tol) {
				t.Errorf("mains open at different times: LOX %g, fuel %g", loxOpen, fuelOpen)
			}
			if !approxEqual(loxClose, fuelClose, tol) {
				t.Errorf("mains close at different times: LOX %g, fuel %g", loxClose, fuelClose)
			}
			// Main-close must never precede its main-open.
			if loxClose < loxOpen-tol {
				t.Errorf("LOX main closes (t=%g) before it opens (t=%g)", loxClose, loxOpen)
			}
			if fuelClose < fuelOpen-tol {
				t.Errorf("fuel main closes (t=%g) before it opens (t=%g)", fuelClose, fuelOpen)
			}
			// Cutoff is absolute, and the burn has positive length.
			if !approxEqual(loxClose, c.cutoff, tol) {
				t.Errorf("cutoff at t=%g, want the absolute SEQ-CUTOFF-T %g", loxClose, c.cutoff)
			}
			if loxClose <= loxOpen {
				t.Errorf("burn length is %g s (open %g, close %g): not a positive burn",
					loxClose-loxOpen, loxOpen, loxClose)
			}
			// The igniter is not left energised past cutoff.
			if ignOff > loxClose+tol {
				t.Errorf("igniter off at t=%g, after cutoff %g", ignOff, loxClose)
			}
		})
	}
}

// TestShippedConfig_CutoffBeforeMainsRefused guards the wait_until schedule:
// a cutoff earlier than the main-valve opening must not command a main close
// before that main ever opened.  With `wait_until T-TIME > SEQ-CUTOFF-T`, an
// operator-set cutoff before t=2s simply means the "close" step waits until
// SEQ-CUTOFF-T is reached like everything else — since T-TIME only increases,
// it then waits until AFTER the mains open, never closing them early.  This
// pins that self-consistent behaviour (replacing the old daq_local
// negative-sleep guard, which no longer applies: this state has no daq_local
// send-time resolution any more).
func TestShippedConfig_CutoffBeforeMainsRefused(t *testing.T) {
	const tickHz = 1000
	vals := map[string]float64{
		"SEQ-IGN-LEAD":   0.5,
		"SEQ-CUTOFF-T":   1.5, // before the mains open at 2 s
		"LIM-CPT01-HIGH": 450,
		"LIM-CPT01-LOW":  50,
	}
	// SEQ-CUTOFF-T=1.5s never exceeds T-TIME's path past the mains-open sleep
	// until T-TIME actually passes 1.5s, which happens at t=1.5s — BEFORE the
	// unconditional "sleep 2 - SEQ-IGN-LEAD" resolves at t=2s. So
	// "wait_until T-TIME > SEQ-CUTOFF-T" is already true the instant the mains
	// open, and the close steps fire immediately after — never before.
	res := driveFiringSequence(t, vals, tickHz, 4000)
	ws := res.writes

	loxOpen := firstTime(t, ws, "OV-05-CMD", 1)
	loxClose := firstTime(t, ws, "OV-05-CMD", 0)
	const tol = 3.0 / tickHz
	if loxClose < loxOpen-tol {
		t.Fatalf("main closed (t=%g) before it opened (t=%g): a cutoff earlier than "+
			"the main-valve opening produced a close-before-open ordering", loxClose, loxOpen)
	}
}
