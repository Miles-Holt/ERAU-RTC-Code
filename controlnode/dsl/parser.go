package dsl

import (
	"fmt"
	"strconv"
)

// parseInt64 converts a lexer-produced integer/duration value (durations are
// already normalised to milliseconds) to an int64.
func parseInt64(s string) (int64, error) {
	return strconv.ParseInt(s, 10, 64)
}

// ── Parser ───────────────────────────────────────────────────────────────
//
// Every token the grammar requires is taken with expect(), which returns a
// `file:line:` error when the token is missing.  A truncated file therefore
// always fails to parse — it never yields a silently-wrong AST.

type Parser struct {
	tokens []Token
	pos    int
}

// Parse parses a token stream and returns a top-level declaration or an error.
func Parse(tokens []Token) (Decl, error) {
	p := &Parser{tokens: tokens, pos: 0}

	// Skip any leading indent/dedent tokens that shouldn't be there
	p.skipNewlines()

	decl, err := p.parseDecl()
	if err != nil {
		return nil, err
	}
	if decl == nil {
		return nil, fmt.Errorf("file:1: empty file or unexpected tokens")
	}
	return decl, nil
}

// ── Top-level declarations ────────────────────────────────────────────────

func (p *Parser) parseDecl() (Decl, error) {
	if p.isEOF() {
		return nil, nil
	}

	tok := p.peek()
	switch tok.Type {
	case TOK_MACHINE:
		return p.parseMachine()
	case TOK_CHANNEL:
		return p.parseChannel()
	case TOK_TEMPLATE:
		return p.parseTemplate()
	case TOK_ALERT:
		return p.parseAlert()
	default:
		return nil, p.errorf(tok.Line, "expected machine, channel, template, or alert, got %s", p.tokStr(tok))
	}
}

// ── Machine parsing ───────────────────────────────────────────────────────

func (p *Parser) parseMachine() (*MachineDef, error) {
	mtok, err := p.expect(TOK_MACHINE)
	if err != nil {
		return nil, err
	}
	line := mtok.Line
	nameToken, err := p.expect(TOK_IDENT)
	if err != nil {
		return nil, err
	}
	name := nameToken.Value

	p.skipNewlines()

	states := []*StateDef{}
	for p.peek().Type == TOK_STATE {
		state, err := p.parseState()
		if err != nil {
			return nil, err
		}
		states = append(states, state)
		p.skipNewlines()
	}

	if len(states) == 0 {
		return nil, p.errorf(line, "machine %q: no states defined", name)
	}
	if !p.isEOF() {
		return nil, p.errorf(p.peek().Line, "unexpected %s after machine %q", p.tokStr(p.peek()), name)
	}

	return &MachineDef{
		Name:   name,
		States: states,
		LineNo: line,
	}, nil
}

func (p *Parser) parseState() (*StateDef, error) {
	stok, err := p.expect(TOK_STATE)
	if err != nil {
		return nil, err
	}
	line := stok.Line
	nameToken, err := p.expect(TOK_IDENT)
	if err != nil {
		return nil, err
	}
	name := nameToken.Value

	// A state with no body is allowed (a bare manual-control mode).
	if !p.is(TOK_INDENT) {
		return &StateDef{Name: name, LineNo: line}, nil
	}
	p.advance() // INDENT
	p.skipNewlines()

	state := &StateDef{
		Name:       name,
		LineNo:     line,
		Controller: []Stmt{},
		Sequence:   []Stmt{},
		AbortRules: []*AbortRule{},
	}

	// Parse state-level keywords: operator, daq_local, controller, sequence,
	// abort_sequence, abort_rule.
	for {
		tok := p.peek()
		switch tok.Type {
		case TOK_OPERATOR:
			p.advance()
			state.Operator = true

		case TOK_DAQ_LOCAL:
			p.advance()
			nodeToken, err := p.expect(TOK_IDENT)
			if err != nil {
				return nil, err
			}
			state.DaqLocal = nodeToken.Value

		case TOK_CONTROLLER:
			p.advance()
			body, err := p.parseBlock()
			if err != nil {
				return nil, err
			}
			state.Controller = append(state.Controller, body...)

		case TOK_SEQUENCE:
			p.advance()
			body, err := p.parseBlock()
			if err != nil {
				return nil, err
			}
			state.Sequence = append(state.Sequence, body...)

		case TOK_ABORT_SEQUENCE:
			p.advance()
			body, err := p.parseBlock()
			if err != nil {
				return nil, err
			}
			state.HasAbortSequence = true
			state.AbortSeqLine = tok.Line
			state.AbortSequence = append(state.AbortSequence, body...)

		case TOK_ABORT_RULE:
			rule, err := p.parseAbortRule()
			if err != nil {
				return nil, err
			}
			state.AbortRules = append(state.AbortRules, rule)

		case TOK_DEDENT:
			p.advance()
			return state, nil

		default:
			return nil, p.errorf(tok.Line, "unexpected token in state: %s", p.tokStr(tok))
		}
	}
}

