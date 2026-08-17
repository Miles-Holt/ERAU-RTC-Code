package dsl

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// ── Token types ──────────────────────────────────────────────────────────────

type TokenType int

const (
	TOK_EOF TokenType = iota

	// Indentation
	TOK_INDENT
	TOK_DEDENT

	// Literals
	TOK_INT
	TOK_FLOAT
	TOK_DURATION
	TOK_STRING
	TOK_TRUE
	TOK_FALSE

	// Identifiers and keywords
	TOK_IDENT

	// Keywords
	TOK_MACHINE
	TOK_STATE
	TOK_OPERATOR
	TOK_CONTROLLER
	TOK_SEQUENCE
	TOK_DAQ_LOCAL
	TOK_ABORT_RULE
	TOK_ABORT_SEQUENCE
	TOK_TRANSITION
	TOK_SLEEP
	TOK_WAIT_UNTIL
	TOK_TIMEOUT
	TOK_CHANNEL
	TOK_TYPE
	TOK_DEFAULT
	TOK_MIN
	TOK_MAX
	TOK_UNITS
	TOK_COMPUTE
	TOK_DESCRIPTION
	TOK_TEMPLATE
	TOK_ON
	TOK_ALERT
	TOK_IF
	TOK_ELIF
	TOK_ELSE
	TOK_SEVERITY
	TOK_MESSAGE
	TOK_LATCH
	TOK_AND
	TOK_OR
	TOK_NOT

	// Operators
	TOK_PLUS
	TOK_MINUS
	TOK_STAR
	TOK_SLASH
	TOK_PERCENT
	TOK_EQ
	TOK_NEQ
	TOK_LT
	TOK_LTE
	TOK_GT
	TOK_GTE
	TOK_ASSIGN
	TOK_INCREMENT
	TOK_DECREMENT

	// Delimiters
	TOK_LPAREN
	TOK_RPAREN
	TOK_DOT
	TOK_ARROW
	TOK_COMMA
)

// Token represents a lexical token.
type Token struct {
	Type  TokenType
	Value string
	Line  int
}

// ── Lexer ─────────────────────────────────────────────────────────────────

type Lexer struct {
	input       string
	pos         int    // current position
	line        int    // current line number (1-indexed)
	tokens      []Token
	indent      []int // stack of indentation levels
	indentType  string // "space" or "tab" - consistent throughout file
	atLineStart bool
}

// NewLexer creates a new lexer from input source code.
//
// Line endings are normalised to "\n" first: config files are edited on Windows
// and git's autocrlf hands us CRLF, which the indentation and newline handling
// below would otherwise reject as a stray character.
func NewLexer(input string) *Lexer {
	if strings.ContainsRune(input, '\r') {
		input = strings.ReplaceAll(input, "\r\n", "\n")
		input = strings.ReplaceAll(input, "\r", "\n")
	}
	return &Lexer{
		input:       input,
		pos:         0,
		line:        1,
		indent:      []int{0},
		indentType:  "",
		atLineStart: true,
	}
}

