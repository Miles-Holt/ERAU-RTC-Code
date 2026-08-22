package dsl

import (
	"errors"
	"fmt"
	"math"
)

// ── Value type ────────────────────────────────────────────────────────────

// Value represents a typed runtime value.
type Value struct {
	typ   string      // "float", "bool", "string"
	fval  float64
	bval  bool
	sval  string
}

// Float returns the float64 value; panics if type is not float.
func (v Value) Float() float64 { return v.fval }

// Bool returns the bool value; panics if type is not bool.
func (v Value) Bool() bool { return v.bval }

// String returns the string value; panics if type is not string.
func (v Value) String() string { return v.sval }

// Type returns the type name ("float", "bool", "string").
func (v Value) Type() string { return v.typ }

// NewFloat creates a float value.
func NewFloat(f float64) Value {
	return Value{typ: "float", fval: f}
}

// NewBool creates a bool value.
func NewBool(b bool) Value {
	return Value{typ: "bool", bval: b}
}

// NewString creates a string value.
func NewString(s string) Value {
	return Value{typ: "string", sval: s}
}

// ── ChannelSpace interface ────────────────────────────────────────────────

// ChannelSpace provides access to the channel/machine state during evaluation.
type ChannelSpace interface {
	Get(name string) (Value, bool)
}

// ── Unresolved references ─────────────────────────────────────────────────
//
// Every identifier in a compiled expression was checked against the full
// channel space at startup, so a lookup that fails at RUNTIME almost always
// means "nothing has published a value for this channel yet" (typically a DAQ
// node that has not connected), not "this channel does not exist".  Reporting
// it as `unknown channel "CPT-01"` sent operators hunting for a config typo
// that was not there.  The evaluator raises this typed error instead, and
// callers that know the configured channel set turn it into the precise
// message via DescribeEvalError.

// UnresolvedError reports an identifier that had no value at evaluation time.
type UnresolvedError struct {
	Name string
	Line int
}

func (e *UnresolvedError) Error() string {
	return fmt.Sprintf("line %d: no value yet for channel %q", e.Line, e.Name)
}

// DescribeEvalError rewrites an unresolved-reference error using the caller's
// knowledge of the configured channel space: a name the config knows about is
// reported as "no value yet" (wait for the node), an unknown one as "unknown
// channel" (fix the config).  Any other error is returned unchanged.
// known may be nil, in which case the error is returned unchanged.
func DescribeEvalError(err error, known func(string) bool) error {
	var ue *UnresolvedError
	if err == nil || known == nil || !errors.As(err, &ue) {
		return err
	}
	if known(ue.Name) {
		return fmt.Errorf("line %d: no value yet for channel %q (nothing has published it since startup)",
			ue.Line, ue.Name)
	}
	return fmt.Errorf("line %d: unknown channel %q", ue.Line, ue.Name)
}

// ── Evaluator ────────────────────────────────────────────────────────────

// Eval evaluates an expression against the given channel space.
// Returns an error if the expression references unknown channels or involves type mismatches.
func Eval(expr Expr, cs ChannelSpace) (Value, error) {
	switch e := expr.(type) {
	case *LiteralExpr:
		return evalLiteral(e)
	case *IdentExpr:
		return evalIdent(e, cs)
	case *BinaryExpr:
		return evalBinary(e, cs)
	case *UnaryExpr:
		return evalUnary(e, cs)
	default:
		return Value{}, fmt.Errorf("unknown expression type")
	}
}

func evalLiteral(e *LiteralExpr) (Value, error) {
	switch v := e.Value.(type) {
	case int64:
		return NewFloat(float64(v)), nil
	case float64:
		return NewFloat(v), nil
	case bool:
		return NewBool(v), nil
	case string:
		return NewString(v), nil
	default:
		return Value{}, fmt.Errorf("line %d: unknown literal type", e.LineNo)
	}
}

// LiteralFloat reduces a literal's underlying value to a float64: an int64 or
// float64 literal converts directly, and bool reduces to 1/0 (so e.g.
// `VENT-CMD = true` behaves the same as `VENT-CMD = 1`). ok is false for a
// string literal, which has no numeric form.
func LiteralFloat(lit *LiteralExpr) (f float64, ok bool) {
	switch v := lit.Value.(type) {
	case int64:
		return float64(v), true
	case float64:
		return v, true
	case bool:
		if v {
			return 1, true
		}
		return 0, true
	default:
		return 0, false
	}
}

func evalIdent(e *IdentExpr, cs ChannelSpace) (Value, error) {
	val, ok := cs.Get(e.Name)
	if !ok {
		return Value{}, &UnresolvedError{Name: e.Name, Line: e.LineNo}
	}
	return val, nil
}

