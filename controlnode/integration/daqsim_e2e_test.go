package integration

import (
	"controlnode/daqsim"
	"testing"
	"time"
)

// wantSchedule is the shipped daq001.sm autoSequence entry_sequence at its
// defaults (SEQ-IGN-LEAD=500, SEQ-CUTOFF-T=3000), pinned identically to
// controlnode/statemachine's TestShippedConfig_AutoSequenceSchedule so both
// tests break together if the two ever disagree about what the machine
// actually does.
var wantSchedule = []struct {
	tMs    float64
	refDes string
	value  float64
}{
	{0, "OV-01-CMD", 0},
	{0, "FV-01-CMD", 0},
	{0, "NV-03-CMD", 1},
	{0, "NV-04-CMD", 1},
	{500, "IG-01-CMD", 1},
	{2000, "OV-05-CMD", 1},
	{2000, "FV-03-CMD", 1},
	{3000, "OV-05-CMD", 0},
	{3000, "FV-03-CMD", 0},
	{3000, "IG-01-CMD", 0},
}

// TestNominalBurn drives autoSequence to completion against a real daqsim and
// asserts the applied set-points arrive in the shipped daq001.sm order and at
// the shipped relative times, sequence_complete echoes the runId, and the
// machine ends back in safe. This is the test that could not exist before
// daqsim: every prior assertion about "what the control node sends" was
// against a hand-written fake that could not disagree with itself.
func TestNominalBurn(t *testing.T) {
	// daq001.sm arms a LOW-pressure abort_rule too (CPT-01 < LIM-CPT01-LOW,
	// window [SEQ-IGN-LEAD, SEQ-CUTOFF-T]) — a real burn must show chamber
	// pressure rising after ignition or that is a genuine (and correct) abort.
	// daqsim's sensor default is a flat 0, so a nominal-burn test has to give
	// it a plausible in-band reading, same as an operator would need real
	// hardware reading real pressure for this burn to complete normally.
	h := newHarness(t, daqsim.NewFakeClock(), map[string]daqsim.SensorSpec{
		"CPT-01": {Base: 200}, // between LIM-CPT01-LOW (50) and LIM-CPT01-HIGH (450)
	})

	h.waitConnected(2 * time.Second)
	h.waitState("fuelSeq", "safe", 2*time.Second)
	if err := h.eng.RequestTarget("fuelSeq", "manualControl"); err != nil {
		t.Fatalf("RequestTarget manualControl: %v", err)
	}
	h.waitState("fuelSeq", "manualControl", 2*time.Second)

	if err := h.eng.RequestTarget("fuelSeq", "autoSequence"); err != nil {
		t.Fatalf("RequestTarget autoSequence: %v", err)
	}

	// Under a FakeClock the whole burn can complete in well under a
	// millisecond, so "autoSequence" can be too fleeting for a 2ms poll to
	// ever observe — wait directly for the completion transition back to
	// safe instead; the AppliedLog assertions below are what actually prove
	// autoSequence ran.
	h.waitState("fuelSeq", "safe", 2*time.Second)

	runs := h.sim.Runs()
	if len(runs) != 1 || runs[0].Outcome != "completed" || runs[0].State != "autoSequence" {
		t.Fatalf("daqsim Runs() = %+v, want one completed autoSequence run", runs)
	}
	runID := runs[0].RunID
	if runID == 0 {
		t.Error("runId was 0/unset — the control node did not stamp a runId into state_update")
	}

	log := h.sim.AppliedLog()
	var entry []daqsim.AppliedSetPoint
	for _, sp := range log {
		if sp.RunID == runID && sp.Phase == "entry" {
			entry = append(entry, sp)
		}
	}
	if len(entry) != len(wantSchedule) {
		t.Fatalf("applied %d entry step(s), want %d\ngot: %+v", len(entry), len(wantSchedule), entry)
	}
	for i, want := range wantSchedule {
		got := entry[i]
		if got.RefDes != want.refDes || got.Value != want.value || got.TMs != want.tMs {
			t.Errorf("step %d: got t=%g %s=%g, want t=%g %s=%g",
				i, got.TMs, got.RefDes, got.Value, want.tMs, want.refDes, want.value)
		}
	}
}