// Tokenize performs lexical analysis and returns the token stream or an error.
func (l *Lexer) Tokenize() ([]Token, error) {
	for l.pos < len(l.input) {
		if l.atLineStart {
			if err := l.handleIndentation(); err != nil {
				return nil, err
			}
		}
		if l.pos >= len(l.input) {
			break
		}

		c := l.input[l.pos]

		// Skip whitespace (not indentation)
		if c == ' ' || c == '\t' {
			l.pos++
			continue
		}

		// Newline
		if c == '\n' {
			l.line++
			l.pos++
			l.atLineStart = true
			continue
		}

		// Comment
		if c == '#' {
			l.skipComment()
			continue
		}

		// Two-character operators
		if l.pos+1 < len(l.input) {
			twoChar := l.input[l.pos : l.pos+2]
			if tok, ok := l.twoCharOp(twoChar); ok {
				l.tokens = append(l.tokens, Token{Type: tok, Value: twoChar, Line: l.line})
				l.pos += 2
				continue
			}
		}

		// String
		if c == '"' {
			if err := l.readString(); err != nil {
				return nil, err
			}
			continue
		}

		// Single-character delimiters
		switch c {
		case '(':
			l.tokens = append(l.tokens, Token{Type: TOK_LPAREN, Value: "(", Line: l.line})
			l.pos++
			continue
		case ')':
			l.tokens = append(l.tokens, Token{Type: TOK_RPAREN, Value: ")", Line: l.line})
			l.pos++
			continue
		case '.':
			l.tokens = append(l.tokens, Token{Type: TOK_DOT, Value: ".", Line: l.line})
			l.pos++
			continue
		case ',':
			// Only list separators use a comma today (`operator from a, b`).
			l.tokens = append(l.tokens, Token{Type: TOK_COMMA, Value: ",", Line: l.line})
			l.pos++
			continue
		}

		// Single-character operators
		switch c {
		case '+':
			l.tokens = append(l.tokens, Token{Type: TOK_PLUS, Value: "+", Line: l.line})
			l.pos++
			continue
		case '-':
			// A token can never START with '-': identifiers match
			// [A-Za-z_][A-Za-z0-9_.-]*, so the hyphens inside `PT-01` are
			// consumed by readIdentOrDuration from the leading letter.  Reaching
			// here therefore always means an operator — which is what makes
			// `A -B` subtraction (spec: binary minus requires spaces) while
			// `A-B` stays a single identifier.
			if l.pos+1 < len(l.input) && l.input[l.pos+1] == '-' {
				l.tokens = append(l.tokens, Token{Type: TOK_DECREMENT, Value: "--", Line: l.line})
				l.pos += 2
				continue
			}
			l.tokens = append(l.tokens, Token{Type: TOK_MINUS, Value: "-", Line: l.line})
			l.pos++
			continue
		case '*':
			l.tokens = append(l.tokens, Token{Type: TOK_STAR, Value: "*", Line: l.line})
			l.pos++
			continue
		case '/':
			l.tokens = append(l.tokens, Token{Type: TOK_SLASH, Value: "/", Line: l.line})
			l.pos++
			continue
		case '%':
			l.tokens = append(l.tokens, Token{Type: TOK_PERCENT, Value: "%", Line: l.line})
			l.pos++
			continue
		case '=':
			l.tokens = append(l.tokens, Token{Type: TOK_ASSIGN, Value: "=", Line: l.line})
			l.pos++
			continue
		case '<':
			l.tokens = append(l.tokens, Token{Type: TOK_LT, Value: "<", Line: l.line})
			l.pos++
			continue
		case '>':
			l.tokens = append(l.tokens, Token{Type: TOK_GT, Value: ">", Line: l.line})
			l.pos++
			continue
		}

		// Numbers (int, float, or duration)
		if unicode.IsDigit(rune(c)) {
			if err := l.readNumber(); err != nil {
				return nil, err
			}
			continue
		}

		// Identifiers and keywords
		if l.isIdentifierStart(c) {
			if err := l.readIdentOrDuration(); err != nil {
				return nil, err
			}
			continue
		}

		return nil, fmt.Errorf("lexer: line %d: unexpected character %q", l.line, c)
	}

	// Emit final DEDENTs
	for len(l.indent) > 1 {
		l.indent = l.indent[:len(l.indent)-1]
		l.tokens = append(l.tokens, Token{Type: TOK_DEDENT, Line: l.line})
	}

	l.tokens = append(l.tokens, Token{Type: TOK_EOF, Line: l.line})
	return l.tokens, nil
}

// ── Indentation handling ──────────────────────────────────────────────────

