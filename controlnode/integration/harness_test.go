// Package integration is the payoff of the daqsim work: it wires the REAL
// control node pieces (broker, softchan store loaded from config/channels,
// statemachine engine compiled from the REAL config/machines/daq001.sm, and
// daqnode.Client) against controlnode/daqsim over a real localhost
// WebSocket, and drives the actual controlNode<->daqNode protocol end to
// end — something no test in this repo did before daqsim existed, since the
// only other daqNode implementation is LabVIEW.
//
// This file holds the shared test harness; daqsim_e2e_test.go holds the four
// scenarios themselves.
package integration

import (
	"context"
	"controlnode/broker"
	"controlnode/config"
	"controlnode/daqnode"
	"controlnode/daqsim"
	"controlnode/dsl"
	"controlnode/softchan"
	"controlnode/statemachine"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// daqLocalFixtureSrc is a small daq_local machine kept ONLY so this suite can
// still exercise the daq_local / abort_rule / abort_sequence wire protocol
// against a real daqsim connection now that config/machines/daq001.sm's
// autoSequence runs entirely control-node side (no daq_local in that machine
// any more — see docs/restructure/dsl_spec.md). It is compiled ALONGSIDE the
// real shipped config, not in place of it: newHarness still loads the actual
// config/machines/daq001.sm too, so production config keeps being exercised
// as-is. Timings are independent of daq001.sm's SEQ-IGN-LEAD/SEQ-CUTOFF-T
// channels (deliberately hardcoded) so the two machines' schedules can never
// interact through a shared channel.
const daqLocalFixtureSrc = "" +
	"machine daqLocalFixture\n" +
	"\n" +
	"state idle\n" +
	"    operator\n" +
	"\n" +
	"state burn\n" +
	"    operator from idle\n" +
	"    daq_local DAQ001\n" +
	"    abort_rule CPT-01 > LIM-CPT01-HIGH from 0ms to 2000ms\n" +
	"    abort_rule CPT-01 < LIM-CPT01-LOW from 50ms to 300ms\n" +
	"    sequence\n" +
	"        OV-01-CMD = 0\n" +
	"        FV-01-CMD = 0\n" +
	"        NV-03-CMD = 1\n" +
	"        NV-04-CMD = 1\n" +
	"        sleep 50ms\n" +
	"        IG-01-CMD = 1\n" +
	"        sleep 100ms\n" +
	"        OV-05-CMD = 1\n" +
	"        FV-03-CMD = 1\n" +
	"        sleep 150ms\n" +
	"        OV-05-CMD = 0\n" +
	"        FV-03-CMD = 0\n" +
	"        IG-01-CMD = 0\n" +
	"        transition safe\n" +
	"    abort_sequence\n" +
	"        OV-05-CMD = 0\n" +
	"        FV-03-CMD = 0\n" +
	"        IG-01-CMD = 0\n" +
	"        NV-03-CMD = 0\n" +
	"        NV-04-CMD = 0\n" +
	"        transition abort\n" +
	"\n" +
	"state safe\n" +
	"    operator from burn, abort\n" +
	"\n" +
	"state abort\n" +
	"    operator from burn\n"

// configDir is the real, shipped configuration — the same one main.go loads
// in production. Using anything else would defeat the point: this is the
// suite that proves the protocol works against the config that actually runs
// the stand.
const configDir = "../../config"

// harness bundles the real control-node pieces plus a daqsim standing in for
// DAQ001, all wired together as main.go wires them.
type harness struct {
	t      *testing.T
	b      *broker.Broker
	sc     *softchan.Store
	eng    *statemachine.Engine
	client *daqnode.Client
	sim    *daqsim.Simulator

	ctx    context.Context
	cancel context.CancelFunc
}

// engineReader/engineWriter mirror main.go's unexported adapters: hardware
// channels resolve through the broker, software channels through the store.
type engineReader struct {
	b  *broker.Broker
	sc *softchan.Store
}

func (r *engineReader) Get(name string) (dsl.Value, bool) {
	if v, ok := r.b.CurrentValue(name); ok {
		return dsl.NewFloat(v), true
	}
	if v, ok := r.sc.Get(name); ok {
		return dsl.NewFloat(v), true
	}
	return dsl.Value{}, false
}

func (r *engineReader) FillSnapshot(dst map[string]dsl.Value) {
	clear(dst)
	r.b.EachValue(func(name string, v float64) { dst[name] = dsl.NewFloat(v) })
	r.sc.EachValue(func(name string, v float64) { dst[name] = dsl.NewFloat(v) })
}

type engineWriter struct {
	b  *broker.Broker
	sc *softchan.Store
}

func (w *engineWriter) Set(refDes string, value float64) error {
	if _, ok := w.sc.Get(refDes); ok {
		return w.sc.Set(refDes, value)
	}
	w.b.SendCmd(broker.CmdMsg{Type: "cmd", RefDes: refDes, Value: value, User: "test"})
	return nil
}

// newHarness loads the real shipped config and machine, starts a daqsim in
// place of DAQ001, and connects a real daqnode.Client to it. simClock lets
// the caller choose FakeClock (nominal/abort — instant multi-second burns) or
// RealClock (reconnect/stale — needs a genuine in-flight window; pair with
// shrinkBurnTiming).
func newHarness(t *testing.T, simClock daqsim.Clock, sensors map[string]daqsim.SensorSpec) *harness {
	t.Helper()
	h := newHarnessDeferredConnect(t, simClock, sensors)
	h.connectClient()
	return h
}

// newHarnessExtra is newHarness plus additional `.sm` sources compiled into
// the SAME program as the real shipped config and daqLocalFixtureSrc. Kept
// separate from newHarness (rather than a variadic on it) so every existing
// call site — which must NOT get an extra autonomous machine racing its own
// RequestTarget calls — is untouched.
func newHarnessExtra(t *testing.T, simClock daqsim.Clock, sensors map[string]daqsim.SensorSpec, extra ...statemachine.Source) *harness {
	t.Helper()
	h := newHarnessDeferredConnectExtra(t, simClock, sensors, extra...)
	h.connectClient()
	return h
}

// newHarnessDeferredConnect builds the same harness as newHarness (real
// config, real engine, real daqsim standing in for DAQ001) but does NOT start
// the daqnode.Client's Run() loop — the WebSocket connection to daqsim is not
// even attempted until the caller invokes h.connectClient(). This lets a test
// drive the engine (e.g. RequestTarget into a daq_local state) BEFORE the
// node's very first connection completes, which is exactly the ordering
// needed to exercise the first-connect-while-already-running and
// undeliverable-state_update cases: see TestFirstConnectWhileAlreadyRunning
// and TestStateUpdateUndeliverableWhileDisconnected in daqsim_e2e_test.go.
func newHarnessDeferredConnect(t *testing.T, simClock daqsim.Clock, sensors map[string]daqsim.SensorSpec) *harness {
	t.Helper()
	return newHarnessDeferredConnectExtra(t, simClock, sensors)
}

// newHarnessDeferredConnectExtra is newHarnessDeferredConnect plus additional
// `.sm` sources compiled into the same program; see newHarnessExtra.
func newHarnessDeferredConnectExtra(t *testing.T, simClock daqsim.Clock, sensors map[string]daqsim.SensorSpec, extra ...statemachine.Source) *harness {
	t.Helper()

	cfg, err := config.ParseDir(configDir)
	if err != nil {
		t.Fatalf("config.ParseDir: %v", err)
	}
	refDesMap := config.BuildRefDesMap(cfg)

	sc, err := softchan.LoadFromDir(configDir+"/channels", configDir+"/nonexistent-softChannelValues.yaml")
	if err != nil {
		t.Fatalf("softchan.LoadFromDir: %v", err)
	}
	machineNames, err := statemachine.ScanMachineNames(configDir + "/machines")
	if err != nil {
		t.Fatalf("ScanMachineNames: %v", err)
	}
	machineNames = append(machineNames, "daqLocalFixture")
	for _, src := range extra {
		if name, ok := scanMachineName(src.Text); ok {
			machineNames = append(machineNames, name)
		}
	}
	sc.RegisterStateMachineChannels(machineNames)
	sc.RegisterCycleTimeChannel(200) // must match TickHz below
	for k, v := range sc.RefDesMap() {
		refDesMap[k] = v
	}

	knownChannels := make([]string, 0, len(refDesMap))
	for refDes := range refDesMap {
		knownChannels = append(knownChannels, refDes)
	}

	// Compile the REAL shipped config (config/machines/*.sm, including
	// daq001.sm/firingSequence) plus the daq_local fixture above, as one
	// program — proving the real config still boots while giving this suite
	// something with daq_local to drive against daqsim.
	smPaths, err := statemachine.SMFiles(configDir + "/machines")
	if err != nil {
		t.Fatalf("SMFiles: %v", err)
	}
	sources := make([]statemachine.Source, 0, len(smPaths)+1)
	for _, p := range smPaths {
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			t.Fatalf("read %s: %v", p, rerr)
		}
		sources = append(sources, statemachine.Source{Name: p, Text: string(data)})
	}
	sources = append(sources, statemachine.Source{Name: "daqlocal_fixture.sm", Text: daqLocalFixtureSrc})
	sources = append(sources, extra...)
	prog, err := statemachine.Compile(sources, statemachine.Options{KnownChannels: knownChannels})
	if err != nil {
		t.Fatalf("statemachine.Compile: %v", err)
	}

	b := broker.New(refDesMap, nil, nil)
	go b.Run(200)
	go sc.Run(b, 200)

	reader := &engineReader{b: b, sc: sc}
	writer := &engineWriter{b: b, sc: sc}

	daqClients := make(map[string]*daqnode.Client)

	stateChanges := make(chan struct{ machine, state string }, 64)
	errs := make(chan error, 64)

	eng, err := statemachine.New(statemachine.Config{
		Program: prog,
		Reader:  reader,
		Writer:  writer,
		TickHz:  200, // 5ms/tick: fast enough to keep the suite quick, slow enough to be real time
		OnStateChange: func(machine, state string) {
			sc.SetInternal("SM-"+machine+"-STATE", 0) // index not needed by these tests
			select {
			case stateChanges <- struct{ machine, state string }{machine, state}:
			default:
			}
		},
		KnownChannels: knownChannels,
		PreTick:       func() { _ = sc.Recompute(b) },
		OnDaqStateEnter: func(machine, node string, p *statemachine.DaqStateUpdate) {
			if c, ok := daqClients[node]; ok {
				c.SendStateUpdate(p)
			}
		},
		OnError: func(machine string, e error) {
			select {
			case errs <- fmt.Errorf("%s: %w", machine, e):
			default:
			}
			t.Logf("engine error: %s: %v", machine, e)
		},
	})
	if err != nil {
		t.Fatalf("statemachine.New: %v", err)
	}

	sim := daqsim.New(daqsim.Options{
		RefDes:  "DAQ001",
		Clock:   simClock,
		Sensors: sensors,
		Logf:    func(format string, args ...interface{}) { t.Logf("daqsim: "+format, args...) },
	})
	addr, err := sim.Start("127.0.0.1:0")
	if err != nil {
		t.Fatalf("sim.Start: %v", err)
	}
	host, portStr, _ := strings.Cut(addr, ":")
	if host == "" {
		host = "127.0.0.1"
	}
	var port int
	fmt.Sscanf(portStr, "%d", &port)

	nodeConfigJSON, err := config.BuildDaqNodeConfigJSON(cfg, "DAQ001", 200)
	if err != nil {
		t.Fatalf("BuildDaqNodeConfigJSON: %v", err)
	}
	client := daqnode.New("DAQ001", host, port, nodeConfigJSON, b, eng)
	client.SetRetryDelay(10 * time.Millisecond)
	daqClients["DAQ001"] = client

	ctx, cancel := context.WithCancel(context.Background())
	go eng.Run(ctx)

	h := &harness{t: t, b: b, sc: sc, eng: eng, client: client, sim: sim, ctx: ctx, cancel: cancel}
	t.Cleanup(func() {
		cancel()
		sim.Close()
	})
	return h
}

