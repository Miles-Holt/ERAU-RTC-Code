package dsl

import (
	"os"
	"testing"
)

// TestSmokeTest_ParsesAndRoundTrips compiles testdata/smokeTest.sm — a
// fixture covering every construct the DSL grammar accepts (see the file
// itself for a section-by-section rundown) — and checks it survives a
// Parse -> Format -> Parse -> Format round trip unchanged.
//
// This exists so tests exercise the DSL surface without pinning to live
// production config: shippedconfig_test.go pins to config/machines/daq001.sm
// on purpose (it is checking THAT file), but a rename or edit of a real
// machine should never be able to break "does the parser still work at
// all" — which is exactly what broke when daq001.sm was renamed. testdata/
// is ignored by the Go toolchain for builds, so this fixture is never loaded
// into a running control node (see main.go's config/machines/*.sm scan).
func TestSmokeTest_ParsesAndRoundTrips(t *testing.T) {
	src, err := os.ReadFile("testdata/smokeTest.sm")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	decl1 := parseSmoke(t, string(src))
	m1, ok := decl1.(*MachineDef)
	if !ok {
		t.Fatalf("expected MachineDef, got %T", decl1)
	}
	if m1.Name != "smokeTest" {
		t.Errorf("machine name: got %q, want smokeTest", m1.Name)
	}
	const wantStates = 6 // idle, manual, armed, running, sequenced, abort
	if len(m1.States) != wantStates {
		t.Fatalf("expected %d states, got %d", wantStates, len(m1.States))
	}

	// Round trip: format the parsed AST back to source, reparse it, and
	// format again.  Two formatted texts matching is the drift check the
	// task calls for — it catches a parser accepting something the
	// formatter cannot reproduce (or vice versa) anywhere in the grammar,
	// not just in whichever hand-picked construct a narrower test happened
	// to cover.
	text1 := FormatMachine(m1)

	decl2 := parseSmoke(t, text1)
	m2, ok := decl2.(*MachineDef)
	if !ok {
		t.Fatalf("round trip: expected MachineDef, got %T", decl2)
	}
	text2 := FormatMachine(m2)

	if text1 != text2 {
		t.Errorf("round trip drift: formatting is not idempotent\n--- first format ---\n%s\n--- second format ---\n%s", text1, text2)
	}

	// Belt and suspenders: the reparsed AST should describe the same
	// machine shape as the original, not just format identically to
	// itself.
	if len(m2.States) != len(m1.States) {
		t.Fatalf("round trip: got %d states, want %d", len(m2.States), len(m1.States))
	}
	for i := range m1.States {
		a, b := m1.States[i], m2.States[i]
		if a.Name != b.Name {
			t.Errorf("state %d: name got %q, want %q", i, b.Name, a.Name)
		}
		if a.Operator != b.Operator {
			t.Errorf("state %q: operator got %v, want %v", a.Name, b.Operator, a.Operator)
		}
		if a.DaqLocal != b.DaqLocal {
			t.Errorf("state %q: daqLocal got %q, want %q", a.Name, b.DaqLocal, a.DaqLocal)
		}
		if len(a.AbortRules) != len(b.AbortRules) {
			t.Errorf("state %q: abort rules got %d, want %d", a.Name, len(b.AbortRules), len(a.AbortRules))
		}
	}
}

// parseSmoke tokenizes and parses src, failing the test on any error.
func parseSmoke(t *testing.T, src string) Decl {
	t.Helper()
	toks, err := NewLexer(src).Tokenize()
	if err != nil {
		t.Fatalf("lex: %v", err)
	}
	decl, err := Parse(toks)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return decl
}
