package dsl

import (
	"fmt"
	"strings"
	"testing"
)

func TestLexer_SimpleTokens(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect []TokenType
	}{
		{
			name:   "keywords",
			input:  "machine state operator controller sequence",
			expect: []TokenType{TOK_MACHINE, TOK_STATE, TOK_OPERATOR, TOK_CONTROLLER, TOK_SEQUENCE, TOK_EOF},
		},
		{
			name:   "identifiers",
			input:  "foo bar_baz PT-01 OV-05-CMD",
			expect: []TokenType{TOK_IDENT, TOK_IDENT, TOK_IDENT, TOK_IDENT, TOK_EOF},
		},
		{
			name:   "numbers",
			input:  "123 45.67 100ms 5s 2m",
			expect: []TokenType{TOK_INT, TOK_FLOAT, TOK_DURATION, TOK_DURATION, TOK_DURATION, TOK_EOF},
		},
		{
			name:   "operators",
			input:  "+ - * / %",
			expect: []TokenType{TOK_PLUS, TOK_MINUS, TOK_STAR, TOK_SLASH, TOK_PERCENT, TOK_EOF},
		},
		{
			name:   "comparisons",
			input:  "== != < <= > >=",
			expect: []TokenType{TOK_EQ, TOK_NEQ, TOK_LT, TOK_LTE, TOK_GT, TOK_GTE, TOK_EOF},
		},
		{
			name:   "assignment and increment",
			input:  "= ++ --",
			expect: []TokenType{TOK_ASSIGN, TOK_INCREMENT, TOK_DECREMENT, TOK_EOF},
		},
		{
			name:   "delimiters",
			input:  "( ) . ->",
			expect: []TokenType{TOK_LPAREN, TOK_RPAREN, TOK_DOT, TOK_ARROW, TOK_EOF},
		},
		{
			name:   "booleans",
			input:  "true false",
			expect: []TokenType{TOK_TRUE, TOK_FALSE, TOK_EOF},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lexer := NewLexer(tt.input)
			toks, err := lexer.Tokenize()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(toks) != len(tt.expect) {
				t.Errorf("got %d tokens, expected %d", len(toks), len(tt.expect))
				return
			}
			for i, expTyp := range tt.expect {
				if toks[i].Type != expTyp {
					t.Errorf("token %d: got %v, expected %v", i, toks[i].Type, expTyp)
				}
			}
		})
	}
}

func TestLexer_Strings(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect string
	}{
		{
			name:   "simple string",
			input:  `"hello"`,
			expect: "hello",
		},
		{
			name:   "string with spaces",
			input:  `"hello world"`,
			expect: "hello world",
		},
		{
			name:   "string with braces",
			input:  `"Chamber pressure: {CPT-01} psi"`,
			expect: "Chamber pressure: {CPT-01} psi",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lexer := NewLexer(tt.input)
			toks, err := lexer.Tokenize()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(toks) < 1 || toks[0].Type != TOK_STRING {
				t.Fatalf("expected string token")
			}
			if toks[0].Value != tt.expect {
				t.Errorf("got %q, expected %q", toks[0].Value, tt.expect)
			}
		})
	}
}

func TestLexer_Comments(t *testing.T) {
	input := `machine fuel  # this is a comment
state safe
    operator # comment at end`

	lexer := NewLexer(input)
	toks, err := lexer.Tokenize()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Comments should be stripped, machine and identifiers should be present
	// We should see MACHINE, IDENT, STATE, IDENT, INDENT, OPERATOR, DEDENT, EOF
	expectedTypes := []TokenType{TOK_MACHINE, TOK_IDENT, TOK_STATE, TOK_IDENT, TOK_INDENT, TOK_OPERATOR, TOK_DEDENT, TOK_EOF}

	if len(toks) != len(expectedTypes) {
		t.Errorf("got %d tokens, expected %d", len(toks), len(expectedTypes))
		for i, tok := range toks {
			t.Logf("  %d: %v (%q)", i, tok.Type, tok.Value)
		}
		return
	}

	for i, expTyp := range expectedTypes {
		if toks[i].Type != expTyp {
			t.Errorf("token %d: got %v (%q), expected %v", i, toks[i].Type, toks[i].Value, expTyp)
		}
	}
}