// connectClient starts the daqnode.Client's Run() loop, i.e. attempts the
// node's very first connection to daqsim. newHarness calls this immediately;
// newHarnessDeferredConnect callers call it explicitly once they've set up
// whatever pre-connection state they need.
func (h *harness) connectClient() {
	h.t.Helper()
	go h.client.Run(h.ctx)
}

// scanMachineName returns the `machine <name>` declared by a `.sm` source's
// text, without compiling it — used to register an extra fixture machine's
// SM-<NAME>-STATE/-TARGET channels before compilation (see
// statemachine.ScanMachineNames, which does the same thing but from files on
// disk rather than an in-memory source).
func scanMachineName(text string) (string, bool) {
	toks, err := dsl.NewLexer(text).Tokenize()
	if err != nil {
		return "", false
	}
	for i := 0; i+1 < len(toks); i++ {
		if toks[i].Type == dsl.TOK_MACHINE && toks[i+1].Type == dsl.TOK_IDENT {
			return toks[i+1].Value, true
		}
	}
	return "", false
}

// waitFor polls cond until true or timeout, matching the convention already
// used throughout controlnode/daqnode's tests: a generous ceiling, not a
// synchronisation primitive.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	if !cond() {
		t.Fatalf("condition not met within %s", timeout)
	}
}