// parseBlock parses an optional indented statement block (controller, sequence,
// abort_sequence).  A missing INDENT means an empty block, which is legal.
func (p *Parser) parseBlock() ([]Stmt, error) {
	if !p.is(TOK_INDENT) {
		return nil, nil
	}
	p.advance()
	var out []Stmt
	for !p.is(TOK_DEDENT) && !p.is(TOK_EOF) {
		stmt, err := p.parseStmt()
		if err != nil {
			return nil, err
		}
		out = append(out, stmt)
	}
	if p.is(TOK_DEDENT) {
		p.advance()
	}
	return out, nil
}

func (p *Parser) parseAbortRule() (*AbortRule, error) {
	rtok, err := p.expect(TOK_ABORT_RULE)
	if err != nil {
		return nil, err
	}
	line := rtok.Line
	channelToken, err := p.expect(TOK_IDENT)
	if err != nil {
		return nil, err
	}
	channel := channelToken.Value

	op := p.peek().Value
	switch p.peek().Type {
	case TOK_GT, TOK_LT, TOK_GTE, TOK_LTE, TOK_EQ, TOK_NEQ:
		p.advance()
	default:
		return nil, p.errorf(line, "abort_rule: expected comparison operator, got %s", p.tokStr(p.peek()))
	}

	value, err := p.parseExpr()
	if err != nil {
		return nil, err
	}

	// Expect "from" keyword
	fromToken, err := p.expect(TOK_IDENT)
	if err != nil {
		return nil, err
	}
	if fromToken.Value != "from" {
		return nil, p.errorf(fromToken.Line, "abort_rule: expected 'from', got %q", fromToken.Value)
	}

	fromExpr, err := p.parseExpr()
	if err != nil {
		return nil, err
	}

	// Expect "to" keyword
	toToken, err := p.expect(TOK_IDENT)
	if err != nil {
		return nil, err
	}
	if toToken.Value != "to" {
		return nil, p.errorf(toToken.Line, "abort_rule: expected 'to', got %q", toToken.Value)
	}

	toExpr, err := p.parseExpr()
	if err != nil {
		return nil, err
	}

	return &AbortRule{
		Channel: channel,
		Op:      op,
		Value:   value,
		FromMs:  fromExpr,
		ToMs:    toExpr,
		LineNo:  line,
	}, nil
}

// ── Statement parsing ──────────────────────────────────────────────────────

func (p *Parser) parseStmt() (Stmt, error) {
	tok := p.peek()

	switch tok.Type {
	case TOK_IF:
		return p.parseIf()
	case TOK_TRANSITION:
		return p.parseTransition()
	case TOK_SLEEP:
		return p.parseSleep()
	case TOK_WAIT_UNTIL:
		return p.parseWaitUntil()
	case TOK_IDENT:
		// Assignment, increment, or decrement
		return p.parseAssignOrOp()
	default:
		return nil, p.errorf(tok.Line, "unexpected statement: %s", p.tokStr(tok))
	}
}