// TestAbort drives autoSequence with chamber pressure pre-driven above
// LIM-CPT01-HIGH (450 psia) so the first abort_rule scan trips it: daqsim
// must run the abort_sequence's exit steps locally (mains closed, igniter
// off, press valves closed) and report abort_triggered, and the control
// node's machine must land in the declared abort destination ("abort").
func TestAbort(t *testing.T) {
	h := newHarness(t, daqsim.NewFakeClock(), map[string]daqsim.SensorSpec{
		"CPT-01": {Base: 900}, // > LIM-CPT01-HIGH (450) from t=0
	})

	h.waitConnected(2 * time.Second)
	h.waitState("fuelSeq", "safe", 2*time.Second)
	if err := h.eng.RequestTarget("fuelSeq", "manualControl"); err != nil {
		t.Fatalf("RequestTarget manualControl: %v", err)
	}
	h.waitState("fuelSeq", "manualControl", 2*time.Second)
	if err := h.eng.RequestTarget("fuelSeq", "autoSequence"); err != nil {
		t.Fatalf("RequestTarget autoSequence: %v", err)
	}

	h.waitState("fuelSeq", "abort", 2*time.Second)

	runs := h.sim.Runs()
	if len(runs) != 1 || runs[0].Outcome != "aborted" {
		t.Fatalf("daqsim Runs() = %+v, want one aborted run", runs)
	}
	if runs[0].TrippedIf != "CPT-01 > 450" {
		t.Errorf("tripped by %q, want \"CPT-01 > 450\"", runs[0].TrippedIf)
	}

	wantExit := map[string]float64{
		"OV-05-CMD": 0, "FV-03-CMD": 0, "IG-01-CMD": 0, "NV-03-CMD": 0, "NV-04-CMD": 0,
	}
	got := make(map[string]float64)
	for _, sp := range h.sim.AppliedLog() {
		if sp.Phase == "exit" {
			got[sp.RefDes] = sp.Value
		}
	}
	for refDes, want := range wantExit {
		if v, ok := got[refDes]; !ok || v != want {
			t.Errorf("exit_sequence: %s = %v (ok=%v), want %v", refDes, v, ok, want)
		}
	}

	// The igniter step scheduled at t=500 (after the t=0 abort) must never
	// have been commanded — this is the failure mode a real igniter safety
	// case cares about.
	for _, sp := range h.sim.AppliedLog() {
		if sp.Phase == "entry" && sp.RefDes == "IG-01-CMD" && sp.Value == 1 {
			t.Errorf("igniter fired at t=%g despite the immediate abort", sp.TMs)
		}
	}
}

// TestStaleSequenceCompleteIgnored proves the runId correlation gap the
// protocol was widened to close (docs/websocket-protocol.md "sequence_complete",
// docs/TODO.md's LabVIEW runId item) against a REAL node connection: a
// sequence_complete carrying a runId that does not match the currently-armed
// run must be ignored, not applied as if it were the real completion.
func TestStaleSequenceCompleteIgnored(t *testing.T) {
	// See TestNominalBurn: CPT-01 needs a plausible in-band reading or the
	// shipped LOW-pressure abort_rule fires for real once its window opens.
	h := newHarness(t, daqsim.RealClock{}, map[string]daqsim.SensorSpec{
		"CPT-01": {Base: 200},
	})
	h.shrinkBurnTiming(t)

	h.waitConnected(2 * time.Second)
	h.waitState("fuelSeq", "safe", 2*time.Second)
	if err := h.eng.RequestTarget("fuelSeq", "manualControl"); err != nil {
		t.Fatal(err)
	}
	h.waitState("fuelSeq", "manualControl", 2*time.Second)
	if err := h.eng.RequestTarget("fuelSeq", "autoSequence"); err != nil {
		t.Fatal(err)
	}
	h.waitState("fuelSeq", "autoSequence", 2*time.Second)

	// Wait for the real run to be armed and its first (t=0) steps applied —
	// proof it is genuinely in flight, not a guess about timing.
	waitFor(t, time.Second, func() bool {
		state, _, ok := h.sim.LastArmed()
		return ok && state == "autoSequence"
	})
	waitFor(t, time.Second, func() bool { return len(h.sim.AppliedLog()) >= 4 })

	_, realRunID, _ := h.sim.LastArmed()
	staleRunID := realRunID + 999 // guaranteed not to match the armed run

	if err := h.sim.SendRaw(map[string]interface{}{"type": "sequence_complete", "runId": staleRunID}); err != nil {
		t.Fatalf("SendRaw stale sequence_complete: %v", err)
	}

	// The stale report must NOT move the machine off autoSequence. Give the
	// (nonexistent) bad transition a real window to have happened.
	time.Sleep(100 * time.Millisecond)
	if s, _ := h.eng.State("fuelSeq"); s != "autoSequence" {
		t.Fatalf("machine moved to %q on a stale sequence_complete (runId %d, real run %d)", s, staleRunID, realRunID)
	}

	// The real completion, with the correct runId, still applies normally.
	// The shrunk burn still takes ~2.1s of real wall time (see
	// shrinkBurnTiming) — the timeout gives it comfortable margin.
	h.waitState("fuelSeq", "safe", 4*time.Second)
}

