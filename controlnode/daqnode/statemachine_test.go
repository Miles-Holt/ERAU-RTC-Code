package daqnode

import (
	"controlnode/config"
	"encoding/json"
	"testing"
)

// fakeVars is a stand-in soft-channel store for {{VAR}} resolution.
type fakeVars map[string]float64

func (f fakeVars) Get(refDes string) (float64, bool) {
	v, ok := f[refDes]
	return v, ok
}

// testControl builds a compact but representative state machine mirroring the
// real config/control/daq001_control.yaml: safe → manualControl → autoSequence,
// with abort_triggered / sequence_complete / operator_abort transitions.
func testControl() *config.DaqControl {
	return &config.DaqControl{
		DaqNode: "DAQ001",
		Variables: map[string]string{
			"BURN_DUR": "SEQ-BURN-DUR",
			"CPT_HIGH": "LIM-CPT01-HIGH",
		},
		States: map[string]config.DaqState{
			"safe": {
				Transitions: []config.DaqTransition{
					{Target: "manualControl", On: "operator_request"},
				},
			},
			"manualControl": {
				OperatorControl: true,
				Transitions: []config.DaqTransition{
					{Target: "autoSequence", On: "operator_request"},
					{Target: "safe", On: "operator_request"},
					{Target: "abort", On: "operator_abort", ExitType: "exit"},
				},
			},
			"autoSequence": {
				EntrySequence: []config.SequenceStep{
					{T_ms: 0, RefDes: "OV-05-CMD", Value: 1, Label: "LOX main open"},
					{T_ms: "{{BURN_DUR}}", RefDes: "OV-05-CMD", Value: 0, Label: "LOX main close"},
				},
				AbortRules: []config.AbortRule{
					{If: "CPT-01 > {{CPT_HIGH}}", T_ms_on: 0, T_ms_off: 20000},
				},
				Transitions: []config.DaqTransition{
					{Target: "abort", On: "abort_triggered", ExitType: "hard_exit"},
					{Target: "safe", On: "sequence_complete", ExitType: "hard_exit"},
					{Target: "abort", On: "operator_abort", ExitType: "exit"},
				},
			},
			"abort": {
				Transitions: []config.DaqTransition{},
			},
		},
	}
}

// decodeExit unmarshals an exit/hard_exit/state_update message.
func decodeExit(t *testing.T, raw []byte) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("message not valid JSON: %v", err)
	}
	return m
}

// TestSMStartsInSafe verifies the initial state.
func TestSMStartsInSafe(t *testing.T) {
	sm := newStateMachine(testControl(), fakeVars{})
	if sm.Current() != "safe" || sm.Pending() != "safe" {
		t.Fatalf("initial state = (%q,%q), want (safe,safe)", sm.Current(), sm.Pending())
	}
}

// TestSMNilControlIsNoOp verifies a nil control makes methods safe no-ops.
func TestSMNilControlIsNoOp(t *testing.T) {
	sm := newStateMachine(nil, nil)
	if sm.Current() != "" {
		t.Errorf("nil-control Current() = %q, want empty", sm.Current())
	}
	if _, err := sm.RequestTransition("operator_request", "manualControl"); err == nil {
		t.Error("RequestTransition on nil control should error")
	}
	if _, err := sm.HandleStateReq(); err == nil {
		t.Error("HandleStateReq on nil control should error")
	}
	// sequence_complete is a benign no-op (nil,nil) rather than an error.
	if msg, err := sm.HandleSequenceComplete(); err != nil || msg != nil {
		t.Errorf("HandleSequenceComplete on nil control = (%v,%v), want (nil,nil)", msg, err)
	}
}

