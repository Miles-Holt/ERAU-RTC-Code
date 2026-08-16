package dsl

import (
	"testing"
)

// MockChannelSpace implements ChannelSpace for testing.
type MockChannelSpace struct {
	values map[string]Value
}

func NewMockChannelSpace() *MockChannelSpace {
	return &MockChannelSpace{values: make(map[string]Value)}
}

func (m *MockChannelSpace) Set(name string, val Value) {
	m.values[name] = val
}

func (m *MockChannelSpace) Get(name string) (Value, bool) {
	v, ok := m.values[name]
	return v, ok
}

func TestEval_Literals(t *testing.T) {
	tests := []struct {
		name   string
		expr   Expr
		expect Value
	}{
		{
			name:   "int literal",
			expr:   &LiteralExpr{Value: int64(42), LineNo: 1},
			expect: NewFloat(42),
		},
		{
			name:   "float literal",
			expr:   &LiteralExpr{Value: 3.14, LineNo: 1},
			expect: NewFloat(3.14),
		},
		{
			name:   "bool true",
			expr:   &LiteralExpr{Value: true, LineNo: 1},
			expect: NewBool(true),
		},
		{
			name:   "bool false",
			expr:   &LiteralExpr{Value: false, LineNo: 1},
			expect: NewBool(false),
		},
		{
			name:   "string",
			expr:   &LiteralExpr{Value: "hello", LineNo: 1},
			expect: NewString("hello"),
		},
	}

	cs := NewMockChannelSpace()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			val, err := Eval(tt.expr, cs)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if val.typ != tt.expect.typ {
				t.Errorf("type: got %q, expected %q", val.typ, tt.expect.typ)
			}
			switch val.typ {
			case "float":
				if val.fval != tt.expect.fval {
					t.Errorf("value: got %v, expected %v", val.fval, tt.expect.fval)
				}
			case "bool":
				if val.bval != tt.expect.bval {
					t.Errorf("value: got %v, expected %v", val.bval, tt.expect.bval)
				}
			case "string":
				if val.sval != tt.expect.sval {
					t.Errorf("value: got %q, expected %q", val.sval, tt.expect.sval)
				}
			}
		})
	}
}

func TestEval_Arithmetic(t *testing.T) {
	cs := NewMockChannelSpace()
	cs.Set("a", NewFloat(10))
	cs.Set("b", NewFloat(3))

	tests := []struct {
		name   string
		expr   Expr
		expect float64
	}{
		{
			name: "addition",
			expr: &BinaryExpr{
				Left:   &IdentExpr{Name: "a", LineNo: 1},
				Op:     "+",
				Right:  &IdentExpr{Name: "b", LineNo: 1},
				LineNo: 1,
			},
			expect: 13,
		},
		{
			name: "subtraction",
			expr: &BinaryExpr{
				Left:   &IdentExpr{Name: "a", LineNo: 1},
				Op:     "-",
				Right:  &IdentExpr{Name: "b", LineNo: 1},
				LineNo: 1,
			},
			expect: 7,
		},
		{
			name: "multiplication",
			expr: &BinaryExpr{
				Left:   &IdentExpr{Name: "a", LineNo: 1},
				Op:     "*",
				Right:  &IdentExpr{Name: "b", LineNo: 1},
				LineNo: 1,
			},
			expect: 30,
		},
		{
			name: "division",
			expr: &BinaryExpr{
				Left:   &IdentExpr{Name: "a", LineNo: 1},
				Op:     "/",
				Right:  &IdentExpr{Name: "b", LineNo: 1},
				LineNo: 1,
			},
			expect: 10.0 / 3.0,
		},
		{
			name: "modulo",
			expr: &BinaryExpr{
				Left:   &IdentExpr{Name: "a", LineNo: 1},
				Op:     "%",
				Right:  &IdentExpr{Name: "b", LineNo: 1},
				LineNo: 1,
			},
			expect: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			val, err := Eval(tt.expr, cs)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if val.typ != "float" {
				t.Errorf("type: got %q, expected float", val.typ)
			}
			if val.fval != tt.expect {
				t.Errorf("value: got %v, expected %v", val.fval, tt.expect)
			}
		})
	}
}

