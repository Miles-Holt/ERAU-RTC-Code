package main

import (
	"context"
	"controlnode/alerts"
	"controlnode/broker"
	"controlnode/config"
	"controlnode/daqnode"
	"controlnode/dsl"
	"controlnode/health"
	"controlnode/softchan"
	"controlnode/statemachine"
	"controlnode/webclient"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	configDir := flag.String("config-dir", "../config", "path to config directory")
	webRoot := flag.String("webroot", "", "directory to serve as web client UI (empty = use embedded)")
	flag.Parse()

	// Strip the "static/" prefix from the embedded FS so index.html is at the root.
	embeddedSub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatalf("embedded FS sub: %v", err)
	}

	// ── Parse YAML config ─────────────────────────────────────────────────
	cfg, err := config.ParseDir(*configDir)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	log.Printf("config loaded: broadcast %d Hz, WS port %d",
		cfg.Network.BroadcastRateHz, cfg.Network.WebSocketPort)

	// ── Build refDes → DAQ node map for command routing ───────────────────
	refDesMap := config.BuildRefDesMap(cfg)

	// ── Collect restart command refDes values ─────────────────────────────
	var restartRefDes []string
	var allCtrCmdRefDes []string
	for _, cmd := range cfg.CtrNode.Health.Commands {
		rd := strings.TrimSpace(cmd.RefDes)
		if strings.EqualFold(strings.TrimSpace(cmd.Role), "cmd-bool") {
			allCtrCmdRefDes = append(allCtrCmdRefDes, rd)
			// Convention: any CTR command with "restart" in refDes triggers exit.
			if strings.Contains(strings.ToLower(rd), "restart") {
				restartRefDes = append(restartRefDes, rd)
			}
		}
	}

	// ── Build web client config JSON (sent to browsers on connect) ────────
	wcConfigJSON, err := config.BuildWebClientConfigJSON(cfg)
	if err != nil {
		log.Fatalf("build web client config JSON: %v", err)
	}

	// ── Software channels (load before broker so refDesMap is complete) ─────
	// A malformed .chan file is fatal: continuing without the store leaves every
	// downstream user (engine reader/writer, state channels) silently degraded.
	sc, err := softchan.LoadFromDir(
		filepath.Join(*configDir, "channels"),
		filepath.Join(*configDir, "softChannelValues.yaml"),
	)
	if err != nil {
		log.Fatalf("softchan: %v", err)
	}

	// ── Load state machines from config/machines/*.sm files ────────────────
	machinesDir := filepath.Join(*configDir, "machines")

	// Register the auto-generated SM-<NAME>-STATE / SM-<NAME>-TARGET channels
	// BEFORE building knownChannels, so a machine may reference them.  The names
	// come from a cheap pre-scan because compiling needs the channel set.
	machinesDirExists := true
	if _, statErr := os.Stat(machinesDir); os.IsNotExist(statErr) {
		machinesDirExists = false
		log.Printf("statemachine: %s does not exist — no machines will run", machinesDir)
	}

	var machineNames []string
	if machinesDirExists {
		machineNames, err = statemachine.ScanMachineNames(machinesDir)
		if err != nil {
			log.Fatalf("scan state machines in %s: %v", machinesDir, err)
		}
	}
	sc.RegisterStateMachineChannels(machineNames)
	// CYCLE_TIME (tick period in seconds) must exist in the known-channel set
	// before machines compile: daq001.sm's controller reads it every tick.
	sc.RegisterCycleTimeChannel(cfg.Network.EngineTickRateHz)

	for k, v := range sc.RefDesMap() {
		refDesMap[k] = v
	}
	softchanConfigJSON := sc.ConfigJSON()

	// Build known channels list: all hardware refDes + all soft channels
	// (soft channels are already merged into refDesMap above).
	knownChannels := make([]string, 0, len(refDesMap))
	for refDes := range refDesMap {
		knownChannels = append(knownChannels, refDes)
	}

	var programOrNil *statemachine.Program
	if machinesDirExists {
		smFiles, ferr := statemachine.SMFiles(machinesDir)
		if ferr != nil {
			log.Fatalf("state machines: %v", ferr)
		}
		prog, smErr := statemachine.LoadDir(machinesDir,
			statemachine.Options{KnownChannels: knownChannels})
		if smErr != nil {
			log.Fatalf("load state machines: %v", smErr)
		}
		programOrNil = prog
		switch {
		case len(smFiles) == 0:
			// An empty machines/ directory is a legitimate configuration.
			log.Printf("statemachine: no .sm files in %s — no machines will run", machinesDir)
		case prog == nil || len(prog.Machines) == 0:
			// Files were present but produced nothing: never fail silently.
			log.Fatalf("statemachine: %d .sm file(s) in %s but no machines were loaded",
				len(smFiles), machinesDir)
		default:
			log.Printf("statemachine: loaded %d machine(s) from %d file(s)",
				len(prog.Machines), len(smFiles))
		}
	}

	// ── Build state machine config (sent to browsers) ────────
	var stateConfigJSON []byte
	// Will be built after the engine is created (if program is loaded)
	// ── Build channel bounds for bad-data detection ───────────────────────
	cfgBounds := config.BuildChannelBoundsMap(cfg)
	brokerBounds := make(map[string]broker.ChannelBounds, len(cfgBounds))
	for k, v := range cfgBounds {
		brokerBounds[k] = broker.ChannelBounds{Min: v.Min, Max: v.Max}
	}

	// ── Create broker ─────────────────────────────────────────────────────
	b := broker.New(refDesMap, restartRefDes, brokerBounds)
	go b.Run(cfg.Network.BroadcastRateHz)

	// ── Start software channel publisher/handler ───────────────────────────
	go sc.Run(b, cfg.Network.BroadcastRateHz)

	// Persist soft-channel values off the hot path.
	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()
	go sc.RunPersister(rootCtx, 500*time.Millisecond)

	// ── Alerts ────────────────────────────────────────────────────────────
	// The control node is the single source of alerts: rule alerts from
	// config/alerts/*.alert, per-daqNode template alerts (disconnect, reconnect,
	// bad data, stale) and the webclient's own notices all land in ONE registry,
	// which the webclient server publishes.  The browser only renders.
	alertsDir := filepath.Join(*configDir, "alerts")
	alertFiles, err := alerts.AlertFiles(alertsDir)
	if err != nil {
		log.Fatalf("alerts: %v", err)
	}
	machineStates := make(map[string][]string)
	if programOrNil != nil {
		for _, m := range programOrNil.Machines {
			names := make([]string, len(m.States))
			for i, st := range m.States {
				names[i] = st.Name
			}
			machineStates[m.Name] = names
		}
	}
	alertCfg, err := alerts.LoadDir(alertsDir, alerts.Options{
		KnownChannels: knownChannels,
		MachineNames:  machineNames,
		MachineStates: machineStates,
	})
	if err != nil {
		// An unknown channel reference or a malformed rule is fatal, exactly
		// like a bad .sm or .chan file: a mis-typed alert is a silent alert.
		log.Fatalf("alerts: %v", err)
	}

	var daqNodeNames []string
	for i := range cfg.DaqNodes.Nodes {
		if cfg.DaqNodes.Nodes[i].Enabled {
			daqNodeNames = append(daqNodeNames, cfg.DaqNodes.Nodes[i].RefDes)
		}
	}

	// channelSpace resolves channel values for both the state machine engine and
	// alert message interpolation outside a tick.
	channelSpace := &engineReader{b: b, sc: sc}

	// Every SENSOR channel with a validMin/validMax band gets an auto-generated,
	// latching out-of-range alarm — no .alert file involved.  Command channels
	// (role cmd-*) are excluded: a setpoint the operator typed is not a reading,
	// and a band on it would describe the command, not the plant.
	var sensorAlerts []alerts.SensorChannel
	for _, ctrl := range cfg.ControlList.Controls {
		if !ctrl.Enabled {
			continue
		}
		for _, ch := range ctrl.Channels {
			if ch.Role != "" {
				continue // command channel, not a sensor
			}
			bounds, ok := cfgBounds[ch.RefDes]
			if !ok {
				continue // no validMin/validMax configured — no alert at all
			}
			sensorAlerts = append(sensorAlerts, alerts.SensorChannel{
				RefDes: ch.RefDes, Min: bounds.Min, Max: bounds.Max,
			})
		}
	}

	alertRegistry := alerts.NewRegistry()
	alertEngine, err := alerts.NewEngine(alerts.EngineConfig{
		Config:        alertCfg,
		Registry:      alertRegistry,
		Nodes:         daqNodeNames,
		Values:        channelSpace,
		Sensors:       sensorAlerts,
		KnownChannels: knownChannels,
		OnError: func(e error) {
			log.Printf("alerts: %v", e)
		},
	})
	if err != nil {
		log.Fatalf("alerts: %v", err)
	}
	// DAQ connect/disconnect/data and bad-data transitions all reach the alert
	// engine through the broker, which already tracks them.
	b.SetEventSink(alertEngine)
	log.Printf("alerts: %d rule(s) from %d file(s), template=%v, %d daqNode(s), stale timeout %d ms",
		len(alertCfg.Rules), len(alertFiles), alertCfg.Template != nil,
		len(daqNodeNames), alertEngine.StaleMs())
	sensorIDs := alertEngine.SensorAlerts()
	log.Printf("alerts: %d auto-generated sensor out-of-range alarm(s) from validMin/validMax bounds",
		len(sensorIDs))
	if len(sensorIDs) > 0 {
		log.Printf("alerts: sensor bounds alarms: %s", strings.Join(sensorIDs, " "))
	}

	// ── Create the state machine engine ────────────────────────────────────
	// daqClients is filled in below and read from the engine's OnDaqStateEnter
	// hook; the engine loop is only started once it is complete.
	daqClients := make(map[string]*daqnode.Client)

	var engine webclient.StateMachineRequester // set if state machines are loaded
	var daqEngine daqnode.EngineController     // same engine, for daqnode clients
	var eng *statemachine.Engine
	if programOrNil != nil && len(programOrNil.Machines) > 0 {
		reader := channelSpace

		stateChangeCallback := func(machine, state string) {
			// Publish SM-<NAME>-STATE value (numeric index)
			stateIndex := 0
			if m, ok := programOrNil.Machine(machine); ok {
				if st, ok := m.State(state); ok {
					stateIndex = st.Index
				}
			}
			sc.SetInternal("SM-"+machine+"-STATE", float64(stateIndex))

			// Broadcast state_change (the authoritative state NAME) for browsers.
			// Built by webclient so the browser contract test asserts these bytes.
			b.Publish(webclient.StateChangeJSON(machine, state))
		}

		writer := &engineWriter{b: b, sc: sc, prog: programOrNil}

		eng, err = statemachine.New(statemachine.Config{
			Program:       programOrNil,
			Reader:        reader,
			Writer:        writer,
			TickHz:        cfg.Network.EngineTickRateHz,
			OnStateChange: stateChangeCallback,
			// Used only to phrase runtime faults: a configured channel with no
			// value yet (node not connected) reads differently from a typo.
			KnownChannels: knownChannels,

			// Computed channels are refreshed once per tick, before controllers.
			PreTick: func() {
				if err := sc.Recompute(b); err != nil {
					log.Printf("softchan: recompute: %v", err)
				}
			},

			// Alert rules evaluate last, on the same per-tick snapshot the
			// controllers ran against: computed channels → controllers → alerts.
			PostTick: alertEngine.Tick,

			// A daq_local state entry sends the freshly-resolved payload now.
			OnDaqStateEnter: func(machine, node string, p *statemachine.DaqStateUpdate) {
				client, ok := daqClients[node]
				if !ok {
					log.Printf("statemachine: %s entered daq_local state %q but node %s has no client",
						machine, p.State, node)
					b.PublishErr(broker.ErrEvent{
						DaqRefDes: node,
						Err: fmt.Sprintf("machine %s entered daq_local state %q but node %s is not configured",
							machine, p.State, node),
					})
					return
				}
				client.SendStateUpdate(p)
			},

			// Engine-side faults (including daq_local resolution failures and
			// reconnect state-uncertainty) reach the operator as alarms.
			OnError: func(machine string, e error) {
				log.Printf("statemachine: %s: %v", machine, e)
				b.PublishErr(broker.ErrEvent{DaqRefDes: "CTR", Err: fmt.Sprintf("machine %s: %v", machine, e)})
			},
		})
		if err != nil {
			log.Fatalf("create engine: %v", err)
		}

		writer.engine = eng
		engine = eng    // save for webclient server
		daqEngine = eng // save for daqnode clients

		// ── Build state_config message from machines ──────
		stateConfigJSON = webclient.BuildStateConfigJSON(programOrNil)
	}

	// ── Health publisher ──────────────────────────────────────────────────
	sensorRefDes := buildHealthSensorMap(cfg)
	if len(sensorRefDes) > 0 {
		hp := health.New(b, sensorRefDes, allCtrCmdRefDes)
		go hp.Run(rootCtx, cfg.Network.BroadcastRateHz)
	}

	// ── DAQ node clients (one goroutine per enabled DAQ node) ─────────────
	for i := range cfg.DaqNodes.Nodes {
		node := &cfg.DaqNodes.Nodes[i]
		if !node.Enabled {
			log.Printf("daqnode %s: disabled, skipping", node.RefDes)
			continue
		}
		if node.WSPort == 0 {
			log.Printf("daqnode %s: no wsPort configured, skipping", node.RefDes)
			continue
		}
		nodeConfigJSON, err := config.BuildDaqNodeConfigJSON(cfg, node.RefDes, cfg.Network.BroadcastRateHz)
		if err != nil {
			log.Fatalf("build DAQ node config JSON for %s: %v", node.RefDes, err)
		}

		client := daqnode.New(node.RefDes, node.IP, node.WSPort, nodeConfigJSON, b, daqEngine)
		daqClients[node.RefDes] = client
	}

	// ── Start the engine, then the DAQ clients ────────────────────────────
	// The engine must be running before a node connects (a reconnect while a
	// machine is mid-flight has to reach the engine), and daqClients must be
	// complete before the engine can enter a daq_local state.
	if eng != nil {
		go eng.Run(rootCtx)
		log.Printf("statemachine: engine started at %d Hz", cfg.Network.EngineTickRateHz)
	} else {
		// No machines: nothing drives PostTick, so the alert engine gets its own
		// loop.  Software channels still need recomputing for rules to read.
		go func() {
			stop := make(chan struct{})
			go func() { <-rootCtx.Done(); close(stop) }()
			alertEngine.Run(stop, cfg.Network.EngineTickRateHz, func() dsl.ChannelSpace {
				if err := sc.Recompute(b); err != nil {
					log.Printf("softchan: recompute: %v", err)
				}
				return channelSpace
			})
		}()
		log.Printf("alerts: no state machines — alert engine ticking standalone at %d Hz",
			cfg.Network.EngineTickRateHz)
	}
	// Shared aggregator: while any node is still trying to connect, log ONE
	// periodic summary line ("waiting for connection: A, B (2 of 4 nodes, ...")
	// instead of every client logging its own "retrying in 2s" every attempt.
	connAgg := daqnode.NewConnectAggregator(len(daqClients), 30*time.Second, nil, nil)
	go connAgg.Run(rootCtx.Done())
	for refDes, client := range daqClients {
		client.SetAggregator(connAgg)
		go client.Run(rootCtx)
		log.Printf("daqnode %s: client started", refDes)
	}

	// ── Load front panel layout files ─────────────────────────────────────
	panelLayoutsPath := filepath.Join(*configDir, "panelLayouts.yaml")
	panelCfg, err := config.LoadPanelLayouts(panelLayoutsPath)
	if err != nil {
		log.Fatalf("panelLayouts.yaml: %v", err)
	}
	panelMessages := loadPanelMessages(panelCfg, *configDir)

	layoutPaths := make(map[string]string)
	for _, p := range panelCfg.Panels {
		if p.Enabled {
			layoutPaths[filepath.Base(p.File)] = filepath.Join(*configDir, p.File)
		}
	}

	// ── Load user auth config ─────────────────────────────────────────────
	authCfg, err := webclient.LoadUserAuth(filepath.Join(*configDir, "userAuth.yaml"))
	if err != nil {
		log.Printf("webclient: userAuth.yaml not loaded, auth disabled: %v", err)
		authCfg = nil
	}

	// ── Web client WebSocket server (blocks forever) ──────────────────────
	srv := webclient.New(cfg.Network.WebSocketPort, wcConfigJSON, softchanConfigJSON, stateConfigJSON, panelMessages, b, *webRoot, embeddedSub, authCfg, layoutPaths, engine, alertRegistry)

	// ── /docs — self-documentation from the COMPILED config ───────────────
	// The same structures the engine runs against are handed to the docs
	// renderer, so the pages can never drift from the loaded configuration.
	srv.SetDocs(&webclient.DocsInput{
		System:       cfg,
		Program:      programOrNil,
		Soft:         sc,
		Alerts:       alertCfg,
		AlertStaleMs: alertEngine.StaleMs(),
		// Prefer the working-copy markdown when running from a checkout; the
		// build-time embedded copy covers a deployed exe with no docs/ next to it.
		ProtocolPath: filepath.Join(*configDir, "..", "docs", "websocket-protocol.md"),
	})
	log.Printf("webclient: configuration reference at http://localhost:%d/docs", cfg.Network.WebSocketPort)

	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("webclient server: %v", err)
	}
}

