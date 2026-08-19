package statemachine

import (
	"context"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"controlnode/dsl"
)

// ── Test doubles ──────────────────────────────────────────────────────────────

// fakeSpace is a concurrency-safe channel space that doubles as Reader and
// Writer, recording every write in order.
type fakeSpace struct {
	mu     sync.Mutex
	values map[string]dsl.Value
	writes []write
	err    error // returned by Set when non-nil
}

type write struct {
	RefDes string
	Value  float64
}

func newFakeSpace(nums map[string]float64, bools map[string]bool) *fakeSpace {
	f := &fakeSpace{values: make(map[string]dsl.Value)}
	for k, v := range nums {
		f.values[k] = dsl.NewFloat(v)
	}
	for k, v := range bools {
		f.values[k] = dsl.NewBool(v)
	}
	return f
}

func (f *fakeSpace) Get(name string) (dsl.Value, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.values[name]
	return v, ok
}

func (f *fakeSpace) Set(refDes string, value float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.values[refDes] = dsl.NewFloat(value)
	f.writes = append(f.writes, write{refDes, value})
	return nil
}

func (f *fakeSpace) setNum(name string, v float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.values[name] = dsl.NewFloat(v)
}

func (f *fakeSpace) setBool(name string, v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.values[name] = dsl.NewBool(v)
}

func (f *fakeSpace) snapshotWrites() []write {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]write, len(f.writes))
	copy(out, f.writes)
	return out
}

func (f *fakeSpace) writeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.writes)
}

// stateLog records state changes emitted by the engine.
type stateLog struct {
	mu      sync.Mutex
	entries []string // "machine:state"
}

func (l *stateLog) record(machine, state string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, machine+":"+state)
}

func (l *stateLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.entries))
	copy(out, l.entries)
	return out
}

// ── Harness ───────────────────────────────────────────────────────────────────

type harness struct {
	eng    *Engine
	clock  *ManualClock
	space  *fakeSpace
	log    *stateLog
	errs   chan error
	seqEnd chan uint64
	cancel context.CancelFunc
	done   chan struct{}
}

// startEngine compiles src, starts the engine on a manual clock and returns a
// harness.  The engine is stopped automatically when the test ends.
func startEngine(t *testing.T, prog *Program, space *fakeSpace) *harness {
	t.Helper()

	h := &harness{
		clock:  NewManualClock(),
		space:  space,
		log:    &stateLog{},
		errs:   make(chan error, 32),
		seqEnd: make(chan uint64, 32),
		done:   make(chan struct{}),
	}

	eng, err := New(Config{
		Program:       prog,
		Reader:        space,
		Writer:        space,
		Clock:         h.clock,
		TickHz:        100, // 10 ms per tick
		OnStateChange: h.log.record,
		OnError: func(machine string, err error) {
			select {
			case h.errs <- err:
			default:
			}
		},
	})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	eng.onSeqDone = func(machine string, epoch uint64) {
		select {
		case h.seqEnd <- epoch:
		default:
		}
	}
	h.eng = eng

	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() {
		eng.Run(ctx)
		close(h.done)
	}()
	t.Cleanup(func() {
		cancel()
		h.clock.Stop()
		<-h.done
	})
	return h
}

// tickUntil advances the clock until cond holds, failing after maxTicks.
// Sequences run in their own goroutines, so after each tick the helper yields
// briefly to let them observe it before re-testing the condition.
func (h *harness) tickUntil(t *testing.T, what string, maxTicks int, cond func() bool) {
	t.Helper()
	for i := 0; i < maxTicks; i++ {
		if h.settled(cond) {
			return
		}
		h.clock.Tick()
	}
	if h.settled(cond) {
		return
	}
	state, _ := h.eng.State("coldFlow")
	t.Fatalf("%s: not reached within %d ticks (state=%q writes=%v)", what, maxTicks, state, h.space.snapshotWrites())
}

// settled polls cond a few times, yielding to the sequence goroutines between
// attempts.  Returns as soon as cond holds.
func (h *harness) settled(cond func() bool) bool {
	for i := 0; i < 4; i++ {
		if cond() {
			return true
		}
		runtime.Gosched()
		if i > 0 {
			time.Sleep(200 * time.Microsecond)
		}
	}
	return cond()
}

func (h *harness) waitState(t *testing.T, machine, state string, maxTicks int) {
	t.Helper()
	h.tickUntil(t, "state "+machine+":"+state, maxTicks, func() bool {
		cur, _ := h.eng.State(machine)
		return cur == state
	})
}

func (h *harness) assertNoErrors(t *testing.T) {
	t.Helper()
	select {
	case err := <-h.errs:
		t.Fatalf("unexpected engine error: %v", err)
	default:
	}
}

