package statemachine

import (
	"path/filepath"
	"strings"
	"testing"

	"controlnode/dsl"
)

// mockReader implements the Reader interface for testing.
type mockReader struct {
	values map[string]float64
}

func (mr *mockReader) Get(name string) (dsl.Value, bool) {
	v, ok := mr.values[name]
	if ok {
		return dsl.NewFloat(v), true
	}
	return dsl.Value{}, false
}

// coldflowPath is the reference machine used across the tests.
const coldflowPath = "../../docs/restructure/demo/coldflow.sm"

// coldflowChannels is the channel space coldflow.sm references.
var coldflowChannels = []string{
	"OV-05-CMD", "FV-02-CMD", "VENT-CMD",
	"CPT-01", "PT-FUEL-AVG", "IGNITION-OK",
	"LIM-CPT01-HIGH", "SEQ-TARGET-PRESS", "SEQ-BURN-DUR",
}

func loadColdflow(t *testing.T) *Program {
	t.Helper()
	prog, err := LoadFiles([]string{filepath.FromSlash(coldflowPath)}, Options{KnownChannels: coldflowChannels})
	if err != nil {
		t.Fatalf("compile coldflow.sm: %v", err)
	}
	return prog
}

func TestCompile_Coldflow(t *testing.T) {
	prog := loadColdflow(t)

	if len(prog.Machines) != 1 {
		t.Fatalf("machines: got %d, want 1", len(prog.Machines))
	}
	m, ok := prog.Machine("coldFlow")
	if !ok {
		t.Fatalf("machine coldFlow not found")
	}
	if m.Source != "coldflow.sm" {
		t.Errorf("source: got %q, want coldflow.sm", m.Source)
	}
	if m.Initial == nil || m.Initial.Name != "safe" {
		t.Errorf("initial state: got %v, want safe", m.Initial)
	}

	want := []struct {
		name     string
		operator bool
		daqLocal string
		ctrl     int
		seq      int
		rules    int
	}{
		{"safe", true, "", 0, 3, 0},
		{"pressurize", true, "", 1, 4, 0},
		{"fire", false, "", 2, 4, 0},
		{"vent", false, "", 0, 4, 0},
		{"abort", false, "DAQ001", 0, 4, 1},
	}
	if len(m.States) != len(want) {
		t.Fatalf("states: got %d, want %d", len(m.States), len(want))
	}
	for i, w := range want {
		st := m.States[i]
		if st.Name != w.name {
			t.Errorf("state %d: name got %q, want %q", i, st.Name, w.name)
		}
		if st.Index != i {
			t.Errorf("state %s: index got %d, want %d", st.Name, st.Index, i)
		}
		if st.Operator != w.operator {
			t.Errorf("state %s: operator got %v, want %v", st.Name, st.Operator, w.operator)
		}
		if st.DaqLocal != w.daqLocal {
			t.Errorf("state %s: daqLocal got %q, want %q", st.Name, st.DaqLocal, w.daqLocal)
		}
		if len(st.Controller) != w.ctrl {
			t.Errorf("state %s: controller stmts got %d, want %d", st.Name, len(st.Controller), w.ctrl)
		}
		if len(st.Sequence) != w.seq {
			t.Errorf("state %s: sequence stmts got %d, want %d", st.Name, len(st.Sequence), w.seq)
		}
		if len(st.AbortRules) != w.rules {
			t.Errorf("state %s: abort rules got %d, want %d", st.Name, len(st.AbortRules), w.rules)
		}
		if _, ok := m.State(w.name); !ok {
			t.Errorf("state %s: not reachable via State()", w.name)
		}
	}

	// pressurize: wait_until with a 30 s timeout falling back to safe.
	pressurize, _ := m.State("pressurize")
	wu, ok := pressurize.Sequence[2].(*dsl.WaitUntilStmt)
	if !ok {
		t.Fatalf("pressurize stmt 3: got %T, want *dsl.WaitUntilStmt", pressurize.Sequence[2])
	}
	if wu.TimeoutState != "safe" {
		t.Errorf("pressurize wait_until timeout target: got %q, want safe", wu.TimeoutState)
	}
	lit, ok := wu.Timeout.(*dsl.LiteralExpr)
	if !ok {
		t.Fatalf("pressurize wait_until timeout: got %T, expected LiteralExpr", wu.Timeout)
	}
	if to, err := literalNumber(lit); err != nil || to != 30000 {
		t.Errorf("pressurize wait_until timeout: got %v (%v), want 30000", to, err)
	}

	// fire: sleep driven by a soft channel, not a literal.
	fire, _ := m.State("fire")
	sl, ok := fire.Sequence[1].(*dsl.SleepStmt)
	if !ok {
		t.Fatalf("fire stmt 2: got %T, want *dsl.SleepStmt", fire.Sequence[1])
	}
	if id, ok := sl.Duration.(*dsl.IdentExpr); !ok || id.Name != "SEQ-BURN-DUR" {
		t.Errorf("fire sleep duration: got %v, want SEQ-BURN-DUR ident", sl.Duration)
	}

	// abort: daq_local payload precompiled, no controlnode-side controller.
	abort, _ := m.State("abort")
	if !abort.HasDaqLocal() {
		t.Fatalf("abort: expected a daq_local definition")
	}

	// Create a mock reader to resolve daq_local payloads (all channels have default values)
	mockReader := &mockReader{values: map[string]float64{
		"LIM-CPT01-HIGH": 850,
	}}
	byNode, err := m.DaqStateUpdates(mockReader)
	if err != nil {
		t.Fatalf("DaqStateUpdates: %v", err)
	}
	if len(byNode["DAQ001"]) != 1 || byNode["DAQ001"][0].State != "abort" {
		t.Errorf("DaqStateUpdates: got %v, want one abort payload on DAQ001", byNode)
	}
}