// loadPanelMessages reads each enabled front-panel YAML from disk and builds
// the pid_layout JSON payloads that are sent to browsers on connect.
// File paths in panelLayouts.yaml are resolved relative to configDir.
func loadPanelMessages(cfg *config.PanelLayoutsConfig, configDir string) [][]byte {
	var msgs [][]byte
	for _, p := range cfg.Panels {
		if !p.Enabled {
			log.Printf("front panel %q: disabled, skipping", p.Name)
			continue
		}
		absPath := filepath.Join(configDir, p.File)
		content, err := os.ReadFile(absPath)
		if err != nil {
			log.Printf("front panel %q: read %s: %v", p.Name, absPath, err)
			continue
		}
		payload := webclient.PidLayoutJSON(p.Name, filepath.Base(p.File), string(content))
		if payload == nil {
			log.Printf("front panel %q: marshal failed", p.Name)
			continue
		}
		msgs = append(msgs, payload)
		log.Printf("front panel loaded: %q (%s)", p.Name, p.File)
	}
	return msgs
}

// The state_config message is built by webclient.BuildStateConfigJSON so that
// the browser contract test (webclient/protocol_test.go) exercises the exact
// bytes the browser receives.

// buildHealthSensorMap maps well-known metric keys ("uptime", "loopTime",
// "daqConnected", "wcConnected") to their refDes values from controlNode.yaml
// by matching keywords in the refDes string.
func buildHealthSensorMap(cfg *config.SystemConfig) map[string]string {
	m := make(map[string]string)
	for _, s := range cfg.CtrNode.Health.Sensors {
		rd := strings.TrimSpace(s.RefDes)
		lower := strings.ToLower(rd)
		switch {
		case strings.Contains(lower, "uptime"):
			m["uptime"] = rd
		case strings.Contains(lower, "looptime") || strings.Contains(lower, "loop-time") || strings.Contains(lower, "loop_time"):
			m["loopTime"] = rd
		case strings.Contains(lower, "daqconnected") || strings.Contains(lower, "daq-connected") || strings.Contains(lower, "daq_connected"):
			m["daqConnected"] = rd
		case strings.Contains(lower, "wcconnected") || strings.Contains(lower, "wc-connected") || strings.Contains(lower, "wc_connected"):
			m["wcConnected"] = rd
		}
	}
	return m
}

