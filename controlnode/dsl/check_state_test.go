package dsl

import (
	"strings"
	"testing"
)

// fuelSeqStates is the fixed state list the tests below validate literals
// against, standing in for a compiled machine's real states.
func fuelSeqStates(machine string) ([]string, bool) {
	if machine == "fuelSeq" {
		return []string{"safe", "armed", "abort"}, true
	}
	return nil, false
}

func fuelSeqValidator(machine, field string) bool {
	return machine == "fuelSeq" && field == "state"
}

func TestCheck_StateLiteral_Typo(t *testing.T) {
	decl := &AlertDef{
		Name: "TEST",
		Condition: &BinaryExpr{
			Left:   &IdentExpr{Name: "machine.fuelSeq.state", LineNo: 3},
			Op:     "==",
			Right:  &LiteralExpr{Value: "engineRunning", LineNo: 3}, // not a real state
			LineNo: 3,
		},
		LineNo: 1,
	}

	checker := NewChecker([]string{}, fuelSeqValidator).WithMachineStates(fuelSeqStates)
	result := checker.Check(decl)

	if len(result.Errors) != 1 {
		t.Fatalf("expected exactly 1 error, got %d: %v", len(result.Errors), result.Errors)
	}
	msg := result.Errors[0]
	for _, want := range []string{"fuelSeq", "engineRunning", "safe", "armed", "abort"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
}

func TestCheck_StateLiteral_Correct(t *testing.T) {
	decl := &AlertDef{
		Name: "TEST",
		Condition: &BinaryExpr{
			Left:   &IdentExpr{Name: "machine.fuelSeq.state", LineNo: 3},
			Op:     "==",
			Right:  &LiteralExpr{Value: "abort", LineNo: 3},
			LineNo: 1,
		},
		LineNo: 1,
	}

	checker := NewChecker([]string{}, fuelSeqValidator).WithMachineStates(fuelSeqStates)
	result := checker.Check(decl)
	if len(result.Errors) != 0 {
		t.Errorf("expected no errors, got %d: %v", len(result.Errors), result.Errors)
	}
}

func TestCheck_StateLiteral_NotEqualTypo(t *testing.T) {
	decl := &AlertDef{
		Name: "TEST",
		Condition: &BinaryExpr{
			Left:   &IdentExpr{Name: "machine.fuelSeq.state", LineNo: 5},
			Op:     "!=",
			Right:  &LiteralExpr{Value: "abrot", LineNo: 5}, // typo
			LineNo: 1,
		},
		LineNo: 1,
	}

	checker := NewChecker([]string{}, fuelSeqValidator).WithMachineStates(fuelSeqStates)
	result := checker.Check(decl)
	if len(result.Errors) != 1 {
		t.Fatalf("expected exactly 1 error, got %d: %v", len(result.Errors), result.Errors)
	}
}

func TestCheck_StateLiteral_NotEqualCorrect(t *testing.T) {
	decl := &AlertDef{
		Name: "TEST",
		Condition: &BinaryExpr{
			Left:   &IdentExpr{Name: "machine.fuelSeq.state", LineNo: 5},
			Op:     "!=",
			Right:  &LiteralExpr{Value: "abort", LineNo: 5},
			LineNo: 1,
		},
		LineNo: 1,
	}

	checker := NewChecker([]string{}, fuelSeqValidator).WithMachineStates(fuelSeqStates)
	result := checker.Check(decl)
	if len(result.Errors) != 0 {
		t.Errorf("expected no errors, got %d: %v", len(result.Errors), result.Errors)
	}
}

// TestCheck_StateLiteral_ReverseOperandOrder covers "abort" == machine.fuelSeq.state.
func TestCheck_StateLiteral_ReverseOperandOrder(t *testing.T) {
	decl := &AlertDef{
		Name: "TEST",
		Condition: &BinaryExpr{
			Left:   &LiteralExpr{Value: "bogus", LineNo: 5},
			Op:     "==",
			Right:  &IdentExpr{Name: "machine.fuelSeq.state", LineNo: 5},
			LineNo: 1,
		},
		LineNo: 1,
	}

	checker := NewChecker([]string{}, fuelSeqValidator).WithMachineStates(fuelSeqStates)
	result := checker.Check(decl)
	if len(result.Errors) != 1 {
		t.Fatalf("expected exactly 1 error, got %d: %v", len(result.Errors), result.Errors)
	}
}

// TestCheck_StateLiteral_NonLiteralComparisonLeftAlone covers comparing
// machine state against another expression (not a literal): must not error,
// per spec ("Non-literal comparisons ... are left alone").
func TestCheck_StateLiteral_NonLiteralComparisonLeftAlone(t *testing.T) {
	decl := &AlertDef{
		Name: "TEST",
		Condition: &BinaryExpr{
			Left:   &IdentExpr{Name: "machine.fuelSeq.state", LineNo: 5},
			Op:     "==",
			Right:  &IdentExpr{Name: "machine.other.state", LineNo: 5},
			LineNo: 1,
		},
		LineNo: 1,
	}

	// other machine unknown here on purpose to prove the checker never even
	// looks at machineStates for a non-literal comparison.
	checker := NewChecker([]string{}, func(machine, field string) bool {
		return field == "state" && (machine == "fuelSeq" || machine == "other")
	}).WithMachineStates(fuelSeqStates)
	result := checker.Check(decl)
	if len(result.Errors) != 0 {
		t.Errorf("expected no errors for non-literal comparison, got %d: %v", len(result.Errors), result.Errors)
	}
}

// TestCheck_StateLiteral_WithoutMachineStates confirms the feature is opt-in:
// a Checker built without WithMachineStates never flags a bad literal (the
// pre-existing, unchecked behaviour), so old call sites are unaffected.
func TestCheck_StateLiteral_WithoutMachineStates(t *testing.T) {
	decl := &AlertDef{
		Name: "TEST",
		Condition: &BinaryExpr{
			Left:   &IdentExpr{Name: "machine.fuelSeq.state", LineNo: 3},
			Op:     "==",
			Right:  &LiteralExpr{Value: "totallyBogus", LineNo: 3},
			LineNo: 1,
		},
		LineNo: 1,
	}

	checker := NewChecker([]string{}, fuelSeqValidator) // no WithMachineStates
	result := checker.Check(decl)
	if len(result.Errors) != 0 {
		t.Errorf("expected no errors without WithMachineStates, got %d: %v", len(result.Errors), result.Errors)
	}
}
