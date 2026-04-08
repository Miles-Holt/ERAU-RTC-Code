package main

import (
	"log"
	"sort"
	"sync"
	"time"
)

// stateMachine executes state definitions received from the control node.
// It runs entry sequences (timed), monitors abort rules at acquisition rate,
// and executes exit sequences on state transitions.
//
// The control node resolves all {{VAR}} references before sending state_update,
// so the DAQ node only sees concrete float64 values.
type stateMachine struct {
	mu        sync.Mutex
	state     StateUpdateMsg // current cached state definition
	hasState  bool

	commander *Commander
	outCh     chan<- []byte // state messages to send to the control node

	// sequence runner (non-nil while an entry sequence is executing)
	seqCancel chan struct{}

	// abort monitoring state
	seqStart time.Time
	seqActive bool

	stopCh <-chan struct{}

	// incoming messages from server read loop
	updateCh chan StateUpdateMsg
	exitCh   chan ExitMsg
	sampleCh <-chan map[string]float64
}

func newStateMachine(
	cmd *Commander,
	outCh chan<- []byte,
	sampleCh <-chan map[string]float64,
	stopCh <-chan struct{},
) *stateMachine {
	return &stateMachine{
		commander: cmd,
		outCh:     outCh,
		sampleCh:  sampleCh,
		stopCh:    stopCh,
		updateCh:  make(chan StateUpdateMsg, 4),
		exitCh:    make(chan ExitMsg, 4),
	}
}

// HandleStateUpdate enqueues a new state definition from the control node.
func (sm *stateMachine) HandleStateUpdate(msg StateUpdateMsg) {
	sm.updateCh <- msg
}

// HandleExit enqueues an exit or hard_exit message from the control node.
func (sm *stateMachine) HandleExit(msg ExitMsg) {
	sm.exitCh <- msg
}

// Run is the main loop. Call in a goroutine.
func (sm *stateMachine) Run() {
	for {
		select {
		case <-sm.stopCh:
			sm.cancelSequence()
			return

		case msg := <-sm.updateCh:
			sm.applyStateUpdate(msg)

		case msg := <-sm.exitCh:
			sm.handleExit(msg)

		case sample, ok := <-sm.sampleCh:
			if !ok {
				return
			}
			sm.checkAbortRules(sample)
		}
	}
}

// applyStateUpdate caches the new state and starts the entry sequence.
func (sm *stateMachine) applyStateUpdate(msg StateUpdateMsg) {
	sm.cancelSequence() // stop any running sequence

	sm.mu.Lock()
	sm.state = msg
	sm.hasState = true
	sm.seqStart = time.Now()
	sm.seqActive = len(msg.AbortRules) > 0
	sm.mu.Unlock()

	log.Printf("statemachine: entering state %q (%d entry steps, %d abort rules)",
		msg.State, len(msg.EntrySequence), len(msg.AbortRules))

	if len(msg.EntrySequence) > 0 {
		sm.seqCancel = make(chan struct{})
		go sm.runEntrySequence(msg.EntrySequence, sm.seqCancel)
	} else {
		// No entry sequence — immediately signal complete so control node
		// can check for auto-transitions (sequence_complete trigger).
		sm.send(msgSequenceComplete())
		log.Printf("statemachine: state %q has no entry sequence, sent sequence_complete", msg.State)
	}
}

// handleExit runs or skips the exit sequence, then sends state_req.
func (sm *stateMachine) handleExit(msg ExitMsg) {
	sm.cancelSequence()

	sm.mu.Lock()
	sm.seqActive = false
	exitSeq := sm.state.ExitSequence
	sm.mu.Unlock()

	if msg.Type == "exit" && len(exitSeq) > 0 {
		log.Printf("statemachine: running exit sequence (%d steps) → %s", len(exitSeq), msg.Target)
		sm.runExitSequence(exitSeq)
	} else {
		log.Printf("statemachine: hard_exit → %s (skip exit sequence)", msg.Target)
	}

	sm.send(msgStateReq())
}

// checkAbortRules evaluates all abort rules against the current sample.
// Called at acquisition rate (1000 Hz) — must be fast.
func (sm *stateMachine) checkAbortRules(sample map[string]float64) {
	sm.mu.Lock()
	if !sm.seqActive || !sm.hasState {
		sm.mu.Unlock()
		return
	}
	rules := sm.state.AbortRules
	exitSeq := sm.state.ExitSequence
	elapsed := float64(time.Since(sm.seqStart).Milliseconds())
	sm.mu.Unlock()

	for _, rule := range rules {
		// Check time window
		if elapsed < rule.T_ms_on || elapsed > rule.T_ms_off {
			continue
		}
		val, ok := sample[rule.RefDes]
		if !ok {
			continue
		}
		if evalRule(val, rule.Op, rule.Value) {
			sm.mu.Lock()
			sm.seqActive = false
			sm.mu.Unlock()

			sm.cancelSequence()
			log.Printf("statemachine: abort triggered — %s %s %v (current: %v)",
				rule.RefDes, rule.Op, rule.Value, val)

			// Run exit sequence immediately (safety-critical)
			if len(exitSeq) > 0 {
				sm.runExitSequence(exitSeq)
			}
			sm.send(msgAbortTriggered())
			return
		}
	}
}

// runEntrySequence executes timed steps, sending sequence_complete when done.
func (sm *stateMachine) runEntrySequence(steps []SequenceStep, cancel <-chan struct{}) {
	sorted := make([]SequenceStep, len(steps))
	copy(sorted, steps)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].T_ms < sorted[j].T_ms
	})

	start := time.Now()
	for _, step := range sorted {
		delay := time.Duration(step.T_ms)*time.Millisecond - time.Since(start)
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-cancel:
				log.Printf("statemachine: entry sequence cancelled")
				return
			}
		}
		sm.commander.Execute(step.RefDes, step.Value)
		if step.Label != "" {
			log.Printf("statemachine: seq step t=%dms %s = %v (%s)",
				int(step.T_ms), step.RefDes, step.Value, step.Label)
		}
	}

	select {
	case <-cancel:
		return
	default:
	}

	sm.mu.Lock()
	sm.seqActive = false
	sm.mu.Unlock()

	sm.send(msgSequenceComplete())
	log.Printf("statemachine: entry sequence complete")
}

// runExitSequence executes all steps immediately (t_ms is ignored; all fire at once).
func (sm *stateMachine) runExitSequence(steps []SequenceStep) {
	for _, step := range steps {
		sm.commander.Execute(step.RefDes, step.Value)
	}
	log.Printf("statemachine: exit sequence ran (%d steps)", len(steps))
}

// cancelSequence stops any running entry sequence goroutine.
func (sm *stateMachine) cancelSequence() {
	if sm.seqCancel != nil {
		close(sm.seqCancel)
		sm.seqCancel = nil
	}
}

// send enqueues an outbound message non-blocking.
func (sm *stateMachine) send(payload []byte) {
	select {
	case sm.outCh <- payload:
	case <-sm.stopCh:
	}
}

// evalRule evaluates: sensorVal <op> threshold
func evalRule(sensorVal float64, op string, threshold float64) bool {
	switch op {
	case ">":
		return sensorVal > threshold
	case "<":
		return sensorVal < threshold
	case ">=":
		return sensorVal >= threshold
	case "<=":
		return sensorVal <= threshold
	case "==":
		return sensorVal == threshold
	case "!=":
		return sensorVal != threshold
	}
	return false
}