func TestLexer_Indentation(t *testing.T) {
	input := `machine fuel
state safe
    operator
    controller
        if a > b
            transition abort
state abort`

	lexer := NewLexer(input)
	toks, err := lexer.Tokenize()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Count INDENTs and DEDENTs
	indents := 0
	dedents := 0
	for _, tok := range toks {
		if tok.Type == TOK_INDENT {
			indents++
		} else if tok.Type == TOK_DEDENT {
			dedents++
		}
	}

	if indents != dedents {
		t.Errorf("indents (%d) != dedents (%d)", indents, dedents)
	}
}

func TestLexer_MixedIndentationError(t *testing.T) {
	input := "machine fuel\n\tstate safe\n    operator"

	lexer := NewLexer(input)
	_, err := lexer.Tokenize()
	if err == nil {
		t.Errorf("expected error for mixed indentation, got nil")
	}
}

func TestLexer_Identifiers(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect []string
	}{
		{
			name:   "simple identifier",
			input:  "foo",
			expect: []string{"foo"},
		},
		{
			name:   "identifier with underscore",
			input:  "foo_bar",
			expect: []string{"foo_bar"},
		},
		{
			name:   "refDes with hyphen",
			input:  "PT-01",
			expect: []string{"PT-01"},
		},
		{
			name:   "complex refDes",
			input:  "OV-05-CMD",
			expect: []string{"OV-05-CMD"},
		},
		{
			name:   "binary minus requires spaces",
			input:  "a - b",
			expect: []string{"a", "b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lexer := NewLexer(tt.input)
			toks, err := lexer.Tokenize()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			var idents []string
			for _, tok := range toks {
				if tok.Type == TOK_IDENT {
					idents = append(idents, tok.Value)
				}
			}

			if len(idents) != len(tt.expect) {
				t.Errorf("got %d identifiers, expected %d: %v", len(idents), len(tt.expect), idents)
				return
			}

			for i, exp := range tt.expect {
				if idents[i] != exp {
					t.Errorf("identifier %d: got %q, expected %q", i, idents[i], exp)
				}
			}
		})
	}
}

// TestLexer_Durations checks that suffixed duration literals normalise to
// SECONDS — the DSL's base time unit — not milliseconds.
func TestLexer_Durations(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect float64
	}{
		{
			name:   "milliseconds",
			input:  "100ms",
			expect: 0.1,
		},
		{
			name:   "seconds",
			input:  "5s",
			expect: 5,
		},
		{
			name:   "minutes",
			input:  "2m",
			expect: 120,
		},
		{
			name:   "float milliseconds",
			input:  "250ms",
			expect: 0.25,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lexer := NewLexer(tt.input)
			toks, err := lexer.Tokenize()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			var found float64
			for _, tok := range toks {
				if tok.Type == TOK_DURATION {
					var val float64
					_, _ = fmt.Sscanf(tok.Value, "%f", &val)
					found = val
					break
				}
			}

			if found != tt.expect {
				t.Errorf("got %v, expected %v", found, tt.expect)
			}
		})
	}
}

func TestLexer_LineNumbers(t *testing.T) {
	input := `machine fuel
state safe
    operator`

	lexer := NewLexer(input)
	toks, err := lexer.Tokenize()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// machine should be on line 1
	if toks[0].Line != 1 {
		t.Errorf("machine: expected line 1, got %d", toks[0].Line)
	}

	// state should be on line 2
	for i, tok := range toks {
		if tok.Type == TOK_STATE {
			if tok.Line != 2 {
				t.Errorf("state (token %d): expected line 2, got %d", i, tok.Line)
			}
			break
		}
	}

	// operator should be on line 3
	for i, tok := range toks {
		if tok.Type == TOK_OPERATOR {
			if tok.Line != 3 {
				t.Errorf("operator (token %d): expected line 3, got %d", i, tok.Line)
			}
			break
		}
	}
}

