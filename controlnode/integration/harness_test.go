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
	"strings"
	"testing"
	"time"
)

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
	sc.RegisterStateMachineChannels(machineNames)
	for k, v := range sc.RefDesMap() {
		refDesMap[k] = v
	}

	knownChannels := make([]string, 0, len(refDesMap))
	for refDes := range refDesMap {
		knownChannels = append(knownChannels, refDes)
	}

	prog, err := statemachine.LoadDir(configDir+"/machines", statemachine.Options{KnownChannels: knownChannels})
	if err != nil {
		t.Fatalf("statemachine.LoadDir: %v", err)
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
	go client.Run(ctx)

	h := &harness{t: t, b: b, sc: sc, eng: eng, client: client, sim: sim, cancel: cancel}
	t.Cleanup(func() {
		cancel()
		sim.Close()
	})
	return h
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

// shrinkBurnTiming lowers SEQ-CUTOFF-T to the shortest valid burn so a
// RealClock-timed daqsim run finishes as quickly as the shipped machine
// allows. It cannot go below ~2000ms: daq001.sm opens the mains at a
// hardcoded absolute t=2000 ("sleep 2000 - SEQ-IGN-LEAD" then
// "sleep SEQ-CUTOFF-T - 2000"), which is not itself soft-channel-tunable, and
// statemachine.TestShippedConfig_CutoffBeforeMainsRefused pins that a cutoff
// at or before 2000 is refused as a negative sleep. So the two RealClock
// tests that need a genuine in-flight window (reconnect, stale completion)
// cost ~2.1s of real wall time each — an intrinsic property of the shipped
// config, not a shortcut taken here.
func (h *harness) shrinkBurnTiming(t *testing.T) {
	t.Helper()
	if err := h.sc.Set("SEQ-CUTOFF-T", 2100); err != nil {
		t.Fatalf("shrink SEQ-CUTOFF-T: %v", err)
	}
}
