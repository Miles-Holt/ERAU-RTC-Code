package statemachine

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"

	"controlnode/dsl"
)

const defaultTickHz = 100

// ── Injected collaborators ────────────────────────────────────────────────────

// Reader resolves channel values for expression evaluation.  Implementations
// must be safe for concurrent use: the engine loop and every running sequence
// goroutine read through it.
type Reader interface {
	Get(name string) (dsl.Value, bool)
}

// SnapshotReader is an optional Reader capability.  When a Reader implements it
// the engine takes exactly ONE snapshot per tick and evaluates every controller
// against that single consistent view, instead of doing a fresh (and possibly
// torn) lookup per identifier.  FillSnapshot must clear dst and refill it with
// the whole channel space; the engine reuses the same map every tick so no
// allocation is required.
type SnapshotReader interface {
	FillSnapshot(dst map[string]dsl.Value)
}

// mapReader serves a tick snapshot.  Only the engine loop touches it.
type mapReader map[string]dsl.Value

func (m mapReader) Get(name string) (dsl.Value, bool) {
	v, ok := m[name]
	return v, ok
}

// Writer receives every channel assignment made by a controller or sequence.
// The integration layer routes these to broker commands / DAQ cmd messages.
// Implementations must be safe for concurrent use.
type Writer interface {
	Set(refDes string, value float64) error
}

// Config configures an Engine.
type Config struct {
	Program *Program
	Reader  Reader
	Writer  Writer

	// Clock is the tick source; defaults to NewRealClock(TickHz).
	Clock Clock
	// TickHz is the engine tick rate.  It also defines the engine's notion of
	// elapsed time: every tick advances the clock by 1/TickHz seconds, which
	// is what `sleep` and `wait_until … timeout` are measured against, and the
	// value published on the read-only CYCLE_TIME channel.
	TickHz int

	// OnStateChange is called from the engine loop for every state entry,
	// including the initial entry at startup.  It must not block or call back
	// into the engine.
	OnStateChange func(machine, state string)

	// OnError receives runtime evaluation/write errors.  Defaults to log.Printf.
	OnError func(machine string, err error)

	// KnownChannels is the configured channel space (the same list passed to
	// Compile).  It is used only to phrase runtime errors: a reference that
	// resolves to nothing is reported as "no value yet" when the channel is
	// configured, and as "unknown channel" when it is not.  Optional.
	KnownChannels []string

	// PreTick runs at the top of every tick, before the tick snapshot is taken
	// and before any controller.  The integration layer wires the software
	// channel Recompute here so computed channels are fresh for this tick.
	// It must not block or call back into the engine.
	PreTick func()

	// PostTick runs at the end of every tick, after every active controller has
	// run, against the SAME per-tick channel snapshot the controllers saw (with
	// `machine.<name>.state` resolved).  The integration layer wires the alert
	// engine here, giving the spec's tick order: computed channels → controllers
	// → alert rules.  It must not block, must not call back into the engine, and
	// must NOT retain the space it is given — the snapshot map is reused every
	// tick.
	PostTick func(space dsl.ChannelSpace)

	// OnDaqStateEnter is called from the engine loop whenever a machine enters
	// a daq_local state, carrying the freshly-resolved payload to send to the
	// node.  `state_update` means "enter this state now", so the send happens
	// on entry — not at connect time.  It must not block (the daqnode client
	// enqueues on a buffered channel).
	OnDaqStateEnter func(machine, node string, payload *DaqStateUpdate)
}

// ── Engine ────────────────────────────────────────────────────────────────────