func evalBinary(e *BinaryExpr, cs ChannelSpace) (Value, error) {
	switch e.Op {
	case "and":
		left, err := Eval(e.Left, cs)
		if err != nil {
			return Value{}, err
		}
		if left.typ != "bool" {
			return Value{}, fmt.Errorf("line %d: and requires bool operands", e.LineNo)
		}
		if !left.bval {
			return NewBool(false), nil // short-circuit
		}
		right, err := Eval(e.Right, cs)
		if err != nil {
			return Value{}, err
		}
		if right.typ != "bool" {
			return Value{}, fmt.Errorf("line %d: and requires bool operands", e.LineNo)
		}
		return NewBool(right.bval), nil

	case "or":
		left, err := Eval(e.Left, cs)
		if err != nil {
			return Value{}, err
		}
		if left.typ != "bool" {
			return Value{}, fmt.Errorf("line %d: or requires bool operands", e.LineNo)
		}
		if left.bval {
			return NewBool(true), nil // short-circuit
		}
		right, err := Eval(e.Right, cs)
		if err != nil {
			return Value{}, err
		}
		if right.typ != "bool" {
			return Value{}, fmt.Errorf("line %d: or requires bool operands", e.LineNo)
		}
		return NewBool(right.bval), nil

	default:
		left, err := Eval(e.Left, cs)
		if err != nil {
			return Value{}, err
		}
		right, err := Eval(e.Right, cs)
		if err != nil {
			return Value{}, err
		}
		return evalBinaryOp(e.Op, left, right, e.LineNo)
	}
}

func evalBinaryOp(op string, left, right Value, line int) (Value, error) {
	switch op {
	case "+":
		if left.typ != "float" || right.typ != "float" {
			return Value{}, fmt.Errorf("line %d: + requires numeric operands", line)
		}
		return NewFloat(left.fval + right.fval), nil

	case "-":
		if left.typ != "float" || right.typ != "float" {
			return Value{}, fmt.Errorf("line %d: - requires numeric operands", line)
		}
		return NewFloat(left.fval - right.fval), nil

	case "*":
		if left.typ != "float" || right.typ != "float" {
			return Value{}, fmt.Errorf("line %d: * requires numeric operands", line)
		}
		return NewFloat(left.fval * right.fval), nil

	case "/":
		if left.typ != "float" || right.typ != "float" {
			return Value{}, fmt.Errorf("line %d: / requires numeric operands", line)
		}
		if right.fval == 0 {
			return Value{}, fmt.Errorf("line %d: division by zero", line)
		}
		return NewFloat(left.fval / right.fval), nil

	case "%":
		if left.typ != "float" || right.typ != "float" {
			return Value{}, fmt.Errorf("line %d: %% requires numeric operands", line)
		}
		if right.fval == 0 {
			return Value{}, fmt.Errorf("line %d: modulo by zero", line)
		}
		return NewFloat(math.Mod(left.fval, right.fval)), nil

	case "==":
		if left.typ != right.typ {
			return NewBool(false), nil
		}
		switch left.typ {
		case "float":
			return NewBool(left.fval == right.fval), nil
		case "bool":
			return NewBool(left.bval == right.bval), nil
		case "string":
			return NewBool(left.sval == right.sval), nil
		}
		return Value{}, fmt.Errorf("line %d: unknown type in ==", line)

	case "!=":
		if left.typ != right.typ {
			return NewBool(true), nil
		}
		switch left.typ {
		case "float":
			return NewBool(left.fval != right.fval), nil
		case "bool":
			return NewBool(left.bval != right.bval), nil
		case "string":
			return NewBool(left.sval != right.sval), nil
		}
		return Value{}, fmt.Errorf("line %d: unknown type in !=", line)

	case "<":
		if left.typ != "float" || right.typ != "float" {
			return Value{}, fmt.Errorf("line %d: < requires numeric operands", line)
		}
		return NewBool(left.fval < right.fval), nil

	case "<=":
		if left.typ != "float" || right.typ != "float" {
			return Value{}, fmt.Errorf("line %d: <= requires numeric operands", line)
		}
		return NewBool(left.fval <= right.fval), nil

	case ">":
		if left.typ != "float" || right.typ != "float" {
			return Value{}, fmt.Errorf("line %d: > requires numeric operands", line)
		}
		return NewBool(left.fval > right.fval), nil

	case ">=":
		if left.typ != "float" || right.typ != "float" {
			return Value{}, fmt.Errorf("line %d: >= requires numeric operands", line)
		}
		return NewBool(left.fval >= right.fval), nil

	default:
		return Value{}, fmt.Errorf("line %d: unknown operator %q", line, op)
	}
}

func evalUnary(e *UnaryExpr, cs ChannelSpace) (Value, error) {
	switch e.Op {
	case "not":
		operand, err := Eval(e.Operand, cs)
		if err != nil {
			return Value{}, err
		}
		if operand.typ != "bool" {
			return Value{}, fmt.Errorf("line %d: not requires bool operand", e.LineNo)
		}
		return NewBool(!operand.bval), nil

	case "-":
		operand, err := Eval(e.Operand, cs)
		if err != nil {
			return Value{}, err
		}
		if operand.typ != "float" {
			return Value{}, fmt.Errorf("line %d: unary - requires numeric operand", e.LineNo)
		}
		return NewFloat(-operand.fval), nil

	default:
		return Value{}, fmt.Errorf("line %d: unknown unary operator %q", e.LineNo, e.Op)
	}
}