// ── State machine engine adapters ──────────────────────────────────────────

// engineReader implements statemachine.Reader, resolving channel values from
// the broker (hardware) and softchan (software) stores.
type engineReader struct {
	b  *broker.Broker
	sc *softchan.Store
}

func (r *engineReader) Get(name string) (dsl.Value, bool) {
	// Try hardware channels first
	if v, ok := r.b.CurrentValue(name); ok {
		return dsl.NewFloat(v), true
	}

	// Try software channels
	if r.sc != nil {
		if v, ok := r.sc.Get(name); ok {
			return dsl.NewFloat(v), true
		}
	}

	return dsl.Value{}, false
}

// FillSnapshot implements statemachine.SnapshotReader: the engine takes one
// consistent view of the whole channel space per tick instead of a fresh
// per-identifier lookup (which both copied the full broker map every time and
// allowed two comparisons in one controller to see different values).
func (r *engineReader) FillSnapshot(dst map[string]dsl.Value) {
	clear(dst)
	r.b.EachValue(func(name string, v float64) {
		dst[name] = dsl.NewFloat(v)
	})
	if r.sc != nil {
		r.sc.EachValue(func(name string, v float64) {
			dst[name] = dsl.NewFloat(v)
		})
	}
}

// engineWriter implements statemachine.Writer, routing assignments to the
// broker (hardware) and softchan (software) stores.  SM-<NAME>-TARGET writes are
// machine-to-machine coordination and go to engine.RequestTarget.
type engineWriter struct {
	b      *broker.Broker
	sc     *softchan.Store
	prog   *statemachine.Program
	engine *statemachine.Engine
}