// TestLexer_MinusAdjacency covers the `A -B` case: hyphens belong to
// identifiers (PT-01), but a hyphen that follows whitespace after an operand is
// a binary minus, per the spec's "binary minus requires spaces" rule.
func TestLexer_MinusAdjacency(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []TokenType
	}{
		{"spaced minus", "A - B", []TokenType{TOK_IDENT, TOK_MINUS, TOK_IDENT}},
		{"left-spaced minus", "A -B", []TokenType{TOK_IDENT, TOK_MINUS, TOK_IDENT}},
		{"glued hyphen is one identifier", "PT-01", []TokenType{TOK_IDENT}},
		{"number minus identifier", "2000 - SEQ-IGN-LEAD", []TokenType{TOK_INT, TOK_MINUS, TOK_IDENT}},
		{"number minus spaced-left identifier", "2000 -SEQ-IGN-LEAD", []TokenType{TOK_INT, TOK_MINUS, TOK_IDENT}},
		{"unary minus after operator", "X = -B", []TokenType{TOK_IDENT, TOK_ASSIGN, TOK_MINUS, TOK_IDENT}},
		{"unary minus at start", "-B", []TokenType{TOK_MINUS, TOK_IDENT}},
		{"minus after paren", "(A) - B", []TokenType{TOK_LPAREN, TOK_IDENT, TOK_RPAREN, TOK_MINUS, TOK_IDENT}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toks, err := NewLexer(tt.input).Tokenize()
			if err != nil {
				t.Fatalf("tokenize %q: %v", tt.input, err)
			}
			var got []TokenType
			for _, tk := range toks {
				if tk.Type == TOK_EOF || tk.Type == TOK_INDENT || tk.Type == TOK_DEDENT {
					continue
				}
				got = append(got, tk.Type)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("tokenize %q: got %d tokens %v, want %d %v", tt.input, len(got), got, len(tt.want), tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("tokenize %q: token %d = %v, want %v", tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}

	// `A -B` must mean subtraction, so the second operand is plain `B`.
	toks, err := NewLexer("A -B").Tokenize()
	if err != nil {
		t.Fatalf("tokenize: %v", err)
	}
	if toks[2].Value != "B" {
		t.Errorf("right operand = %q, want B (not -B)", toks[2].Value)
	}
}

// TestLexer_CompoundAssign covers the tricky tokenisation cases for += and
// -=: hyphenated identifiers (legal, and themselves built from '-') sitting
// right next to a "-=" operator, with and without surrounding space, plus a
// negative-literal RHS.
func TestLexer_CompoundAssign(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []TokenType
	}{
		{"spaced +=", "A += B", []TokenType{TOK_IDENT, TOK_PLUS_ASSIGN, TOK_IDENT}},
		{"spaced -=", "A -= B", []TokenType{TOK_IDENT, TOK_MINUS_ASSIGN, TOK_IDENT}},
		{"hyphenated target -=", "T-TIME -= B", []TokenType{TOK_IDENT, TOK_MINUS_ASSIGN, TOK_IDENT}},
		{"hyphenated target and value -=", "A-B -= C", []TokenType{TOK_IDENT, TOK_MINUS_ASSIGN, TOK_IDENT}},
		{"glued -= after hyphenated ident", "T-TIME-=1", []TokenType{TOK_IDENT, TOK_MINUS_ASSIGN, TOK_INT}},
		{"glued -= after plain ident", "A-=B", []TokenType{TOK_IDENT, TOK_MINUS_ASSIGN, TOK_IDENT}},
		{"negative literal RHS", "A -= -1", []TokenType{TOK_IDENT, TOK_MINUS_ASSIGN, TOK_MINUS, TOK_INT}},
		{"negative literal RHS glued", "A -=-1", []TokenType{TOK_IDENT, TOK_MINUS_ASSIGN, TOK_MINUS, TOK_INT}},
		{"glued +=", "A+=1", []TokenType{TOK_IDENT, TOK_PLUS_ASSIGN, TOK_INT}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toks, err := NewLexer(tt.input).Tokenize()
			if err != nil {
				t.Fatalf("tokenize %q: %v", tt.input, err)
			}
			var got []TokenType
			for _, tk := range toks {
				if tk.Type == TOK_EOF || tk.Type == TOK_INDENT || tk.Type == TOK_DEDENT {
					continue
				}
				got = append(got, tk.Type)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("tokenize %q: got %d tokens %v, want %d %v", tt.input, len(got), got, len(tt.want), tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("tokenize %q: token %d = %v, want %v", tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}

	// The target must not swallow the "-" that belongs to "-=".
	toks, err := NewLexer("T-TIME-=1").Tokenize()
	if err != nil {
		t.Fatalf("tokenize: %v", err)
	}
	if toks[0].Value != "T-TIME" {
		t.Errorf("target = %q, want T-TIME", toks[0].Value)
	}
}

// TestLexer_DurationSuffixes covers `5min`: an unrecognised duration unit is a
// lex error, not a silently truncated `5m` plus a stray identifier.
func TestLexer_DurationSuffixes(t *testing.T) {
	ok := map[string]string{
		"100ms": "0.1",
		"5s":    "5",
		"2m":    "120",
		"1.5s":  "1.5",
	}
	for src, want := range ok {
		toks, err := NewLexer(src).Tokenize()
		if err != nil {
			t.Errorf("tokenize %q: unexpected error %v", src, err)
			continue
		}
		if toks[0].Type != TOK_DURATION || toks[0].Value != want {
			t.Errorf("tokenize %q: got %v %q, want DURATION %q", src, toks[0].Type, toks[0].Value, want)
		}
	}

	bad := []string{"5min", "5sec", "10hours", "3x"}
	for _, src := range bad {
		if _, err := NewLexer(src).Tokenize(); err == nil {
			t.Errorf("tokenize %q: expected an unknown-duration-suffix error", src)
		} else if !strings.Contains(err.Error(), "duration suffix") {
			t.Errorf("tokenize %q: error %q should mention the duration suffix", src, err)
		}
	}

	// A bare number followed by a separate identifier is still fine.
	toks, err := NewLexer("5 min").Tokenize()
	if err != nil {
		t.Fatalf("tokenize %q: %v", "5 min", err)
	}
	if toks[0].Type != TOK_INT || toks[1].Type != TOK_MIN {
		t.Errorf("tokenize \"5 min\": got %v %v, want INT then min", toks[0].Type, toks[1].Type)
	}
}

// TestLexer_CRLF guards the Windows path: git's autocrlf hands the lexer CRLF
// (and the editors on the test stand produce it too), so a config file must
// tokenise identically either way, with line numbers intact.
func TestLexer_CRLF(t *testing.T) {
	lf := "channel PT-01\n    type float\n    default 3\n"
	crlf := strings.ReplaceAll(lf, "\n", "\r\n")

	want, err := NewLexer(lf).Tokenize()
	if err != nil {
		t.Fatalf("tokenize LF: %v", err)
	}
	got, err := NewLexer(crlf).Tokenize()
	if err != nil {
		t.Fatalf("tokenize CRLF: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("CRLF token count = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Type != want[i].Type || got[i].Value != want[i].Value || got[i].Line != want[i].Line {
			t.Errorf("token %d: got %v %q line %d, want %v %q line %d",
				i, got[i].Type, got[i].Value, got[i].Line,
				want[i].Type, want[i].Value, want[i].Line)
		}
	}
}