// Engine runs every compiled machine on one tick loop.  All state changes —
// from controllers, sequences and operator requests alike — are serialized
// through that loop, so a machine's current state is never written concurrently.
type Engine struct {
	prog       *Program
	reader     Reader
	snapReader SnapshotReader // non-nil when reader supports per-tick snapshots
	writer     Writer
	clock      Clock
	tickSec    float64 // seconds advanced per tick (1/TickHz); immutable after New
	onChange   func(machine, state string)
	onError    func(machine string, err error)
	preTick    func()
	postTick   func(space dsl.ChannelSpace)
	onDaqEnter func(machine, node string, payload *DaqStateUpdate)

	machines map[string]*machineRT // immutable after New
	order    []*machineRT          // immutable after New

	transitions chan transitionReq
	// ticks counts elapsed engine ticks.  Elapsed time in seconds is derived
	// from it (ticks * tickSec) rather than accumulated as a float directly:
	// sync/atomic has no atomic float64, and multiplying an exact integer
	// tick count avoids compounding rounding error over a long-running test.
	ticks atomic.Int64

	// snap is the per-tick channel snapshot, reused across ticks.  Only the
	// engine loop reads or writes it, so it needs no lock.
	snap mapReader

	// prio is the never-dropped path for safety-critical requests (abort,
	// sequence completion).  A bounded channel could drop them under load, so
	// they go on an unbounded slice with a one-slot wake-up signal.
	prioMu  sync.Mutex
	prio    []transitionReq
	prioSig chan struct{}

	// runID is a monotonically increasing id stamped into every daq_local
	// payload.  A node that echoes it back on sequence_complete /
	// abort_triggered lets the engine correlate the report with the exact run.
	runID atomic.Int64

	// known is the configured channel space, used only to phrase runtime
	// resolution failures ("no value yet" vs "unknown channel").  nil disables
	// the distinction.
	known map[string]bool

	// onSeqDone is a test hook fired when a sequence goroutine exits.
	onSeqDone func(machine string, epoch uint64)
}

// machineRT is the runtime state of one machine.
type machineRT struct {
	def *Machine

	mu    sync.RWMutex
	cur   *State
	epoch uint64 // bumped on every state entry; stale sequences are ignored

	// Correlation for the last daq_local payload actually sent for this
	// machine: which state, at which epoch, with which runId.  A DAQ report is
	// only acted on when it still matches.
	daqState string
	daqEpoch uint64
	daqRunID int64

	run *seqRun // only touched from the engine loop
}

func (m *machineRT) current() *State {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cur
}

func (m *machineRT) currentEpoch() uint64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.epoch
}

// transitionReq asks the engine loop to move a machine to a new state.
// epoch 0 means "unconditional" (operator requests); any other value is only
// honoured while the machine is still in the state that produced it.
type transitionReq struct {
	machine string
	target  string
	epoch   uint64
}

// New creates an Engine from a compiled program.
func New(cfg Config) (*Engine, error) {
	if cfg.Program == nil {
		return nil, fmt.Errorf("statemachine: Program is required")
	}
	if cfg.Reader == nil {
		return nil, fmt.Errorf("statemachine: Reader is required")
	}
	if cfg.Writer == nil {
		return nil, fmt.Errorf("statemachine: Writer is required")
	}
	tickHz := cfg.TickHz
	if tickHz <= 0 {
		tickHz = defaultTickHz
	}
	clock := cfg.Clock
	if clock == nil {
		clock = NewRealClock(tickHz)
	}
	onError := cfg.OnError
	if onError == nil {
		onError = func(machine string, err error) {
			log.Printf("statemachine: %s: %v", machine, err)
		}
	}
	tickSec := 1.0 / float64(tickHz)

	e := &Engine{
		prog:        cfg.Program,
		reader:      cfg.Reader,
		writer:      cfg.Writer,
		clock:       clock,
		tickSec:     tickSec,
		onChange:    cfg.OnStateChange,
		onError:     onError,
		preTick:     cfg.PreTick,
		postTick:    cfg.PostTick,
		onDaqEnter:  cfg.OnDaqStateEnter,
		machines:    make(map[string]*machineRT, len(cfg.Program.Machines)),
		transitions: make(chan transitionReq, 64),
		prioSig:     make(chan struct{}, 1),
		snap:        make(mapReader, 256),
	}
	if sr, ok := cfg.Reader.(SnapshotReader); ok {
		e.snapReader = sr
	}
	if len(cfg.KnownChannels) > 0 {
		e.known = make(map[string]bool, len(cfg.KnownChannels))
		for _, c := range cfg.KnownChannels {
			e.known[c] = true
		}
	}
	for _, m := range cfg.Program.Machines {
		rt := &machineRT{def: m, cur: m.Initial}
		e.machines[m.Name] = rt
		e.order = append(e.order, rt)
	}
	return e, nil
}