func TestCompile_Errors(t *testing.T) {
	tests := []struct {
		name    string
		sources []Source
		opts    Options
		want    string
	}{
		{
			name: "unknown transition target",
			sources: []Source{{Name: "a.sm", Text: "" +
				"machine a\n" +
				"state safe\n" +
				"    sequence\n" +
				"        transition nowhere\n"}},
			want: `transition to unknown state "nowhere"`,
		},
		{
			name: "unknown wait_until timeout target",
			sources: []Source{{Name: "a.sm", Text: "" +
				"machine a\n" +
				"state safe\n" +
				"    sequence\n" +
				"        wait_until X > 1 timeout 5s -> gone\n"}},
			want: `wait_until timeout target "gone"`,
		},
		{
			name: "wait_until timeout without target",
			sources: []Source{{Name: "a.sm", Text: "" +
				"machine a\n" +
				"state safe\n" +
				"    sequence\n" +
				"        wait_until X > 1 timeout 5s\n"}},
			want: `wait_until timeout requires`,
		},
		{
			name: "sleep in controller",
			sources: []Source{{Name: "a.sm", Text: "" +
				"machine a\n" +
				"state safe\n" +
				"    controller\n" +
				"        sleep 10ms\n"}},
			want: "sleep is not allowed",
		},
		{
			name: "duplicate state",
			sources: []Source{{Name: "a.sm", Text: "" +
				"machine a\n" +
				"state safe\n" +
				"    sequence\n" +
				"        X = 1\n" +
				"state safe\n" +
				"    sequence\n" +
				"        X = 2\n"}},
			want: `state "safe" already defined`,
		},
		{
			name: "machine name collision",
			sources: []Source{
				{Name: "a.sm", Text: "machine dup\nstate safe\n    sequence\n        X = 1\n"},
				{Name: "b.sm", Text: "machine dup\nstate safe\n    sequence\n        X = 1\n"},
			},
			want: `machine "dup" already defined in a.sm`,
		},
		{
			name: "abort_rule without daq_local",
			sources: []Source{{Name: "a.sm", Text: "" +
				"machine a\n" +
				"state safe\n" +
				"    abort_rule X > 10 from 0ms to 100ms\n" +
				"    sequence\n" +
				"        X = 1\n"}},
			want: "abort_rule requires daq_local",
		},
		{
			name: "daq_local non-arithmetic assignment",
			sources: []Source{{Name: "a.sm", Text: "" +
				"machine a\n" +
				"state safe\n" +
				"    daq_local DAQ001\n" +
				"    sequence\n" +
				"        X = Y > 1\n"}},
			want: "must be a literal, soft-channel identifier, or constant arithmetic",
		},
		{
			name: "daq_local wait_until",
			sources: []Source{{Name: "a.sm", Text: "" +
				"machine a\n" +
				"state safe\n" +
				"    daq_local DAQ001\n" +
				"    sequence\n" +
				"        wait_until X > 1\n"}},
			want: "allow only assignments, sleeps, and a trailing transition",
		},
		{
			name: "daq_local abort_rule without abort_sequence",
			sources: []Source{{Name: "a.sm", Text: "" +
				"machine a\n" +
				"state safe\n" +
				"    daq_local DAQ001\n" +
				"    abort_rule CPT-01 > 850 from 0ms to 20000ms\n" +
				"    sequence\n" +
				"        X = 1\n"}},
			want: "must declare an abort_sequence",
		},
		{
			name: "abort_sequence without daq_local",
			sources: []Source{{Name: "a.sm", Text: "" +
				"machine a\n" +
				"state safe\n" +
				"    sequence\n" +
				"        X = 1\n" +
				"    abort_sequence\n" +
				"        X = 0\n" +
				"        transition safe\n"}},
			want: "abort_sequence requires daq_local",
		},
		{
			name: "abort_sequence without a destination",
			sources: []Source{{Name: "a.sm", Text: "" +
				"machine a\n" +
				"state safe\n" +
				"    daq_local DAQ001\n" +
				"    abort_rule CPT-01 > 850 from 0ms to 20000ms\n" +
				"    sequence\n" +
				"        X = 1\n" +
				"    abort_sequence\n" +
				"        X = 0\n"}},
			want: "must end with \"transition <state>\"",
		},
		{
			name: "abort destination must exist",
			sources: []Source{{Name: "a.sm", Text: "" +
				"machine a\n" +
				"state safe\n" +
				"    daq_local DAQ001\n" +
				"    abort_rule CPT-01 > 850 from 0ms to 20000ms\n" +
				"    sequence\n" +
				"        X = 1\n" +
				"    abort_sequence\n" +
				"        X = 0\n" +
				"        transition nowhere\n"}},
			want: `transition to unknown state "nowhere"`,
		},
		{
			name: "daq_local controller",
			sources: []Source{{Name: "a.sm", Text: "" +
				"machine a\n" +
				"state safe\n" +
				"    daq_local DAQ001\n" +
				"    controller\n" +
				"        X = 1\n"}},
			want: "cannot have a controller block",
		},
		{
			name: "unknown channel",
			sources: []Source{{Name: "a.sm", Text: "" +
				"machine a\n" +
				"state safe\n" +
				"    sequence\n" +
				"        NOPE-01 = 1\n"}},
			opts: Options{KnownChannels: []string{"X"}},
			want: `unknown channel "NOPE-01"`,
		},
		{
			name: "unknown machine reference",
			sources: []Source{{Name: "a.sm", Text: "" +
				"machine a\n" +
				"state safe\n" +
				"    controller\n" +
				"        if machine.ghost.state == \"safe\"\n" +
				"            transition safe\n"}},
			opts: Options{KnownChannels: []string{"X"}},
			want: `unknown machine "ghost"`,
		},
		{
			name:    "not a machine file",
			sources: []Source{{Name: "a.sm", Text: "channel X\n    type float\n"}},
			want:    "expected a machine definition",
		},
		{
			name: "operator from: unknown gate state name",
			sources: []Source{{Name: "a.sm", Text: "" +
				"machine a\n" +
				"state safe\n" +
				"    operator from nowhere\n" +
				"    sequence\n" +
				"        X = 1\n"}},
			want: `"operator from" names "nowhere", which is not a state of machine "a"`,
		},
		{
			name: "operator from: self-reference",
			sources: []Source{{Name: "a.sm", Text: "" +
				"machine a\n" +
				"state safe\n" +
				"    operator from safe\n" +
				"    sequence\n" +
				"        X = 1\n" +
				"state manualControl\n" +
				"    operator\n"}},
			want: `may not list the state itself`,
		},
		{
			name: "operator from: from without operator",
			sources: []Source{{Name: "a.sm", Text: "" +
				"machine a\n" +
				"state safe\n" +
				"    from manualControl\n" +
				"    sequence\n" +
				"        X = 1\n"}},
			want: `requires the operator flag on the same line`,
		},
		{
			name: "operator from: duplicate name",
			sources: []Source{{Name: "a.sm", Text: "" +
				"machine a\n" +
				"state safe\n" +
				"    operator from manualControl, manualControl\n" +
				"    sequence\n" +
				"        X = 1\n" +
				"state manualControl\n" +
				"    operator\n"}},
			want: `duplicate state "manualControl" in "operator from"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Compile(tt.sources, tt.opts)
			if err == nil {
				t.Fatalf("expected a compile error containing %q", tt.want)
			}
			if tt.want != "" && !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.want)
			}
			if !strings.Contains(err.Error(), ".sm:") {
				t.Errorf("error %q is missing a file:line prefix", err.Error())
			}
		})
	}
}

func TestCompile_KnownChannelsSatisfied(t *testing.T) {
	// The reference machine must compile cleanly against exactly the channel
	// list documented in its header comment.
	if _, err := LoadFiles([]string{filepath.FromSlash(coldflowPath)}, Options{KnownChannels: coldflowChannels}); err != nil {
		t.Fatalf("coldflow.sm should compile against its documented channels: %v", err)
	}
	// Dropping one channel must be caught.
	short := append([]string{}, coldflowChannels[1:]...)
	if _, err := LoadFiles([]string{filepath.FromSlash(coldflowPath)}, Options{KnownChannels: short}); err == nil {
		t.Fatalf("expected an unknown-channel error when OV-05-CMD is missing")
	}
}
