package statemachine

import (
	"fmt"
	"math"
	"strconv"

	"controlnode/dsl"
)

// ── DAQ-local payloads ────────────────────────────────────────────────────────
//
// A state flagged `daq_local <NODE>` is serialized at compile time into the
// existing DAQ `state_update` message so the node can run it locally (<1 ms)
// without a network round trip.  The JSON shape is unchanged from the YAML-era
// payload built by config/control.go + daqnode/statemachine.go.
//
// Assignment values, sleep durations, and abort_rule thresholds/windows may be
// literals or soft-channel identifiers, resolved at send-time via a Reader.
// Compile-time still rejects anything that isn't a literal or plain identifier.

// DaqStep is one timed set-point in an entry sequence.
type DaqStep struct {
	TMs    float64 `json:"t_ms"`
	RefDes string  `json:"refDes"`
	Value  float64 `json:"value"`
}

// DaqAbortRule is one DAQ-side abort trigger, active between t_ms_on and
// t_ms_off relative to sequence start.
type DaqAbortRule struct {
	If     string  `json:"if"`
	TMsOn  float64 `json:"t_ms_on"`
	TMsOff float64 `json:"t_ms_off"`
}

// DaqStateUpdate is the `state_update` payload cached on a daqNode.
//
// `state_update` means "enter this state now": the controlnode sends it when the
// engine enters the daq_local state, and re-sends it on `state_req`.
//
// ExitSequence keeps the JSON field name the LabVIEW node already understands
// (`exit_sequence`): the timed set-steps the node runs locally when one of
// AbortRules trips.
type DaqStateUpdate struct {
	Type          string         `json:"type"`
	State         string         `json:"state"`
	RunID         int64          `json:"runId"`
	EntrySequence []DaqStep      `json:"entry_sequence"`
	ExitSequence  []DaqStep      `json:"exit_sequence"`
	AbortRules    []DaqAbortRule `json:"abort_rules"`

	// Node is the daqNode the payload belongs to; not part of the wire message.
	Node string `json:"-"`
	// Machine is the machine the payload belongs to; not part of the wire message.
	Machine string `json:"-"`
}

// DaqLocalState holds the pre-resolved expressions for a daq_local state.
// The expressions are resolved at send-time via a Reader.
type DaqLocalState struct {
	State            string              // state name
	Steps            []DaqLocalStep      // entry: assignment + sleep steps with expressions
	AbortSteps       []DaqLocalStep      // abort_sequence: assignment + sleep steps
	Rules            []DaqLocalAbortRule // abort rules with expressions
	Node             string              // daqNode name
	CompletionTarget string              // target state on sequence_complete; "" if none
	AbortTarget      string              // target state on abort_triggered; "" if none
}

// DaqLocalStep is one assignment or sleep with an expression value (not yet resolved).
type DaqLocalStep struct {
	TMs    float64  // cumulative time in ms (updated during resolution)
	RefDes string   // for assignments; empty for sleeps
	Expr   dsl.Expr // expression to resolve at send-time
}

// DaqLocalAbortRule is one abort rule with expressions (not yet resolved).
type DaqLocalAbortRule struct {
	Channel    string   // channel to monitor
	Op         string   // comparison operator
	ValueExpr  dsl.Expr // threshold value expression
	FromExpr   dsl.Expr // start time expression
	ToExpr     dsl.Expr // end time expression
}

