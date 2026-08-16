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
		c.checkExpr(e.Left)
		c.checkExpr(e.Right)

	case *UnaryExpr:
		c.checkExpr(e.Operand)
	}
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
