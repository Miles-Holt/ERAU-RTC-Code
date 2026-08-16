package statemachine

import (
	"context"
	"errors"
	"fmt"

	"controlnode/dsl"
)

// ── Sequence runs ─────────────────────────────────────────────────────────────

// errCanceled means the sequence's state was left (transition, or engine shutdown).
var errCanceled = errors.New("sequence canceled")

// errTransitioned means the sequence asked the engine for a state change and
// must therefore stop executing.
var errTransitioned = errors.New("sequence transitioned")

// seqRun is one running sequence goroutine.
type seqRun struct {
	ctx    context.Context
	cancel context.CancelFunc
	tick   chan struct{} // engine pokes this once per tick
	epoch  uint64        // state generation this sequence belongs to
}

func newSeqRun(parent context.Context, epoch uint64) *seqRun {
	ctx, cancel := context.WithCancel(parent)
	return &seqRun{
		ctx:    ctx,
		cancel: cancel,
		tick:   make(chan struct{}, 1),
		epoch:  epoch,
	}
}

// poke wakes the sequence for one tick.  Never blocks: a sequence that is busy
// simply picks up the elapsed time on the next tick it observes.
func (r *seqRun) poke() {
	select {
	case r.tick <- struct{}{}:
	default:
	}
}

// await blocks until the next engine tick or cancellation.
func (r *seqRun) await() error {
	select {
	case <-r.ctx.Done():
		return errCanceled
	case <-r.tick:
		return nil
	}
}

// ── Execution ─────────────────────────────────────────────────────────────────

// runSequence executes a state's sequence block in its own goroutine.
// Completing without a transition simply leaves the state active.
func (e *Engine) runSequence(m *machineRT, r *seqRun, st *State) {
	defer func() {
		if e.onSeqDone != nil {
			e.onSeqDone(m.def.Name, r.epoch)
		}
	}()

	err := e.execSequence(m, r, st.Sequence)
	switch {
	case err == nil, errors.Is(err, errCanceled), errors.Is(err, errTransitioned):
		return
	default:
		e.onError(m.def.Name, fmt.Errorf("state %q sequence: %w", st.Name, err))
	}
}

func (e *Engine) execSequence(m *machineRT, r *seqRun, stmts []dsl.Stmt) error {
	for _, s := range stmts {
		if r.ctx.Err() != nil {
			return errCanceled
		}

		switch v := s.(type) {
		case *dsl.AssignStmt:
			if err := e.execAssign(e.reader, v); err != nil {
				return err
			}

		case *dsl.IncrementStmt:
			if err := e.execStep(e.reader, v.Target, 1, v.LineNo); err != nil {
				return err
			}

		case *dsl.DecrementStmt:
			if err := e.execStep(e.reader, v.Target, -1, v.LineNo); err != nil {
				return err
			}

		case *dsl.TransitionStmt:
			return e.requestTransition(m, r, v.Target)

		case *dsl.SleepStmt:
			if err := e.execSleep(r, v); err != nil {
				return err
			}

		case *dsl.WaitUntilStmt:
			if err := e.execWaitUntil(m, r, v); err != nil {
				return err
			}

		case *dsl.IfStmt:
			body, err := e.selectBranch(e.reader, v)
			if err != nil {
				return err
			}
			if body == nil {
				continue
			}
			if err := e.execSequence(m, r, body); err != nil {
				return err
			}

		default:
			return fmt.Errorf("line %d: statement %T is not allowed in a sequence", s.Line(), s)
		}
	}
	return nil
}

// execSleep waits out a duration in engine time.  Cancellation (a transition
// from the controller or the operator) returns immediately.
func (e *Engine) execSleep(r *seqRun, s *dsl.SleepStmt) error {
	ms, err := e.evalNumber(e.reader, s.Duration)
	if err != nil {
		return err
	}
	start := e.nowMs()
	for float64(e.nowMs()-start) < ms {
		if err := r.await(); err != nil {
			return err
		}
	}
	return nil
}

// execWaitUntil polls the condition once per tick until it holds, the optional
// timeout expires (transitioning to the fallback state) or the run is canceled.
func (e *Engine) execWaitUntil(m *machineRT, r *seqRun, s *dsl.WaitUntilStmt) error {
	var timeoutMs float64
	if s.Timeout != nil {
		v, err := e.evalNumber(e.reader, s.Timeout)
		if err != nil {
			return err
		}
		timeoutMs = v
	}

	start := e.nowMs()
	for {
		ok, err := e.evalBool(e.reader, s.Condition)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		if s.Timeout != nil && float64(e.nowMs()-start) >= timeoutMs {
			return e.requestTransition(m, r, s.TimeoutState)
		}
		if err := r.await(); err != nil {
			return err
		}
	}
}

// requestTransition hands the state change to the engine loop.  The request
// carries the sequence's epoch so a transition raced by a controller abort is
// discarded rather than resurrecting a dead state.
func (e *Engine) requestTransition(m *machineRT, r *seqRun, target string) error {
	select {
	case e.transitions <- transitionReq{machine: m.def.Name, target: target, epoch: r.epoch}:
	case <-r.ctx.Done():
		return errCanceled
	}
	return errTransitioned
}