// Run drives every machine until ctx is cancelled.  It blocks.
func (e *Engine) Run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer e.clock.Stop()

	// Enter each machine's initial state (first state in its file).
	for _, m := range e.order {
		e.enterState(ctx, m, m.def.Initial)
	}

	for {
		select {
		case <-ctx.Done():
			e.stopAll()
			return

		case <-e.prioSig:
			e.drainPriority(ctx)

		case req := <-e.transitions:
			e.applyTransition(ctx, req)

		case <-e.clock.Ticks():
			e.tick(ctx)
			e.clock.TickDone()
		}
	}
}

// tick runs one engine cycle: PreTick, pending transitions, one channel
// snapshot, every active controller, then a wake-up for the running sequences.
func (e *Engine) tick(ctx context.Context) {
	e.ticks.Add(1)

	if e.preTick != nil {
		e.preTick()
	}

	e.drainPriority(ctx)
	e.drainTransitions(ctx)

	// One snapshot per tick: every controller and every comparison inside it
	// sees the same values, so a channel that changes mid-tick cannot produce
	// a torn comparison.
	r := e.tickReader()

	for _, m := range e.order {
		st := m.current()
		if len(st.Controller) == 0 {
			continue
		}
		target, err := e.execController(r, m, st.Controller)
		if err != nil {
			e.onError(m.def.Name, fmt.Errorf("state %q controller: %w", st.Name, err))
			continue
		}
		if target != "" {
			next, ok := m.def.State(target)
			if !ok { // unreachable: checked at compile time
				e.onError(m.def.Name, fmt.Errorf("state %q controller: unknown target %q", st.Name, target))
				continue
			}
			e.enterState(ctx, m, next)
		}
	}

	// Alert rules evaluate last, against the same snapshot, so they judge the
	// state the controllers just produced.
	if e.postTick != nil {
		e.postTick(space{e: e, r: r})
	}

	for _, m := range e.order {
		if m.run != nil {
			m.run.poke()
		}
	}
}

// tickReader returns the reader controllers evaluate against this tick.  With a
// SnapshotReader that is a single refilled map; otherwise it falls back to the
// live reader (used by tests with simple Reader doubles).
func (e *Engine) tickReader() Reader {
	if e.snapReader == nil {
		return e.reader
	}
	e.snapReader.FillSnapshot(e.snap)
	return e.snap
}

func (e *Engine) drainTransitions(ctx context.Context) {
	for {
		select {
		case req := <-e.transitions:
			e.applyTransition(ctx, req)
		default:
			return
		}
	}
}

// enqueuePriority queues a request that must never be dropped.  The backing
// slice is unbounded and the wake-up signal is a one-slot channel, so the
// caller never blocks and the engine always drains everything queued.
func (e *Engine) enqueuePriority(req transitionReq) {
	e.prioMu.Lock()
	e.prio = append(e.prio, req)
	e.prioMu.Unlock()
	select {
	case e.prioSig <- struct{}{}:
	default: // a wake-up is already pending; the drain takes the whole slice
	}
}

func (e *Engine) drainPriority(ctx context.Context) {
	for {
		e.prioMu.Lock()
		batch := e.prio
		e.prio = nil
		e.prioMu.Unlock()
		if len(batch) == 0 {
			return
		}
		for _, req := range batch {
			e.applyTransition(ctx, req)
		}
	}
}

func (e *Engine) applyTransition(ctx context.Context, req transitionReq) {
	m, ok := e.machines[req.machine]
	if !ok {
		return
	}
	// Stale request from a sequence whose state has already been left.
	if req.epoch != 0 && req.epoch != m.currentEpoch() {
		return
	}
	next, ok := m.def.State(req.target)
	if !ok {
		e.onError(m.def.Name, fmt.Errorf("transition to unknown state %q", req.target))
		return
	}
	e.enterState(ctx, m, next)
}