func TestEval_Comparisons(t *testing.T) {
	cs := NewMockChannelSpace()
	cs.Set("a", NewFloat(10))
	cs.Set("b", NewFloat(5))

	tests := []struct {
		name   string
		expr   Expr
		expect bool
	}{
		{
			name: "less than true",
			expr: &BinaryExpr{
				Left:   &IdentExpr{Name: "b", LineNo: 1},
				Op:     "<",
				Right:  &IdentExpr{Name: "a", LineNo: 1},
				LineNo: 1,
			},
			expect: true,
		},
		{
			name: "less than false",
			expr: &BinaryExpr{
				Left:   &IdentExpr{Name: "a", LineNo: 1},
				Op:     "<",
				Right:  &IdentExpr{Name: "b", LineNo: 1},
				LineNo: 1,
			},
			expect: false,
		},
		{
			name: "equal true",
			expr: &BinaryExpr{
				Left:   &IdentExpr{Name: "a", LineNo: 1},
				Op:     "==",
				Right:  &LiteralExpr{Value: int64(10), LineNo: 1},
				LineNo: 1,
			},
			expect: true,
		},
		{
			name: "not equal",
			expr: &BinaryExpr{
				Left:   &IdentExpr{Name: "a", LineNo: 1},
				Op:     "!=",
				Right:  &IdentExpr{Name: "b", LineNo: 1},
				LineNo: 1,
			},
			expect: true,
		},
		{
			name: "greater than or equal",
			expr: &BinaryExpr{
				Left:   &IdentExpr{Name: "a", LineNo: 1},
				Op:     ">=",
				Right:  &IdentExpr{Name: "a", LineNo: 1},
				LineNo: 1,
			},
			expect: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			val, err := Eval(tt.expr, cs)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if val.typ != "bool" {
				t.Errorf("type: got %q, expected bool", val.typ)
			}
			if val.bval != tt.expect {
				t.Errorf("value: got %v, expected %v", val.bval, tt.expect)
			}
		})
	}
}

func TestEval_Boolean(t *testing.T) {
	cs := NewMockChannelSpace()
	cs.Set("true_val", NewBool(true))
	cs.Set("false_val", NewBool(false))

	tests := []struct {
		name   string
		expr   Expr
		expect bool
	}{
		{
			name: "and true true",
			expr: &BinaryExpr{
				Left:   &IdentExpr{Name: "true_val", LineNo: 1},
				Op:     "and",
				Right:  &IdentExpr{Name: "true_val", LineNo: 1},
				LineNo: 1,
			},
			expect: true,
		},
		{
			name: "and true false",
			expr: &BinaryExpr{
				Left:   &IdentExpr{Name: "true_val", LineNo: 1},
				Op:     "and",
				Right:  &IdentExpr{Name: "false_val", LineNo: 1},
				LineNo: 1,
			},
			expect: false,
		},
		{
			name: "or false false",
			expr: &BinaryExpr{
				Left:   &IdentExpr{Name: "false_val", LineNo: 1},
				Op:     "or",
				Right:  &IdentExpr{Name: "false_val", LineNo: 1},
				LineNo: 1,
			},
			expect: false,
		},
		{
			name: "or true false",
			expr: &BinaryExpr{
				Left:   &IdentExpr{Name: "true_val", LineNo: 1},
				Op:     "or",
				Right:  &IdentExpr{Name: "false_val", LineNo: 1},
				LineNo: 1,
			},
			expect: true,
		},
		{
			name: "not true",
			expr: &UnaryExpr{
				Op:       "not",
				Operand:  &IdentExpr{Name: "true_val", LineNo: 1},
				LineNo:   1,
			},
			expect: false,
		},
		{
			name: "not false",
			expr: &UnaryExpr{
				Op:       "not",
				Operand:  &IdentExpr{Name: "false_val", LineNo: 1},
				LineNo:   1,
			},
			expect: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			val, err := Eval(tt.expr, cs)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if val.typ != "bool" {
				t.Errorf("type: got %q, expected bool", val.typ)
			}
			if val.bval != tt.expect {
				t.Errorf("value: got %v, expected %v", val.bval, tt.expect)
			}
		})
	}
}