func (p *Parser) parseAssignOrOp() (Stmt, error) {
	tok, err := p.expect(TOK_IDENT)
	if err != nil {
		return nil, err
	}
	target, line := tok.Value, tok.Line

	switch p.peek().Type {
	case TOK_ASSIGN:
		p.advance()
		expr, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		return &AssignStmt{Target: target, Value: expr, LineNo: line}, nil

	case TOK_INCREMENT:
		p.advance()
		return &IncrementStmt{Target: target, LineNo: line}, nil

	case TOK_DECREMENT:
		p.advance()
		return &DecrementStmt{Target: target, LineNo: line}, nil

	default:
		return nil, p.errorf(line, "expected =, ++, or --, got %s", p.tokStr(p.peek()))
	}
}

func (p *Parser) parseIf() (*IfStmt, error) {
	itok, err := p.expect(TOK_IF)
	if err != nil {
		return nil, err
	}
	line := itok.Line
	cond, err := p.parseExpr()
	if err != nil {
		return nil, err
	}

	if !p.is(TOK_INDENT) {
		return nil, p.errorf(line, "if: expected indented block")
	}
	p.advance()

	body := []Stmt{}
	for !p.is(TOK_DEDENT) && !p.is(TOK_ELIF) && !p.is(TOK_ELSE) && !p.is(TOK_EOF) {
		stmt, err := p.parseStmt()
		if err != nil {
			return nil, err
		}
		body = append(body, stmt)
	}
	if p.is(TOK_DEDENT) {
		p.advance()
	}

	ifStmt := &IfStmt{
		Condition: cond,
		Body:      body,
		LineNo:    line,
	}

	// Handle elif/else
	for p.is(TOK_ELIF) {
		elifLine := p.peek().Line
		p.advance()
		elifCond, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if !p.is(TOK_INDENT) {
			return nil, p.errorf(elifLine, "elif: expected indented block")
		}
		p.advance()

		elifBody := []Stmt{}
		for !p.is(TOK_DEDENT) && !p.is(TOK_ELIF) && !p.is(TOK_ELSE) && !p.is(TOK_EOF) {
			stmt, err := p.parseStmt()
			if err != nil {
				return nil, err
			}
			elifBody = append(elifBody, stmt)
		}
		if p.is(TOK_DEDENT) {
			p.advance()
		}

		ifStmt.Elif = append(ifStmt.Elif, &IfStmt{
			Condition: elifCond,
			Body:      elifBody,
			LineNo:    elifLine,
		})
	}

	// Handle else
	if p.is(TOK_ELSE) {
		elseLine := p.peek().Line
		p.advance()
		if !p.is(TOK_INDENT) {
			return nil, p.errorf(elseLine, "else: expected indented block")
		}
		p.advance()

		elseBody := []Stmt{}
		for !p.is(TOK_DEDENT) && !p.is(TOK_EOF) {
			stmt, err := p.parseStmt()
			if err != nil {
				return nil, err
			}
			elseBody = append(elseBody, stmt)
		}
		if p.is(TOK_DEDENT) {
			p.advance()
		}

		ifStmt.Elif = append(ifStmt.Elif, &IfStmt{
			Condition: nil, // nil condition = else
			Body:      elseBody,
			LineNo:    elseLine,
		})
	}

	return ifStmt, nil
}

func (p *Parser) parseTransition() (*TransitionStmt, error) {
	ttok, err := p.expect(TOK_TRANSITION)
	if err != nil {
		return nil, err
	}
	target, err := p.expect(TOK_IDENT)
	if err != nil {
		return nil, err
	}
	return &TransitionStmt{Target: target.Value, LineNo: ttok.Line}, nil
}

func (p *Parser) parseSleep() (*SleepStmt, error) {
	stok, err := p.expect(TOK_SLEEP)
	if err != nil {
		return nil, err
	}
	duration, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	return &SleepStmt{Duration: duration, LineNo: stok.Line}, nil
}

func (p *Parser) parseWaitUntil() (*WaitUntilStmt, error) {
	wtok, err := p.expect(TOK_WAIT_UNTIL)
	if err != nil {
		return nil, err
	}
	cond, err := p.parseExpr()
	if err != nil {
		return nil, err
	}

	stmt := &WaitUntilStmt{Condition: cond, LineNo: wtok.Line}

	// Optional timeout
	if p.is(TOK_TIMEOUT) {
		p.advance()
		timeout, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		stmt.Timeout = timeout

		if p.is(TOK_ARROW) {
			p.advance()
			targetToken, err := p.expect(TOK_IDENT)
			if err != nil {
				return nil, err
			}
			stmt.TimeoutState = targetToken.Value
		}
	}

	return stmt, nil
}