// enterState cancels any running sequence, switches the machine's state,
// notifies the integration layer and launches the new state's sequence.
// Only ever called from the engine loop goroutine.
func (e *Engine) enterState(ctx context.Context, m *machineRT, st *State) {
	if m.run != nil {
		m.run.cancel()
		m.run = nil
	}

	// Resolve the daq_local payload BEFORE the state becomes visible, so the
	// state change and its correlation record can be published in a single
	// mutex hold below.  Doing this after publishing the state would leave a
	// window in which the machine is observably in the new state at the new
	// epoch while (daqState, daqEpoch, daqRunID) still describes the previous
	// run — and a sequence_complete delivered in that window (the DAQ client
	// runs on its own goroutine) would be rejected as stale despite being
	// fresh.  A resolution failure (unresolvable identifier, negative resolved
	// sleep) is a hard error surfaced to the operator: the node is never given
	// a half-valid schedule, and no record is armed for it.
	var payload *DaqStateUpdate
	var resolveErr error
	if st.DaqLocal != "" && st.daqLocal != nil {
		payload, resolveErr = st.resolvePayload(e.reader)
		if resolveErr == nil {
			payload.Machine = m.def.Name
			payload.RunID = e.runID.Add(1)
		}
	}

	m.mu.Lock()
	m.cur = st
	m.epoch++
	epoch := m.epoch
	if payload != nil {
		// Armed atomically with the state entry itself.  Note the record is
		// deliberately NOT cleared when entering a non-daq_local state:
		// NotifyAbortTriggered falls back to the last-armed state's declared
		// destination, while completion correlation is already invalidated by
		// the epoch comparison.
		m.daqState = st.Name
		m.daqEpoch = epoch
		m.daqRunID = payload.RunID
	}
	m.mu.Unlock()

	if e.onChange != nil {
		e.onChange(m.def.Name, st.Name)
	}

	// daq_local sequences are executed by the DAQ node from the payload we send
	// right now — `state_update` means "enter this state now".  Re-running them
	// here would double-command the same valves.
	if st.DaqLocal != "" {
		if resolveErr != nil {
			e.onError(m.def.Name, fmt.Errorf("state %q: cannot send daq_local payload to %s: %w",
				st.Name, st.DaqLocal, resolveErr))
			return
		}
		if payload != nil && e.onDaqEnter != nil {
			e.onDaqEnter(m.def.Name, st.DaqLocal, payload)
		}
		return
	}

	if len(st.Sequence) > 0 {
		r := newSeqRun(ctx, epoch)
		m.run = r
		go e.runSequence(m, r, st)
	}
}

func (e *Engine) stopAll() {
	for _, m := range e.order {
		if m.run != nil {
			m.run.cancel()
			m.run = nil
		}
	}
}

// nowSec is the engine's monotonic notion of elapsed time in seconds,
// derived from the exact tick count so it never accumulates rounding error.
func (e *Engine) nowSec() float64 { return float64(e.ticks.Load()) * e.tickSec }

// TickSeconds returns the engine's tick period in seconds (1/TickHz) — the
// value published on the read-only CYCLE_TIME channel.
func (e *Engine) TickSeconds() float64 { return e.tickSec }

// ── Public accessors ──────────────────────────────────────────────────────────

// State returns the current state name of a machine.
func (e *Engine) State(machine string) (string, bool) {
	m, ok := e.machines[machine]
	if !ok {
		return "", false
	}
	return m.current().Name, true
}

// States returns a snapshot of every machine's current state.
func (e *Engine) States() map[string]string {
	out := make(map[string]string, len(e.order))
	for _, m := range e.order {
		out[m.def.Name] = m.current().Name
	}
	return out
}

// RequestTarget is the operator entry point (HMI writes to SM-<NAME>-TARGET).
// It is rejected unless the target state carries the `operator` flag AND — when
// that flag declares an `operator from` gate list — the machine is currently in
// one of the listed states.  The state change itself is applied by the engine
// loop, not by the caller.
//
// The gate restricts OPERATOR input only.  Engine-internal paths (`transition`
// statements in .sm code, NotifyAbortTriggered, NotifySequenceComplete,
// NotifyDaqReconnect) deliberately bypass it: a DAQ-detected abort must never be
// blocked by a graph the operator's console draws.
func (e *Engine) RequestTarget(machine, state string) error {
	m, ok := e.machines[machine]
	if !ok {
		return fmt.Errorf("unknown machine %q", machine)
	}
	st, ok := m.def.State(state)
	if !ok {
		return fmt.Errorf("machine %q has no state %q", machine, state)
	}
	if !st.Operator {
		return fmt.Errorf("machine %q: state %q is not operator-commandable", machine, state)
	}
	if cur := m.current(); !st.operatorCommandableFrom(stateName(cur)) {
		return fmt.Errorf("machine %q: cannot command %q from %q (allowed from: %s)",
			machine, state, stateName(cur), strings.Join(st.operatorFrom, ", "))
	}
	select {
	case e.transitions <- transitionReq{machine: machine, target: state}:
		return nil
	default:
		return fmt.Errorf("machine %q: engine transition queue full", machine)
	}
}