func (l *Lexer) handleIndentation() error {
	// Count indentation at the start of a line
	indent := 0
	currentLineIndentType := ""

	for l.pos < len(l.input) {
		c := l.input[l.pos]
		if c == ' ' {
			if currentLineIndentType == "tab" {
				return fmt.Errorf("lexer: line %d: mixed indentation within line", l.line)
			}
			currentLineIndentType = "space"
			indent += 1
			l.pos++
		} else if c == '\t' {
			if currentLineIndentType == "space" {
				return fmt.Errorf("lexer: line %d: mixed indentation within line", l.line)
			}
			currentLineIndentType = "tab"
			indent += 4 // tab counts as 4 spaces
			l.pos++
		} else {
			break
		}
	}

	// Skip blank lines and comments
	if l.pos >= len(l.input) || l.input[l.pos] == '\n' || l.input[l.pos] == '#' {
		l.atLineStart = false
		return nil
	}

	// Check for mixed indentation across the file
	if currentLineIndentType != "" {
		if l.indentType == "" {
			l.indentType = currentLineIndentType
		} else if l.indentType != currentLineIndentType {
			return fmt.Errorf("lexer: line %d: mixed indentation (file uses %s and spaces)", l.line, l.indentType)
		}
	}

	// Compare with current indent level
	current := l.indent[len(l.indent)-1]

	if indent > current {
		// Increase indentation
		l.indent = append(l.indent, indent)
		l.tokens = append(l.tokens, Token{Type: TOK_INDENT, Line: l.line})
	} else if indent < current {
		// Decrease indentation: emit multiple DEDENTs
		for len(l.indent) > 0 && l.indent[len(l.indent)-1] > indent {
			l.indent = l.indent[:len(l.indent)-1]
			l.tokens = append(l.tokens, Token{Type: TOK_DEDENT, Line: l.line})
		}
		if len(l.indent) == 0 || l.indent[len(l.indent)-1] != indent {
			return fmt.Errorf("lexer: line %d: indentation not aligned with any outer level", l.line)
		}
	}

	l.atLineStart = false
	return nil
}

// ── Helper methods ───────────────────────────────────────────────────────

func (l *Lexer) isIdentifierStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}

func (l *Lexer) isIdentifierChar(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' || c == '.'
}

func (l *Lexer) readIdentOrDuration() error {
	start := l.pos
	for l.pos < len(l.input) && l.isIdentifierChar(l.input[l.pos]) {
		// Stop if we encounter "--" or "++" (which should be separate tokens)
		if l.pos+1 < len(l.input) {
			if (l.input[l.pos] == '-' && l.input[l.pos+1] == '-') ||
				(l.input[l.pos] == '+' && l.input[l.pos+1] == '+') {
				break
			}
		}
		l.pos++
	}
	value := l.input[start:l.pos]

	// Check if it's a keyword
	if tok, ok := l.keyword(value); ok {
		l.tokens = append(l.tokens, Token{Type: tok, Value: value, Line: l.line})
		return nil
	}

	// Check if it's a boolean
	if value == "true" {
		l.tokens = append(l.tokens, Token{Type: TOK_TRUE, Value: value, Line: l.line})
		return nil
	}
	if value == "false" {
		l.tokens = append(l.tokens, Token{Type: TOK_FALSE, Value: value, Line: l.line})
		return nil
	}

	// Otherwise it's an identifier
	l.tokens = append(l.tokens, Token{Type: TOK_IDENT, Value: value, Line: l.line})
	return nil
}

func (l *Lexer) readNumber() error {
	start := l.pos
	hasDecimal := false

	// Read digits and optional decimal point
	for l.pos < len(l.input) {
		c := l.input[l.pos]
		if unicode.IsDigit(rune(c)) {
			l.pos++
		} else if c == '.' && !hasDecimal {
			hasDecimal = true
			l.pos++
		} else {
			break
		}
	}

	// Check for a duration suffix.  The whole trailing identifier run is
	// consumed so that a bogus unit (`5min`, `5sec`) is a lex error rather than
	// a silently truncated `5m` followed by the identifier `in`.
	sufStart := l.pos
	for l.pos < len(l.input) && l.isIdentifierChar(l.input[l.pos]) && l.input[l.pos] != '-' && l.input[l.pos] != '.' {
		l.pos++
	}
	suffix := l.input[sufStart:l.pos]
	hasDuration := suffix != ""

	value := l.input[start:l.pos]

	if hasDuration {
		switch suffix {
		case "ms", "s", "m":
		default:
			return fmt.Errorf("lexer: line %d: unknown duration suffix %q in %q", l.line, suffix, value)
		}
		// Parse duration and convert to SECONDS: the DSL's base time unit.
		// (The DAQ wire protocol still speaks milliseconds — that conversion
		// happens only at daq_local payload serialization, never here.)
		secValue, err := l.parseDuration(value)
		if err != nil {
			return err
		}
		l.tokens = append(l.tokens, Token{Type: TOK_DURATION, Value: formatSeconds(secValue), Line: l.line})
		return nil
	}

	if hasDecimal {
		l.tokens = append(l.tokens, Token{Type: TOK_FLOAT, Value: value, Line: l.line})
	} else {
		l.tokens = append(l.tokens, Token{Type: TOK_INT, Value: value, Line: l.line})
	}

	return nil
}

