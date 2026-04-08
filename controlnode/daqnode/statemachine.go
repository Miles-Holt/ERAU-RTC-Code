package daqnode

import (
	"controlnode/config"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// varGetter retrieves the current value of a named channel (soft channel store).
type varGetter interface {
	Get(refDes string) (float64, bool)
}

// resolvedStep is a sequence step with all variables resolved to concrete numbers.
type resolvedStep struct {
	T_ms   float64 `json:"t_ms"`
	RefDes string  `json:"refDes"`
	Value  float64 `json:"value"`
	Label  string  `json:"label,omitempty"`
}

// resolvedAbortRule is an abort rule with variable references resolved.
type resolvedAbortRule struct {
	RefDes   string  `json:"refDes"`
	Op       string  `json:"op"`
	Value    float64 `json:"value"`
	T_ms_on  float64 `json:"t_ms_on"`
	T_ms_off float64 `json:"t_ms_off"`
}

var varPattern = regexp.MustCompile(`^\{\{(\w+)\}\}$`)
var abortIfPattern = regexp.MustCompile(`^(\S+)\s*(>|<|>=|<=|==|!=)\s*(.+)$`)

// stateMachine tracks the current and pending state for one DAQ node and
// builds the JSON messages that drive the handshake with LabVIEW.
type stateMachine struct {
	mu      sync.Mutex
	current string              // last state confirmed by DAQ (state_req received)
	pending string              // target state set when transition is initiated
	control *config.DaqControl  // state definitions from YAML; nil = no state machine
	vars    varGetter            // soft channel store for {{VAR}} resolution
}

// newStateMachine creates a stateMachine starting in the "safe" state.
// If control is nil, all methods become no-ops.
func newStateMachine(control *config.DaqControl, vars varGetter) *stateMachine {
	sm := &stateMachine{
		control: control,
		vars:    vars,
	}
	if control != nil {
		sm.current = "safe"
		sm.pending = "safe"
	}
	return sm
}

// Current returns the last confirmed state name.
func (sm *stateMachine) Current() string {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.current
}

// Pending returns the target state name (set at the start of a transition,
// before the DAQ confirms with state_req).
func (sm *stateMachine) Pending() string {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.pending
}

// RequestTransition validates that `trigger` (e.g. "operator_request",
// "operator_abort") exists as a transition out of the current state leading to
// `target`, then sets pending = target and returns the exit/hard_exit JSON to
// send to the DAQ node.  current is NOT updated yet — that happens in HandleStateReq.
func (sm *stateMachine) RequestTransition(trigger, target string) ([]byte, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.control == nil {
		return nil, fmt.Errorf("no state machine configured")
	}

	st, ok := sm.control.States[sm.current]
	if !ok {
		return nil, fmt.Errorf("current state %q not found in config", sm.current)
	}

	exitType := ""
	for _, t := range st.Transitions {
		if t.On == trigger && t.Target == target {
			exitType = t.ExitType
			break
		}
	}
	if exitType == "" {
		// Also try matching by target only (for operator_request which covers multiple targets)
		for _, t := range st.Transitions {
			if t.Target == target {
				exitType = t.ExitType
				break
			}
		}
	}
	if exitType == "" {
		return nil, fmt.Errorf("no transition from %q to %q with trigger %q", sm.current, target, trigger)
	}

	if _, ok := sm.control.States[target]; !ok {
		return nil, fmt.Errorf("target state %q not defined in config", target)
	}

	if exitType == "" {
		exitType = "hard_exit"
	}

	sm.pending = target
	return sm.buildExitMsg(exitType, target), nil
}

// HandleStateReq is called when the DAQ sends "state_req".  It promotes
// pending → current and returns the state_update JSON for the new current state.
func (sm *stateMachine) HandleStateReq() ([]byte, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.control == nil {
		return nil, fmt.Errorf("no state machine configured")
	}

	sm.current = sm.pending
	return sm.buildStateUpdate(sm.current)
}

// HandleAbortTriggered is called when the DAQ sends "abort_triggered".
// It finds the abort_triggered transition from the current state, sets
// pending to the target, and returns the exit message to send to the DAQ.
// (The DAQ already ran its exit sequence, so this will typically be hard_exit.)
func (sm *stateMachine) HandleAbortTriggered() ([]byte, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.control == nil {
		return nil, fmt.Errorf("no state machine configured")
	}

	st, ok := sm.control.States[sm.current]
	if !ok {
		return nil, fmt.Errorf("current state %q not in config", sm.current)
	}

	for _, t := range st.Transitions {
		if t.On == "abort_triggered" {
			if _, ok := sm.control.States[t.Target]; !ok {
				return nil, fmt.Errorf("abort_triggered target %q not defined", t.Target)
			}
			exitType := t.ExitType
			if exitType == "" {
				exitType = "hard_exit"
			}
			sm.pending = t.Target
			log.Printf("statemachine: abort_triggered — transitioning %s → %s", sm.current, sm.pending)
			return sm.buildExitMsg(exitType, sm.pending), nil
		}
	}

	// No abort_triggered transition defined; default to "abort" state if it exists.
	if _, ok := sm.control.States["abort"]; ok {
		sm.pending = "abort"
		log.Printf("statemachine: abort_triggered (no transition defined) — defaulting to abort state")
		return sm.buildExitMsg("hard_exit", "abort"), nil
	}

	return nil, fmt.Errorf("no abort_triggered transition from state %q", sm.current)
}

// HandleSequenceComplete is called when the DAQ sends "sequence_complete".
// It finds the sequence_complete transition from the current state, sets
// pending, and returns the exit/hard_exit JSON.  Returns nil, nil if no
// such transition exists (no auto-transition for this state).
func (sm *stateMachine) HandleSequenceComplete() ([]byte, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.control == nil {
		return nil, nil
	}

	st, ok := sm.control.States[sm.current]
	if !ok {
		return nil, fmt.Errorf("current state %q not in config", sm.current)
	}

	for _, t := range st.Transitions {
		if t.On == "sequence_complete" {
			if _, ok := sm.control.States[t.Target]; !ok {
				return nil, fmt.Errorf("sequence_complete target %q not defined", t.Target)
			}
			exitType := t.ExitType
			if exitType == "" {
				exitType = "hard_exit"
			}
			sm.pending = t.Target
			log.Printf("statemachine: sequence_complete — transitioning %s → %s", sm.current, sm.pending)
			return sm.buildExitMsg(exitType, sm.pending), nil
		}
	}

	return nil, nil // no sequence_complete transition defined for this state
}

// ── Internal helpers ──────────────────────────────────────────────────────────

// buildExitMsg marshals an exit or hard_exit message.
func (sm *stateMachine) buildExitMsg(exitType, target string) []byte {
	b, _ := json.Marshal(map[string]interface{}{
		"type":   exitType,
		"target": target,
	})
	return b
}

// buildStateUpdate builds the state_update JSON for the named state,
// resolving all {{VAR}} references against the soft channel store.
func (sm *stateMachine) buildStateUpdate(stateName string) ([]byte, error) {
	st, ok := sm.control.States[stateName]
	if !ok {
		return nil, fmt.Errorf("state %q not defined", stateName)
	}

	entry, err := sm.resolveSequence(st.EntrySequence)
	if err != nil {
		return nil, fmt.Errorf("entry_sequence for %q: %w", stateName, err)
	}
	exit, err := sm.resolveSequence(st.ExitSequence)
	if err != nil {
		return nil, fmt.Errorf("exit_sequence for %q: %w", stateName, err)
	}
	rules, err := sm.resolveAbortRules(st.AbortRules)
	if err != nil {
		return nil, fmt.Errorf("abort_rules for %q: %w", stateName, err)
	}

	msg := map[string]interface{}{
		"type":           "state_update",
		"state":          stateName,
		"entry_sequence": entry,
		"exit_sequence":  exit,
		"abort_rules":    rules,
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal state_update: %w", err)
	}
	return b, nil
}

// resolveSequence resolves all {{VAR}} references in a sequence, returning
// concrete resolvedStep values.
func (sm *stateMachine) resolveSequence(steps []config.SequenceStep) ([]resolvedStep, error) {
	out := make([]resolvedStep, 0, len(steps))
	for i, s := range steps {
		tms, err := sm.resolveExpr(s.T_ms)
		if err != nil {
			return nil, fmt.Errorf("step %d t_ms: %w", i, err)
		}
		val, err := sm.resolveExpr(s.Value)
		if err != nil {
			return nil, fmt.Errorf("step %d value: %w", i, err)
		}
		out = append(out, resolvedStep{
			T_ms:   tms,
			RefDes: s.RefDes,
			Value:  val,
			Label:  s.Label,
		})
	}
	return out, nil
}

// resolveAbortRules resolves all {{VAR}} references in abort rules.
func (sm *stateMachine) resolveAbortRules(rules []config.AbortRule) ([]resolvedAbortRule, error) {
	out := make([]resolvedAbortRule, 0, len(rules))
	for i, r := range rules {
		refDes, op, valExpr, err := parseAbortIf(r.If)
		if err != nil {
			return nil, fmt.Errorf("rule %d if: %w", i, err)
		}
		val, err := sm.resolveExpr(valExpr)
		if err != nil {
			return nil, fmt.Errorf("rule %d value: %w", i, err)
		}
		tmsOn, err := sm.resolveExpr(r.T_ms_on)
		if err != nil {
			return nil, fmt.Errorf("rule %d t_ms_on: %w", i, err)
		}
		tmsOff, err := sm.resolveExpr(r.T_ms_off)
		if err != nil {
			return nil, fmt.Errorf("rule %d t_ms_off: %w", i, err)
		}
		out = append(out, resolvedAbortRule{
			RefDes:   refDes,
			Op:       op,
			Value:    val,
			T_ms_on:  tmsOn,
			T_ms_off: tmsOff,
		})
	}
	return out, nil
}

// resolveExpr converts an expression to float64.
// expr may be:
//   - nil              → 0
//   - int / float64    → direct conversion
//   - bool             → 0 or 1
//   - string "{{VAR}}" → look up VAR in variables map → resolve softchan refDes
//   - string "123"     → parse as number
func (sm *stateMachine) resolveExpr(expr interface{}) (float64, error) {
	switch v := expr.(type) {
	case nil:
		return 0, nil
	case int:
		return float64(v), nil
	case float64:
		return v, nil
	case bool:
		if v {
			return 1, nil
		}
		return 0, nil
	case string:
		// Check for {{VAR}} pattern
		if m := varPattern.FindStringSubmatch(strings.TrimSpace(v)); m != nil {
			varName := m[1]
			refDes, ok := sm.control.Variables[varName]
			if !ok {
				log.Printf("statemachine: unknown variable %q, using 0", varName)
				return 0, nil
			}
			val, ok := sm.vars.Get(refDes)
			if !ok {
				log.Printf("statemachine: soft channel %q (var %q) not found, using 0", refDes, varName)
				return 0, nil
			}
			return val, nil
		}
		// Try parsing as a plain number
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0, fmt.Errorf("cannot resolve %q as number or variable", v)
		}
		return f, nil
	}
	return 0, fmt.Errorf("unsupported expression type %T", expr)
}

// parseAbortIf parses a string like "CPT-01 > {{CPT_HIGH}}" into its parts.
// Returns (refDes, op, valueExpr, error).  valueExpr is passed to resolveExpr.
func parseAbortIf(s string) (refDes, op string, valueExpr interface{}, err error) {
	m := abortIfPattern.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return "", "", nil, fmt.Errorf("cannot parse abort rule %q", s)
	}
	return m[1], m[2], m[3], nil
}
