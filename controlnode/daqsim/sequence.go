package daqsim

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ── Wire shapes (CTR -> DAQ state_update), mirroring
// controlnode/statemachine/daqlocal.go's DaqStateUpdate/DaqStep/DaqAbortRule.
// Defined independently rather than importing statemachine: the simulator is
// the *counterparty* on the wire and should only ever agree with the JSON
// contract in docs/websocket-protocol.md, never with the control node's
// internal Go types (which could drift from the wire without either side
// noticing).

type wireStep struct {
	TMs    float64 `json:"t_ms"`
	RefDes string  `json:"refDes"`
	Value  float64 `json:"value"`
}

type wireAbortRule struct {
	If     string  `json:"if"`
	TMsOn  float64 `json:"t_ms_on"`
	TMsOff float64 `json:"t_ms_off"`
}

type wireStateUpdate struct {
	Type          string          `json:"type"`
	State         string          `json:"state"`
	RunID         int64           `json:"runId"`
	EntrySequence []wireStep      `json:"entry_sequence"`
	ExitSequence  []wireStep      `json:"exit_sequence"`
	AbortRules    []wireAbortRule `json:"abort_rules"`
}

// parsedRule is an abort_rule's "if" string split into evaluable parts:
// "<refDes> <op> <number>" (docs/websocket-protocol.md, "state_update").
type parsedRule struct {
	channel   string
	op        string
	threshold float64
	tMsOn     float64
	tMsOff    float64
	raw       string
}

func parseAbortRules(in []wireAbortRule) ([]parsedRule, error) {
	out := make([]parsedRule, 0, len(in))
	for _, r := range in {
		fields := strings.Fields(r.If)
		if len(fields) != 3 {
			return nil, fmt.Errorf("abort_rule %q: expected \"<refDes> <op> <number>\"", r.If)
		}
		thr, err := strconv.ParseFloat(fields[2], 64)
		if err != nil {
			return nil, fmt.Errorf("abort_rule %q: threshold %q: %w", r.If, fields[2], err)
		}
		out = append(out, parsedRule{
			channel: fields[0], op: fields[1], threshold: thr,
			tMsOn: r.TMsOn, tMsOff: r.TMsOff, raw: r.If,
		})
	}
	return out, nil
}

// handleStateUpdate parses an incoming `state_update` and, on success, starts
// executing its entry_sequence in the background. `state_update` means
// "enter this state now" (docs/websocket-protocol.md): execution starts
// immediately at t=0 on receipt, independent of anything already running —
// a fresh state_update always supersedes a prior in-flight run, matching the
// control node's own epoch-based re-entry semantics.
func (s *Simulator) handleStateUpdate(raw []byte) {
	var u wireStateUpdate
	if err := json.Unmarshal(raw, &u); err != nil {
		s.logf("bad state_update: %v", err)
		return
	}
	rules, err := parseAbortRules(u.AbortRules)
	if err != nil {
		s.logf("state_update %q: %v", u.State, err)
		return
	}
	s.armedMu.Lock()
	s.armedState = u.State
	s.armedRunID = u.RunID
	s.armedMu.Unlock()

	gen := s.armRun()
	s.logf("state_update %q: runId=%d, %d entry step(s), %d abort_rule(s) — running locally",
		u.State, u.RunID, len(u.EntrySequence), len(rules))
	go s.runEntry(u, rules, gen)
}

// armRun bumps the run generation, superseding any in-flight run: a fresh
// state_update always wins, exactly as re-entering a state on the control
// node cancels its previous sequence goroutine.
func (s *Simulator) armRun() int64 {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	s.activeGen++
	return s.activeGen
}

func (s *Simulator) stillCurrent(gen int64) bool {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	return s.activeGen == gen
}