// ── Channel parsing ────────────────────────────────────────────────────────

func (p *Parser) parseChannel() (*ChannelDef, error) {
	ctok, err := p.expect(TOK_CHANNEL)
	if err != nil {
		return nil, err
	}
	line := ctok.Line
	nameToken, err := p.expect(TOK_IDENT)
	if err != nil {
		return nil, err
	}
	name := nameToken.Value

	if !p.is(TOK_INDENT) {
		return nil, p.errorf(line, "channel %q: expected indented block", name)
	}
	p.advance()
	p.skipNewlines()

	ch := &ChannelDef{
		Name:   name,
		LineNo: line,
	}

	for {
		tok := p.peek()
		switch tok.Type {
		case TOK_TYPE:
			p.advance()
			typeToken, err := p.expect(TOK_IDENT)
			if err != nil {
				return nil, err
			}
			ch.Type = typeToken.Value

		case TOK_DEFAULT, TOK_MIN, TOK_MAX:
			p.advance()
			expr, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			lit, ok := expr.(*LiteralExpr)
			if !ok {
				return nil, p.errorf(tok.Line, "%s: expected literal value", p.tokName(tok.Type))
			}
			switch tok.Type {
			case TOK_DEFAULT:
				ch.Default = lit
			case TOK_MIN:
				ch.Min = lit
			case TOK_MAX:
				ch.Max = lit
			}

		case TOK_UNITS:
			p.advance()
			unitsToken, err := p.expect(TOK_IDENT)
			if err != nil {
				return nil, err
			}
			ch.Units = unitsToken.Value

		case TOK_COMPUTE:
			p.advance()
			expr, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			ch.Compute = expr

		case TOK_DESCRIPTION:
			p.advance()
			descToken, err := p.expect(TOK_STRING)
			if err != nil {
				return nil, err
			}
			ch.Description = descToken.Value

		case TOK_DEDENT:
			p.advance()
			return ch, nil

		case TOK_EOF:
			return ch, nil

		default:
			return nil, p.errorf(tok.Line, "unexpected keyword in channel: %s", p.tokStr(tok))
		}
	}
}

// ── Template parsing ──────────────────────────────────────────────────────

func (p *Parser) parseTemplate() (*TemplateDef, error) {
	ttok, err := p.expect(TOK_TEMPLATE)
	if err != nil {
		return nil, err
	}
	line := ttok.Line
	nameToken, err := p.expect(TOK_IDENT)
	if err != nil {
		return nil, err
	}
	name := nameToken.Value

	if !p.is(TOK_INDENT) {
		return nil, p.errorf(line, "template %q: expected indented block", name)
	}
	p.advance()
	p.skipNewlines()

	tmpl := &TemplateDef{
		Name:   name,
		Rules:  []*TemplateRule{},
		LineNo: line,
	}

	for !p.is(TOK_DEDENT) && !p.is(TOK_EOF) {
		onTok, err := p.expect(TOK_ON)
		if err != nil {
			return nil, err
		}

		eventToken, err := p.expect(TOK_IDENT)
		if err != nil {
			return nil, err
		}

		// Optional duration qualifier: `on stale 5s -> …`.  It only has meaning
		// for timeout-shaped events; the consumer rejects it on the others.
		var durMs int64
		if p.is(TOK_DURATION) {
			durTok := p.advance()
			ms, perr := parseInt64(durTok.Value)
			if perr != nil {
				return nil, p.errorf(durTok.Line, "template %q: bad duration %q", name, durTok.Value)
			}
			if ms <= 0 {
				return nil, p.errorf(durTok.Line, "template %q: duration must be positive", name)
			}
			durMs = ms
		}

		if _, err := p.expect(TOK_ARROW); err != nil {
			return nil, err
		}

		severityToken, err := p.expect(TOK_IDENT)
		if err != nil {
			return nil, err
		}

		msgToken, err := p.expect(TOK_STRING)
		if err != nil {
			return nil, err
		}

		tmpl.Rules = append(tmpl.Rules, &TemplateRule{
			Event:      eventToken.Value,
			Severity:   severityToken.Value,
			Message:    msgToken.Value,
			DurationMs: durMs,
			LineNo:     onTok.Line,
		})
	}

	if p.is(TOK_DEDENT) {
		p.advance()
	}
	return tmpl, nil
}

