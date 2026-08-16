package dsl

import (
	"strings"
	"testing"
)

func TestCheck_KnownChannels(t *testing.T) {
	decl := &ChannelDef{
		Name: "TEST",
		Compute: &BinaryExpr{
			Left:   &IdentExpr{Name: "CH-A", LineNo: 1},
			Op:     "+",
			Right:  &IdentExpr{Name: "CH-B", LineNo: 1},
			LineNo: 1,
		},
		LineNo: 1,
	}

	checker := NewChecker([]string{"CH-A", "CH-B"}, nil)
	result := checker.Check(decl)

	if len(result.Errors) != 0 {
		t.Errorf("expected no errors, got %d: %v", len(result.Errors), result.Errors)
	}
}

func TestCheck_UnknownChannel(t *testing.T) {
	decl := &ChannelDef{
		Name: "TEST",
		Compute: &BinaryExpr{
			Left:   &IdentExpr{Name: "UNKNOWN", LineNo: 2},
			Op:     "+",
			Right:  &IdentExpr{Name: "CH-B", LineNo: 2},
			LineNo: 2,
		},
		LineNo: 1,
	}

	checker := NewChecker([]string{"CH-B"}, nil)
	result := checker.Check(decl)

	if len(result.Errors) == 0 {
		t.Errorf("expected errors, got none")
		return
	}

	if !strings.Contains(result.Errors[0], "UNKNOWN") {
		t.Errorf("error should mention unknown channel, got: %s", result.Errors[0])
	}

	if !strings.Contains(result.Errors[0], ":2:") {
		t.Errorf("error should include line number, got: %s", result.Errors[0])
	}
}

func TestCheck_MultipleUnknown(t *testing.T) {
	decl := &ChannelDef{
		Name: "TEST",
		Compute: &BinaryExpr{
			Left: &BinaryExpr{
				Left:   &IdentExpr{Name: "UNKNOWN1", LineNo: 2},
				Op:     "+",
				Right:  &IdentExpr{Name: "UNKNOWN2", LineNo: 2},
				LineNo: 2,
			},
			Op: "*",
			Right: &IdentExpr{
				Name:   "UNKNOWN3",
				LineNo: 2,
			},
			LineNo: 2,
		},
		LineNo: 1,
	}

	checker := NewChecker([]string{}, nil)
	result := checker.Check(decl)

	if len(result.Errors) == 0 {
		t.Errorf("expected errors, got none")
		return
	}

	if len(result.Errors) < 3 {
		t.Errorf("expected at least 3 errors, got %d", len(result.Errors))
	}
}

func TestCheck_MachineState(t *testing.T) {
	decl := &AlertDef{
		Name: "TEST",
		Condition: &BinaryExpr{
			Left:   &IdentExpr{Name: "machine.fuelSeq.state", LineNo: 1},
			Op:     "==",
			Right:  &LiteralExpr{Value: "abort", LineNo: 1},
			LineNo: 1,
		},
		LineNo: 1,
	}

	validator := func(machine, field string) bool {
		return machine == "fuelSeq" && field == "state"
	}

	checker := NewChecker([]string{}, validator)
	result := checker.Check(decl)

	if len(result.Errors) != 0 {
		t.Errorf("expected no errors, got %d: %v", len(result.Errors), result.Errors)
	}
}

func TestCheck_UnknownMachine(t *testing.T) {
	decl := &AlertDef{
		Name: "TEST",
		Condition: &BinaryExpr{
			Left:   &IdentExpr{Name: "machine.unknown.state", LineNo: 1},
			Op:     "==",
			Right:  &LiteralExpr{Value: "abort", LineNo: 1},
			LineNo: 1,
		},
		LineNo: 1,
	}

	validator := func(machine, field string) bool {
		return machine == "fuelSeq" && field == "state"
	}

	checker := NewChecker([]string{}, validator)
	result := checker.Check(decl)

	if len(result.Errors) == 0 {
		t.Errorf("expected error for unknown machine")
	}
}

func TestCheck_MachineDef(t *testing.T) {
	machine := &MachineDef{
		Name: "fuel",
		States: []*StateDef{
			{
				Name: "safe",
				Controller: []Stmt{
					&AssignStmt{
						Target: "HEARTBEAT",
						Value:  &LiteralExpr{Value: int64(1), LineNo: 2},
						LineNo: 2,
					},
				},
				LineNo: 1,
			},
		},
		LineNo: 1,
	}

	checker := NewChecker([]string{"HEARTBEAT"}, nil)
	result := checker.Check(machine)

	if len(result.Errors) != 0 {
		t.Errorf("expected no errors, got %d: %v", len(result.Errors), result.Errors)
	}
}

