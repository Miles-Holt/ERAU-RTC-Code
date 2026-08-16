package dsl

import (
	"strings"
	"testing"
)

func indent(n int) string {
	s := ""
	for i := 0; i < n; i++ {
		s += " "
	}
	return s
}

func TestParser_SimpleMachine(t *testing.T) {
	input := "machine fuelSequence\n" +
		"state safe\n" +
		indent(4) + "operator\n" +
		indent(4) + "controller\n" +
		indent(8) + "if PT-FUEL-AVG > LIM-CPT01-HIGH\n" +
		indent(12) + "transition abort\n" +
		indent(8) + "HEARTBEAT-CTR++\n" +
		indent(4) + "sequence\n" +
		indent(8) + "OV-05-CMD = 0\n" +
		indent(8) + "wait_until OV-05.closed timeout 5s -> abort\n" +
		indent(8) + "sleep 250ms\n" +
		indent(8) + "transition ready\n" +
		"state abort\n" +
		indent(4) + "daq_local DAQ001\n" +
		indent(4) + "sequence\n" +
		indent(8) + "OV-05-CMD = 0\n" +
		indent(8) + "sleep 100ms\n" +
		indent(8) + "FV-02-CMD = 0\n"

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

	if machine.Name != "fuelSequence" {
		t.Errorf("machine name: got %q, expected %q", machine.Name, "fuelSequence")
	}

	if len(machine.States) != 2 {
		t.Errorf("number of states: got %d, expected 2", len(machine.States))
	}

	// Check safe state
	safeState := machine.States[0]
	if safeState.Name != "safe" {
		t.Errorf("safe state name: got %q, expected %q", safeState.Name, "safe")
	}
	if !safeState.Operator {
		t.Errorf("safe state: expected operator=true")
	}
	if len(safeState.Controller) == 0 {
		t.Errorf("safe state: expected controller block with statements")
	}
	if len(safeState.Sequence) == 0 {
		t.Errorf("safe state: expected sequence block with statements")
	}

	// Check abort state
	abortState := machine.States[1]
	if abortState.Name != "abort" {
		t.Errorf("abort state name: got %q, expected %q", abortState.Name, "abort")
	}
	if abortState.DaqLocal != "DAQ001" {
		t.Errorf("abort state: expected daq_local=DAQ001, got %q", abortState.DaqLocal)
	}
}

func TestParser_Channel(t *testing.T) {
	input := "channel SEQ-BURN-DUR\n" +
		indent(4) + "type float\n" +
		indent(4) + "default 3000\n" +
		indent(4) + "min 500\n" +
		indent(4) + "max 10000\n" +
		indent(4) + "units ms\n"

	lexer := NewLexer(input)
	toks, err := lexer.Tokenize()
	if err != nil {
		t.Fatalf("lexer error: %v", err)
	}

	decl, err := Parse(toks)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	ch, ok := decl.(*ChannelDef)
	if !ok {
		t.Fatalf("expected ChannelDef, got %T", decl)
	}

	if ch.Name != "SEQ-BURN-DUR" {
		t.Errorf("channel name: got %q, expected %q", ch.Name, "SEQ-BURN-DUR")
	}

	if ch.Type != "float" {
		t.Errorf("channel type: got %q, expected %q", ch.Type, "float")
	}

	if ch.Default == nil || ch.Default.Value != int64(3000) {
		t.Errorf("channel default: expected 3000")
	}

	if ch.Min == nil || ch.Min.Value != int64(500) {
		t.Errorf("channel min: expected 500")
	}

	if ch.Max == nil || ch.Max.Value != int64(10000) {
		t.Errorf("channel max: expected 10000")
	}

	if ch.Units != "ms" {
		t.Errorf("channel units: got %q, expected %q", ch.Units, "ms")
	}
}