// CommandMachine implements a `command <machine> -> <state>` statement: one
// machine's own controller/sequence logic directly commanding another
// machine. This is NOT an operator command — it deliberately bypasses the
// target state's `operator` flag and any `operator from` gate entirely.
// Gating exists to stop a human operator skipping steps (e.g. manualControl),
// not to stop a firing sequence driving a subordinate machine such as a press
// system; see docs/restructure/dsl_spec.md.
//
// fromMachine/machine/target are all validated at compile time
// (statemachine/compile.go's checkCommands), so the error paths here are
// defensive rather than expected. Like any other transition it still goes
// through full engine arbitration (epoch/queue) — but it is never dropped:
// it is queued on the same never-dropped priority path NotifyAbortTriggered
// uses, not the bounded e.transitions channel, so a command can never be
// silently lost under load. Any failure to apply is returned to the caller,
// which (via execController/execSequence) routes it through e.onError to an
// operator alert — a command failure is never a silent no-op.
func (e *Engine) CommandMachine(fromMachine, machine, state string) error {
	m, ok := e.machines[machine]
	if !ok {
		return fmt.Errorf("command %s -> %s: unknown machine %q (commanded by %q)", machine, state, machine, fromMachine)
	}
	if _, ok := m.def.State(state); !ok {
		return fmt.Errorf("command %s -> %s: machine %q has no state %q (commanded by %q)", machine, state, machine, state, fromMachine)
	}
	e.enqueuePriority(transitionReq{machine: machine, target: state, epoch: 0})
	return nil
}

// DaqStateUpdates resolves and returns the DAQ state update payloads for a machine,
// resolving all identifiers from the current channel state.
// Only machines with daq_local states will have payloads; others return empty map.
func (e *Engine) DaqStateUpdates(machine string) (map[string][]*DaqStateUpdate, error) {
	m, ok := e.machines[machine]
	if !ok {
		return nil, fmt.Errorf("unknown machine %q", machine)
	}
	return m.def.DaqStateUpdates(e.reader)
}

// MachinesForNode returns the machines that have at least one daq_local state on
// the named node, in program order.
func (e *Engine) MachinesForNode(node string) []string {
	var out []string
	for _, m := range e.order {
		for _, st := range m.def.States {
			if st.DaqLocal == node {
				out = append(out, m.def.Name)
				break
			}
		}
	}
	return out
}

// IsRunningOnNode reports whether the machine's current state is a daq_local
// state on the named node.  Side-effect free (unlike CurrentDaqPayload, which
// stamps a new runId), so it is safe to use as a plain reconnect predicate.
func (e *Engine) IsRunningOnNode(machine, node string) bool {
	m, ok := e.machines[machine]
	if !ok {
		return false
	}
	cur := m.current()
	return cur != nil && cur.DaqLocal == node
}

