package dsl

import (
	"fmt"
	"strings"
)

// ── Reference checker ────────────────────────────────────────────────────────

// CheckResult holds the result of a reference check.
type CheckResult struct {
	Errors []string // list of "file:line: message" error strings
}

// Checker walks an AST and collects unknown-identifier errors.
type Checker struct {
	knownChannels    map[string]bool
	machineValidator func(machine, field string) bool
	machineStates    func(machine string) ([]string, bool)
	errors           []string
}

// NewChecker creates a reference checker.
// machineValidator is called to validate machine.X.state references;
// return true if valid, false otherwise.
func NewChecker(knownChannels []string, machineValidator func(machine, field string) bool) *Checker {
	known := make(map[string]bool)
	for _, ch := range knownChannels {
		known[ch] = true
	}

	return &Checker{
		knownChannels:    known,
		machineValidator: machineValidator,
		errors:           []string{},
	}
}

// WithMachineStates enables compile-time validation of the string literal on
// the other side of a `machine.<M>.state == "…"` / `!= "…"` comparison
// against M's real state names. statesFn returns the machine's state names in
// declaration order, with ok=false for an unknown machine (which is already
// reported separately by machineValidator, so WithMachineStates stays quiet
// about it). Returns the receiver so callers can chain it onto NewChecker.
func (c *Checker) WithMachineStates(statesFn func(machine string) ([]string, bool)) *Checker {
	c.machineStates = statesFn
	return c
}

// Check walks the given AST and returns a CheckResult.
func (c *Checker) Check(decl Decl) *CheckResult {
	c.errors = []string{}
	c.checkDecl(decl)
	return &CheckResult{Errors: c.errors}
}

func (c *Checker) checkDecl(decl Decl) {
	switch d := decl.(type) {
	case *MachineDef:
		for _, state := range d.States {
			c.checkState(state)
		}
	case *ChannelDef:
		if d.Compute != nil {
			c.checkExpr(d.Compute)
		}
	case *TemplateDef:
		// Templates don't reference channels directly
	case *AlertDef:
		c.checkExpr(d.Condition)
	}
}

func (c *Checker) checkState(state *StateDef) {
	for _, stmt := range state.Controller {
		c.checkStmt(stmt)
	}
	for _, stmt := range state.Sequence {
		c.checkStmt(stmt)
	}
	for _, stmt := range state.AbortSequence {
		c.checkStmt(stmt)
	}
	for _, rule := range state.AbortRules {
		c.checkExpr(rule.Value)
		c.checkExpr(rule.FromMs)
		c.checkExpr(rule.ToMs)
	}
}

func (c *Checker) checkStmt(stmt Stmt) {
	switch s := stmt.(type) {
	case *AssignStmt:
		// Check that target is a known channel
		if !c.isKnownChannel(s.Target) {
			c.addError(s.LineNo, "unknown channel %q", s.Target)
		}
		c.checkExpr(s.Value)

	case *IncrementStmt:
		if !c.isKnownChannel(s.Target) {
			c.addError(s.LineNo, "unknown channel %q", s.Target)
		}

	case *DecrementStmt:
		if !c.isKnownChannel(s.Target) {
			c.addError(s.LineNo, "unknown channel %q", s.Target)
		}

	case *IfStmt:
		c.checkExpr(s.Condition)
		for _, stmt := range s.Body {
			c.checkStmt(stmt)
		}
		for _, elif := range s.Elif {
			if elif.Condition != nil {
				c.checkExpr(elif.Condition)
			}
			for _, stmt := range elif.Body {
				c.checkStmt(stmt)
			}
		}

	case *TransitionStmt:
		// State names are checked later (not in this pass)

	case *CommandStmt:
		// Target machine/state are checked later, with whole-program knowledge
		// (statemachine/compile.go's checkCommands): this pass only knows one
		// machine's own channel space.

	case *SleepStmt:
		c.checkExpr(s.Duration)

	case *WaitUntilStmt:
		c.checkExpr(s.Condition)
		if s.Timeout != nil {
			c.checkExpr(s.Timeout)
		}
	}
}

