package statemachine

import (
	"runtime"
	"strings"
	"testing"
	"time"

	"controlnode/dsl"
)

// commandGateProgram is a two-machine orchestration fixture: master's "run"
// state commands sub into a state that IS operator-flagged but gated
// ("operator from elsewhere") in a way that would refuse an operator command
// from sub's initial state (idle). This is the whole point of `command`: it
// bypasses that gate entirely because it is not operator input. master's
// initial state is "wait", not "run" — its own sequence never fires the
// command on its own, so the test controls exactly when it does by commanding
// master into "run" itself (via the same never-dropped internal path
// NotifyAbortTriggered uses), keeping the test deterministic instead of
// racing a `sleep`/tick-count boundary against goroutine scheduling.
func commandGateProgram(t *testing.T) *Program {
	t.Helper()
	sources := []Source{
		{Name: "master.sm", Text: "" +
			"machine master\n" +
			"state wait\n" +
			"    operator\n" +
			"state run\n" +
			"    sequence\n" +
			"        command sub -> restricted\n" +
			"        transition done\n" +
			"state done\n" +
			"    operator\n"},
		{Name: "sub.sm", Text: "" +
			"machine sub\n" +
			"state idle\n" +
			"    operator\n" +
			"state restricted\n" +
			"    operator from elsewhere\n" +
			"    sequence\n" +
			"        sleep 5\n" +
			"        transition looped\n" +
			"state looped\n" +
			"    operator\n" +
			"state elsewhere\n" +
			"    operator\n"},
	}
	prog, err := Compile(sources, Options{})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return prog
}

// TestEngine_CommandBypassesOperatorGate is the central assertion of Feature
// 1's authority rule: a `command` from another machine's own logic applies
// even into a state that IS operator-flagged and gated (`operator from`) in a
// way that would refuse an operator's own RequestTarget from the machine's
// current state. Gating exists to stop a human skipping steps, not to stop a
// firing sequence driving a subordinate machine.
func TestEngine_CommandBypassesOperatorGate(t *testing.T) {
	h := startEngine(t, commandGateProgram(t), newFakeSpace(nil, nil))

	if got, _ := h.eng.State("master"); got != "wait" {
		t.Fatalf("master initial state: got %q, want wait", got)
	}
	if got, _ := h.eng.State("sub"); got != "idle" {
		t.Fatalf("sub initial state: got %q, want idle", got)
	}

	// Prove the gate is real: an OPERATOR command into "restricted" from
	// "idle" must be refused (the gate only allows "elsewhere").
	if err := h.eng.RequestTarget("sub", "restricted"); err == nil {
		t.Fatalf("RequestTarget(restricted) from idle: expected the gate to refuse this, got no error")
	} else if !strings.Contains(err.Error(), "allowed from: elsewhere") {
		t.Errorf("rejection message %q does not name the allowed gate", err.Error())
	}
	if got, _ := h.eng.State("sub"); got != "idle" {
		t.Errorf("sub moved on a rejected operator command: got %q", got)
	}

	// Now let master's sequence run and issue `command sub -> restricted`.
	h.eng.enqueuePriority(transitionReq{machine: "master", target: "run"})
	h.waitState(t, "sub", "restricted", 50)

	// The exact same transition an operator was just refused now applies
	// because it came from master's own logic, not operator input.
	if got, _ := h.eng.State("sub"); got != "restricted" {
		t.Fatalf("sub: got %q, want restricted (command must bypass the operator gate)", got)
	}
	h.waitState(t, "master", "done", 50)
	h.assertNoErrors(t)
}

// commandAbortProgram is a fixture where master commands sub2 into a
// daq_local state that is also operator-gated; sub2 then aborts (a DAQ
// report, the same priority path a real abort_rule trip uses) and must land
// correctly on the declared abort destination — proving a command-driven
// state entry participates in the engine's normal epoch/correlation
// arbitration exactly like a RequestTarget-driven one.
func commandAbortProgram(t *testing.T) *Program {
	t.Helper()
	sources := []Source{
		{Name: "master2.sm", Text: "" +
			"machine master2\n" +
			"state wait\n" +
			"    operator\n" +
			"state run\n" +
			"    sequence\n" +
			"        command sub2 -> armed\n" +
			"        transition done\n" +
			"state done\n" +
			"    operator\n"},
		{Name: "sub2.sm", Text: "" +
			"machine sub2\n" +
			"state idle\n" +
			"    operator\n" +
			"state armed\n" +
			"    operator from elsewhere\n" +
			"    daq_local DAQ001\n" +
			"    abort_rule CPT-01 > 850 from 0ms to 1000ms\n" +
			"    sequence\n" +
			"        IGN-CMD = 1\n" +
			"    abort_sequence\n" +
			"        IGN-CMD = 0\n" +
			"        transition idle\n" +
			"state elsewhere\n" +
			"    operator\n"},
	}
	prog, err := Compile(sources, Options{KnownChannels: []string{"IGN-CMD", "CPT-01"}})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return prog
}