// ── Alert parsing ────────────────────────────────────────────────────────

func (p *Parser) parseAlert() (*AlertDef, error) {
	atok, err := p.expect(TOK_ALERT)
	if err != nil {
		return nil, err
	}
	line := atok.Line
	nameToken, err := p.expect(TOK_IDENT)
	if err != nil {
		return nil, err
	}
	name := nameToken.Value

	if !p.is(TOK_INDENT) {
		return nil, p.errorf(line, "alert %q: expected indented block", name)
	}
	p.advance()
	p.skipNewlines()

	alert := &AlertDef{
		Name:   name,
		LineNo: line,
	}

	for {
		tok := p.peek()
		switch tok.Type {
		case TOK_IF:
			p.advance()
			expr, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			alert.Condition = expr

		case TOK_SEVERITY:
			p.advance()
			severityToken, err := p.expect(TOK_IDENT)
			if err != nil {
				return nil, err
			}
			alert.Severity = severityToken.Value

		case TOK_MESSAGE:
			p.advance()
			msgToken, err := p.expect(TOK_STRING)
			if err != nil {
				return nil, err
			}
			alert.Message = msgToken.Value

		case TOK_LATCH:
			p.advance()
			alert.Latch = true

		case TOK_DEDENT:
			p.advance()
			return alert, nil

		case TOK_EOF:
			return alert, nil

		default:
			return nil, p.errorf(tok.Line, "unexpected keyword in alert: %s", p.tokStr(tok))
		}
	}
}

// ── Expression parsing (precedence climbing) ──────────────────────────────

func (p *Parser) parseExpr() (Expr, error) {
	return p.parseOrExpr()
}

func (p *Parser) parseOrExpr() (Expr, error) {
	left, err := p.parseAndExpr()
	if err != nil {
		return nil, err
	}

	for p.is(TOK_OR) {
		op := p.peek().Value
		p.advance()
		right, err := p.parseAndExpr()
		if err != nil {
			return nil, err
		}
		left = &BinaryExpr{Left: left, Op: op, Right: right, LineNo: p.tokens[p.pos-1].Line}
	}

	return left, nil
}

func (p *Parser) parseAndExpr() (Expr, error) {
	left, err := p.parseComparisonExpr()
	if err != nil {
		return nil, err
	}

	for p.is(TOK_AND) {
		op := p.peek().Value
		p.advance()
		right, err := p.parseComparisonExpr()
		if err != nil {
			return nil, err
		}
		left = &BinaryExpr{Left: left, Op: op, Right: right, LineNo: p.tokens[p.pos-1].Line}
	}

	return left, nil
}

func (p *Parser) parseComparisonExpr() (Expr, error) {
	left, err := p.parseAdditiveExpr()
	if err != nil {
		return nil, err
	}

	for {
		tok := p.peek()
		var op string
		switch tok.Type {
		case TOK_EQ:
			op = "=="
		case TOK_NEQ:
			op = "!="
		case TOK_LT:
			op = "<"
		case TOK_LTE:
			op = "<="
		case TOK_GT:
			op = ">"
		case TOK_GTE:
			op = ">="
		default:
			return left, nil
		}
		p.advance()
		right, err := p.parseAdditiveExpr()
		if err != nil {
			return nil, err
		}
		left = &BinaryExpr{Left: left, Op: op, Right: right, LineNo: tok.Line}
	}
}