func (c *Checker) checkExpr(expr Expr) {
	if expr == nil {
		return
	}

	switch e := expr.(type) {
	case *LiteralExpr:
		// No identifiers to check

	case *IdentExpr:
		c.checkIdent(e)

	case *BinaryExpr:
		if e.Op == "==" || e.Op == "!=" {
			c.checkStateComparison(e)
		}
		c.checkExpr(e.Left)
		c.checkExpr(e.Right)

	case *UnaryExpr:
		c.checkExpr(e.Operand)
	}
}

// checkStateComparison validates the string literal on the other side of a
// `machine.<M>.state == "…"` / `!= "…"` comparison against M's real state
// names, in either operand order.  Without this, a typo'd state name is a
// guard that silently never fires — the same class of bug the old
// `{{VAR}}`-resolves-to-0 behaviour was.  Comparisons against a non-literal
// expression, or a literal of a non-string type, are left alone: the former
// cannot be checked at compile time, the latter is an existing type-mismatch
// case handled at eval time.
func (c *Checker) checkStateComparison(e *BinaryExpr) {
	if c.machineStates == nil {
		return
	}
	machine, lit, ok := stateComparisonParts(e)
	if !ok {
		return
	}
	want, isStr := lit.Value.(string)
	if !isStr {
		return
	}
	states, ok := c.machineStates(machine)
	if !ok {
		// Unknown machine: already reported by checkIdent via machineValidator.
		return
	}
	for _, s := range states {
		if s == want {
			return
		}
	}
	c.addError(lit.LineNo, "machine %q has no state %q (valid states: %s)",
		machine, want, strings.Join(states, ", "))
}

// stateComparisonParts extracts the machine name and literal operand from a
// machine.<M>.state == <literal> comparison, checking both operand orders.
// ok is false when the expression is not shaped like that (e.g. comparing
// machine.<M>.state to another expression rather than a literal).
func stateComparisonParts(e *BinaryExpr) (machine string, lit *LiteralExpr, ok bool) {
	if m, isState := machineStateName(e.Left); isState {
		if l, isLit := e.Right.(*LiteralExpr); isLit {
			return m, l, true
		}
	}
	if m, isState := machineStateName(e.Right); isState {
		if l, isLit := e.Left.(*LiteralExpr); isLit {
			return m, l, true
		}
	}
	return "", nil, false
}

// machineStateName reports whether expr is a `machine.<name>.state` ident and,
// if so, the machine name.
func machineStateName(expr Expr) (machine string, ok bool) {
	ident, isIdent := expr.(*IdentExpr)
	if !isIdent || !strings.HasPrefix(ident.Name, "machine.") {
		return "", false
	}
	parts := strings.Split(ident.Name, ".")
	if len(parts) == 3 && parts[2] == "state" {
		return parts[1], true
	}
	return "", false
}

func (c *Checker) checkIdent(ident *IdentExpr) {
	name := ident.Name

	// Check for machine.X.state pattern
	if strings.HasPrefix(name, "machine.") {
		parts := strings.Split(name, ".")
		if len(parts) == 3 && parts[2] == "state" {
			machine := parts[1]
			if c.machineValidator != nil && !c.machineValidator(machine, "state") {
				c.addError(ident.LineNo, "unknown machine %q", machine)
			}
			return
		}
		// Other machine.X patterns are not validated here
		return
	}

	// Regular channel name
	if !c.isKnownChannel(name) {
		c.addError(ident.LineNo, "unknown channel %q", name)
	}
}

func (c *Checker) isKnownChannel(name string) bool {
	return c.knownChannels[name]
}

func (c *Checker) addError(line int, msg string, args ...interface{}) {
	errMsg := fmt.Sprintf("file:%d: %s", line, fmt.Sprintf(msg, args...))
	c.errors = append(c.errors, errMsg)
}