func TestParser_ComputedChannel(t *testing.T) {
	input := "channel PT-FUEL-AVG\n" +
		indent(4) + "units psi\n" +
		indent(4) + "compute (PT-01 + PT-02) / 2\n"

	lexer := NewLexer(input)
	toks, err := lexer.Tokenize()
	if err != nil {
		t.Fatalf("lexer error: %v", err)
	}

	decl, err := Parse(toks)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	ch, ok := decl.(*ChannelDef)
	if !ok {
		t.Fatalf("expected ChannelDef, got %T", decl)
	}

	if ch.Name != "PT-FUEL-AVG" {
		t.Errorf("channel name: got %q, expected %q", ch.Name, "PT-FUEL-AVG")
	}

	if ch.Compute == nil {
		t.Errorf("channel: expected compute expression")
	}

	if _, ok := ch.Compute.(*BinaryExpr); !ok {
		t.Errorf("expected BinaryExpr in compute, got %T", ch.Compute)
	}
}

func TestParser_BoolComputedChannel(t *testing.T) {
	input := "channel IGNITION-OK\n" +
		indent(4) + "type bool\n" +
		indent(4) + "compute TC-01 > 400 and PT-FUEL-AVG > 300\n"

	lexer := NewLexer(input)
	toks, err := lexer.Tokenize()
	if err != nil {
		t.Fatalf("lexer error: %v", err)
	}

	decl, err := Parse(toks)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	ch, ok := decl.(*ChannelDef)
	if !ok {
		t.Fatalf("expected ChannelDef, got %T", decl)
	}

	if ch.Type != "bool" {
		t.Errorf("channel type: got %q, expected bool", ch.Type)
	}

	if ch.Compute == nil {
		t.Errorf("channel: expected compute expression")
	}
}

func TestParser_Alert(t *testing.T) {
	input := "alert CHAMBER-HIGH\n" +
		indent(4) + "if CPT-01 > LIM-CPT01-HIGH\n" +
		indent(4) + "severity alarm\n" +
		indent(4) + "message \"Chamber pressure high: {CPT-01} psi\"\n" +
		indent(4) + "latch\n"

	lexer := NewLexer(input)
	toks, err := lexer.Tokenize()
	if err != nil {
		t.Fatalf("lexer error: %v", err)
	}

	decl, err := Parse(toks)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	alert, ok := decl.(*AlertDef)
	if !ok {
		t.Fatalf("expected AlertDef, got %T", decl)
	}

	if alert.Name != "CHAMBER-HIGH" {
		t.Errorf("alert name: got %q, expected CHAMBER-HIGH", alert.Name)
	}

	if alert.Severity != "alarm" {
		t.Errorf("alert severity: got %q, expected alarm", alert.Severity)
	}

	if alert.Latch != true {
		t.Errorf("alert latch: got false, expected true")
	}

	if alert.Condition == nil {
		t.Errorf("alert: expected condition")
	}
}

func TestParser_Template(t *testing.T) {
	input := "template every_daqnode\n" +
		indent(4) + "on disconnect -> alarm \"{node} disconnected\"\n" +
		indent(4) + "on reconnect -> info \"{node} reconnected\"\n"

	lexer := NewLexer(input)
	toks, err := lexer.Tokenize()
	if err != nil {
		t.Fatalf("lexer error: %v", err)
	}

	decl, err := Parse(toks)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	tmpl, ok := decl.(*TemplateDef)
	if !ok {
		t.Fatalf("expected TemplateDef, got %T", decl)
	}

	if tmpl.Name != "every_daqnode" {
		t.Errorf("template name: got %q, expected every_daqnode", tmpl.Name)
	}

	if len(tmpl.Rules) != 2 {
		t.Errorf("template rules: got %d, expected 2", len(tmpl.Rules))
	}

	if tmpl.Rules[0].Event != "disconnect" {
		t.Errorf("rule 0 event: got %q, expected disconnect", tmpl.Rules[0].Event)
	}

	if tmpl.Rules[1].Event != "reconnect" {
		t.Errorf("rule 1 event: got %q, expected reconnect", tmpl.Rules[1].Event)
	}
}