// coldflowSpace is the nominal channel space for the reference machine.
// cutoffSec is SEQ-CUTOFF-T in seconds (the DSL's base time unit).
func coldflowSpace(cutoffSec float64) *fakeSpace {
	return newFakeSpace(map[string]float64{
		"OV-05-CMD":        0,
		"FV-02-CMD":        0,
		"VENT-CMD":         0,
		"CPT-01":           100,
		"PT-FUEL-AVG":      0,
		"LIM-CPT01-HIGH":   850,
		"SEQ-TARGET-PRESS": 300,
		"SEQ-CUTOFF-T":     cutoffSec,
	}, map[string]bool{
		"IGNITION-OK": true,
	})
}

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestEngine_InitialStateAndSafeSequence(t *testing.T) {
	prog := loadColdflow(t)
	space := coldflowSpace(0.05)
	h := startEngine(t, prog, space)

	if got, _ := h.eng.State("coldFlow"); got != "safe" {
		t.Fatalf("initial state: got %q, want safe", got)
	}
	h.tickUntil(t, "safe sequence writes", 50, func() bool { return h.space.writeCount() >= 3 })

	want := []write{{"FV-02-CMD", 0}, {"OV-05-CMD", 0}, {"VENT-CMD", 1}}
	if got := h.space.snapshotWrites(); !equalWrites(got, want) {
		t.Errorf("safe sequence writes:\n got %v\nwant %v", got, want)
	}
	if got := h.log.snapshot(); len(got) != 1 || got[0] != "coldFlow:safe" {
		t.Errorf("state change log: got %v, want [coldFlow:safe]", got)
	}
	h.assertNoErrors(t)
}

// TestEngine_NominalWalk runs the full nominal sequence documented in
// demo_walkthrough.md and asserts the exact ordered command writes.
func TestEngine_NominalWalk(t *testing.T) {
	prog := loadColdflow(t)
	space := coldflowSpace(0.05) // 0.05 s burn = 5 ticks at 100 Hz
	h := startEngine(t, prog, space)

	// startup: safe sequence safes both valves and opens the vent.
	h.tickUntil(t, "safe sequence", 50, func() bool { return h.space.writeCount() >= 3 })

	// operator commands pressurize.
	if err := h.eng.RequestTarget("coldFlow", "pressurize"); err != nil {
		t.Fatalf("RequestTarget(pressurize): %v", err)
	}
	h.waitState(t, "coldFlow", "pressurize", 50)
	h.tickUntil(t, "pressurize writes", 50, func() bool { return h.space.writeCount() >= 5 })

	// tank reaches target pressure -> sequence transitions to fire.
	space.setNum("PT-FUEL-AVG", 350)
	h.waitState(t, "coldFlow", "fire", 50)
	h.tickUntil(t, "main valve open", 50, func() bool { return h.space.writeCount() >= 6 })

	// burn duration elapses -> fire closes the valve and transitions to vent.
	h.waitState(t, "coldFlow", "vent", 50)
	h.tickUntil(t, "vent writes", 50, func() bool { return h.space.writeCount() >= 9 })

	// tank vents below 50 psi -> back to safe.
	space.setNum("PT-FUEL-AVG", 10)
	h.waitState(t, "coldFlow", "safe", 50)
	h.tickUntil(t, "final safe writes", 50, func() bool { return h.space.writeCount() >= 12 })

	want := []write{
		// safe
		{"FV-02-CMD", 0}, {"OV-05-CMD", 0}, {"VENT-CMD", 1},
		// pressurize
		{"VENT-CMD", 0}, {"OV-05-CMD", 1},
		// fire
		{"FV-02-CMD", 1}, {"FV-02-CMD", 0},
		// vent
		{"OV-05-CMD", 0}, {"VENT-CMD", 1},
		// safe again
		{"FV-02-CMD", 0}, {"OV-05-CMD", 0}, {"VENT-CMD", 1},
	}
	if got := h.space.snapshotWrites(); !equalWrites(got, want) {
		t.Errorf("nominal walk writes:\n got %v\nwant %v", got, want)
	}

	wantStates := []string{
		"coldFlow:safe", "coldFlow:pressurize", "coldFlow:fire", "coldFlow:vent", "coldFlow:safe",
	}
	if got := h.log.snapshot(); !equalStrings(got, wantStates) {
		t.Errorf("state changes:\n got %v\nwant %v", got, wantStates)
	}
	h.assertNoErrors(t)
}

// TestEngine_ControllerGuardTransition covers the controller running every tick:
// CPT-01 above the limit aborts out of pressurize.
func TestEngine_ControllerGuardTransition(t *testing.T) {
	tests := []struct {
		name  string
		setup func(f *fakeSpace)
		from  string
		want  string
	}{
		{
			name:  "pressure high in pressurize",
			setup: func(f *fakeSpace) { f.setNum("CPT-01", 900) },
			from:  "pressurize",
			want:  "abort",
		},
		{
			name:  "pressure nominal in pressurize",
			setup: func(f *fakeSpace) { f.setNum("CPT-01", 100) },
			from:  "pressurize",
			want:  "pressurize",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prog := loadColdflow(t)
			space := coldflowSpace(0.05)
			h := startEngine(t, prog, space)

			if err := h.eng.RequestTarget("coldFlow", tt.from); err != nil {
				t.Fatalf("RequestTarget: %v", err)
			}
			h.waitState(t, "coldFlow", tt.from, 50)

			tt.setup(space)
			h.clock.TickN(3)

			if got, _ := h.eng.State("coldFlow"); got != tt.want {
				t.Errorf("state: got %q, want %q", got, tt.want)
			}
			h.assertNoErrors(t)
		})
	}
}