// CurrentDaqPayload returns a freshly-resolved payload for the machine's current
// state, but only when that state is a daq_local state on the given node.
// ok == false means "this machine is not currently running on that node" — the
// caller must send nothing.  A non-nil error means the state IS daq_local here
// but its values could not be resolved; the caller must surface that, never
// treat it as "nothing to send".
func (e *Engine) CurrentDaqPayload(machine, node string) (payload *DaqStateUpdate, ok bool, err error) {
	m, found := e.machines[machine]
	if !found {
		return nil, false, fmt.Errorf("unknown machine %q", machine)
	}
	m.mu.RLock()
	cur, epoch := m.cur, m.epoch
	m.mu.RUnlock()
	if cur == nil || cur.DaqLocal != node || cur.daqLocal == nil {
		return nil, false, nil
	}
	p, err := cur.resolvePayload(e.reader)
	if err != nil {
		return nil, true, fmt.Errorf("state %q: %w", cur.Name, err)
	}
	p.Machine = machine
	p.RunID = e.runID.Add(1)

	m.mu.Lock()
	// Only re-stamp the correlation record if we are still in the same run.
	if m.epoch == epoch {
		m.daqState = cur.Name
		m.daqEpoch = epoch
		m.daqRunID = p.RunID
	}
	m.mu.Unlock()
	return p, true, nil
}

// NotifyAbortTriggered handles a DAQ-reported abort: the node has already run
// its cached exit_sequence locally, and the engine now moves the machine to the
// destination DECLARED by that state's abort_sequence.  There is no hardcoded
// "abort" state name any more, and compile time guarantees the destination
// exists.  The request is queued on the never-dropped priority path.
func (e *Engine) NotifyAbortTriggered(machine string) error {
	m, ok := e.machines[machine]
	if !ok {
		return fmt.Errorf("unknown machine %q", machine)
	}

	m.mu.RLock()
	cur, lastSent := m.cur, m.daqState
	m.mu.RUnlock()

	target := ""
	if cur != nil && cur.AbortTarget != "" {
		target = cur.AbortTarget
	} else if lastSent != "" {
		// The machine already left the daq_local state, but the node ran that
		// state's exit sequence: honour the destination it was armed with.
		if st, ok := m.def.State(lastSent); ok {
			target = st.AbortTarget
		}
	}
	if target == "" {
		return fmt.Errorf("machine %q: abort_triggered but no abort destination is armed (current state %q)",
			machine, stateName(cur))
	}
	e.enqueuePriority(transitionReq{machine: machine, target: target, epoch: 0})
	return nil
}

// NotifySequenceComplete is called when a DAQ node reports a daq_local sequence
// finished without echoing a runId.  See NotifySequenceCompleteRun.
func (e *Engine) NotifySequenceComplete(machine string) error {
	return e.NotifySequenceCompleteRun(machine, 0)
}

// NotifySequenceCompleteRun applies a DAQ sequence_complete report.
//
// Correlation (F-A4): the report only fires the completion transition when the
// machine is still in the state whose payload was sent, at the same epoch.
// Aborting out of the state and getting a late completion therefore does
// nothing.  When the node echoes the payload's runId (runID > 0) the match is
// exact.  Residual risk without an echo: if the machine LEFT the daq_local
// state and RE-ENTERED it before a stale report was delivered, the stale report
// is indistinguishable from a fresh one and will be accepted.  That window
// cannot be closed from this side alone — closing it needs the DAQ to echo
// runId, which the payload already carries for exactly that purpose.
func (e *Engine) NotifySequenceCompleteRun(machine string, runID int64) error {
	m, ok := e.machines[machine]
	if !ok {
		return fmt.Errorf("unknown machine %q", machine)
	}

	m.mu.RLock()
	cur, epoch := m.cur, m.epoch
	sentState, sentEpoch, sentRun := m.daqState, m.daqEpoch, m.daqRunID
	m.mu.RUnlock()

	if cur == nil {
		return fmt.Errorf("machine %q has no current state", machine)
	}
	if sentState == "" || sentState != cur.Name || sentEpoch != epoch {
		return fmt.Errorf("machine %q: stale sequence_complete for %q ignored (now in %q)",
			machine, sentState, cur.Name)
	}
	if runID > 0 && runID != sentRun {
		return fmt.Errorf("machine %q: sequence_complete for runId %d ignored (current run %d)",
			machine, runID, sentRun)
	}
	if cur.CompletionTarget == "" {
		return nil // finished, but the state declares no follow-on transition
	}
	// epoch-guarded: if the machine leaves the state between here and the
	// engine loop picking it up, the transition is discarded.
	e.enqueuePriority(transitionReq{machine: machine, target: cur.CompletionTarget, epoch: epoch})
	return nil
}

