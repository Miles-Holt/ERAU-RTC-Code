package dsl

import (
	"fmt"
	"strconv"
	"strings"
)

// ── Source reconstruction ─────────────────────────────────────────────────────
//
// The compiled AST is the only description of the running configuration that is
// guaranteed to be in sync with what the engine executes (a .sm file on disk may
// have been edited since startup).  These helpers render it back to DSL-shaped
// text so the /docs pages can show the actual compiled logic.

// ExprString renders an expression back to DSL source text.  Parentheses are
// added wherever the sub-expression binds looser than its parent, so the output
// re-parses to the same tree.
func ExprString(e Expr) string {
	return exprString(e, 0)
}

// precedence mirrors the parser's binding strength; higher binds tighter.
func precedence(op string) int {
	switch op {
	case "or":
		return 1
	case "and":
		return 2
	case "==", "!=", "<", "<=", ">", ">=":
		return 3
	case "+", "-":
		return 4
	case "*", "/", "%":
		return 5
	}
	return 6
}

func exprString(e Expr, parentPrec int) string {
	switch v := e.(type) {
	case nil:
		return ""
	case *LiteralExpr:
		return LiteralString(v)
	case *IdentExpr:
		return v.Name
	case *UnaryExpr:
		inner := exprString(v.Operand, 6)
		if v.Op == "not" {
			s := "not " + inner
			if parentPrec > 0 {
				return "(" + s + ")"
			}
			return s
		}
		return v.Op + inner
	case *BinaryExpr:
		p := precedence(v.Op)
		// Left-associative: the right operand needs parens at equal precedence.
		s := exprString(v.Left, p) + " " + v.Op + " " + exprString(v.Right, p+1)
		if p < parentPrec {
			return "(" + s + ")"
		}
		return s
	default:
		return fmt.Sprintf("%v", e)
	}
}

// LiteralString renders a literal the way it would be written in source.
func LiteralString(l *LiteralExpr) string {
	if l == nil {
		return ""
	}
	switch v := l.Value.(type) {
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		if v {
			return "true"
		}
		return "false"
	case string:
		return strconv.Quote(v)
	default:
		return fmt.Sprintf("%v", l.Value)
	}
}

// StmtLines renders a statement block back to DSL source lines, one entry per
// line, indented with `indent` levels of four spaces for nested if/elif/else
// bodies.  Used by the /docs machine pages to list what a sequence actually does.
func StmtLines(stmts []Stmt, indent int) []string {
	var out []string
	pad := strings.Repeat("    ", indent)
	for _, s := range stmts {
		switch v := s.(type) {
		case *AssignStmt:
			out = append(out, pad+v.Target+" = "+ExprString(v.Value))
		case *IncrementStmt:
			out = append(out, pad+v.Target+"++")
		case *DecrementStmt:
			out = append(out, pad+v.Target+"--")
		case *CompoundAssignStmt:
			out = append(out, pad+v.Target+" "+v.Op+" "+ExprString(v.Value))
		case *TransitionStmt:
			out = append(out, pad+"transition "+v.Target)
		case *CommandStmt:
			out = append(out, pad+"command "+v.Machine+" -> "+v.Target)
		case *SleepStmt:
			out = append(out, pad+"sleep "+ExprString(v.Duration))
		case *WaitUntilStmt:
			line := pad + "wait_until " + ExprString(v.Condition)
			if v.Timeout != nil {
				line += " timeout " + ExprString(v.Timeout)
				if v.TimeoutState != "" {
					line += " -> " + v.TimeoutState
				}
			}
			out = append(out, line)
		case *IfStmt:
			out = append(out, pad+"if "+ExprString(v.Condition))
			out = append(out, StmtLines(v.Body, indent+1)...)
			for _, e := range v.Elif {
				if e.Condition == nil {
					out = append(out, pad+"else")
				} else {
					out = append(out, pad+"elif "+ExprString(e.Condition))
				}
				out = append(out, StmtLines(e.Body, indent+1)...)
			}
		default:
			out = append(out, pad+fmt.Sprintf("%T", s))
		}
	}
	return out
}

// OperatorString renders the `operator` state flag back to source form,
// including its optional gate list: `operator` when from is empty,
// `operator from a, b` otherwise.  The output re-parses to the same flag.
func OperatorString(from []string) string {
	if len(from) == 0 {
		return "operator"
	}
	return "operator from " + strings.Join(from, ", ")
}

// AbortRuleString renders an abort_rule back to source form.
func AbortRuleString(r *AbortRule) string {
	if r == nil {
		return ""
	}
	return fmt.Sprintf("abort_rule %s %s %s from %s to %s",
		r.Channel, r.Op, ExprString(r.Value), ExprString(r.FromMs), ExprString(r.ToMs))
}

// FormatMachine renders a whole machine definition back to DSL source text,
// state by state, in declaration order.  It is assembled from the same
// per-piece helpers this file already exposes for /docs (OperatorString,
// StmtLines, AbortRuleString) rather than adding a second, parallel
// reconstruction path — so Parse -> FormatMachine -> Parse exercises those
// helpers against the DSL's own parser on a WHOLE file, which is what catches
// formatter/parser drift that a single expression or statement block round
// trip would miss (e.g. state-level ordering of operator/daq_local/
// abort_rule/controller/sequence/abort_sequence).
func FormatMachine(m *MachineDef) string {
	var b strings.Builder
	fmt.Fprintf(&b, "machine %s\n", m.Name)
	for _, st := range m.States {
		fmt.Fprintf(&b, "state %s\n", st.Name)
		if st.Operator {
			fmt.Fprintf(&b, "    %s\n", OperatorString(st.OperatorFrom))
		}
		if st.DaqLocal != "" {
			fmt.Fprintf(&b, "    daq_local %s\n", st.DaqLocal)
		}
		for _, r := range st.AbortRules {
			fmt.Fprintf(&b, "    %s\n", AbortRuleString(r))
		}
		if len(st.Controller) > 0 {
			b.WriteString("    controller\n")
			for _, line := range StmtLines(st.Controller, 2) {
				b.WriteString(line + "\n")
			}
		}
		if len(st.Sequence) > 0 {
			b.WriteString("    sequence\n")
			for _, line := range StmtLines(st.Sequence, 2) {
				b.WriteString(line + "\n")
			}
		}
		if st.HasAbortSequence {
			b.WriteString("    abort_sequence\n")
			for _, line := range StmtLines(st.AbortSequence, 2) {
				b.WriteString(line + "\n")
			}
		}
	}
	return b.String()
}
