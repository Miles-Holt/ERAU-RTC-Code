package dsl

import (
	"testing"
)

func TestParser_DemoFile(t *testing.T) {
	// coldflow.sm demo file: exercises every v1 language feature
	input := `# Demo state machine: LOX cold-flow test
# Exercises every v1 language feature — see demo_walkthrough.md for how it executes.
#
# Channels used (would come from controls.yaml / .chan files):
#   OV-05-CMD, FV-02-CMD, VENT-CMD      hardware command channels on DAQ001
#   CPT-01, PT-FUEL-AVG, IGNITION-OK    sensor + computed channels
#   LIM-CPT01-HIGH, SEQ-TARGET-PRESS,   operator-settable soft channels
#   SEQ-BURN-DUR

machine coldFlow

state safe                          # first state in file = initial state
    operator                        # operator may command entry from the HMI
    sequence
        FV-02-CMD = 0
        OV-05-CMD = 0
        VENT-CMD = 1
        # no transition: machine rests here until the operator commands one

state pressurize
    operator
    controller                      # runs every engine tick while active
        if CPT-01 > LIM-CPT01-HIGH
            transition abort
    sequence                        # runs once on entry, in its own goroutine
        VENT-CMD = 0
        OV-05-CMD = 1
        wait_until PT-FUEL-AVG > SEQ-TARGET-PRESS timeout 30s -> safe
        transition fire

state fire                          # not operator-flagged: only reachable by sequence
    controller
        if CPT-01 > LIM-CPT01-HIGH
            transition abort
        if not IGNITION-OK
            transition abort
    sequence
        FV-02-CMD = 1
        sleep SEQ-BURN-DUR          # duration from a soft channel (ms)
        FV-02-CMD = 0
        transition vent

state vent
    sequence
        OV-05-CMD = 0
        VENT-CMD = 1
        wait_until PT-FUEL-AVG < 50 timeout 2m -> safe
        transition safe

state abort
    daq_local DAQ001                # compiled + cached on DAQ001 for local execution
    abort_rule CPT-01 > 850 from 0ms to 20000ms
    sequence                        # restricted subset: literal sets + sleeps only
        FV-02-CMD = 0
        OV-05-CMD = 0
        sleep 100ms
        VENT-CMD = 1
`

	lexer := NewLexer(input)
	toks, err := lexer.Tokenize()
	if err != nil {
		t.Fatalf("lexer error: %v", err)
	}

	decl, err := Parse(toks)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	machine, ok := decl.(*MachineDef)
	if !ok {
		t.Fatalf("expected MachineDef, got %T", decl)
	}

	if machine.Name != "coldFlow" {
		t.Errorf("machine name: got %q, expected coldFlow", machine.Name)
	}

	if len(machine.States) != 5 {
		t.Errorf("expected 5 states, got %d", len(machine.States))
	}

	expectedStates := []struct {
		name     string
		operator bool
		daqLocal string
		hasCtrl  bool
		hasSeq   bool
		rules    int
	}{
		{"safe", true, "", false, true, 0},
		{"pressurize", true, "", true, true, 0},
		{"fire", false, "", true, true, 0},
		{"vent", false, "", false, true, 0},
		{"abort", false, "DAQ001", false, true, 1},
	}

	for i, exp := range expectedStates {
		if i >= len(machine.States) {
			break
		}
		state := machine.States[i]

		if state.Name != exp.name {
			t.Errorf("state %d: name got %q, expected %q", i, state.Name, exp.name)
		}
		if state.Operator != exp.operator {
			t.Errorf("state %d (%s): operator got %v, expected %v", i, state.Name, state.Operator, exp.operator)
		}
		if state.DaqLocal != exp.daqLocal {
			t.Errorf("state %d (%s): daqLocal got %q, expected %q", i, state.Name, state.DaqLocal, exp.daqLocal)
		}
		if (len(state.Controller) > 0) != exp.hasCtrl {
			t.Errorf("state %d (%s): controller presence mismatch", i, state.Name)
		}
		if (len(state.Sequence) > 0) != exp.hasSeq {
			t.Errorf("state %d (%s): sequence presence mismatch", i, state.Name)
		}
		if len(state.AbortRules) != exp.rules {
			t.Errorf("state %d (%s): abort rules got %d, expected %d", i, state.Name, len(state.AbortRules), exp.rules)
		}
	}

	// Verify specific features
	// pressurize state should have a wait_until with timeout
	pressurizeSeq := machine.States[1].Sequence
	if len(pressurizeSeq) < 3 {
		t.Errorf("pressurize sequence: expected at least 3 statements, got %d", len(pressurizeSeq))
	} else if _, ok := pressurizeSeq[2].(*WaitUntilStmt); !ok {
		t.Errorf("pressurize sequence stmt 3: expected WaitUntilStmt, got %T", pressurizeSeq[2])
	}

	// fire state should have a sleep with channel reference
	fireSeq := machine.States[2].Sequence
	if len(fireSeq) < 2 {
		t.Errorf("fire sequence: expected at least 2 statements, got %d", len(fireSeq))
	} else if sleep, ok := fireSeq[1].(*SleepStmt); !ok {
		t.Errorf("fire sequence stmt 2: expected SleepStmt, got %T", fireSeq[1])
	} else if _, ok := sleep.Duration.(*IdentExpr); !ok {
		t.Errorf("sleep duration: expected IdentExpr, got %T", sleep.Duration)
	}

	// abort state should have an abort rule
	abortRules := machine.States[4].AbortRules
	if len(abortRules) > 0 {
		rule := abortRules[0]
		if rule.Channel != "CPT-01" {
			t.Errorf("abort rule channel: got %q, expected CPT-01", rule.Channel)
		}
		if rule.Op != ">" {
			t.Errorf("abort rule op: got %q, expected >", rule.Op)
		}
	}
}