// NotifyDaqReconnect is the state-uncertain path (F-A6): a node reconnected
// while the machine was mid-flight in a daq_local state on it.  The node's
// local sequence timeline cannot be trusted and re-sending the payload would
// re-fire the sequence from t=0, so the engine fires the state's declared abort
// destination and reports an error the HMI raises as an alarm.
func (e *Engine) NotifyDaqReconnect(machine, node string) error {
	m, ok := e.machines[machine]
	if !ok {
		return fmt.Errorf("unknown machine %q", machine)
	}
	cur := m.current()
	if cur == nil || cur.DaqLocal != node {
		return nil // not running on that node — nothing is uncertain
	}
	err := fmt.Errorf("node %s reconnected while state %q was running on it: state is uncertain, firing abort destination",
		node, cur.Name)
	e.onError(machine, err)

	if cur.AbortTarget == "" {
		return fmt.Errorf("machine %q: state %q has no abort destination to fall back to after %s reconnect",
			machine, cur.Name, node)
	}
	e.enqueuePriority(transitionReq{machine: machine, target: cur.AbortTarget, epoch: 0})
	return err
}

// NotifyDaqFirstConnect is the first-connect analogue of NotifyDaqReconnect
// (F-A6b): a node completes its very first handshake ever while a machine is
// already believed to be running a daq_local state on it. This is NOT a
// reconnect — the node has no prior timeline on this control node to have
// gone uncertain about — but it is no safer to treat as "just enter the
// state": the control node has no way to know how much of the sequence, if
// any, has already elapsed on a node it has never exchanged a single message
// with. Silently sending state_update now would fire the sequence from t=0
// into a timeline the engine already considers under way — the same
// double-command hazard state-uncertain handling exists to prevent, just
// arriving from the opposite direction (node showing up late instead of
// dropping and coming back).
//
// The corrective action is the same as a reconnect — fire the state's
// declared abort destination — but the reported error is deliberately
// worded differently so operators, logs and alarms never conflate "this
// node just dropped and came back" with "this node has never been talked to
// before and a sequence was already believed to be running on it."
func (e *Engine) NotifyDaqFirstConnect(machine, node string) error {
	m, ok := e.machines[machine]
	if !ok {
		return fmt.Errorf("unknown machine %q", machine)
	}
	cur := m.current()
	if cur == nil || cur.DaqLocal != node {
		return nil // not running on that node — nothing to refuse
	}
	err := fmt.Errorf("node %s connected for the first time while state %q was already believed to be running on it (no prior connection to this node exists, so how much of the sequence has elapsed cannot be known): firing abort destination",
		node, cur.Name)
	e.onError(machine, err)

	if cur.AbortTarget == "" {
		return fmt.Errorf("machine %q: state %q has no abort destination to fall back to after %s's first connection arrived mid-state",
			machine, cur.Name, node)
	}
	e.enqueuePriority(transitionReq{machine: machine, target: cur.AbortTarget, epoch: 0})
	return err
}

func stateName(st *State) string {
	if st == nil {
		return ""
	}
	return st.Name
}

// ── Expression evaluation ─────────────────────────────────────────────────────

// space is the ChannelSpace handed to the dsl evaluator: it resolves
// `machine.<name>.state` locally and delegates everything else to a Reader.
// Controllers are given the tick snapshot; sequence goroutines, which run
// between ticks, are given the live reader.
type space struct {
	e *Engine
	r Reader
}

func (s space) Get(name string) (dsl.Value, bool) {
	if strings.HasPrefix(name, "machine.") && strings.HasSuffix(name, ".state") {
		mn := name[len("machine.") : len(name)-len(".state")]
		if m, ok := s.e.machines[mn]; ok {
			return dsl.NewString(m.current().Name), true
		}
		return dsl.Value{}, false
	}
	return s.r.Get(name)
}

func (e *Engine) eval(r Reader, expr dsl.Expr) (dsl.Value, error) {
	v, err := dsl.Eval(expr, space{e: e, r: r})
	return v, e.describe(err)
}

// describe phrases an unresolved-reference failure against the configured
// channel space: a channel the config knows about simply has no value yet
// (a DAQ node that has not published), which is a very different problem from
// a name that does not exist at all.
func (e *Engine) describe(err error) error {
	if err == nil || e.known == nil {
		return err
	}
	return dsl.DescribeEvalError(err, func(name string) bool { return e.known[name] })
}