// compileDaqLocal validates a daq_local state and builds the pre-resolved expressions.
// Compile-time checks that all statements are literal assignments, identifiers, or sleeps.
// A trailing transition is allowed as a completion target; transitions elsewhere are errors.
// Expressions are resolved at send-time via resolveDaqLocalState.
func compileDaqLocal(file string, sd *dsl.StateDef, st *State) (*DaqLocalState, error) {
	if len(st.Controller) > 0 {
		return nil, fmt.Errorf("%s:%d: state %q: daq_local states cannot have a controller block",
			file, st.Controller[0].Line(), st.Name)
	}

	out := &DaqLocalState{
		State: st.Name,
		Steps: []DaqLocalStep{},
		Rules: []DaqLocalAbortRule{},
		Node:  st.DaqLocal,
	}

	// ── entry sequence ────────────────────────────────────────────────────
	entryStmts, completion, err := splitTrailingTransition(st.Sequence)
	if err != nil {
		return nil, fmt.Errorf("%s:%d: state %q: %v", file, err.line, st.Name, err.msg)
	}
	out.CompletionTarget = completion
	st.CompletionTarget = completion

	steps, cerr := compileDaqSteps(file, st.Name, "sequence", entryStmts)
	if cerr != nil {
		return nil, cerr
	}
	out.Steps = steps

	// ── abort sequence ────────────────────────────────────────────────────
	// A daq_local state that arms abort rules must declare what the node runs
	// when one trips, and where the engine goes afterwards.
	if len(st.AbortRules) > 0 && !sd.HasAbortSequence {
		return nil, fmt.Errorf("%s:%d: state %q: daq_local state with abort_rule(s) must declare an abort_sequence",
			file, sd.LineNo, st.Name)
	}
	if sd.HasAbortSequence {
		abortStmts, abortTarget, terr := splitTrailingTransition(st.AbortSequence)
		if terr != nil {
			return nil, fmt.Errorf("%s:%d: state %q: abort_sequence: %v", file, terr.line, st.Name, terr.msg)
		}
		if abortTarget == "" {
			return nil, fmt.Errorf("%s:%d: state %q: abort_sequence must end with \"transition <state>\" (the abort destination)",
				file, sd.AbortSeqLine, st.Name)
		}
		out.AbortTarget = abortTarget
		st.AbortTarget = abortTarget

		abortSteps, aerr := compileDaqSteps(file, st.Name, "abort_sequence", abortStmts)
		if aerr != nil {
			return nil, aerr
		}
		out.AbortSteps = abortSteps
	}

	for _, r := range st.AbortRules {
		// Validate that threshold and timing are literals or identifiers
		if !isConstantOrIdent(r.Value) {
			return nil, fmt.Errorf("%s:%d: state %q: abort_rule threshold must be a literal or identifier: %v",
				file, r.LineNo, st.Name, r.Value)
		}
		if !isConstantOrIdent(r.FromMs) {
			return nil, fmt.Errorf("%s:%d: state %q: abort_rule \"from\" must be a literal or identifier: %v",
				file, r.LineNo, st.Name, r.FromMs)
		}
		if !isConstantOrIdent(r.ToMs) {
			return nil, fmt.Errorf("%s:%d: state %q: abort_rule \"to\" must be a literal or identifier: %v",
				file, r.LineNo, st.Name, r.ToMs)
		}
		out.Rules = append(out.Rules, DaqLocalAbortRule{
			Channel:   r.Channel,
			Op:        r.Op,
			ValueExpr: r.Value,
			FromExpr:  r.FromMs,
			ToExpr:    r.ToMs,
		})
	}

	return out, nil
}

// stmtErr carries a line number alongside a message so the caller can add the
// file / state prefix.
type stmtErr struct {
	line int
	msg  string
}

func (e *stmtErr) Error() string { return e.msg }

// splitTrailingTransition peels an optional trailing `transition X` off a
// daq_local block and rejects transitions anywhere else in it.
func splitTrailingTransition(stmts []dsl.Stmt) ([]dsl.Stmt, string, *stmtErr) {
	target := ""
	body := stmts
	if n := len(stmts); n > 0 {
		if trans, ok := stmts[n-1].(*dsl.TransitionStmt); ok {
			target = trans.Target
			body = stmts[:n-1]
		}
	}
	for _, s := range body {
		if t, ok := s.(*dsl.TransitionStmt); ok {
			return nil, "", &stmtErr{t.LineNo,
				"transition statements in a daq_local block must be trailing (at the end)"}
		}
	}
	return body, target, nil
}

