package dsl

import (
	"strings"
	"testing"
)

// parseCommandSrc lexes/parses a one-machine, one-state source and returns the
// requested block (controller or sequence).
func parseCommandMachine(t *testing.T, src string) *MachineDef {
	t.Helper()
	toks, err := NewLexer(src).Tokenize()
	if err != nil {
		t.Fatalf("lex: %v\nsrc:\n%s", err, src)
	}
	decl, err := Parse(toks)
	if err != nil {
		t.Fatalf("parse: %v\nsrc:\n%s", err, src)
	}
	m, ok := decl.(*MachineDef)
	if !ok {
		t.Fatalf("expected MachineDef, got %T", decl)
	}
	return m
}

func TestParser_CommandInController(t *testing.T) {
	src := "machine master\n" +
		"state run\n" +
		indent(4) + "controller\n" +
		indent(8) + "command pressSeq -> engineRunning\n"

	m := parseCommandMachine(t, src)
	stmts := m.States[0].Controller
	if len(stmts) != 1 {
		t.Fatalf("expected 1 statement, got %d", len(stmts))
	}
	cmd, ok := stmts[0].(*CommandStmt)
	if !ok {
		t.Fatalf("expected CommandStmt, got %T", stmts[0])
	}
	if cmd.Machine != "pressSeq" || cmd.Target != "engineRunning" {
		t.Errorf("got command %q -> %q, want pressSeq -> engineRunning", cmd.Machine, cmd.Target)
	}
}

func TestParser_CommandInSequence(t *testing.T) {
	src := "machine master\n" +
		"state run\n" +
		indent(4) + "sequence\n" +
		indent(8) + "command pressSeq -> engineRunning\n" +
		indent(8) + "wait_until AT-PRESSURE timeout 30 -> abort\n"

	m := parseCommandMachine(t, src)
	stmts := m.States[0].Sequence
	if len(stmts) != 2 {
		t.Fatalf("expected 2 statements, got %d", len(stmts))
	}
	cmd, ok := stmts[0].(*CommandStmt)
	if !ok {
		t.Fatalf("expected CommandStmt, got %T", stmts[0])
	}
	if cmd.Machine != "pressSeq" || cmd.Target != "engineRunning" {
		t.Errorf("got command %q -> %q, want pressSeq -> engineRunning", cmd.Machine, cmd.Target)
	}
	if _, ok := stmts[1].(*WaitUntilStmt); !ok {
		t.Errorf("statement 1: expected WaitUntilStmt, got %T", stmts[1])
	}
}

func TestParser_CommandErrors(t *testing.T) {
	tests := []struct {
		name string
		src  string
	}{
		{
			"missing arrow",
			"machine m\nstate s\n" + indent(4) + "sequence\n" + indent(8) + "command pressSeq engineRunning\n",
		},
		{
			"missing state",
			"machine m\nstate s\n" + indent(4) + "sequence\n" + indent(8) + "command pressSeq ->\n",
		},
		{
			"missing machine",
			"machine m\nstate s\n" + indent(4) + "sequence\n" + indent(8) + "command -> engineRunning\n",
		},
		{
			"trailing junk",
			"machine m\nstate s\n" + indent(4) + "sequence\n" + indent(8) + "command pressSeq -> engineRunning extra\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toks, err := NewLexer(tt.src).Tokenize()
			if err != nil {
				// A lex error also satisfies "parse errors for..."
				return
			}
			if _, err := Parse(toks); err == nil {
				t.Errorf("expected a parse error for %q, got none", tt.name)
			}
		})
	}
}

func TestCommandStmt_FormatRoundTrip(t *testing.T) {
	src := "machine master\n" +
		"state run\n" +
		indent(4) + "sequence\n" +
		indent(8) + "command pressSeq -> engineRunning\n"

	m := parseCommandMachine(t, src)
	lines := StmtLines(m.States[0].Sequence, 0)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d: %#v", len(lines), lines)
	}
	want := "command pressSeq -> engineRunning"
	if lines[0] != want {
		t.Errorf("StmtLines: got %q, want %q", lines[0], want)
	}

	// Re-parse the rendered line to confirm it round-trips.
	src2 := "machine master\nstate run\n" + indent(4) + "sequence\n" + indent(8) + lines[0] + "\n"
	m2 := parseCommandMachine(t, src2)
	cmd, ok := m2.States[0].Sequence[0].(*CommandStmt)
	if !ok {
		t.Fatalf("re-parsed statement: expected CommandStmt, got %T", m2.States[0].Sequence[0])
	}
	if cmd.Machine != "pressSeq" || cmd.Target != "engineRunning" {
		t.Errorf("re-parsed command %q -> %q, want pressSeq -> engineRunning", cmd.Machine, cmd.Target)
	}
	if !strings.Contains(strings.Join(lines, "\n"), want) {
		t.Errorf("rendered lines missing %q: %#v", want, lines)
	}
}