func (p *Parser) parseAdditiveExpr() (Expr, error) {
	left, err := p.parseMultiplicativeExpr()
	if err != nil {
		return nil, err
	}

	for {
		tok := p.peek()
		var op string
		switch tok.Type {
		case TOK_PLUS:
			op = "+"
		case TOK_MINUS:
			op = "-"
		default:
			return left, nil
		}
		p.advance()
		right, err := p.parseMultiplicativeExpr()
		if err != nil {
			return nil, err
		}
		left = &BinaryExpr{Left: left, Op: op, Right: right, LineNo: tok.Line}
	}
}

func (p *Parser) parseMultiplicativeExpr() (Expr, error) {
	left, err := p.parseUnaryExpr()
	if err != nil {
		return nil, err
	}

	for {
		tok := p.peek()
		var op string
		switch tok.Type {
		case TOK_STAR:
			op = "*"
		case TOK_SLASH:
			op = "/"
		case TOK_PERCENT:
			op = "%"
		default:
			return left, nil
		}
		p.advance()
		right, err := p.parseUnaryExpr()
		if err != nil {
			return nil, err
		}
		left = &BinaryExpr{Left: left, Op: op, Right: right, LineNo: tok.Line}
	}
}

func (p *Parser) parseUnaryExpr() (Expr, error) {
	tok := p.peek()
	switch tok.Type {
	case TOK_NOT:
		p.advance()
		expr, err := p.parseUnaryExpr()
		if err != nil {
			return nil, err
		}
		return &UnaryExpr{Op: "not", Operand: expr, LineNo: tok.Line}, nil

	case TOK_MINUS:
		p.advance()
		expr, err := p.parseUnaryExpr()
		if err != nil {
			return nil, err
		}
		return &UnaryExpr{Op: "-", Operand: expr, LineNo: tok.Line}, nil

	default:
		return p.parsePrimaryExpr()
	}
}

func (p *Parser) parsePrimaryExpr() (Expr, error) {
	tok := p.peek()

	switch tok.Type {
	case TOK_INT:
		p.advance()
		var val int64
		_, _ = fmt.Sscanf(tok.Value, "%d", &val)
		return &LiteralExpr{Value: val, LineNo: tok.Line}, nil

	case TOK_FLOAT:
		p.advance()
		var val float64
		_, _ = fmt.Sscanf(tok.Value, "%f", &val)
		return &LiteralExpr{Value: val, LineNo: tok.Line}, nil

	case TOK_DURATION:
		p.advance()
		var val int64
		_, _ = fmt.Sscanf(tok.Value, "%d", &val)
		return &LiteralExpr{Value: val, LineNo: tok.Line}, nil

	case TOK_STRING:
		p.advance()
		return &LiteralExpr{Value: tok.Value, LineNo: tok.Line}, nil

	case TOK_TRUE:
		p.advance()
		return &LiteralExpr{Value: true, LineNo: tok.Line}, nil

	case TOK_FALSE:
		p.advance()
		return &LiteralExpr{Value: false, LineNo: tok.Line}, nil

	case TOK_IDENT:
		return p.parseIdentOrMember()

	case TOK_LPAREN:
		p.advance()
		expr, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(TOK_RPAREN); err != nil {
			return nil, err
		}
		return expr, nil

	default:
		return nil, p.errorf(tok.Line, "unexpected token in expression: %s", p.tokStr(tok))
	}
}

func (p *Parser) parseIdentOrMember() (Expr, error) {
	tok, err := p.expect(TOK_IDENT)
	if err != nil {
		return nil, err
	}
	name := tok.Value
	line := tok.Line

	// Handle member access (dots)
	for p.is(TOK_DOT) {
		p.advance()
		memberToken, err := p.expect(TOK_IDENT)
		if err != nil {
			return nil, err
		}
		name += "." + memberToken.Value
	}

	return &IdentExpr{Name: name, LineNo: line}, nil
}

// ── Helper methods ────────────────────────────────────────────────────────

func (p *Parser) peek() Token {
	if p.pos < len(p.tokens) {
		return p.tokens[p.pos]
	}
	return Token{Type: TOK_EOF, Line: p.lastLine()}
}

func (p *Parser) lastLine() int {
	if len(p.tokens) == 0 {
		return 1
	}
	return p.tokens[len(p.tokens)-1].Line
}