// runEntry executes one entry_sequence on a real timeline (real or fake,
// depending on the injected Clock): each step is applied at its t_ms, and on
// every scan (opts.ScanInterval) the abort_rules are evaluated against the
// live model within their [t_ms_on, t_ms_off) window. Monitoring runs only
// while the entry_sequence itself is in flight (t=0 .. its last step) — see
// docs/daqsim.md "Protocol ambiguities" for why that boundary was chosen
// over honouring t_ms_off past sequence completion.
func (s *Simulator) runEntry(u wireStateUpdate, rules []parsedRule, gen int64) {
	steps := u.EntrySequence
	endMs := 0.0
	for _, st := range steps {
		if st.TMs > endMs {
			endMs = st.TMs
		}
	}

	scan := s.opts.ScanInterval
	t := 0.0
	idx := 0
	for {
		if !s.stillCurrent(gen) {
			return // superseded by a newer state_update
		}
		for idx < len(steps) && steps[idx].TMs <= t {
			s.applyStep(u.RunID, u.State, "entry", steps[idx])
			idx++
		}
		if rule, tripped := s.scanAbortRules(rules, t); tripped {
			s.doAbort(u, rule, gen)
			return
		}
		if t >= endMs && idx >= len(steps) {
			break
		}
		next := scan
		if idx < len(steps) {
			if remain := steps[idx].TMs - t; remain < float64(next/time.Millisecond) {
				next = time.Duration(remain) * time.Millisecond
			}
		}
		if next <= 0 {
			next = time.Millisecond
		}
		s.clock.Sleep(next)
		t += float64(next / time.Millisecond)
	}

	if !s.stillCurrent(gen) {
		return
	}
	s.completeRun(u)
}

// scanAbortRules evaluates every armed rule against the live model at
// sequence-relative time t (ms). Returns the first rule that trips.
func (s *Simulator) scanAbortRules(rules []parsedRule, t float64) (parsedRule, bool) {
	for _, r := range rules {
		if t < r.tMsOn || t > r.tMsOff {
			continue
		}
		v, ok := s.model.Get(r.channel, time.Since(s.started).Seconds())
		if !ok {
			continue
		}
		if evalCompare(r.op, v, r.threshold) {
			return r, true
		}
	}
	return parsedRule{}, false
}

// doAbort runs the exit_sequence (all steps, honouring t_ms, no further abort
// monitoring) and reports abort_triggered — the node has already safed the
// hardware locally by the time the message is sent, matching
// docs/websocket-protocol.md's "nothing about the safing action depends on
// this message arriving".
func (s *Simulator) doAbort(u wireStateUpdate, rule parsedRule, gen int64) {
	s.runTimedSteps(u.RunID, u.State, "exit", u.ExitSequence)
	if !s.stillCurrent(gen) {
		return
	}
	s.recordRun(SeqRecord{RunID: u.RunID, State: u.State, Outcome: "aborted", TrippedIf: rule.raw})
	s.logf("state %q: abort_rule %q tripped — exit_sequence run, sending abort_triggered", u.State, rule.raw)
	if err := s.send(map[string]interface{}{"type": "abort_triggered"}); err != nil {
		s.logf("send abort_triggered: %v", err)
	}
}

// completeRun reports sequence_complete, echoing the runId the control node
// needs to correlate the report with the exact run it belongs to.
func (s *Simulator) completeRun(u wireStateUpdate) {
	s.recordRun(SeqRecord{RunID: u.RunID, State: u.State, Outcome: "completed"})
	s.logf("state %q: entry_sequence complete — sending sequence_complete runId=%d", u.State, u.RunID)
	if err := s.send(map[string]interface{}{"type": "sequence_complete", "runId": u.RunID}); err != nil {
		s.logf("send sequence_complete: %v", err)
	}
}

// runTimedSteps applies a step list sequentially, honouring each step's t_ms
// relative to the start of this call (used for exit_sequence, which is not
// subject to abort_rule scanning).
func (s *Simulator) runTimedSteps(runID int64, state, phase string, steps []wireStep) {
	last := 0.0
	for _, st := range steps {
		if st.TMs > last {
			s.clock.Sleep(time.Duration(st.TMs-last) * time.Millisecond)
			last = st.TMs
		}
		s.applyStep(runID, state, phase, st)
	}
}

func (s *Simulator) applyStep(runID int64, state, phase string, st wireStep) {
	s.model.Set(st.RefDes, st.Value)
	s.appliedMu.Lock()
	s.applied = append(s.applied, AppliedSetPoint{
		RunID: runID, State: state, Phase: phase,
		RefDes: st.RefDes, Value: st.Value, TMs: st.TMs, At: s.clock.Now(),
	})
	s.appliedMu.Unlock()
}

func (s *Simulator) recordRun(rec SeqRecord) {
	s.runsMu.Lock()
	s.runs = append(s.runs, rec)
	s.runsMu.Unlock()
}