func TestEval_Unary(t *testing.T) {
	cs := NewMockChannelSpace()
	cs.Set("x", NewFloat(5))

	tests := []struct {
		name   string
		expr   Expr
		expect float64
	}{
		{
			name: "unary minus",
			expr: &UnaryExpr{
				Op:       "-",
				Operand:  &IdentExpr{Name: "x", LineNo: 1},
				LineNo:   1,
			},
			expect: -5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			val, err := Eval(tt.expr, cs)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if val.typ != "float" {
				t.Errorf("type: got %q, expected float", val.typ)
			}
			if val.fval != tt.expect {
				t.Errorf("value: got %v, expected %v", val.fval, tt.expect)
			}
		})
	}
}

func TestEval_ShortCircuit(t *testing.T) {
	// This test verifies that "and" and "or" short-circuit correctly
	// (they don't evaluate the right side if not needed)
	cs := NewMockChannelSpace()
	cs.Set("a", NewBool(false))
	cs.Set("b", NewBool(true))

	// false and b should not need to evaluate b (short-circuit)
	expr := &BinaryExpr{
		Left:   &IdentExpr{Name: "a", LineNo: 1},
		Op:     "and",
		Right:  &IdentExpr{Name: "nonexistent", LineNo: 1},
		LineNo: 1,
	}

	val, err := Eval(expr, cs)
	if err != nil {
		t.Fatalf("short-circuit failed: %v", err)
	}
	if val.bval != false {
		t.Errorf("expected false, got %v", val.bval)
	}

	// true or b should not need to evaluate b
	expr2 := &BinaryExpr{
		Left:   &IdentExpr{Name: "b", LineNo: 1},
		Op:     "or",
		Right:  &IdentExpr{Name: "nonexistent", LineNo: 1},
		LineNo: 1,
	}

	val2, err := Eval(expr2, cs)
	if err != nil {
		t.Fatalf("short-circuit failed: %v", err)
	}
	if val2.bval != true {
		t.Errorf("expected true, got %v", val2.bval)
	}
}

func TestEval_UnknownChannel(t *testing.T) {
	cs := NewMockChannelSpace()

	expr := &IdentExpr{Name: "UNKNOWN", LineNo: 1}
	_, err := Eval(expr, cs)
	if err == nil {
		t.Errorf("expected error for unknown channel")
	}
}

func TestEval_TypeMismatch(t *testing.T) {
	cs := NewMockChannelSpace()
	cs.Set("num", NewFloat(5))
	cs.Set("str", NewString("hello"))

	tests := []struct {
		name string
		expr Expr
	}{
		{
			name: "add float to string",
			expr: &BinaryExpr{
				Left:   &IdentExpr{Name: "num", LineNo: 1},
				Op:     "+",
				Right:  &IdentExpr{Name: "str", LineNo: 1},
				LineNo: 1,
			},
		},
		{
			name: "compare float to string",
			expr: &BinaryExpr{
				Left:   &IdentExpr{Name: "num", LineNo: 1},
				Op:     "<",
				Right:  &IdentExpr{Name: "str", LineNo: 1},
				LineNo: 1,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Eval(tt.expr, cs)
			if err == nil {
				t.Errorf("expected error for type mismatch")
			}
		})
	}
}

func TestEval_StringEquality(t *testing.T) {
	cs := NewMockChannelSpace()
	cs.Set("state", NewString("ready"))

	tests := []struct {
		name   string
		expr   Expr
		expect bool
	}{
		{
			name: "string equal",
			expr: &BinaryExpr{
				Left:   &IdentExpr{Name: "state", LineNo: 1},
				Op:     "==",
				Right:  &LiteralExpr{Value: "ready", LineNo: 1},
				LineNo: 1,
			},
			expect: true,
		},
		{
			name: "string not equal",
			expr: &BinaryExpr{
				Left:   &IdentExpr{Name: "state", LineNo: 1},
				Op:     "!=",
				Right:  &LiteralExpr{Value: "abort", LineNo: 1},
				LineNo: 1,
			},
			expect: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			val, err := Eval(tt.expr, cs)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if val.bval != tt.expect {
				t.Errorf("got %v, expected %v", val.bval, tt.expect)
			}
		})
	}
}
