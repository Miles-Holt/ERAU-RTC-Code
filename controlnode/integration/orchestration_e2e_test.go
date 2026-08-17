package integration

import (
	"controlnode/daqsim"
	"controlnode/statemachine"
	"testing"
	"time"
)

// orchestrationMasterSrc is the owner's real orchestration shape from
// docs/dsl-guide.md: a master machine commands a subordinate into a state
// directly (not via SM-<NAME>-TARGET, and not gated by `operator`), then
// wait_untils on the subordinate's progress before continuing its own burn.
// It sleeps briefly before commanding so the daqsim connection (started
// concurrently by the harness) has time to complete — production has the
// same ordering guarantee because main.go starts the DAQ clients before any
// operator/orchestration logic can run.
const orchestrationMasterSrc = "" +
	"machine orchMaster\n" +
	"\n" +
	"state run\n" +
	"    sequence\n" +
	"        sleep 1s\n" +
	"        command daqLocalFixture -> burn\n" +
	"        wait_until machine.daqLocalFixture.state == \"burn\" timeout 5 -> failed\n" +
	"        transition watching\n" +
	"\n" +
	"state watching\n" +
	"    operator\n" +
	"\n" +
	"state failed\n" +
	"    operator\n"

// TestOrchestrationCommandDrivesSubordinate is the end-to-end shape from the
// owner's real use case: a master firing sequence commands a subordinate
// machine directly (bypassing the operator flag/gate entirely — daqLocalFixture's
// "burn" is only reachable by an operator "from idle", but orchMaster's
// `command` is not operator input and is never subject to that gate), waits
// on the subordinate's progress via `machine.<name>.state`, and continues
// once it is observed — all against a REAL daqsim connection so the
// daq_local wire protocol underneath the subordinate's burn is exercised for
// real, not faked.
//
// This uses the statemachine-level daqLocalFixture as the subordinate rather
// than a second bespoke daq_local machine: it already has a real daqsim-backed
// burn (entry_sequence, abort_rule, sequence_complete) wired through this
// exact harness (see TestNominalBurn), so reusing it keeps the orchestration
// fixture itself minimal — the only new moving part under test is `command`
// and `wait_until machine.<name>.state`. (The gate-bypass assertion itself —
// commanding into an operator-gated state an operator could not currently
// reach — is proven directly at the statemachine/engine level in
// TestEngine_CommandBypassesOperatorGate; this test's job is proving the
// same statement works end to end against a real DAQ connection.)
func TestOrchestrationCommandDrivesSubordinate(t *testing.T) {
	h := newHarnessExtra(t, daqsim.RealClock{}, map[string]daqsim.SensorSpec{
		"CPT-01": {Base: 200}, // in-band: see TestNominalBurn
	}, statemachine.Source{Name: "orch_master.sm", Text: orchestrationMasterSrc})

	h.waitConnected(5 * time.Second)
	h.waitState("daqLocalFixture", "idle", 5*time.Second)
	if s, _ := h.eng.State("orchMaster"); s != "run" {
		t.Fatalf("orchMaster initial state: got %q, want run", s)
	}

	// orchMaster's sequence: sleep 1s, then `command daqLocalFixture -> burn`.
	// wait_until machine.daqLocalFixture.state == "burn" only releases once the
	// master has actually observed the subordinate's progress, not merely that
	// the command was accepted without error.
	h.waitState("daqLocalFixture", "burn", 6*time.Second)
	h.waitState("orchMaster", "watching", 6*time.Second)

	// The burn itself must have continued and completed normally underneath —
	// orchMaster commanding it did not short-circuit the real daq_local run.
	h.waitState("daqLocalFixture", "safe", 6*time.Second)
	runs := h.sim.Runs()
	if len(runs) != 1 || runs[0].Outcome != "completed" || runs[0].State != "burn" {
		t.Fatalf("daqsim Runs() = %+v, want one completed burn run", runs)
	}
}