// compileDaqSteps turns a daq_local block into timed set-steps.  Only
// assignments and sleeps are reducible to a cached schedule.
func compileDaqSteps(file, state, block string, stmts []dsl.Stmt) ([]DaqLocalStep, error) {
	out := []DaqLocalStep{}
	for _, s := range stmts {
		switch v := s.(type) {
		case *dsl.AssignStmt:
			if !isConstantOrIdent(v.Value) {
				return nil, fmt.Errorf("%s:%d: state %q: %s: assignment to %q must be a literal, soft-channel identifier, or constant arithmetic over them",
					file, v.LineNo, state, block, v.Target)
			}
			out = append(out, DaqLocalStep{RefDes: v.Target, Expr: v.Value})

		case *dsl.SleepStmt:
			if !isConstantOrIdent(v.Duration) {
				return nil, fmt.Errorf("%s:%d: state %q: %s: sleep must be a literal duration, soft-channel identifier, or constant arithmetic over them",
					file, v.LineNo, state, block)
			}
			// A statically negative sleep is caught here; identifier-bearing
			// arithmetic is only checkable at send time.
			if lit, ok := v.Duration.(*dsl.LiteralExpr); ok {
				if n, err := literalNumber(lit); err == nil && n < 0 {
					return nil, fmt.Errorf("%s:%d: state %q: %s: sleep must not be negative", file, v.LineNo, state, block)
				}
			}
			out = append(out, DaqLocalStep{RefDes: "", Expr: v.Duration})

		default:
			return nil, fmt.Errorf("%s:%d: state %q: %s: daq_local blocks allow only assignments, sleeps, and a trailing transition, got %T",
				file, s.Line(), state, block, s)
		}
	}
	return out, nil
}

// isConstantOrIdent reports whether an expression is send-time resolvable:
// a literal, a soft-channel identifier, or constant arithmetic (+ - * / %)
// and unary minus over those.  Anything else (comparisons, boolean logic,
// member access on machines) cannot be folded into a number for the node.
func isConstantOrIdent(e dsl.Expr) bool {
	switch v := e.(type) {
	case *dsl.LiteralExpr:
		return true
	case *dsl.IdentExpr:
		return true
	case *dsl.UnaryExpr:
		if v.Op == "-" {
			return isConstantOrIdent(v.Operand)
		}
		return false
	case *dsl.BinaryExpr:
		switch v.Op {
		case "+", "-", "*", "/", "%":
			return isConstantOrIdent(v.Left) && isConstantOrIdent(v.Right)
		}
		return false
	default:
		return false
	}
}

// resolveDaqLocalState takes a pre-compiled DaqLocalState and resolves all
// expressions (identifiers and literals) using the provided Reader.
// Returns a ready-to-send DaqStateUpdate or an error if any identifier is unresolvable.
func resolveDaqLocalState(dls *DaqLocalState, reader Reader) (*DaqStateUpdate, error) {
	out := &DaqStateUpdate{
		Type:          "state_update",
		State:         dls.State,
		EntrySequence: []DaqStep{},
		ExitSequence:  []DaqStep{},
		AbortRules:    []DaqAbortRule{},
		Node:          dls.Node,
	}

	entry, err := resolveSteps(dls.Steps, reader)
	if err != nil {
		return nil, fmt.Errorf("sequence: %v", err)
	}
	out.EntrySequence = entry

	exit, err := resolveSteps(dls.AbortSteps, reader)
	if err != nil {
		return nil, fmt.Errorf("abort_sequence: %v", err)
	}
	out.ExitSequence = exit

	// Resolve abort rules
	for _, rule := range dls.Rules {
		val, err := resolveExpr(rule.ValueExpr, reader)
		if err != nil {
			return nil, fmt.Errorf("abort_rule threshold: %v", err)
		}
		from, err := resolveExpr(rule.FromExpr, reader)
		if err != nil {
			return nil, fmt.Errorf("abort_rule \"from\": %v", err)
		}
		to, err := resolveExpr(rule.ToExpr, reader)
		if err != nil {
			return nil, fmt.Errorf("abort_rule \"to\": %v", err)
		}

		out.AbortRules = append(out.AbortRules, DaqAbortRule{
			If:     fmt.Sprintf("%s %s %s", rule.Channel, rule.Op, formatNumber(val)),
			TMsOn:  from,
			TMsOff: to,
		})
	}

	return out, nil
}