func (w *engineWriter) Set(refDes string, value float64) error {
	// SM-<NAME>-TARGET: one machine commanding another.  The value is a state
	// index (the same encoding SM-<NAME>-STATE publishes).
	if strings.HasPrefix(refDes, "SM-") && strings.HasSuffix(refDes, "-TARGET") {
		return w.requestTarget(refDes, value)
	}

	// Route to softchan if it's a soft channel
	if w.sc != nil {
		if _, ok := w.sc.Get(refDes); ok {
			return w.sc.Set(refDes, value)
		}
	}

	// Otherwise route to broker for hardware channels
	w.b.SendCmd(broker.CmdMsg{
		Type:   "cmd",
		RefDes: refDes,
		Value:  value,
		User:   "statemachine",
	})
	return nil
}

// requestTarget turns an SM-<NAME>-TARGET write into an engine target request.
func (w *engineWriter) requestTarget(refDes string, value float64) error {
	if w.engine == nil || w.prog == nil {
		return fmt.Errorf("%s: no state machine engine is running", refDes)
	}
	machine := refDes[len("SM-") : len(refDes)-len("-TARGET")]
	m, ok := w.prog.Machine(machine)
	if !ok {
		return fmt.Errorf("%s: unknown machine %q", refDes, machine)
	}
	idx := int(value)
	if float64(idx) != value || idx < 0 || idx >= len(m.States) {
		return fmt.Errorf("%s: %v is not a valid state index for machine %q", refDes, value, machine)
	}
	if err := w.engine.RequestTarget(machine, m.States[idx].Name); err != nil {
		return fmt.Errorf("%s: %w", refDes, err)
	}
	if w.sc != nil {
		w.sc.SetInternal(refDes, value)
	}
	return nil
}