// TestSMOperatorRequestFlow walks the normal operator-driven transition path and
// confirms pending is set immediately but current only advances on state_req.
func TestSMOperatorRequestFlow(t *testing.T) {
	sm := newStateMachine(testControl(), fakeVars{})

	raw, err := sm.RequestTransition("operator_request", "manualControl")
	if err != nil {
		t.Fatalf("RequestTransition safe→manualControl: %v", err)
	}
	m := decodeExit(t, raw)
	if m["target"] != "manualControl" {
		t.Errorf("exit target = %v, want manualControl", m["target"])
	}
	// pending advances now; current only after the DAQ confirms with state_req.
	if sm.Pending() != "manualControl" {
		t.Errorf("pending = %q, want manualControl", sm.Pending())
	}
	if sm.Current() != "safe" {
		t.Errorf("current = %q, want safe (not yet confirmed)", sm.Current())
	}

	if _, err := sm.HandleStateReq(); err != nil {
		t.Fatalf("HandleStateReq: %v", err)
	}
	if sm.Current() != "manualControl" {
		t.Errorf("after state_req current = %q, want manualControl", sm.Current())
	}
}

// TestSMInvalidTransition verifies a transition not defined for the current state
// is rejected.
func TestSMInvalidTransition(t *testing.T) {
	sm := newStateMachine(testControl(), fakeVars{})
	// From "safe" there is no direct transition to "autoSequence".
	if _, err := sm.RequestTransition("operator_request", "autoSequence"); err == nil {
		t.Error("expected error for undefined transition safe→autoSequence")
	}
	// State must be unchanged after a rejected request.
	if sm.Pending() != "safe" {
		t.Errorf("pending changed to %q after rejected transition", sm.Pending())
	}
}

// TestSMAbortTriggered verifies the abort_triggered handler picks the configured
// target.
func TestSMAbortTriggered(t *testing.T) {
	sm := newStateMachine(testControl(), fakeVars{})
	// Advance to autoSequence.
	mustAdvance(t, sm, "operator_request", "manualControl")
	mustAdvance(t, sm, "operator_request", "autoSequence")

	raw, err := sm.HandleAbortTriggered()
	if err != nil {
		t.Fatalf("HandleAbortTriggered: %v", err)
	}
	m := decodeExit(t, raw)
	if m["target"] != "abort" {
		t.Errorf("abort target = %v, want abort", m["target"])
	}
	if m["type"] != "hard_exit" {
		t.Errorf("abort exit type = %v, want hard_exit", m["type"])
	}
	if sm.Pending() != "abort" {
		t.Errorf("pending = %q, want abort", sm.Pending())
	}
}

// TestSMSequenceComplete verifies the auto-transition on sequence_complete, and
// that a state with no such transition returns (nil, nil).
func TestSMSequenceComplete(t *testing.T) {
	sm := newStateMachine(testControl(), fakeVars{})
	mustAdvance(t, sm, "operator_request", "manualControl")
	mustAdvance(t, sm, "operator_request", "autoSequence")

	raw, err := sm.HandleSequenceComplete()
	if err != nil {
		t.Fatalf("HandleSequenceComplete: %v", err)
	}
	if raw == nil {
		t.Fatal("expected a transition message from autoSequence, got nil")
	}
	if m := decodeExit(t, raw); m["target"] != "safe" {
		t.Errorf("sequence_complete target = %v, want safe", m["target"])
	}

	// "safe" has no sequence_complete transition → nil, nil.
	sm2 := newStateMachine(testControl(), fakeVars{})
	if raw, err := sm2.HandleSequenceComplete(); err != nil || raw != nil {
		t.Errorf("HandleSequenceComplete from safe = (%v,%v), want (nil,nil)", raw, err)
	}
}

