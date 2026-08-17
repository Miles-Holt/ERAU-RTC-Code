package statemachine

import (
	"encoding/json"
	"reflect"
	"testing"
)

// goldenAbortPayload is the payload documented in
// docs/restructure/demo/demo_walkthrough.md for coldflow.sm's abort state.
const goldenAbortPayload = `{
  "type": "state_update",
  "state": "abort",
  "runId": 0,
  "entry_sequence": [
    { "t_ms": 0,   "refDes": "FV-02-CMD", "value": 0 },
    { "t_ms": 0,   "refDes": "OV-05-CMD", "value": 0 },
    { "t_ms": 100, "refDes": "VENT-CMD",  "value": 1 }
  ],
  "exit_sequence": [
    { "t_ms": 0,   "refDes": "FV-02-CMD", "value": 0 },
    { "t_ms": 0,   "refDes": "OV-05-CMD", "value": 0 },
    { "t_ms": 0,   "refDes": "VENT-CMD",  "value": 1 }
  ],
  "abort_rules": [
    { "if": "CPT-01 > 850", "t_ms_on": 0, "t_ms_off": 20000 }
  ]
}`

func TestDaqLocal_AbortGolden(t *testing.T) {
	prog := loadColdflow(t)
	m, _ := prog.Machine("coldFlow")
	_, ok := m.State("abort")
	if !ok {
		t.Fatalf("abort state missing")
	}

	// Resolve at send-time using a mock reader
	reader := &mockReader{values: map[string]float64{}}
	updates, err := m.DaqStateUpdates(reader)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if len(updates["DAQ001"]) == 0 {
		t.Fatalf("abort has no DAQ payload")
	}
	payload := updates["DAQ001"][0]

	if payload.Node != "DAQ001" {
		t.Errorf("node: got %q, want DAQ001", payload.Node)
	}

	got, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var gotAny, wantAny interface{}
	if err := json.Unmarshal(got, &gotAny); err != nil {
		t.Fatalf("unmarshal got: %v", err)
	}
	if err := json.Unmarshal([]byte(goldenAbortPayload), &wantAny); err != nil {
		t.Fatalf("unmarshal golden: %v", err)
	}
	if !reflect.DeepEqual(gotAny, wantAny) {
		t.Errorf("DAQ payload mismatch\n got: %s\nwant: %s", got, goldenAbortPayload)
	}
}

func TestDaqLocal_TimeAccumulation(t *testing.T) {
	src := Source{Name: "seq.sm", Text: "" +
		"machine seq\n" +
		"state fireLocal\n" +
		"    daq_local DAQ002\n" +
		"    abort_rule CPT-01 >= 12.5 from 100ms to 2s\n" +
		"    sequence\n" +
		"        A-CMD = 1\n" +
		"        sleep 250ms\n" +
		"        B-CMD = true\n" +
		"        sleep 1s\n" +
		"        C-CMD = 0\n" +
		"        D-CMD = -3.5\n" +
		"    abort_sequence\n" +
		"        A-CMD = 0\n" +
		"        transition safed\n" +
		"state safed\n"}

	prog, err := Compile([]Source{src}, Options{})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	m, _ := prog.Machine("seq")

	// Resolve at send-time using a mock reader
	reader := &mockReader{values: map[string]float64{}}
	updates, err := m.DaqStateUpdates(reader)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if len(updates["DAQ002"]) == 0 {
		t.Fatalf("no payload")
	}
	p := updates["DAQ002"][0]

	wantSteps := []DaqStep{
		{TMs: 0, RefDes: "A-CMD", Value: 1},
		{TMs: 250, RefDes: "B-CMD", Value: 1},
		{TMs: 1250, RefDes: "C-CMD", Value: 0},
		{TMs: 1250, RefDes: "D-CMD", Value: -3.5},
	}
	if !reflect.DeepEqual(p.EntrySequence, wantSteps) {
		t.Errorf("entry_sequence:\n got %+v\nwant %+v", p.EntrySequence, wantSteps)
	}

	wantRules := []DaqAbortRule{{If: "CPT-01 >= 12.5", TMsOn: 100, TMsOff: 2000}}
	if !reflect.DeepEqual(p.AbortRules, wantRules) {
		t.Errorf("abort_rules:\n got %+v\nwant %+v", p.AbortRules, wantRules)
	}
}