// TestEngine_ControllerAbortCancelsSleep checks that a controller transition
// kills the running sequence mid-sleep: the burn never completes and no further
// command is written.
func TestEngine_ControllerAbortCancelsSleep(t *testing.T) {
	prog := loadColdflow(t)
	space := coldflowSpace(100) // 100 s burn: far longer than the test runs
	h := startEngine(t, prog, space)

	// Walk to fire.
	if err := h.eng.RequestTarget("coldFlow", "pressurize"); err != nil {
		t.Fatalf("RequestTarget: %v", err)
	}
	h.waitState(t, "coldFlow", "pressurize", 50)
	space.setNum("PT-FUEL-AVG", 350)
	h.waitState(t, "coldFlow", "fire", 50)
	h.tickUntil(t, "main valve open", 50, func() bool {
		w := h.space.snapshotWrites()
		return len(w) > 0 && w[len(w)-1] == write{"FV-02-CMD", 1}
	})

	// Drain sequence-exit notifications from earlier states.
	for len(h.seqEnd) > 0 {
		<-h.seqEnd
	}

	// Chamber pressure spikes: the controller aborts on the next tick.
	space.setNum("CPT-01", 900)
	countBefore := h.space.writeCount()
	h.clock.Tick()

	if got, _ := h.eng.State("coldFlow"); got != "abort" {
		t.Fatalf("state after guard trip: got %q, want abort", got)
	}

	// The fire sequence goroutine must unwind immediately, not after the sleep.
	select {
	case <-h.seqEnd:
	case <-h.done:
		t.Fatalf("engine exited unexpectedly")
	}

	// abort is daq_local: the DAQ runs the cached payload, so the controlnode
	// issues no further writes.
	h.clock.TickN(20)
	if got := h.space.writeCount(); got != countBefore {
		t.Errorf("writes after abort: got %d, want %d (%v)", got, countBefore, h.space.snapshotWrites())
	}
	h.assertNoErrors(t)
}

// TestEngine_WaitUntilTimeout covers `wait_until … timeout … -> safe`.
func TestEngine_WaitUntilTimeout(t *testing.T) {
	srcFmt := "" +
		"machine tmo\n" +
		"state safe\n" +
		"    operator\n" +
		"    sequence\n" +
		"        A-CMD = 0\n" +
		"\n" +
		"state waiting\n" +
		"    operator\n" +
		"    sequence\n" +
		"        A-CMD = 1\n" +
		"        wait_until GO-FLAG > 0 timeout %s -> safe\n" +
		"        transition done\n" +
		"\n" +
		"state done\n" +
		"    sequence\n" +
		"        A-CMD = 2\n"

	tests := []struct {
		name      string
		timeout   string
		satisfy   bool
		wantState string
	}{
		// 50 ms == 5 ticks: expires long before the test satisfies anything.
		{name: "timeout falls back to safe", timeout: "50ms", satisfy: false, wantState: "safe"},
		// 10 s of engine time: the condition wins the race.
		{name: "condition met continues to done", timeout: "10s", satisfy: true, wantState: "done"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := Source{Name: "tmo.sm", Text: fmt.Sprintf(srcFmt, tt.timeout)}
			prog, err := Compile([]Source{src}, Options{KnownChannels: []string{"A-CMD", "GO-FLAG"}})
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			space := newFakeSpace(map[string]float64{"A-CMD": 0, "GO-FLAG": 0}, nil)
			h := startEngine(t, prog, space)

			// Let the initial state's sequence finish before commanding away.
			h.tickUntil(t, "safe entry write", 50, func() bool { return h.space.writeCount() >= 1 })

			if err := h.eng.RequestTarget("tmo", "waiting"); err != nil {
				t.Fatalf("RequestTarget: %v", err)
			}
			h.waitState(t, "tmo", "waiting", 50)
			h.tickUntil(t, "waiting entry write", 50, func() bool { return h.space.writeCount() >= 2 })

			if tt.satisfy {
				space.setNum("GO-FLAG", 1)
			}
			h.tickUntil(t, "settle in "+tt.wantState, 50, func() bool {
				cur, _ := h.eng.State("tmo")
				return cur == tt.wantState
			})

			if got, _ := h.eng.State("tmo"); got != tt.wantState {
				t.Errorf("state: got %q, want %q", got, tt.wantState)
			}
			h.assertNoErrors(t)
		})
	}
}