// A template event may carry a duration qualifier before the arrow
// (`on stale 5s -> …`), which the alert engine uses to override the default
// data-receive timeout.  Events without one report DurationMs 0.
func TestParser_TemplateEventDuration(t *testing.T) {
	input := "template every_daqnode\n" +
		indent(4) + "on stale 5s -> warning \"{node} stale\"\n" +
		indent(4) + "on disconnect -> alarm \"{node} down\"\n"

	toks, err := NewLexer(input).Tokenize()
	if err != nil {
		t.Fatalf("lexer error: %v", err)
	}
	decl, err := Parse(toks)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	tmpl, ok := decl.(*TemplateDef)
	if !ok {
		t.Fatalf("expected TemplateDef, got %T", decl)
	}
	if len(tmpl.Rules) != 2 {
		t.Fatalf("rules: got %d, expected 2", len(tmpl.Rules))
	}
	if tmpl.Rules[0].DurationMs != 5000 {
		t.Errorf("stale duration: got %d ms, expected 5000", tmpl.Rules[0].DurationMs)
	}
	if tmpl.Rules[0].Severity != "warning" || tmpl.Rules[0].Message != "{node} stale" {
		t.Errorf("stale rule: got %+v", tmpl.Rules[0])
	}
	if tmpl.Rules[1].DurationMs != 0 {
		t.Errorf("unqualified event carried a duration: %d", tmpl.Rules[1].DurationMs)
	}
}

func TestParser_Statements(t *testing.T) {
	input := "machine test\n" +
		"state s\n" +
		indent(4) + "sequence\n" +
		indent(8) + "CH = 100\n" +
		indent(8) + "CH++\n" +
		indent(8) + "CH--\n" +
		indent(8) + "sleep 1000ms\n" +
		indent(8) + "wait_until CH > 50 timeout 5s -> abort\n" +
		indent(8) + "transition abort\n"

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

	state := machine.States[0]
	stmts := state.Sequence

	if len(stmts) < 6 {
		t.Errorf("expected at least 6 statements, got %d", len(stmts))
		return
	}

	// Check assignment
	if _, ok := stmts[0].(*AssignStmt); !ok {
		t.Errorf("statement 0: expected AssignStmt, got %T", stmts[0])
	}

	// Check increment
	if _, ok := stmts[1].(*IncrementStmt); !ok {
		t.Errorf("statement 1: expected IncrementStmt, got %T", stmts[1])
	}

	// Check decrement
	if _, ok := stmts[2].(*DecrementStmt); !ok {
		t.Errorf("statement 2: expected DecrementStmt, got %T", stmts[2])
	}

	// Check sleep
	if _, ok := stmts[3].(*SleepStmt); !ok {
		t.Errorf("statement 3: expected SleepStmt, got %T", stmts[3])
	}

	// Check wait_until
	if wu, ok := stmts[4].(*WaitUntilStmt); !ok {
		t.Errorf("statement 4: expected WaitUntilStmt, got %T", stmts[4])
	} else {
		if wu.Timeout == nil {
			t.Errorf("wait_until: expected timeout")
		}
		if wu.TimeoutState != "abort" {
			t.Errorf("wait_until timeout state: got %q, expected abort", wu.TimeoutState)
		}
	}

	// Check transition
	if trans, ok := stmts[5].(*TransitionStmt); !ok {
		t.Errorf("statement 5: expected TransitionStmt, got %T", stmts[5])
	} else {
		if trans.Target != "abort" {
			t.Errorf("transition target: got %q, expected abort", trans.Target)
		}
	}
}

func TestParser_IfElseElse(t *testing.T) {
	input := "machine test\n" +
		"state s\n" +
		indent(4) + "controller\n" +
		indent(8) + "if a > b\n" +
		indent(12) + "transition abort\n" +
		indent(8) + "elif c < d\n" +
		indent(12) + "transition safe\n" +
		indent(8) + "else\n" +
		indent(12) + "transition other\n"

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

	state := machine.States[0]
	ifStmt := state.Controller[0].(*IfStmt)

	if ifStmt.Condition == nil {
		t.Errorf("if: expected condition")
	}

	if len(ifStmt.Elif) != 2 {
		t.Errorf("if: expected 2 elif/else branches, got %d", len(ifStmt.Elif))
	}

	// Check elif
	elif := ifStmt.Elif[0]
	if elif.Condition == nil {
		t.Errorf("elif: expected condition")
	}

	// Check else
	elseStmt := ifStmt.Elif[1]
	if elseStmt.Condition != nil {
		t.Errorf("else: expected nil condition")
	}
}