func TestCheck_UnknownChannelAssignment(t *testing.T) {
	machine := &MachineDef{
		Name: "fuel",
		States: []*StateDef{
			{
				Name: "safe",
				Sequence: []Stmt{
					&AssignStmt{
						Target: "UNKNOWN",
						Value:  &LiteralExpr{Value: int64(1), LineNo: 2},
						LineNo: 2,
					},
				},
				LineNo: 1,
			},
		},
		LineNo: 1,
	}

	checker := NewChecker([]string{"HEARTBEAT"}, nil)
	result := checker.Check(machine)

	if len(result.Errors) == 0 {
		t.Errorf("expected error for unknown channel in assignment")
	}
}

func TestCheck_IfStatements(t *testing.T) {
	machine := &MachineDef{
		Name: "fuel",
		States: []*StateDef{
			{
				Name: "safe",
				Controller: []Stmt{
					&IfStmt{
						Condition: &BinaryExpr{
							Left:   &IdentExpr{Name: "PRESSURE", LineNo: 2},
							Op:     ">",
							Right:  &LiteralExpr{Value: int64(100), LineNo: 2},
							LineNo: 2,
						},
						Body: []Stmt{
							&TransitionStmt{Target: "abort", LineNo: 3},
						},
						LineNo: 2,
					},
				},
				LineNo: 1,
			},
		},
		LineNo: 1,
	}

	checker := NewChecker([]string{"PRESSURE"}, nil)
	result := checker.Check(machine)

	if len(result.Errors) != 0 {
		t.Errorf("expected no errors, got %d: %v", len(result.Errors), result.Errors)
	}
}

func TestCheck_WaitUntil(t *testing.T) {
	machine := &MachineDef{
		Name: "fuel",
		States: []*StateDef{
			{
				Name: "safe",
				Sequence: []Stmt{
					&WaitUntilStmt{
						Condition: &BinaryExpr{
							Left:   &IdentExpr{Name: "READY", LineNo: 2},
							Op:     "==",
							Right:  &LiteralExpr{Value: true, LineNo: 2},
							LineNo: 2,
						},
						Timeout: &LiteralExpr{Value: int64(5000), LineNo: 2},
						LineNo:  2,
					},
				},
				LineNo: 1,
			},
		},
		LineNo: 1,
	}

	checker := NewChecker([]string{"READY"}, nil)
	result := checker.Check(machine)

	if len(result.Errors) != 0 {
		t.Errorf("expected no errors, got %d: %v", len(result.Errors), result.Errors)
	}
}

func TestCheck_AbortRules(t *testing.T) {
	machine := &MachineDef{
		Name: "fuel",
		States: []*StateDef{
			{
				Name: "safe",
				AbortRules: []*AbortRule{
					{
						Channel: "PRESSURE",
						Op:      ">",
						Value:   &LiteralExpr{Value: int64(500), LineNo: 2},
						FromMs:  &LiteralExpr{Value: int64(0), LineNo: 2},
						ToMs:    &LiteralExpr{Value: int64(10000), LineNo: 2},
						LineNo:  2,
					},
				},
				LineNo: 1,
			},
		},
		LineNo: 1,
	}

	checker := NewChecker([]string{"PRESSURE"}, nil)
	result := checker.Check(machine)

	if len(result.Errors) != 0 {
		t.Errorf("expected no errors, got %d: %v", len(result.Errors), result.Errors)
	}
}

func TestCheck_UnknownInExpression(t *testing.T) {
	machine := &MachineDef{
		Name: "fuel",
		States: []*StateDef{
			{
				Name: "safe",
				AbortRules: []*AbortRule{
					{
						Channel: "PRESSURE",
						Op:      ">",
						Value: &BinaryExpr{
							Left:   &IdentExpr{Name: "UNKNOWN", LineNo: 2},
							Op:     "+",
							Right:  &LiteralExpr{Value: int64(100), LineNo: 2},
							LineNo: 2,
						},
						FromMs: &LiteralExpr{Value: int64(0), LineNo: 2},
						ToMs:   &LiteralExpr{Value: int64(10000), LineNo: 2},
						LineNo: 2,
					},
				},
				LineNo: 1,
			},
		},
		LineNo: 1,
	}

	checker := NewChecker([]string{"PRESSURE"}, nil)
	result := checker.Check(machine)

	if len(result.Errors) == 0 {
		t.Errorf("expected error for unknown channel in expression")
	}
}

func TestCheck_IncrementDecrement(t *testing.T) {
	machine := &MachineDef{
		Name: "fuel",
		States: []*StateDef{
			{
				Name: "safe",
				Controller: []Stmt{
					&IncrementStmt{Target: "COUNTER", LineNo: 2},
					&DecrementStmt{Target: "UNKNOWN", LineNo: 3},
				},
				LineNo: 1,
			},
		},
		LineNo: 1,
	}

	checker := NewChecker([]string{"COUNTER"}, nil)
	result := checker.Check(machine)

	if len(result.Errors) == 0 {
		t.Errorf("expected error for unknown channel in decrement")
	}

	// Should have error for UNKNOWN but not for COUNTER
	if len(result.Errors) > 1 {
		t.Errorf("expected 1 error, got %d", len(result.Errors))
	}
}