// TestEngine_OperatorTargets covers acceptance and rejection of operator requests.
func TestEngine_OperatorTargets(t *testing.T) {
	prog := loadColdflow(t)
	space := coldflowSpace(0.05)
	h := startEngine(t, prog, space)

	tests := []struct {
		name    string
		machine string
		state   string
		wantErr string
	}{
		{name: "operator flagged state", machine: "coldFlow", state: "pressurize"},
		{name: "initial state", machine: "coldFlow", state: "safe"},
		{name: "non-operator state", machine: "coldFlow", state: "fire", wantErr: "not operator-commandable"},
		{name: "non-operator vent", machine: "coldFlow", state: "vent", wantErr: "not operator-commandable"},
		{name: "unknown state", machine: "coldFlow", state: "nope", wantErr: `has no state "nope"`},
		{name: "unknown machine", machine: "ghost", state: "safe", wantErr: `unknown machine "ghost"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := h.eng.RequestTarget(tt.machine, tt.state)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("RequestTarget: unexpected error %v", err)
				}
				h.waitState(t, tt.machine, tt.state, 50)
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}

	// A rejected request must not have moved the machine.
	if got, _ := h.eng.State("coldFlow"); got == "fire" {
		t.Errorf("machine entered fire via a rejected operator request")
	}
}

// TestEngine_NotifyAbortTriggered checks the DAQ abort path: the engine goes to
// the destination DECLARED by the running state's abort_sequence, bypassing the
// operator flag, and reports an error when nothing is armed.
func TestEngine_NotifyAbortTriggered(t *testing.T) {
	prog := loadColdflow(t)
	space := coldflowSpace(0.05)
	h := startEngine(t, prog, space)

	// Nothing armed yet: the machine sits in `safe`, which is not daq_local.
	if err := h.eng.NotifyAbortTriggered("coldFlow"); err == nil {
		t.Errorf("abort_triggered with no armed daq_local state: expected an error")
	}

	// Enter the daq_local state, which declares `transition safe` as its abort
	// destination, then abort out of it.
	if err := h.eng.RequestTarget("coldFlow", "pressurize"); err != nil {
		t.Fatalf("RequestTarget to pressurize: %v", err)
	}
	h.waitState(t, "coldFlow", "pressurize", 50)

	h.eng.enqueuePriority(transitionReq{machine: "coldFlow", target: "abort"})
	h.waitState(t, "coldFlow", "abort", 50)

	if err := h.eng.NotifyAbortTriggered("coldFlow"); err != nil {
		t.Fatalf("abort_triggered: unexpected error %v", err)
	}
	h.waitState(t, "coldFlow", "safe", 50) // the declared abort destination

	if err := h.eng.NotifyAbortTriggered("ghost"); err == nil {
		t.Errorf("abort_triggered for unknown machine: expected an error")
	}
}

// TestEngine_AbortNeverDroppedOnFullQueue covers F-A8: the ordinary transition
// channel being saturated must not lose an abort or a sequence completion.
func TestEngine_AbortNeverDroppedOnFullQueue(t *testing.T) {
	prog := loadColdflow(t)
	h := startEngine(t, prog, coldflowSpace(0.05))

	// Enter the daq_local state so an abort destination is armed.
	if err := h.eng.RequestTarget("coldFlow", "abort"); err != nil {
		// `abort` is not operator-flagged; drive it through the priority path.
		h.eng.enqueuePriority(transitionReq{machine: "coldFlow", target: "abort"})
	}
	h.waitState(t, "coldFlow", "abort", 50)

	// Saturate the bounded operator queue with inert requests (unknown machine,
	// so draining them changes nothing).
	for i := 0; i < cap(h.eng.transitions)+8; i++ {
		select {
		case h.eng.transitions <- transitionReq{machine: "ghost", target: "safe"}:
		default:
		}
	}
	// The engine drains concurrently, so exact saturation is not guaranteed —
	// heavy backlog is enough to prove the priority path is independent.
	if len(h.eng.transitions) < cap(h.eng.transitions)/2 {
		t.Fatalf("transition queue not backed up: %d/%d", len(h.eng.transitions), cap(h.eng.transitions))
	}

	// The abort must still be accepted and still be applied.
	if err := h.eng.NotifyAbortTriggered("coldFlow"); err != nil {
		t.Fatalf("abort_triggered on a saturated queue: %v", err)
	}
	h.waitState(t, "coldFlow", "safe", 200)
}

// TestEngine_StaleSequenceComplete covers F-A4: a completion report that arrives
// after the machine has left the state it was sent for is ignored.
func TestEngine_StaleSequenceComplete(t *testing.T) {
	h := startEngine(t, daqLocalCompletionProgram(t), newFakeSpace(
		map[string]float64{"IGN-CMD": 0, "CPT-01": 100}, nil))

	if err := h.eng.RequestTarget("seq", "burn"); err != nil {
		t.Fatalf("RequestTarget burn: %v", err)
	}
	h.waitState(t, "seq", "burn", 50)

	// Abort out of the state; the payload's completion is now stale.
	if err := h.eng.NotifyAbortTriggered("seq"); err != nil {
		t.Fatalf("abort: %v", err)
	}
	h.tickUntil(t, "seq in idle", 50, func() bool {
		s, _ := h.eng.State("seq")
		return s == "idle"
	})

	// The late completion must NOT fire the completion transition.
	if err := h.eng.NotifySequenceComplete("seq"); err == nil {
		t.Errorf("stale sequence_complete: expected it to be rejected")
	}
	h.clock.TickN(5)
	if s, _ := h.eng.State("seq"); s != "idle" {
		t.Errorf("stale sequence_complete moved the machine to %q, want idle", s)
	}

	// A fresh completion, for the run the machine is actually in, applies.
	if err := h.eng.RequestTarget("seq", "burn"); err != nil {
		t.Fatalf("RequestTarget burn: %v", err)
	}
	h.waitState(t, "seq", "burn", 50)
	if err := h.eng.NotifySequenceComplete("seq"); err != nil {
		t.Fatalf("fresh sequence_complete: %v", err)
	}
	h.waitState(t, "seq", "done", 50)

	// A completion echoing a runId from a different run is rejected.
	if err := h.eng.RequestTarget("seq", "burn"); err != nil {
		t.Fatalf("RequestTarget burn: %v", err)
	}
	h.waitState(t, "seq", "burn", 50)
	if err := h.eng.NotifySequenceCompleteRun("seq", 999999); err == nil {
		t.Errorf("sequence_complete with a foreign runId: expected it to be rejected")
	}
}

// daqLocalCompletionProgram is a minimal machine whose daq_local state declares
// both a completion target and an abort destination.
func daqLocalCompletionProgram(t *testing.T) *Program {
	t.Helper()
	src := Source{Name: "s.sm", Text: "" +
		"machine seq\n" +
		"state idle\n" +
		"    operator\n" +
		"state burn\n" +
		"    operator\n" +
		"    daq_local DAQ001\n" +
		"    abort_rule CPT-01 > 850 from 0ms to 1000ms\n" +
		"    sequence\n" +
		"        IGN-CMD = 1\n" +
		"        transition done\n" +
		"    abort_sequence\n" +
		"        IGN-CMD = 0\n" +
		"        transition idle\n" +
		"state done\n" +
		"    operator\n"}
	prog, err := Compile([]Source{src}, Options{KnownChannels: []string{"IGN-CMD", "CPT-01"}})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return prog
}

// TestEngine_FreshCompleteNeverRejectedOnEntry is the regression test for the
// entry-ordering bug behind F-A4: the (state, epoch, runId) correlation record
// used to be written in a SECOND mutex hold, after m.cur/m.epoch had already
// been published.  An observer — such as the DAQ client goroutine delivering
// sequence_complete — could therefore see the machine in the new state at the
// new epoch while the record still described the PREVIOUS run, and a genuinely
// fresh completion was rejected as stale.  The record is now armed in the same
// mutex hold as the state change, so observing the state implies the record.
//
// The watcher goroutine fires the completion the instant the state becomes
// externally visible, which is precisely the window that used to be open.
func TestEngine_FreshCompleteNeverRejectedOnEntry(t *testing.T) {
	h := startEngine(t, daqLocalCompletionProgram(t), newFakeSpace(
		map[string]float64{"IGN-CMD": 0, "CPT-01": 100}, nil))

	const iterations = 100
	for i := 0; i < iterations; i++ {
		result := make(chan error, 1)
		go func() {
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				if s, _ := h.eng.State("seq"); s == "burn" {
					// Fires in the exact window the bug left open.
					result <- h.eng.NotifySequenceComplete("seq")
					return
				}
				runtime.Gosched()
			}
			result <- fmt.Errorf("state burn never became visible")
		}()

		h.eng.enqueuePriority(transitionReq{machine: "seq", target: "burn"})

		var err error
		select {
		case err = <-result:
		case <-time.After(5 * time.Second):
			t.Fatalf("iteration %d: watcher never reported", i)
		}
		if err != nil {
			t.Fatalf("iteration %d: fresh sequence_complete rejected: %v", i, err)
		}

		// The completion must actually carry the machine to its target.
		h.tickUntil(t, fmt.Sprintf("iteration %d: seq in done", i), 200, func() bool {
			s, _ := h.eng.State("seq")
			return s == "done"
		})
	}
}

// TestEngine_DaqStateEnterSendsPayload covers F-A1/B4: entering a daq_local
// state resolves and sends the payload right then, stamped with a rising runId.
func TestEngine_DaqStateEnterSendsPayload(t *testing.T) {
	src := Source{Name: "s.sm", Text: "" +
		"machine seq\n" +
		"state idle\n" +
		"    operator\n" +
		"state burn\n" +
		"    operator\n" +
		"    daq_local DAQ001\n" +
		"    abort_rule CPT-01 > LIM from 0ms to 1000ms\n" +
		"    sequence\n" +
		"        IGN-CMD = 1\n" +
		"        sleep 2 - LEAD\n" +
		"        IGN-CMD = 0\n" +
		"    abort_sequence\n" +
		"        IGN-CMD = 0\n" +
		"        transition idle\n"}
	prog, err := Compile([]Source{src}, Options{KnownChannels: []string{"IGN-CMD", "CPT-01", "LIM", "LEAD"}})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	space := newFakeSpace(map[string]float64{"IGN-CMD": 0, "CPT-01": 100, "LIM": 850, "LEAD": 0.5}, nil)

	var mu sync.Mutex
	var sent []*DaqStateUpdate
	eng, err := New(Config{
		Program: prog, Reader: space, Writer: space,
		Clock: NewManualClock(), TickHz: 100,
		OnDaqStateEnter: func(machine, node string, p *DaqStateUpdate) {
			mu.Lock()
			defer mu.Unlock()
			if machine != "seq" || node != "DAQ001" {
				t.Errorf("OnDaqStateEnter(%q, %q), want (seq, DAQ001)", machine, node)
			}
			sent = append(sent, p)
		},
	})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	if err := eng.RequestTarget("seq", "burn"); err != nil {
		t.Fatalf("RequestTarget: %v", err)
	}
	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		n := len(sent)
		mu.Unlock()
		if n > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("no payload was sent on daq_local state entry")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	mu.Lock()
	p := sent[0]
	mu.Unlock()

	if p.RunID <= 0 {
		t.Errorf("runId = %d, want a positive monotonically increasing id", p.RunID)
	}
	// sleep 2 - LEAD  →  2 - 0.5 = 1.5 s  →  1500 ms on the wire
	want := []DaqStep{
		{TMs: 0, RefDes: "IGN-CMD", Value: 1},
		{TMs: 1500, RefDes: "IGN-CMD", Value: 0},
	}
	if !reflect.DeepEqual(p.EntrySequence, want) {
		t.Errorf("entry_sequence:\n got %+v\nwant %+v", p.EntrySequence, want)
	}
	if len(p.ExitSequence) != 1 || p.ExitSequence[0].RefDes != "IGN-CMD" {
		t.Errorf("exit_sequence: got %+v, want one IGN-CMD step", p.ExitSequence)
	}
	if len(p.AbortRules) != 1 || p.AbortRules[0].If != "CPT-01 > 850" {
		t.Errorf("abort_rules: got %+v", p.AbortRules)
	}
}

// TestEngine_CrossMachineState checks that machine.<name>.state resolves in the
// channel space handed to the evaluator.
func TestEngine_CrossMachineState(t *testing.T) {
	leader := Source{Name: "leader.sm", Text: "" +
		"machine leader\n" +
		"state idle\n" +
		"    operator\n" +
		"state armed\n" +
		"    operator\n"}
	follower := Source{Name: "follower.sm", Text: "" +
		"machine follower\n" +
		"state waiting\n" +
		"    controller\n" +
		"        if machine.leader.state == \"armed\"\n" +
		"            transition ready\n" +
		"state ready\n" +
		"    sequence\n" +
		"        READY-CMD = 1\n"}

	prog, err := Compile([]Source{leader, follower}, Options{KnownChannels: []string{"READY-CMD"}})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	space := newFakeSpace(map[string]float64{"READY-CMD": 0}, nil)
	h := startEngine(t, prog, space)

	h.clock.TickN(2)
	if got, _ := h.eng.State("follower"); got != "waiting" {
		t.Fatalf("follower: got %q, want waiting", got)
	}

	if err := h.eng.RequestTarget("leader", "armed"); err != nil {
		t.Fatalf("RequestTarget: %v", err)
	}
	h.tickUntil(t, "follower follows leader", 50, func() bool {
		cur, _ := h.eng.State("follower")
		return cur == "ready"
	})

	h.tickUntil(t, "ready sequence write", 50, func() bool { return h.space.writeCount() >= 1 })
	if got := h.space.snapshotWrites(); !equalWrites(got, []write{{"READY-CMD", 1}}) {
		t.Errorf("writes: got %v, want [{READY-CMD 1}]", got)
	}
	if states := h.eng.States(); states["leader"] != "armed" || states["follower"] != "ready" {
		t.Errorf("States(): got %v", states)
	}
	h.assertNoErrors(t)
}

// TestEngine_ControllerCounters covers assignments and ++/-- in a controller.
func TestEngine_ControllerCounters(t *testing.T) {
	src := Source{Name: "ctr.sm", Text: "" +
		"machine ctr\n" +
		"state running\n" +
		"    controller\n" +
		"        HEARTBEAT-CTR++\n" +
		"        if HEARTBEAT-CTR >= 3\n" +
		"            transition stopped\n" +
		"state stopped\n" +
		"    controller\n" +
		"        HEARTBEAT-CTR--\n"}

	prog, err := Compile([]Source{src}, Options{KnownChannels: []string{"HEARTBEAT-CTR"}})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	space := newFakeSpace(map[string]float64{"HEARTBEAT-CTR": 0}, nil)
	h := startEngine(t, prog, space)

	h.clock.TickN(3)
	if got, _ := h.eng.State("ctr"); got != "stopped" {
		t.Errorf("state: got %q, want stopped", got)
	}
	v, _ := space.Get("HEARTBEAT-CTR")
	if v.Float() != 3 {
		t.Errorf("HEARTBEAT-CTR after 3 ticks: got %v, want 3", v.Float())
	}
	h.clock.TickN(1)
	v, _ = space.Get("HEARTBEAT-CTR")
	if v.Float() != 2 {
		t.Errorf("HEARTBEAT-CTR after decrement: got %v, want 2", v.Float())
	}
	h.assertNoErrors(t)
}

// TestEngine_ControllerCompoundAssign covers += and -=: `a += b` must behave
// exactly like `a = a + b`, reading the current value of the target and the
// current value of the RHS channel each tick rather than a fixed delta.
func TestEngine_ControllerCompoundAssign(t *testing.T) {
	src := Source{Name: "ctr.sm", Text: "" +
		"machine ctr\n" +
		"state running\n" +
		"    controller\n" +
		"        T-TIME += CYCLE_TIME\n" +
		"        if T-TIME >= 6\n" +
		"            transition stopped\n" +
		"state stopped\n" +
		"    controller\n" +
		"        T-TIME -= 1\n"}

	prog, err := Compile([]Source{src}, Options{KnownChannels: []string{"T-TIME", "CYCLE_TIME"}})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	space := newFakeSpace(map[string]float64{"T-TIME": 0, "CYCLE_TIME": 2}, nil)
	h := startEngine(t, prog, space)

	h.clock.TickN(3)
	if got, _ := h.eng.State("ctr"); got != "stopped" {
		t.Errorf("state: got %q, want stopped", got)
	}
	v, _ := space.Get("T-TIME")
	if v.Float() != 6 {
		t.Errorf("T-TIME after 3 ticks of += CYCLE_TIME(2): got %v, want 6", v.Float())
	}
	h.clock.TickN(1)
	v, _ = space.Get("T-TIME")
	if v.Float() != 5 {
		t.Errorf("T-TIME after -= 1: got %v, want 5", v.Float())
	}
	h.assertNoErrors(t)
}

// TestEngine_RuntimeErrorsReported checks that evaluation failures surface via
// OnError instead of panicking or wedging the loop.
func TestEngine_RuntimeErrorsReported(t *testing.T) {
	src := Source{Name: "bad.sm", Text: "" +
		"machine bad\n" +
		"state running\n" +
		"    controller\n" +
		"        if MISSING-CH > 1\n" +
		"            transition running\n"}

	prog, err := Compile([]Source{src}, Options{}) // no channel checking
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	space := newFakeSpace(nil, nil)
	h := startEngine(t, prog, space)

	h.clock.TickN(2)
	select {
	case e := <-h.errs:
		if !strings.Contains(e.Error(), "MISSING-CH") {
			t.Errorf("error %q does not mention the missing channel", e.Error())
		}
	default:
		t.Fatalf("expected an engine error for the unknown channel")
	}
	if got, _ := h.eng.State("bad"); got != "running" {
		t.Errorf("state: got %q, want running", got)
	}
}

// snapSpace adds the SnapshotReader capability to a fakeSpace, so the engine
// takes one consistent view per tick the way the production reader does.
type snapSpace struct{ *fakeSpace }

func (s snapSpace) FillSnapshot(dst map[string]dsl.Value) {
	clear(dst)
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.values {
		dst[k] = v
	}
}

// The alert engine hangs off PostTick, so the hook must run once per tick,
// AFTER the controllers (spec tick order: computed channels → controllers →
// alert rules) and against the same snapshot the controllers used.
func TestEngine_PostTickOrderAndSnapshot(t *testing.T) {
	src := Source{Name: "counter.sm", Text: "machine counter\n\nstate running\n    controller\n        N++\n"}
	prog, err := Compile([]Source{src}, Options{})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	space := newFakeSpace(map[string]float64{"N": 0}, nil)

	var mu sync.Mutex
	var seen []float64
	clock := NewManualClock()
	eng, err := New(Config{
		// snapSpace makes the reader a SnapshotReader, which is what the real
		// wiring uses — so this exercises the tick-snapshot path.
		Program: prog, Reader: snapSpace{space}, Writer: space, Clock: clock, TickHz: 100,
		PostTick: func(sp dsl.ChannelSpace) {
			v, ok := sp.Get("N")
			mu.Lock()
			if ok {
				seen = append(seen, v.Float())
			}
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { eng.Run(ctx); close(done) }()
	defer func() { cancel(); clock.Stop(); <-done }()

	clock.TickN(3)
	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		n := len(seen)
		mu.Unlock()
		if n >= 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("PostTick ran %d times in 3 ticks, want 3", n)
		default:
			runtime.Gosched()
			time.Sleep(time.Millisecond)
		}
	}

	mu.Lock()
	got := append([]float64(nil), seen[:3]...)
	mu.Unlock()

	// The snapshot is taken BEFORE the controllers run, so PostTick sees the
	// value the controller compared against this tick — 0, 1, 2 while the
	// controller has already written 1, 2, 3.
	want := []float64{0, 1, 2}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("PostTick saw N = %v, want %v (same per-tick snapshot as the controllers)", got, want)
		}
	}
	if v, _ := space.Get("N"); v.Float() < 3 {
		t.Errorf("controller ran %v times, want at least 3 — PostTick must not replace controller execution", v.Float())
	}
}

func TestNew_Validation(t *testing.T) {
	space := newFakeSpace(nil, nil)
	prog := &Program{byName: map[string]*Machine{}}

	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{name: "no program", cfg: Config{Reader: space, Writer: space}, want: "Program is required"},
		{name: "no reader", cfg: Config{Program: prog, Writer: space}, want: "Reader is required"},
		{name: "no writer", cfg: Config{Program: prog, Reader: space}, want: "Writer is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.cfg)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want error containing %q", err, tt.want)
			}
		})
	}

	eng, err := New(Config{Program: prog, Reader: space, Writer: space, Clock: NewManualClock()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := eng.State("nope"); ok {
		t.Errorf("State(nope) should not resolve")
	}
}

// gatedProgram is a minimal machine exercising `operator from a, b`:
//   - idle:   operator from armed   (gated)
//   - armed:  operator from idle    (gated)
//   - manual: operator              (ungated — commandable from anywhere)
//   - burn:   operator from armed, daq_local; its sequence completion AND its
//     abort_sequence both target idle, which idle's gate does not allow from
//     burn — proving those internal paths bypass the gate.
func gatedProgram(t *testing.T) *Program {
	t.Helper()
	src := Source{Name: "g.sm", Text: "" +
		"machine gt\n" +
		"state idle\n" +
		"    operator from armed\n" +
		"state armed\n" +
		"    operator from idle\n" +
		"state manual\n" +
		"    operator\n" +
		"state burn\n" +
		"    operator from armed\n" +
		"    daq_local DAQ001\n" +
		"    abort_rule CPT-01 > 850 from 0ms to 1000ms\n" +
		"    sequence\n" +
		"        IGN-CMD = 1\n" +
		"        transition idle\n" +
		"    abort_sequence\n" +
		"        IGN-CMD = 0\n" +
		"        transition idle\n"}
	prog, err := Compile([]Source{src}, Options{KnownChannels: []string{"IGN-CMD", "CPT-01"}})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return prog
}

// TestEngine_OperatorFromGate covers the `operator from a, b` gate end to end:
// a command from a listed state is accepted, from an unlisted state it is
// rejected with the exact message the engine documents, an ungated operator
// state is commandable from anywhere, and — critically — the gate never
// blocks NotifyAbortTriggered or an in-.sm `transition` statement, even when
// their destination would be refused if an operator tried to command it.
func TestEngine_OperatorFromGate(t *testing.T) {
	h := startEngine(t, gatedProgram(t), newFakeSpace(
		map[string]float64{"IGN-CMD": 0, "CPT-01": 100}, nil))

	// idle is the initial state.
	if got, _ := h.eng.State("gt"); got != "idle" {
		t.Fatalf("initial state: got %q, want idle", got)
	}

	// ACCEPTED: armed is gated "from idle", and the machine is in idle.
	if err := h.eng.RequestTarget("gt", "armed"); err != nil {
		t.Fatalf("RequestTarget(armed) from idle: unexpected error %v", err)
	}
	h.waitState(t, "gt", "armed", 50)

	// Ungated operator state: manual has no `from` list, so it's commandable
	// from armed (or anywhere else) too.
	if err := h.eng.RequestTarget("gt", "manual"); err != nil {
		t.Fatalf("RequestTarget(manual) from armed: unexpected error %v", err)
	}
	h.waitState(t, "gt", "manual", 50)

	// REJECTED: armed is gated "from idle"; the machine is in manual.
	err := h.eng.RequestTarget("gt", "armed")
	if err == nil {
		t.Fatalf("RequestTarget(armed) from manual: expected rejection")
	}
	wantErr := `machine "gt": cannot command "armed" from "manual" (allowed from: idle)`
	if err.Error() != wantErr {
		t.Errorf("rejection message:\n got  %q\n want %q", err.Error(), wantErr)
	}
	// A rejected request must not have moved the machine.
	if got, _ := h.eng.State("gt"); got != "manual" {
		t.Errorf("machine moved to %q on a rejected command", got)
	}

	// manual is a dead end for operator commands in this fixture (armed only
	// accepts from idle, idle only accepts from armed). Reset to idle via the
	// same internal-transition path NotifyAbortTriggered/transition use, which
	// is exactly the point: engine-internal moves are never gate-checked.
	h.eng.enqueuePriority(transitionReq{machine: "gt", target: "idle"})
	h.waitState(t, "gt", "idle", 50)

	// Drive the machine to burn via its only legal operator path (armed is
	// gated "from idle", burn is gated "from armed"), so the abort/completion
	// checks below start from a state idle's gate ("from armed") does NOT list.
	if err := h.eng.RequestTarget("gt", "armed"); err != nil {
		t.Fatalf("RequestTarget(armed) from idle: %v", err)
	}
	h.waitState(t, "gt", "armed", 50)
	if err := h.eng.RequestTarget("gt", "burn"); err != nil {
		t.Fatalf("RequestTarget(burn) from armed: unexpected error %v", err)
	}
	h.waitState(t, "gt", "burn", 50)

	// A plain operator command into idle from burn must still be rejected —
	// this confirms the fixture actually exercises the gate below.
	if err := h.eng.RequestTarget("gt", "idle"); err == nil {
		t.Fatalf("RequestTarget(idle) from burn: expected rejection")
	}

	// NotifyAbortTriggered must still land in idle — the abort_sequence's
	// declared destination — even though idle's gate does not list burn.
	if err := h.eng.NotifyAbortTriggered("gt"); err != nil {
		t.Fatalf("NotifyAbortTriggered: unexpected error %v", err)
	}
	h.waitState(t, "gt", "idle", 50)

	// The in-.sm `transition idle` completion path (burn's sequence, not its
	// abort_sequence) must likewise ignore the gate.
	if err := h.eng.RequestTarget("gt", "armed"); err != nil {
		t.Fatalf("RequestTarget(armed) from idle: %v", err)
	}
	h.waitState(t, "gt", "armed", 50)
	if err := h.eng.RequestTarget("gt", "burn"); err != nil {
		t.Fatalf("RequestTarget(burn) from armed: %v", err)
	}
	h.waitState(t, "gt", "burn", 50)
	if err := h.eng.NotifySequenceComplete("gt"); err != nil {
		t.Fatalf("NotifySequenceComplete: unexpected error %v", err)
	}
	h.waitState(t, "gt", "idle", 50)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func equalWrites(got, want []write) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