// parseDuration parses a suffixed duration literal (100ms, 5s, 2m) and
// normalises it to SECONDS, the DSL's base time unit.
func (l *Lexer) parseDuration(s string) (float64, error) {
	s = strings.TrimSpace(s)

	// Extract numeric part and unit
	var numStr string
	var unit string

	if strings.HasSuffix(s, "ms") {
		numStr = s[:len(s)-2]
		unit = "ms"
	} else if strings.HasSuffix(s, "s") {
		numStr = s[:len(s)-1]
		unit = "s"
	} else if strings.HasSuffix(s, "m") {
		numStr = s[:len(s)-1]
		unit = "m"
	} else {
		return 0, fmt.Errorf("lexer: line %d: invalid duration %q", l.line, s)
	}

	var num float64
	if _, err := fmt.Sscanf(numStr, "%f", &num); err != nil {
		return 0, fmt.Errorf("lexer: line %d: invalid duration %q", l.line, s)
	}

	switch unit {
	case "ms":
		return num / 1000, nil
	case "s":
		return num, nil
	case "m":
		return num * 60, nil
	}

	return 0, fmt.Errorf("lexer: line %d: invalid duration unit %q", l.line, unit)
}

// formatSeconds renders a duration literal's normalised value the way the
// parser expects to read it back: the shortest round-trippable decimal.
func formatSeconds(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func (l *Lexer) readString() error {
	l.pos++ // skip opening "
	start := l.pos

	for l.pos < len(l.input) && l.input[l.pos] != '"' {
		if l.input[l.pos] == '\n' {
			l.line++
		} else if l.input[l.pos] == '\\' && l.pos+1 < len(l.input) {
			l.pos++ // skip escape character
		}
		l.pos++
	}

	if l.pos >= len(l.input) {
		return fmt.Errorf("lexer: line %d: unterminated string", l.line)
	}

	value := l.input[start:l.pos]
	l.pos++ // skip closing "

	l.tokens = append(l.tokens, Token{Type: TOK_STRING, Value: value, Line: l.line})
	return nil
}

func (l *Lexer) skipComment() {
	for l.pos < len(l.input) && l.input[l.pos] != '\n' {
		l.pos++
	}
}

func (l *Lexer) twoCharOp(s string) (TokenType, bool) {
	switch s {
	case "==":
		return TOK_EQ, true
	case "!=":
		return TOK_NEQ, true
	case "<=":
		return TOK_LTE, true
	case ">=":
		return TOK_GTE, true
	case "++":
		return TOK_INCREMENT, true
	case "--":
		return TOK_DECREMENT, true
	case "->":
		return TOK_ARROW, true
	}
	return 0, false
}

func (l *Lexer) keyword(s string) (TokenType, bool) {
	keywords := map[string]TokenType{
		"machine":     TOK_MACHINE,
		"state":       TOK_STATE,
		"operator":    TOK_OPERATOR,
		"controller":  TOK_CONTROLLER,
		"sequence":    TOK_SEQUENCE,
		"daq_local":   TOK_DAQ_LOCAL,
		"abort_rule":     TOK_ABORT_RULE,
		"abort_sequence": TOK_ABORT_SEQUENCE,
		"transition":  TOK_TRANSITION,
		"sleep":       TOK_SLEEP,
		"wait_until":  TOK_WAIT_UNTIL,
		"timeout":     TOK_TIMEOUT,
		"channel":     TOK_CHANNEL,
		"type":        TOK_TYPE,
		"default":     TOK_DEFAULT,
		"min":         TOK_MIN,
		"max":         TOK_MAX,
		"units":       TOK_UNITS,
		"compute":     TOK_COMPUTE,
		"description": TOK_DESCRIPTION,
		"template":    TOK_TEMPLATE,
		"on":          TOK_ON,
		"alert":       TOK_ALERT,
		"if":          TOK_IF,
		"elif":        TOK_ELIF,
		"else":        TOK_ELSE,
		"severity":    TOK_SEVERITY,
		"message":     TOK_MESSAGE,
		"latch":       TOK_LATCH,
		"and":         TOK_AND,
		"or":          TOK_OR,
		"not":         TOK_NOT,
	}
	tok, ok := keywords[s]
	return tok, ok
}