func TestParser_DurationInExpression(t *testing.T) {
	input := "machine test\n" +
		"state s\n" +
		indent(4) + "sequence\n" +
		indent(8) + "sleep 5s\n" +
		indent(8) + "wait_until ready timeout 100ms -> fail\n"

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

	state := machine.States[0]
	sleep := state.Sequence[0].(*SleepStmt)

	if lit, ok := sleep.Duration.(*LiteralExpr); ok {
		if lit.Value != int64(5000) {
			t.Errorf("sleep duration: got %v, expected 5000", lit.Value)
		}
	} else {
		t.Errorf("sleep duration: expected LiteralExpr, got %T", sleep.Duration)
	}
}

func TestParser_MemberAccess(t *testing.T) {
	input := "channel TEST\n" +
		indent(4) + "compute machine.fuelSeq.state == \"ready\"\n"

	lexer := NewLexer(input)
	toks, err := lexer.Tokenize()
	if err != nil {
		t.Fatalf("lexer error: %v", err)
	}

	decl, err := Parse(toks)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	ch, ok := decl.(*ChannelDef)
	if !ok {
		t.Fatalf("expected ChannelDef, got %T", decl)
	}

	binExpr := ch.Compute.(*BinaryExpr)
	leftIdent := binExpr.Left.(*IdentExpr)

	if leftIdent.Name != "machine.fuelSeq.state" {
		t.Errorf("member access: got %q, expected machine.fuelSeq.state", leftIdent.Name)
	}
}

func TestParser_ChannelWithDescription(t *testing.T) {
	input := "channel TEST-CHAN\n" +
		indent(4) + "type float\n" +
		indent(4) + "description \"Test channel with description\"\n" +
		indent(4) + "default 100\n" +
		indent(4) + "units psi\n"

	lexer := NewLexer(input)
	toks, err := lexer.Tokenize()
	if err != nil {
		t.Fatalf("lexer error: %v", err)
	}

	decl, err := Parse(toks)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	ch, ok := decl.(*ChannelDef)
	if !ok {
		t.Fatalf("expected ChannelDef, got %T", decl)
	}

	if ch.Name != "TEST-CHAN" {
		t.Errorf("channel name: got %q, expected TEST-CHAN", ch.Name)
	}

	if ch.Description != "Test channel with description" {
		t.Errorf("description: got %q, expected \"Test channel with description\"", ch.Description)
	}

	if ch.Type != "float" {
		t.Errorf("type: got %q, expected float", ch.Type)
	}

	if ch.Units != "psi" {
		t.Errorf("units: got %q, expected psi", ch.Units)
	}
}

func TestParser_BareState(t *testing.T) {
	// Test a state with neither controller nor sequence block (bare state)
	input := "machine test\n" +
		"state manualControl\n" +
		indent(4) + "operator\n" +
		"state ready\n" +
		indent(4) + "sequence\n"

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

	if len(machine.States) != 2 {
		t.Errorf("number of states: got %d, expected 2", len(machine.States))
	}

	// Check manualControl state — bare state with only operator flag
	manualState := machine.States[0]
	if manualState.Name != "manualControl" {
		t.Errorf("state name: got %q, expected manualControl", manualState.Name)
	}
	if !manualState.Operator {
		t.Errorf("manualControl: expected operator=true")
	}
	if len(manualState.Controller) != 0 {
		t.Errorf("manualControl: expected no controller statements, got %d", len(manualState.Controller))
	}
	if len(manualState.Sequence) != 0 {
		t.Errorf("manualControl: expected no sequence statements, got %d", len(manualState.Sequence))
	}

	// Check ready state — has sequence keyword but no indented block
	readyState := machine.States[1]
	if readyState.Name != "ready" {
		t.Errorf("state name: got %q, expected ready", readyState.Name)
	}
	if len(readyState.Sequence) != 0 {
		t.Errorf("ready: expected empty sequence after keyword with no block, got %d statements", len(readyState.Sequence))
	}
}