// resolveSteps folds a block's expressions into absolute t_ms set-points.
// Sleeps accumulate time; assignments emit a step at the current time.
func resolveSteps(steps []DaqLocalStep, reader Reader) ([]DaqStep, error) {
	out := []DaqStep{}
	tMs := 0.0
	for _, step := range steps {
		val, err := resolveExpr(step.Expr, reader)
		if err != nil {
			return nil, fmt.Errorf("resolve expression: %v", err)
		}
		if step.RefDes == "" {
			// A sleep.  Negative means the operator-tuned constants are
			// inconsistent (e.g. SEQ-IGN-LEAD > 2000): refuse the payload.
			if val < 0 {
				return nil, fmt.Errorf("sleep duration resolved to a negative value (%v ms)", val)
			}
			tMs += val
			continue
		}
		out = append(out, DaqStep{TMs: tMs, RefDes: step.RefDes, Value: val})
	}
	return out, nil
}

// resolveExpr folds a send-time expression to a number using the provided
// Reader: literals, soft-channel identifiers, unary minus and constant
// arithmetic (+ - * / %).  Unresolvable identifiers are an error, never 0.
func resolveExpr(e dsl.Expr, reader Reader) (float64, error) {
	switch v := e.(type) {
	case *dsl.LiteralExpr:
		return literalNumber(v)

	case *dsl.BinaryExpr:
		l, err := resolveExpr(v.Left, reader)
		if err != nil {
			return 0, err
		}
		r, err := resolveExpr(v.Right, reader)
		if err != nil {
			return 0, err
		}
		switch v.Op {
		case "+":
			return l + r, nil
		case "-":
			return l - r, nil
		case "*":
			return l * r, nil
		case "/":
			if r == 0 {
				return 0, fmt.Errorf("line %d: division by zero", v.LineNo)
			}
			return l / r, nil
		case "%":
			if r == 0 {
				return 0, fmt.Errorf("line %d: modulo by zero", v.LineNo)
			}
			return math.Mod(l, r), nil
		default:
			return 0, fmt.Errorf("line %d: operator %q is not supported in daq_local", v.LineNo, v.Op)
		}

	case *dsl.IdentExpr:
		// Look up the identifier in the reader
		val, ok := reader.Get(v.Name)
		if !ok {
			return 0, fmt.Errorf("unresolvable reference: %q", v.Name)
		}
		// The reader returns a dsl.Value; convert it to float64
		f, err := dslValueToFloat(val)
		if err != nil {
			return 0, fmt.Errorf("channel %q: %v", v.Name, err)
		}
		return f, nil

	case *dsl.UnaryExpr:
		if v.Op != "-" {
			return 0, fmt.Errorf("operator %q is not supported in daq_local", v.Op)
		}
		n, err := resolveExpr(v.Operand, reader)
		if err != nil {
			return 0, err
		}
		return -n, nil

	default:
		return 0, fmt.Errorf("unsupported expression type: %T", e)
	}
}

// literalNumber evaluates a compile-time-constant numeric expression.  Booleans
// reduce to 1/0 so `VENT-CMD = true` works the same as `VENT-CMD = 1`.
func literalNumber(e *dsl.LiteralExpr) (float64, error) {
	switch lit := e.Value.(type) {
	case int64:
		return float64(lit), nil
	case float64:
		return lit, nil
	case bool:
		if lit {
			return 1, nil
		}
		return 0, nil
	case string:
		return 0, fmt.Errorf("string literal is not numeric")
	}
	return 0, fmt.Errorf("unknown literal type")
}

// dslValueToFloat converts a dsl.Value to a float64.
func dslValueToFloat(v dsl.Value) (float64, error) {
	switch v.Type() {
	case "float":
		return v.Float(), nil
	case "bool":
		if v.Bool() {
			return 1, nil
		}
		return 0, nil
	case "string":
		return 0, fmt.Errorf("string value cannot be converted to number")
	default:
		return 0, fmt.Errorf("unsupported value type: %s", v.Type())
	}
}

func formatNumber(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}