// TestReconnectMidSequence drops the connection while the machine is mid
// flight in autoSequence: the control node must treat this as state
// uncertain, fire the declared abort destination, raise an alarm, and — the
// dangerous failure mode this whole rule exists to prevent — must NOT
// re-send the autoSequence state_update on reconnect (which would re-fire the
// igniter from t=0).
func TestReconnectMidSequence(t *testing.T) {
	h := newHarness(t, daqsim.RealClock{}, nil)
	h.shrinkBurnTiming(t)

	h.waitConnected(2 * time.Second)
	h.waitState("fuelSeq", "safe", 2*time.Second)
	if err := h.eng.RequestTarget("fuelSeq", "manualControl"); err != nil {
		t.Fatal(err)
	}
	h.waitState("fuelSeq", "manualControl", 2*time.Second)
	if err := h.eng.RequestTarget("fuelSeq", "autoSequence"); err != nil {
		t.Fatal(err)
	}
	h.waitState("fuelSeq", "autoSequence", 2*time.Second)

	// Confirm the run is genuinely in flight (t=0 steps applied, well before
	// the shrunk 150ms cutoff) before pulling the plug.
	waitFor(t, time.Second, func() bool { return len(h.sim.AppliedLog()) >= 4 })
	_, originalRunID, _ := h.sim.LastArmed()

	h.sim.DropConnection()

	// State-uncertain: the engine fires the declared abort destination. This
	// must happen well before the shrunk entry_sequence would naturally
	// finish (150ms) — proof it's the reconnect path, not a coincidental
	// completion racing the drop.
	h.waitState("fuelSeq", "abort", 500*time.Millisecond)

	// The pre-drop run keeps executing locally on daqsim regardless of the
	// link (real hardware would too) and will eventually report its own
	// completion — that's expected and fine. What must NOT happen is a
	// SECOND, freshly-armed autoSequence run: that would mean the control
	// node re-sent state_update on reconnect and re-fired the igniter.
	waitFor(t, 4*time.Second, func() bool { return len(h.sim.Runs()) >= 1 })
	time.Sleep(100 * time.Millisecond) // let a wrongly-sent re-arm have a chance to show up too

	var autoSeqRuns []daqsim.SeqRecord
	for _, r := range h.sim.Runs() {
		if r.State == "autoSequence" {
			autoSeqRuns = append(autoSeqRuns, r)
		}
	}
	if len(autoSeqRuns) != 1 || autoSeqRuns[0].RunID != originalRunID {
		t.Errorf("autoSequence runs after reconnect = %+v, want exactly the original run (runId %d) and nothing re-armed",
			autoSeqRuns, originalRunID)
	}

	igniterFires := 0
	for _, sp := range h.sim.AppliedLog() {
		if sp.RefDes == "IG-01-CMD" && sp.Value == 1 {
			igniterFires++
		}
	}
	if igniterFires > 1 {
		t.Errorf("igniter fired %d times — reconnect must not re-send state_update and re-fire it", igniterFires)
	}
}
