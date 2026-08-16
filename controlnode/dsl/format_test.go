package dsl

import (
	"strings"
	"testing"
)

// parseExprText lexes and parses a bare expression by wrapping it in a channel
// compute block, which is the smallest declaration that carries one.
func parseExprText(t *testing.T, text string) Expr {
	t.Helper()
	toks, err := NewLexer("channel X\n    compute " + text + "\n").Tokenize()
	if err != nil {
		t.Fatalf("lex %q: %v", text, err)
	}
	decl, err := Parse(toks)
	if err != nil {
		t.Fatalf("parse %q: %v", text, err)
	}
	ch, ok := decl.(*ChannelDef)
	if !ok || ch.Compute == nil {
		t.Fatalf("parse %q: not a compute channel", text)
	}
	return ch.Compute
}

// TestExprString_RoundTrip is the property that matters: rendering a parsed
// expression and re-parsing it must produce the same text, so the /docs pages
// cannot show something that would not compile.
func TestExprString_RoundTrip(t *testing.T) {
	for _, src := range []string{
		"PT-01 + PT-02",
		"(PT-01 + PT-02) / 2",
		"TC-01 > 400 and PT-FUEL-AVG > 300",
		"not IGNITION-OK",
		"2000 - SEQ-IGN-LEAD",
		"machine.fuelSeq.state == \"abort\"",
		"a + b * c",
		"(a + b) * c",
		"a - (b - c)",
	} {
		got := ExprString(parseExprText(t, src))
		again := ExprString(parseExprText(t, got))
		if got != again {
			t.Errorf("%q: not stable: %q then %q", src, got, again)
		}
		if got != src {
			// Rendering may normalise spacing, but the structure must survive;
			// an exact match is the stronger and expected outcome here.
			t.Errorf("ExprString(%q) = %q", src, got)
		}
	}
}

// TestStmtLines renders a controller block back to DSL-shaped source.
func TestStmtLines(t *testing.T) {
	src := "machine m\nstate s\n    controller\n        if CPT-01 > 10\n            transition s\n        else\n            HB-CTR++\n"
	toks, err := NewLexer(src).Tokenize()
	if err != nil {
		t.Fatalf("lex: %v", err)
	}
	decl, err := Parse(toks)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	m := decl.(*MachineDef)
	got := strings.Join(StmtLines(m.States[0].Controller, 0), "\n")
	want := "if CPT-01 > 10\n    transition s\nelse\n    HB-CTR++"
	if got != want {
		t.Errorf("StmtLines:\n got %q\nwant %q", got, want)
	}
}

// TestDescribeEvalError checks the distinction operators actually care about:
// a configured channel with no value yet is not a config typo.
func TestDescribeEvalError(t *testing.T) {
	cs := NewMockChannelSpace()
	_, err := Eval(&IdentExpr{Name: "CPT-01", LineNo: 4}, cs)
	if err == nil {
		t.Fatal("expected an evaluation error")
	}

	known := func(name string) bool { return name == "CPT-01" }
	got := DescribeEvalError(err, known).Error()
	if !strings.Contains(got, "no value yet") || !strings.Contains(got, "CPT-01") {
		t.Errorf("configured-but-unset channel reported as %q", got)
	}
	if strings.Contains(got, "unknown channel") {
		t.Errorf("configured channel reported as unknown: %q", got)
	}

	none := func(string) bool { return false }
	got = DescribeEvalError(err, none).Error()
	if !strings.Contains(got, "unknown channel") {
		t.Errorf("genuinely unknown channel reported as %q", got)
	}

	// A nil known-set and a non-evaluation error must both pass through.
	if DescribeEvalError(err, nil) != err {
		t.Error("nil known-set should return the error unchanged")
	}
	if DescribeEvalError(nil, known) != nil {
		t.Error("nil error should stay nil")
	}
}

// TestOperatorString_RoundTrip covers OperatorString's two forms — bare
// `operator` and `operator from a, b` — and that each re-parses to the same
// flag/gate list it was rendered from.
func TestOperatorString_RoundTrip(t *testing.T) {
	tests := []struct {
		name string
		from []string
		want string
	}{
		{"bare operator", nil, "operator"},
		{"single gate", []string{"safe"}, "operator from safe"},
		{"multi gate", []string{"safe", "abort"}, "operator from safe, abort"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := OperatorString(tt.from)
			if got != tt.want {
				t.Fatalf("OperatorString(%#v) = %q, want %q", tt.from, got, tt.want)
			}

			src := "machine m\nstate s\n    " + got + "\n"
			toks, err := NewLexer(src).Tokenize()
			if err != nil {
				t.Fatalf("lex %q: %v", src, err)
			}
			decl, err := Parse(toks)
			if err != nil {
				t.Fatalf("parse %q: %v", src, err)
			}
			m, ok := decl.(*MachineDef)
			if !ok || len(m.States) != 1 {
				t.Fatalf("parse %q: unexpected decl %#v", src, decl)
			}
			st := m.States[0]
			if !st.Operator {
				t.Errorf("re-parsed state: operator=false, want true")
			}
			if !stringSliceEq(st.OperatorFrom, tt.from) {
				t.Errorf("re-parsed OperatorFrom: got %#v, want %#v", st.OperatorFrom, tt.from)
			}
		})
	}
}