// TestEngine_CommandThenAbortRespectsEpoch covers "a command into a machine
// that then aborts still respects epoch arbitration": the state entry caused
// by `command` must arm the same (state, epoch, runId) correlation record any
// other entry does, so a subsequent DAQ-reported abort resolves to the state's
// declared destination rather than being misapplied or ignored as stale.
func TestEngine_CommandThenAbortRespectsEpoch(t *testing.T) {
	h := startEngine(t, commandAbortProgram(t), newFakeSpace(
		map[string]float64{"IGN-CMD": 0, "CPT-01": 100}, nil))

	h.eng.enqueuePriority(transitionReq{machine: "master2", target: "run"})
	h.waitState(t, "sub2", "armed", 50)

	if err := h.eng.NotifyAbortTriggered("sub2"); err != nil {
		t.Fatalf("NotifyAbortTriggered after a command-driven entry: unexpected error %v", err)
	}
	h.waitState(t, "sub2", "idle", 50)
	h.assertNoErrors(t)
}

// TestEngine_CommandMachine_DefensiveErrors exercises CommandMachine's own
// error returns directly. Compile-time checking (statemachine/compile.go's
// checkCommands) means a real `.sm` program can never reach these paths, but
// the runtime method stays defensive rather than assuming that invariant.
func TestEngine_CommandMachine_DefensiveErrors(t *testing.T) {
	prog := loadColdflow(t)
	h := startEngine(t, prog, coldflowSpace(0.05))

	if err := h.eng.CommandMachine("coldFlow", "ghost", "safe"); err == nil {
		t.Errorf("CommandMachine to an unknown machine: expected an error")
	} else if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error %q does not name the unknown machine", err.Error())
	}

	if err := h.eng.CommandMachine("coldFlow", "coldFlow", "nowhere"); err == nil {
		t.Errorf("CommandMachine to an unknown state: expected an error")
	} else if !strings.Contains(err.Error(), "nowhere") {
		t.Errorf("error %q does not name the unknown state", err.Error())
	}
}

// TestEngine_CommandFailureSurfacesError proves "failure is loud": a command
// that cannot be applied at runtime goes through the engine's error path
// (OnError, which the integration layer raises as an operator alert), never a
// silent no-op. The fixture is hand-built rather than Compile()'d: a real
// `.sm` file can never contain a command targeting a nonexistent machine
// (checkCommands rejects that at compile time), so this constructs the AST
// directly to exercise the runtime defensive path a corrupted or
// programmatically-built program could still hit.
func TestEngine_CommandFailureSurfacesError(t *testing.T) {
	running := &State{
		Name:  "running",
		Index: 0,
		Sequence: []dsl.Stmt{
			&dsl.CommandStmt{Machine: "ghost", Target: "somewhere", LineNo: 1},
		},
	}
	master := &Machine{
		Name:   "master3",
		Source: "master3.sm",
		States: []*State{running},
		byName: map[string]*State{"running": running},
	}
	master.Initial = running
	prog := &Program{
		Machines: []*Machine{master},
		byName:   map[string]*Machine{"master3": master},
	}

	h := startEngine(t, prog, newFakeSpace(nil, nil))

	// The sequence runs in its own goroutine from the moment the initial
	// state is entered (before any clock tick), so wait for it rather than
	// assuming a fixed number of ticks has let it run.
	var err error
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err = <-h.errs:
		default:
			h.clock.Tick()
			runtime.Gosched()
			continue
		}
		break
	}
	if err == nil {
		t.Fatalf("expected a command failure to surface via OnError")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error %q does not mention the unknown target machine", err.Error())
	}
	// The command failing must not have wedged or crashed the machine.
	if got, _ := h.eng.State("master3"); got != "running" {
		t.Errorf("state after command failure: got %q, want running", got)
	}
}