// TestSMStateUpdateResolvesVars verifies {{VAR}} references in a state_update
// resolve against the soft-channel store.
func TestSMStateUpdateResolvesVars(t *testing.T) {
	vars := fakeVars{"SEQ-BURN-DUR": 8000, "LIM-CPT01-HIGH": 550}
	sm := newStateMachine(testControl(), vars)
	mustAdvance(t, sm, "operator_request", "manualControl")
	mustAdvance(t, sm, "operator_request", "autoSequence")

	raw, err := sm.HandleStateReq() // promotes pending→current (autoSequence) and builds state_update
	if err != nil {
		t.Fatalf("HandleStateReq: %v", err)
	}

	var su struct {
		Type          string `json:"type"`
		State         string `json:"state"`
		EntrySequence []struct {
			RefDes string  `json:"refDes"`
			Value  float64 `json:"value"`
			T_ms   float64 `json:"t_ms"`
		} `json:"entry_sequence"`
		AbortRules []struct {
			RefDes string  `json:"refDes"`
			Op     string  `json:"op"`
			Value  float64 `json:"value"`
		} `json:"abort_rules"`
	}
	if err := json.Unmarshal(raw, &su); err != nil {
		t.Fatalf("state_update not valid JSON: %v", err)
	}
	if su.Type != "state_update" || su.State != "autoSequence" {
		t.Fatalf("state_update header = (%q,%q)", su.Type, su.State)
	}
	// Second entry step uses {{BURN_DUR}} = 8000.
	if len(su.EntrySequence) != 2 || su.EntrySequence[1].T_ms != 8000 {
		t.Errorf("entry_sequence[1].t_ms = %v, want 8000 (resolved {{BURN_DUR}})", su.EntrySequence)
	}
	// Abort rule "CPT-01 > {{CPT_HIGH}}" resolves to value 550, op ">".
	if len(su.AbortRules) != 1 || su.AbortRules[0].Value != 550 || su.AbortRules[0].Op != ">" || su.AbortRules[0].RefDes != "CPT-01" {
		t.Errorf("abort rule = %+v, want CPT-01 > 550", su.AbortRules)
	}
}

// TestResolveExpr covers the expression resolver directly.
func TestResolveExpr(t *testing.T) {
	vars := fakeVars{"SEQ-BURN-DUR": 8000}
	sm := newStateMachine(testControl(), vars)

	cases := []struct {
		in      interface{}
		want    float64
		wantErr bool
	}{
		{nil, 0, false},
		{int(5), 5, false},
		{float64(3.5), 3.5, false},
		{true, 1, false},
		{false, 0, false},
		{"42", 42, false},
		{"{{BURN_DUR}}", 8000, false},   // resolves via var → softchan
		{"{{UNKNOWN}}", 0, false},        // unknown var → 0, no error
		{"not-a-number", 0, true},        // unparseable → error
	}
	for _, c := range cases {
		got, err := sm.resolveExpr(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("resolveExpr(%v) err = %v, wantErr %v", c.in, err, c.wantErr)
			continue
		}
		if !c.wantErr && got != c.want {
			t.Errorf("resolveExpr(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestParseAbortIf covers the abort-condition string parser.
func TestParseAbortIf(t *testing.T) {
	refDes, op, val, err := parseAbortIf("CPT-01 > {{CPT_HIGH}}")
	if err != nil {
		t.Fatalf("parseAbortIf: %v", err)
	}
	if refDes != "CPT-01" || op != ">" || val != "{{CPT_HIGH}}" {
		t.Errorf("parseAbortIf = (%q,%q,%v), want (CPT-01,>,{{CPT_HIGH}})", refDes, op, val)
	}
	if _, _, _, err := parseAbortIf("garbage"); err == nil {
		t.Error("expected error for unparseable abort rule")
	}
}

// TestRealControlConfigParses ensures the shipped state machine config parses and
// its declared transition targets all reference defined states.
func TestRealControlConfigParses(t *testing.T) {
	cfg, err := config.ParseDir("../../config")
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	if len(cfg.DaqControls) == 0 {
		t.Skip("no DAQ control configs present")
	}
	for _, dc := range cfg.DaqControls {
		for name, st := range dc.States {
			for _, tr := range st.Transitions {
				if _, ok := dc.States[tr.Target]; !ok {
					t.Errorf("%s: state %q transitions to undefined state %q", dc.DaqNode, name, tr.Target)
				}
			}
		}
	}
}

// mustAdvance performs a transition and the follow-up state_req, failing on error.
func mustAdvance(t *testing.T, sm *stateMachine, trigger, target string) {
	t.Helper()
	if _, err := sm.RequestTransition(trigger, target); err != nil {
		t.Fatalf("RequestTransition(%q,%q): %v", trigger, target, err)
	}
	if _, err := sm.HandleStateReq(); err != nil {
		t.Fatalf("HandleStateReq after %q→%q: %v", trigger, target, err)
	}
}