func TestDaqLocal_SendTimeResolution(t *testing.T) {
	// Test that identifiers in daq_local sequences are resolved at send-time
	src := Source{Name: "seq.sm", Text: "" +
		"machine seq\n" +
		"state burn\n" +
		"    daq_local DAQ001\n" +
		"    abort_rule PRESS > LIMIT-HIGH from START-MS to BURN-DUR\n" +
		"    sequence\n" +
		"        IGN-CMD = 1\n" +
		"        sleep IGNITE-DELAY\n" +
		"        MAIN-CMD = 1\n" +
		"        sleep BURN-DUR\n" +
		"        IGN-CMD = 0\n" +
		"        MAIN-CMD = 0\n" +
		"    abort_sequence\n" +
		"        IGN-CMD = 0\n" +
		"        MAIN-CMD = 0\n" +
		"        transition safed\n" +
		"state safed\n"}

	prog, err := Compile([]Source{src}, Options{})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	m, _ := prog.Machine("seq")

	// Resolve with some identifier values.  Values are in the DSL's base time
	// unit (seconds); the wire protocol's t_ms is produced only at
	// serialization, so 0.5 s / 3 s here become 500 / 3500 ms below.
	reader := &mockReader{values: map[string]float64{
		"IGNITE-DELAY": 0.5,
		"BURN-DUR":     3,
		"LIMIT-HIGH":   850,
		"START-MS":     0,
	}}
	updates, err := m.DaqStateUpdates(reader)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if len(updates["DAQ001"]) == 0 {
		t.Fatalf("no payload")
	}
	p := updates["DAQ001"][0]

	// Check that timings are resolved correctly
	wantSteps := []DaqStep{
		{TMs: 0, RefDes: "IGN-CMD", Value: 1},
		{TMs: 500, RefDes: "MAIN-CMD", Value: 1},
		{TMs: 3500, RefDes: "IGN-CMD", Value: 0},
		{TMs: 3500, RefDes: "MAIN-CMD", Value: 0},
	}
	if !reflect.DeepEqual(p.EntrySequence, wantSteps) {
		t.Errorf("entry_sequence:\n got %+v\nwant %+v", p.EntrySequence, wantSteps)
	}

	// Check that abort rule identifiers are resolved
	wantRules := []DaqAbortRule{{If: "PRESS > 850", TMsOn: 0, TMsOff: 3000}}
	if !reflect.DeepEqual(p.AbortRules, wantRules) {
		t.Errorf("abort_rules:\n got %+v\nwant %+v", p.AbortRules, wantRules)
	}

	// Test unresolvable identifier error
	reader2 := &mockReader{values: map[string]float64{}}
	_, err = m.DaqStateUpdates(reader2)
	if err == nil {
		t.Fatalf("expected error for unresolvable identifiers")
	}
	if !reflect.DeepEqual(err.Error(), "state \"burn\": resolve expression: unresolvable reference: \"IGNITE-DELAY\"") {
		// Just check that it's an error about an unresolvable reference
		if err == nil || !contains(err.Error(), "unresolvable") {
			t.Errorf("expected unresolvable error, got: %v", err)
		}
	}
}

// TestDaqLocal_SecondsToMsAtSerialization pins the one place the DSL's
// seconds-based durations are converted to the DAQ wire protocol's
// milliseconds: a 3-second cutoff, held in a soft channel exactly as an
// operator would set SEQ-CUTOFF-T, must still serialize as t_ms 3000 — the
// wire format is unchanged even though the DSL surface now speaks seconds.
func TestDaqLocal_SecondsToMsAtSerialization(t *testing.T) {
	src := Source{Name: "cutoff.sm", Text: "" +
		"machine cutoffTest\n" +
		"state burn\n" +
		"    daq_local DAQ001\n" +
		"    sequence\n" +
		"        IGN-CMD = 1\n" +
		"        sleep CUTOFF\n" +
		"        IGN-CMD = 0\n" +
		"        transition safed\n" +
		"state safed\n"}

	prog, err := Compile([]Source{src}, Options{})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	m, _ := prog.Machine("cutoffTest")

	// CUTOFF = 3 (seconds — the DSL's base time unit, exactly what an
	// operator-set SEQ-CUTOFF-T of "3.0 s" would resolve to).
	reader := &mockReader{values: map[string]float64{"CUTOFF": 3}}
	updates, err := m.DaqStateUpdates(reader)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	p := updates["DAQ001"][0]

	want := []DaqStep{
		{TMs: 0, RefDes: "IGN-CMD", Value: 1},
		{TMs: 3000, RefDes: "IGN-CMD", Value: 0},
	}
	if !reflect.DeepEqual(p.EntrySequence, want) {
		t.Errorf("entry_sequence:\n got %+v\nwant %+v (3 s must serialize as t_ms 3000)", p.EntrySequence, want)
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