func (p *Parser) is(typ TokenType) bool {
	return p.peek().Type == typ
}

// advance consumes the current token without checking it.  Only for tokens the
// caller has already matched with is()/peek().
func (p *Parser) advance() Token {
	tok := p.peek()
	if p.pos < len(p.tokens) {
		p.pos++
	}
	return tok
}

// expect consumes the current token, or fails with a file:line error when it is
// not the required type.  A missing token is never silently skipped.
func (p *Parser) expect(typ TokenType) (Token, error) {
	tok := p.peek()
	if tok.Type != typ {
		return Token{}, p.errorf(tok.Line, "expected %s, got %s", p.tokName(typ), p.tokStr(tok))
	}
	p.pos++
	return tok, nil
}

func (p *Parser) isEOF() bool {
	return p.peek().Type == TOK_EOF
}

func (p *Parser) skipNewlines() {
	for p.is(TOK_INDENT) || p.is(TOK_DEDENT) {
		p.pos++
	}
}

func (p *Parser) errorf(line int, msg string, args ...interface{}) error {
	return fmt.Errorf("file:%d: %s", line, fmt.Sprintf(msg, args...))
}

func (p *Parser) tokStr(tok Token) string {
	if tok.Value != "" {
		return fmt.Sprintf("%q", tok.Value)
	}
	return p.tokName(tok.Type)
}

func (p *Parser) tokName(typ TokenType) string {
	names := map[TokenType]string{
		TOK_EOF:            "EOF",
		TOK_INDENT:         "INDENT",
		TOK_DEDENT:         "DEDENT",
		TOK_INT:            "INT",
		TOK_FLOAT:          "FLOAT",
		TOK_DURATION:       "DURATION",
		TOK_STRING:         "STRING",
		TOK_TRUE:           "true",
		TOK_FALSE:          "false",
		TOK_IDENT:          "IDENT",
		TOK_MACHINE:        "machine",
		TOK_STATE:          "state",
		TOK_OPERATOR:       "operator",
		TOK_CONTROLLER:     "controller",
		TOK_SEQUENCE:       "sequence",
		TOK_DAQ_LOCAL:      "daq_local",
		TOK_ABORT_RULE:     "abort_rule",
		TOK_ABORT_SEQUENCE: "abort_sequence",
		TOK_TRANSITION:     "transition",
		TOK_SLEEP:          "sleep",
		TOK_WAIT_UNTIL:     "wait_until",
		TOK_TIMEOUT:        "timeout",
		TOK_CHANNEL:        "channel",
		TOK_TYPE:           "type",
		TOK_DEFAULT:        "default",
		TOK_MIN:            "min",
		TOK_MAX:            "max",
		TOK_UNITS:          "units",
		TOK_COMPUTE:        "compute",
		TOK_DESCRIPTION:    "description",
		TOK_TEMPLATE:       "template",
		TOK_ON:             "on",
		TOK_ALERT:          "alert",
		TOK_IF:             "if",
		TOK_ELIF:           "elif",
		TOK_ELSE:           "else",
		TOK_SEVERITY:       "severity",
		TOK_MESSAGE:        "message",
		TOK_LATCH:          "latch",
		TOK_AND:            "and",
		TOK_OR:             "or",
		TOK_NOT:            "not",
		TOK_PLUS:           "+",
		TOK_MINUS:          "-",
		TOK_STAR:           "*",
		TOK_SLASH:          "/",
		TOK_PERCENT:        "%",
		TOK_EQ:             "==",
		TOK_NEQ:            "!=",
		TOK_LT:             "<",
		TOK_LTE:            "<=",
		TOK_GT:             ">",
		TOK_GTE:            ">=",
		TOK_ASSIGN:         "=",
		TOK_INCREMENT:      "++",
		TOK_DECREMENT:      "--",
		TOK_LPAREN:         "(",
		TOK_RPAREN:         ")",
		TOK_DOT:            ".",
		TOK_ARROW:          "->",
	}
	if name, ok := names[typ]; ok {
		return name
	}
	return fmt.Sprintf("TOK_%d", typ)
}