// waitConnected waits for the daqnode.Client's handshake with daqsim to fully
// complete (config sent, DaqConnected incremented) before the test commands
// anything. Skipping this and commanding a daq_local state before the very
// first connection finishes hits a real edge in daqnode/client.go: connect()
// calls handleReconnectState() unconditionally after every successful
// handshake, including the first ever one, with no way to tell "the node
// just connected for the first time" from "the node reconnected after
// dropping a link that was already up" — see docs/daqsim.md "Protocol
// ambiguities". Production is not exposed to this because main.go starts the
// DAQ clients before any operator has a chance to command a state; a test
// that fires RequestTarget immediately after wiring everything up can easily
// win that race the other way, so it waits here instead.
func (h *harness) waitConnected(timeout time.Duration) {
	h.t.Helper()
	waitFor(h.t, timeout, func() bool { return h.b.DaqConnected.Load() >= 1 })
}

func (h *harness) waitState(machine, want string, timeout time.Duration) {
	h.t.Helper()
	waitFor(h.t, timeout, func() bool {
		s, ok := h.eng.State(machine)
		return ok && s == want
	})
}

// shrinkBurnTiming lowers SEQ-CUTOFF-T (seconds) to the shortest valid burn so
// a RealClock-timed daqsim run of the real firingSequence machine finishes
// quickly. It cannot go below 2s: daq001.sm opens the mains at a hardcoded
// absolute t=2s ("sleep 2 - SEQ-IGN-LEAD" then
// "wait_until T-TIME > SEQ-CUTOFF-T"), which is not itself soft-channel-tunable.
func (h *harness) shrinkBurnTiming(t *testing.T) {
	t.Helper()
	if err := h.sc.Set("SEQ-CUTOFF-T", 2.1); err != nil {
		t.Fatalf("shrink SEQ-CUTOFF-T: %v", err)
	}
}