// TestParser_TruncatedInputs covers F-A9: every token the grammar requires is
// taken with expect(), so a file that stops mid-construct is a file:line error
// — never a silently-wrong AST built from the tokens that happened to be there.
func TestParser_TruncatedInputs(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string // substring the error must contain
	}{
		{"daq_local without node", "machine m\nstate s\n    daq_local\n", "expected IDENT"},
		{"daq_local at EOF", "machine m\nstate s\n    daq_local", "expected IDENT"},
		{"transition without target", "machine m\nstate s\n    sequence\n        transition\n", "expected IDENT"},
		{"state without name", "machine m\nstate\n", "expected IDENT"},
		{"machine without name", "machine\n", "expected IDENT"},
		{"assignment without value", "machine m\nstate s\n    sequence\n        X =\n", "unexpected token in expression"},
		{"sleep without duration", "machine m\nstate s\n    sequence\n        sleep\n", "unexpected token in expression"},
		{"wait_until arrow without target", "machine m\nstate s\n    sequence\n        wait_until X > 1 timeout 5s ->\n", "expected IDENT"},
		{"abort_rule without operator", "machine m\nstate s\n    daq_local D\n    abort_rule CPT-01\n", "expected comparison operator"},
		{"abort_rule without to", "machine m\nstate s\n    daq_local D\n    abort_rule CPT-01 > 1 from 0ms\n", "expected IDENT"},
		{"unclosed paren", "machine m\nstate s\n    sequence\n        X = (1 + 2\n", "expected )"},
		{"if without condition", "machine m\nstate s\n    controller\n        if\n", "unexpected token in expression"},
		{"template rule without message", "template t\n    on disconnect -> alarm\n", "expected STRING"},
		{"channel without name", "channel\n", "expected IDENT"},
		{"alert without name", "alert\n", "expected IDENT"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toks, err := NewLexer(tt.src).Tokenize()
			if err != nil {
				return // a lex error is also a real, reported failure
			}
			decl, err := Parse(toks)
			if err == nil {
				t.Fatalf("truncated input parsed successfully into %#v", decl)
			}
			if decl != nil {
				t.Errorf("error returned alongside a non-nil AST: %#v", decl)
			}
			if !strings.HasPrefix(err.Error(), "file:") {
				t.Errorf("error %q is missing a file:line prefix", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not mention %q", err, tt.want)
			}
		})
	}
}

// TestParser_AbortSequence checks the abort_sequence block parses into timed
// steps plus a trailing transition (the abort destination).
func TestParser_AbortSequence(t *testing.T) {
	src := "" +
		"machine m\n" +
		"state fire\n" +
		"    daq_local DAQ001\n" +
		"    abort_rule CPT-01 > 850 from 0ms to 20s\n" +
		"    sequence\n" +
		"        IG-01-CMD = 1\n" +
		"        sleep 2000 - SEQ-IGN-LEAD\n" +
		"        IG-01-CMD = 0\n" +
		"    abort_sequence\n" +
		"        IG-01-CMD = 0\n" +
		"        transition abort\n" +
		"state abort\n"

	toks, err := NewLexer(src).Tokenize()
	if err != nil {
		t.Fatalf("lex: %v", err)
	}
	decl, err := Parse(toks)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	m := decl.(*MachineDef)
	fire := m.States[0]
	if !fire.HasAbortSequence {
		t.Fatal("abort_sequence not recorded")
	}
	if len(fire.AbortSequence) != 2 {
		t.Fatalf("abort_sequence: got %d statements, want 2", len(fire.AbortSequence))
	}
	tr, ok := fire.AbortSequence[1].(*TransitionStmt)
	if !ok || tr.Target != "abort" {
		t.Errorf("abort_sequence trailing statement: got %#v, want transition abort", fire.AbortSequence[1])
	}
	// `sleep 2000 - SEQ-IGN-LEAD` must parse as constant arithmetic.
	sl, ok := fire.Sequence[1].(*SleepStmt)
	if !ok {
		t.Fatalf("sequence stmt 2: got %T, want *SleepStmt", fire.Sequence[1])
	}
	bin, ok := sl.Duration.(*BinaryExpr)
	if !ok || bin.Op != "-" {
		t.Fatalf("sleep duration: got %#v, want a binary minus", sl.Duration)
	}
	if id, ok := bin.Right.(*IdentExpr); !ok || id.Name != "SEQ-IGN-LEAD" {
		t.Errorf("sleep right operand: got %#v, want SEQ-IGN-LEAD", bin.Right)
	}
}