func (e *Engine) evalBool(r Reader, expr dsl.Expr) (bool, error) {
	v, err := e.eval(r, expr)
	if err != nil {
		return false, err
	}
	if v.Type() != "bool" {
		return false, fmt.Errorf("line %d: condition must be boolean, got %s", expr.Line(), v.Type())
	}
	return v.Bool(), nil
}

func (e *Engine) evalNumber(r Reader, expr dsl.Expr) (float64, error) {
	v, err := e.eval(r, expr)
	if err != nil {
		return 0, err
	}
	if v.Type() != "float" {
		return 0, fmt.Errorf("line %d: expected a number, got %s", expr.Line(), v.Type())
	}
	return v.Float(), nil
}

// ── Statement execution ───────────────────────────────────────────────────────

// execAssign evaluates and writes one assignment.  Booleans coerce to 1/0.
func (e *Engine) execAssign(r Reader, s *dsl.AssignStmt) error {
	v, err := e.eval(r, s.Value)
	if err != nil {
		return err
	}
	var f float64
	switch v.Type() {
	case "float":
		f = v.Float()
	case "bool":
		if v.Bool() {
			f = 1
		}
	default:
		return fmt.Errorf("line %d: cannot assign %s value to %q", s.LineNo, v.Type(), s.Target)
	}
	if err := e.writer.Set(s.Target, f); err != nil {
		return fmt.Errorf("line %d: set %s: %w", s.LineNo, s.Target, err)
	}
	return nil
}

// execStep applies delta to a numeric channel (++ / --).
func (e *Engine) execStep(r Reader, target string, delta float64, line int) error {
	v, ok := r.Get(target)
	if !ok {
		return e.describe(&dsl.UnresolvedError{Name: target, Line: line})
	}
	if v.Type() != "float" {
		return fmt.Errorf("line %d: %q is not numeric", line, target)
	}
	if err := e.writer.Set(target, v.Float()+delta); err != nil {
		return fmt.Errorf("line %d: set %s: %w", line, target, err)
	}
	return nil
}

// execController runs a controller block top to bottom.  It returns the target
// state as soon as a `transition` is reached; "" means no transition.
func (e *Engine) execController(r Reader, m *machineRT, stmts []dsl.Stmt) (string, error) {
	for _, s := range stmts {
		switch v := s.(type) {
		case *dsl.AssignStmt:
			if err := e.execAssign(r, v); err != nil {
				return "", err
			}

		case *dsl.IncrementStmt:
			if err := e.execStep(r, v.Target, 1, v.LineNo); err != nil {
				return "", err
			}

		case *dsl.DecrementStmt:
			if err := e.execStep(r, v.Target, -1, v.LineNo); err != nil {
				return "", err
			}

		case *dsl.TransitionStmt:
			return v.Target, nil

		case *dsl.CommandStmt:
			if err := e.CommandMachine(m.def.Name, v.Machine, v.Target); err != nil {
				return "", err
			}

		case *dsl.IfStmt:
			body, err := e.selectBranch(r, v)
			if err != nil {
				return "", err
			}
			if body == nil {
				continue
			}
			target, err := e.execController(r, m, body)
			if err != nil || target != "" {
				return target, err
			}

		default:
			return "", fmt.Errorf("line %d: statement %T is not allowed in a controller", s.Line(), s)
		}
	}
	return "", nil
}

// selectBranch evaluates an if/elif/else chain and returns the body to run,
// or nil when no branch matches.
func (e *Engine) selectBranch(r Reader, s *dsl.IfStmt) ([]dsl.Stmt, error) {
	ok, err := e.evalBool(r, s.Condition)
	if err != nil {
		return nil, err
	}
	if ok {
		return s.Body, nil
	}
	for _, alt := range s.Elif {
		if alt.Condition == nil { // else
			return alt.Body, nil
		}
		ok, err := e.evalBool(r, alt.Condition)
		if err != nil {
			return nil, err
		}
		if ok {
			return alt.Body, nil
		}
	}
	return nil, nil
}
