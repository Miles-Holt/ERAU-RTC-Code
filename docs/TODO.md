# controlNode TODO

## Firing sequence SM — manual work (owner)

Items on `config/machines/daq001.sm` (`machine firingSequence`) to be implemented
by hand. Each one is a procedure decision, not a code change, which is why they
are not being done for you. Ordered roughly by consequence.

- [ ] **`safe` does not close everything it should.** It zeroes NV-01/NV-02
  (POS/NEG), the vents, fill, bleed, ISO and the mains — but never touches
  **NV-03/NV-04 (press), NV-05/NV-06 (purge), or IG-01 (igniter)**. Two
  consequences: coming back `abort → safe` leaves the **purge valves open**,
  because `abort` opened them and nothing closes them; and `safe` cannot be
  relied on as a "put the stand in a known state" command, since three valves
  and the igniter keep whatever value they had. Decide whether `safe` should
  drive every commandable channel to a defined state.
- [ ] **Comment/refDes mismatch in `safe`.** The block comment reads
  `# FV-02: fuel vent` immediately above `FV-01-CMD = 0`, while further down
  `# FV-02: Fuel Iso` sits above `FV-02-CMD = 0`. One of the two labels is
  wrong; on a P&ID-driven stand a mislabelled vent is worth fixing before it is
  read under pressure.
- [ ] **Confirm mains-close-at-cutoff.** The sequence now closes the mains AT
  `SEQ-CUTOFF-T`. Your earlier `wait_until T-TIME > SEQ-CUTOFF-T - 2.0` would
  have closed them 2 s *before* cutoff — at the defaults that is t=1 s, before
  they open at t=2 s. Confirm the intended cutoff semantics.
- [ ] **Confirm the `postTest` procedure.** It currently closes the press valves
  (NV-03/NV-04) and holds, with no vent, no purge and no auto-transition, so the
  operator decides what happens next. If a burn should vent or purge on its own,
  that belongs here.
- [ ] **T-TIME relative to ignition.** Your own note in the file: T-TIME counts
  up from sequence start, not from ignition. Deciding the zero point affects
  every abort window that uses it.
- [ ] **T-TIME reset races the controller.** The sequence sets `T-TIME = 0.0` as
  its first statement, but the controller (which does `T-TIME = T-TIME + CYCLE_TIME`)
  may tick once between state entry and that assignment, so T-TIME can start at
  one tick rather than zero. Harmless at 10 ms, but worth knowing if the zero
  point becomes ignition-relative.
- [ ] **Orchestrate the press and ignition machines.** The pattern you described:
  `command pressSeq -> engineRunning`, then `wait_until AT-PRESSURE timeout N -> abort`,
  then continue. Needs (a) the subordinate machines defined with their states,
  (b) `AT-PRESSURE` as a computed `.chan` channel, e.g.
  `compute PT-LOX-AVG > SEQ-TARGET-PRESS and PT-FUEL-AVG > SEQ-TARGET-PRESS`.
  The `command` statement and compile-checked state names are DONE (code work):
  `command <machine> -> <state>` is a first-class statement (`controlnode/dsl`,
  `controlnode/statemachine`), and `machine.<M>.state == "…"` string literals are
  validated against M's real state names at compile time. See
  `docs/restructure/dsl_spec.md` and `docs/dsl-guide.md` for the syntax and a
  worked orchestration example. What is left here is still the procedure
  decision: actually writing `pressSeq`/ignition's `.sm` files and wiring this
  sequence's `command`/`wait_until` into them.
- [ ] **Abort on subordinate failure.** Once the press/ignition machines exist,
  the `autoSequence` controller should also abort on their faults, e.g.
  `if machine.pressSeq.state == "fault"` → `transition abort`. Without it the
  firing sequence keeps running against a failed press system.
- [ ] **Put a timeout on every `wait_until` in `autoSequence`.** A bare
  `wait_until` in a sequence that has already opened valves waits forever if the
  condition never arrives; with two machines waiting on each other it deadlocks
  with propellant loaded. `timeout <s> -> abort` is the safety net.

## Restructure follow-ups

Left over after the DSL restructure (Phases 1–5). None of these block operation;
all of them are things a future run should close.

- [ ] **LabVIEW: echo `runId` on `sequence_complete`** — the control node stamps a
  monotonic `runId` into every `state_update` and the correlation logic already
  prefers it (`NotifySequenceCompleteRun`). Until LabVIEW echoes it, completions
  fall back to state+epoch matching, which cannot tell two runs of the same state
  apart. This is the last correlation gap in the protocol. **Now demonstrated
  end to end**: `controlnode/daqsim` (a non-LabVIEW daqNode) echoes `runId` on
  every `sequence_complete`, and `controlnode/integration/TestStaleSequenceCompleteIgnored`
  proves against a real node connection that a stale `runId` is rejected. LabVIEW
  still owes the same echo — daqsim is the reference for what that should look
  like on the wire.
- [ ] **LabVIEW: new `state_update` lifecycle** — `state_update` now means
  "enter this state now", not "here is your cached config". It arrives on state
  entry (not at connect), `exit_sequence` must be run locally the moment an
  `abort_rule` trips, and `state_req` is answered only while a machine is
  actually in a `daq_local` state on that node. The node side needs to be
  reviewed against `docs/websocket-protocol.md` Part 2. `controlnode/daqsim`
  implements this lifecycle correctly (see `docs/daqsim.md`) and can serve as
  a reference implementation while LabVIEW catches up.
- [x] **non-LabVIEW daqNode for CI, builds, and tests** — done.
  `controlnode/daqsim` is a Go daqNode simulator (library + `controlnode/cmd/daqsim`
  standalone binary) that speaks the real protocol: config-driven channel model
  (no hardcoded channel names), timed entry/exit sequences, live `abort_rule`
  scanning, and `runId`-echoing `sequence_complete`. `controlnode/integration`
  wires it against the real broker/softchan/statemachine engine and the real
  `config/machines/daq001.sm` over an actual localhost WebSocket — the first
  tests in this repo that run the protocol against something that executes it,
  rather than a hand-written fake. See `docs/daqsim.md`, including a short list
  of genuine protocol ambiguities found while building it.
- [x] **`operator from X,Y` transition gating** — done. `operator from a, b`
  restricts operator-commanded entry to that state to the listed current
  states; bare `operator` stays commandable from anywhere. Gating is
  operator-input-only — DAQ aborts, sequence completions, and in-`.sm`
  `transition` statements are never gated. Applied to
  `config/machines/daq001.sm` (safe/manualControl/autoSequence/abort). See
  `docs/dsl-guide.md` and `docs/restructure/dsl_spec.md`.
- [x] **Rename `SEQ-BURN-DUR`** — done. It is an **absolute cutoff time** measured
  from sequence start, not a burn duration (the burn is `SEQ-CUTOFF-T - 2000` ms
  long). Renamed to `SEQ-CUTOFF-T` everywhere — `config/channels/softchannels.chan`
  (description now spells out the absolute-time semantics), `config/machines/daq001.sm`
  (arithmetic + abort_rule window + comment block), the persisted value in
  `softChannelValues.yaml` (key migrated, not orphaned), `docs/dsl-guide.md`,
  `docs/restructure/dsl_spec.md`, `docs/websocket-protocol.md` (+ embedded copy),
  the `docs/restructure/demo/` files, and Go test fixtures referencing the name.
- [ ] **daq_local re-entry semantics on reconnect** — a reconnect mid-`daq_local`
  currently fires the abort destination with an alarm (state-uncertain). If the
  node ever gains a "report your current schedule position" message, this could
  become a real resync instead.
- [x] **First connection was indistinguishable from a reconnect** — done.
  `daqnode.Client.connect()` called the state-uncertain/reconnect path
  (`handleReconnectState`) after *every* successful handshake, including a
  node's very first-ever connection. A machine already parked in a
  `daq_local` state when a node connected for the first time would fire the
  abort destination and report a "reconnect" that had never happened.
  `Client` now tracks `hasConnected` and only reconnects take
  `handleReconnectState`; the first-ever handshake takes a distinct
  `handleFirstConnectState` / `statemachine.Engine.NotifyDaqFirstConnect`
  path — same corrective action, clearly distinguishable wording. Fixed
  alongside it: `SendStateUpdate` used to queue an undeliverable
  `state_update` (node not connected) with only a log line, no alarm; it now
  refuses and reports through the broker error path, and `connect()` drains
  any payload left queued from a previous connection before it can be
  forwarded stale. See `docs/websocket-protocol.md` ("Reconnect behaviour"),
  `docs/restructure/dsl_spec.md`, and `docs/daqsim.md` ("Protocol
  ambiguities" #4). Tests: `controlnode/daqnode/engine_test.go`
  (`TestClient_ReconnectWhileRunningIsStateUncertain`) and
  `controlnode/integration/daqsim_e2e_test.go`
  (`TestFirstConnectWhileAlreadyRunningIsDistinctFromReconnect`,
  `TestStateUpdateUndeliverableWhileDisconnected`).
- [ ] **generate syntax highlighting** - for custom code, it is difficult to identify if how the code is scructured and spot errors. it would be useful to have some syntax highlighting for our custom code
- [ ] **calc channels** - enable software calc'd channels rather than just set numbers, allowing for logic and advanced algebra
- [ ] **global finctions** - there are some things in the future that would be very useful to have as global functions, low prio, but please implement these. im envisitioning them defined seperatly, then called within statemachines, please think critically and change as nessisary for the best funcationality

## Open

### documentation
- [x] **controlNode -> daqNode JSON messages** — `docs/websocket-protocol.md` Part 2 documents the whole DAQ link (`config_req`/`config`, `data`, `err`, `cmd`, `state_update` with `runId` + `entry_sequence`/`exit_sequence`/`abort_rules`, `state_req`, `abort_triggered`, `sequence_complete`, reconnect-uncertain behaviour). `docs/dsl-guide.md` is the config-authoring guide, and the running config is served live at `http://<controlnode>:8000/docs`.

### console
- [x] **Clean up** — done. Per-retry "retrying in 2s" lines are gone; only the
  first connect attempt and real state changes (connected/disconnected) log
  individually.
- [x] **Future proof** — done. `daqnode.ConnectAggregator`
  (`controlnode/daqnode/connectlog.go`) tracks the SET of nodes currently
  awaiting connection and logs one summary line ("waiting for connection:
  DAQ001, DAQ003 (2 of 4 nodes, retrying every 2s)") on membership change and
  at a slow periodic cadence, plus a single "all nodes connected" line when the
  last pending node connects. Scales to any number of nodes without console
  spam. See `controlnode/daqnode/connectlog_test.go`.

### dataHealth
- [x] **Bad data detection** — server-side range checks (`broker.checkBounds`) emit `bad_data` / `bad_data_snapshot` when a value leaves `[validMin, validMax]`; the browser shows a red LED and an alert-bar alarm. Bounds come from the YAML config.

### configFile
- [x] **quality of life** — unused config removed: `loggingRateHz` (never parsed) is gone from `system.yaml`, and the dead `config.BuildStateConfigJSON` stub + unused `CtrNodeDef.WSPort` field are gone from `config/yaml.go`. Everything else in `system.yaml` / `controlNode.yaml` / `daqNodes/*.yaml` is read by the parser; the module `slotId` / `ioMode` / `description` fields are parsed but not forwarded to LabVIEW and are kept deliberately as chassis documentation (noted in the `Module` doc comment).

### commandability
- [x] **Autosequence and Aborts** — superseded by the DSL restructure. Autosequences and aborts are now written in `config/machines/*.sm`; a `daq_local` state compiles to the `state_update` payload (`entry_sequence` / `exit_sequence` / `abort_rules`) and is sent on state entry. Thresholds and timings reference operator-settable `.chan` channels and are resolved at send time, which is what the "edit and upload a config" idea was really after. The compiled result is visible at `/docs/machines`.

- [ ] **released for commanability** - add a `release` command to .sm files setting the channel as commandable by the operator. all channels start as unreleased, when [channel].released == True, the channels will be commandable by an operator through the websocket. to set a channel as commandable, the state machine will have something to the effect of "release OV-02" releasing all channels related to the OV-02 valve. all channels will be commandable by the statemachines, this is just a filter on the commands allowed from the webclient, and a visual indicator to the operator about what channels are availble to command by updating the valve icon and similar the the GUI. statemachine target state is always commandable, statemachine current state is a talkback and therefore never commanable, sensor channels are another example of channels that are never commandable

# WebClient TODO

See `CONTEXT.md` for full project/architecture context.

---

## Open

### General
- [x] **make light mode button**
- [X] **make dark mode text lighter**
- [x] **stale data detection** - context: when any data is recieved from a node, ALL data from that node is maked as NOT stale. SCOPE: instead, the stale flag should be per channel depenedent incase data is only being recieved from a new channel rather than the whole daqNode.

### Auth
- [x] **Auth rejects incorrect logins** — server-side PIN auth now validates against `config/userAuth.yaml` on the `/ws/ctrl` socket (`webclient/auth.go`); `auth_request` → `auth_response{approved}`. Command widgets are gated on a successful login.

### Front Panel Tab
- [X] **P&ID background** — load a P&ID image or SVG as the canvas background; support multiple P&ID views selectable per tab (e.g. LOX panel, fuel panel, engine)
- [X] **Redo edit mode entry** — rethink how the user enters edit mode; current UX is not acceptable
- [x] **Pipe colors** — optional `color` property per connection (P&ID YAML `connections[].color`), rendered by both the viewer (`pid.js`) and the standalone editor (`WebClient/js/editor.js`) as an inline stroke override on top of the existing fluid-type CSS classes; absent = unchanged default appearance. The editor's pipe sidebar gets a swatch (reusing the shared themed color-picker popup, now in `pidRender.js`) + a "Default" clear button. Round-trips through `pidToYaml`/`pidFromYaml`; layouts without colors still load unchanged (verified via smoke test against `config/test_panel.yaml`, which has none). The control node writes `set_layout` content through as opaque YAML text (`controlnode/webclient/server.go`), so no Go change was needed.
- [x] **Objects reference controls, not channels** — sensor objects now bind to a control (`controlRefDes`, + optional `channelRefDes` to pick one of several readable channels), matching how valve objects already worked. `obj.refDes` (a bare channel) is kept as the legacy binding form — both resolve through `resolveSensorBinding()` (`WebClient/js/pidRender.js`, shared by viewer + editor) so old layouts keep rendering/editing exactly as before. `pidToYaml`/`pidFromYaml` round-trip either form. The editor's sensor sidebar now offers a control picker (consistent with the valve object's picker) instead of a flat channel list.
- [ ] **Rework sensor P&ID object** — current sensor object design is not working well; needs a full rethink (deliberately not attempted — separate visual redesign, not specified)

---

### Channel List Tab
- [x] **Bad data detection** — `.dv-led-bad` (red LED) + red value text now driven by `validMin`/`validMax`; server `bad_data` messages also raise an alert-bar alarm.

---

### Graph Tab
- [x] **data not collected when tab/window isnt focused** — root cause: the rolling buffer was never actually coupled to rendering (`bufferGraphData()` is called straight from the WebSocket `onmessage` handler in `ws.js`, independent of `requestAnimationFrame`/`setInterval`), so it keeps filling while backgrounded. The user-visible bug was that the chart *redraw* runs on a plain `setInterval` (`_graphInterval` in `app.js`), which the browser throttles once the tab/window loses focus (clamped to ≥1s, then ~1/min after 5 min hidden) — so the chart looked frozen/stale for up to a minute after refocusing, reading as "data wasn't collected". Fixed with a `visibilitychange` listener in `app.js` that forces an immediate `updateAllGraphs()`/`updateAllDataViews()` redraw the instant the page becomes visible again, so the catch-up is instant instead of waiting for the next throttled tick.
- [x] **data lines snap at chart boundary** — `buildChartData()` in `graph.js` rewritten to keep one real datapoint beyond each edge of the rendered slice (instead of hard-clipping/interpolating exactly at the edge), so Chart.js's own chart-area clipping draws the boundary-crossing segment smoothly rather than dropping it. The rolling-buffer window itself (`channelBuffers`/`bufferGraphData`) is unchanged — this only changes what's sliced out for rendering.
- [x] **Data tooltip position** — tooltip is not rendering next to the user mouse correctly
- [x] **Y-axis lock** — clicking a y-axis's min/max overlay label (shown live over each active axis in the chart area) opens the themed popup (matching the existing color-picker popup style) to type a custom bound, which locks that end of that axis for that graph cell; clicking "Auto" restores auto-scaling. Per-axis, per-graph-cell (`cell.axisLocks`), and survives live updates/pan/zoom since it's applied via the Chart.js scale's `min`/`max` options, which persist across `chart.update()` calls. Implemented in `graph.js` (`applyAxisLock`, `setAxisLock`, `updateAxisLockLabels`, `openAxisLockPopup`).
- **data tool top line** add a vertical line and data point on the channel data points that are getting displayed on the tooltip

---

### Dev Tab
- [ ] **Browser memory accuracy** — JS heap via `performance.memory` always reads ~10 MB; investigate whether Chrome is clamping the value or whether the read timing is wrong
- [x] **Comms instrumentation** — Dev tab now shows data throughput (bytes/s) and average frame size; useful for gauging broadcast volume before/after tuning. (Measures decoded payload, so it reflects channel-count/state-volume, not on-the-wire compression.)

---

## Cleanup / tech debt (from usability + efficiency pass)

Deferred deliberately during the usability pass — extracted here so they aren't lost:

- [~] **Unify P&ID renderer (partial)** — the byte-identical pure helpers (`svgN`, `pidSvgPt`, `portPos`, `pidFromYaml`, and the valve-symbol geometry helpers) are now shared in `js/pidRender.js`, loaded by both `index.html` and `editor.html`. `PID` (differs: `VALVE_PORT_OFF` 0 vs 40) and `pidToYaml` (editor-only; viewer copy was dead, removed) stay per-file. **Still deferred:** the group builders / `renderPidObj` genuinely diverge (edit mode adds ports + drag), and the editor keeps its own WS/auth stack + separate `editor.html` page (kept intentionally — a standalone editor window is desired). Verify the viewer + editor in a browser after this change.
- [x] **Extract shared channel-search dropdown** — `createChannelSearchDropdown()` (`WebClient/js/utils.js`) consolidates the graph cell (`graph.js`), object sidebar (`objectSidebar.js`), channel list (`dataview.js`), and in-panel P&ID graph (`pid.js`, `showLeftSidebar`) copies. Differences (already-selected set, pick action, dropdown position above/below, whether the input gets `.input-error` styling) are parameters, not flattened. The editor's own graph-object channel picker (`editor.js`, string-built, inline-positioned, no body-append) is structurally different and standalone (editor.html has no `configControls`/`utils.js`) — left as-is.
- [x] **Extract stale-timer helper** — `makeStaleTimer(ms, onStale)` (`WebClient/js/pidRender.js`, shared by both `index.html` and `editor.html`) replaces the repeated `clearTimeout; setTimeout(markStale, ms)` idiom in `pid.js` (sensor value, valve feedback ring, daqControl connection), `dataview.js` (channel-list LED), and `editor.js` (right-sidebar live value).
- [x] **Dead code: `cards.js`** — verified unreferenced (grepped every `WebClient/js/*.js`, `index.html`, `editor.html` for `buildCard`/`build*Card`; only cards.js itself matched) and deleted, along with its `<script>` tag in `index.html`. Rebuilt with `build.bat` and smoke-tested (`GET /` → 200, no `cards.js` reference in served HTML).
- [x] **Alert bar sources** — done by Phase 4. The control node is the single alert source: rules live in `config/alerts/*.alert` (DSL, not `alertRules.yaml`), and the `every_daqnode` template raises disconnect / reconnect / bad_data / stale per node. The browser only renders `alert` / `alert_snapshot` / `alert_acked`. Rendered at `/docs/alerts`.
- [x] **State machine safe-state sequence** — the machine moved to `config/machines/daq001.sm`; `state safe` closes both press valves (`NV-01`, `NV-02` POS/NEG), waits 1 s, then opens the vents. Verify against the current P&ID when the valve list changes — `/docs/machines` shows the compiled sequence.

---

## Done
- [x] **complete restructure** — regex search bar adds individual channel rows; each row shows a status LED, refDes + description (left), 15 s sparkline (center), and value readout or numeric command input (right)
- [x] **Offline Chart.js** — `chart.umd.min.js` bundled locally in `WebClient/js/`; no CDN dependency
- [x] **Fix graph grid** — canvas height now renders correctly; `maintainAspectRatio: false` + explicit flex constraints on `.graph-canvas` and `.graph-cell`
- [x] **Channel roles in XML** — added explicit `<role>` nodes to all `<channel>` elements in `nodeConfigs_0.0.2.xml`; cmd-bool assigned to all command channels; sensor assigned to all read-only channels
- [x] **Update channel roles in app.js** — replaced `role === 'cmd'` checks with `isCmd(ch)` helper covering `cmd-bool`, `cmd-pct`, `cmd-float`; widget type driven from channel role instead of control subType
- [x] **Tab system** — VS Code-style tab bar implemented; `+` adds Front Panel tab; right-click to change type (with clickable shortcuts in boot overlay); double-click to rename; ✕ to close; first tab of each type has no number suffix
- [x] **Tab persistence removed** — localStorage save/restore deleted; every page load opens a clean Front Panel tab
- [x] **Login / access control** — operator name prompt (header button); command widgets disabled until name is set; `user` field attached to every outgoing `cmd` message; unauthenticated users can view live data but all command controls are disabled
- [x] **Front Panel tab** — interactive P&ID editor and viewer built; SVG canvas with 20 px grid snap; drag-and-drop Sensor and Node objects from left sidebar; orthogonal auto-routed pipe connections; right sidebar for refDes/units config; Edit/View mode toggle; live channel data binding in View mode; Save YAML download; layouts sent from control node via `pid_layout` WebSocket message
- [x] **Data View tab** — commandable controls rendered as cards (top section); sensor-only controls rendered as a live-updating table with refDes / description / value / units (bottom section)
- [x] **Graph tab** — adjustable grid (1–4 rows × 1–8 cols); per-cell regex channel search with body-appended dropdown (appears above search bar); Chart.js line charts; 15-minute rolling buffer per channel; left-side panel with channel list; up to 6 independent Y-axes (left-click badge to increment, right-click to decrement); custom themed color picker popup; scroll-to-zoom (30s–20min, anchored to cursor, smooth exponential); live-follow auto-scroll with 5s snap-back tolerance; relative time x-axis; proximity tooltip (28px threshold)
- [x] **Graph resume auto-scroll threshold** — snap back to live triggers when view is within 5% of the right edge rather than a fixed number of seconds
- [x] **Console tab** — live log of all WS messages; direction toggles (← in / → out), type toggles (data / config / cmd / other), free-text and regex filter, configurable buffer limit, clear button
- [x] **Dev tab** — WS stats (endpoint, state, uptime, message count, rate, missed cycles) + browser memory (Chrome only); Force reconnect button (available in Dev Mode); all stats verified live in `refreshDevTabs()`

---

# Testing TODO

## Done (Go smoke tests — `cd controlnode && go test ./...`)
- [x] **config** — parses the real `config/` dir; validates browser/DAQ JSON builders, refDes map, `parseOptFloat`
- [x] **broker** — data fan-out, cmd routing, unknown-refDes drop, bad-data transition + snapshot, restart-command exit hook (injectable `os.Exit`), slow-subscriber frame-drop
- [x] **softchan** — load/defaults, bounds/read-only/unknown `Set` guards, disk persistence, config JSON
- [x] **webclient** — auth matrix, `/ws/data` config handshake, `/ws/ctrl` auth→cmd (and unauthorized reject), static file serving, embedded-FS serving (production path)
- [x] **daqnode** — `config_req` handshake + data bridging against a fake DAQ WS server; `SYS-TARGET-STATE` interception through `writeLoop`
- [x] **statemachine** — transitions (operator_request / operator_abort / abort_triggered / sequence_complete), pending vs current, `{{VAR}}` resolution, abort-rule parsing, real-config target validation
- [x] **Go↔JS protocol contract** — `webclient/protocol_test.go` asserts every message the browser parses (`ws.js`) is emitted by Go with the exact fields the JS reads; fails on field-name drift
- [x] **Bug found + fixed** — `statemachine.RequestTransition` rejected valid transitions whose `exit_type` was omitted (the documented default). Core operator transitions (`safe→manualControl`, etc.) were affected. Fixed with a `found` flag.

## Open (deferred — needs dedicated dev tooling / build server)
- [ ] **WebClient JS syntax gate** — run `node --check` on each `WebClient/js/*.js` in CI so a broken JS file fails the build. *Requires Node.js installed (not currently available on the dev machine).*
- [ ] **WebClient behavioral tests** — Node.js + `jsdom`: load `ws.js`/`pid.js` against a fake DOM, feed synthetic `data`/`config`/`bad_data` messages, assert the UI state updates. Pairs with the Go-side contract test to cover both ends of the wire. *Requires Node.js + npm.*
- [x] **`-race` in CI** — gcc is present on this box now; `CGO_ENABLED=1 go test -race -mod=vendor -count=2 ./daqnode/... ./webclient/... ./health/... ./alerts/... ./broker/...` is clean across two runs (still worth wiring into an actual CI job on a build server, but the local gap is closed).
- [x] **Reconnection coverage** — `daqnode.Client.Run()` now takes a `context.Context` (was previously an infinite loop with no cancellation hook) and an injectable per-client retry delay (`SetRetryDelay`); `daqnode/client_reconnect_test.go` drives real reconnect cycles against a fake WS server that drops the connection after every handshake, asserting `DaqConnected` never exceeds 1 and settles to 0, the `ConnectAggregator` sees the expected pending/connected transitions across cycles, a node that never comes up keeps retrying without leaking goroutines, and ctx cancellation stops the loop promptly. WebClient `scheduleReconnect` is still untested (JS, needs Node — see above).
- [x] **`set_layout` / `ack_alert` paths** — `webclient/set_layout_test.go` and `webclient/ack_alert_test.go` cover both handlers against a temp directory (never `config/`): successful save writes the file and republishes `pid_layout` with name/filename/content intact plus a notice alert; a filename outside the server's allowed `layoutPaths` (including path-traversal-style names) is rejected with nothing written; unauthenticated attempts are rejected for both `set_layout` and `ack_alert`; `ack_alert` marks the registry record acked and broadcasts `alert_acked`; an unknown id is a harmless no-op; a latching-style alert (raised, never independently cleared) stays active until acked. No production path-handling hole was found — `layoutPaths` is a `filename → absolute path` allowlist map built from `filepath.Base` at startup, so any string that isn't an exact known basename simply misses the map lookup; traversal segments can't reach `os.WriteFile` at all.
- [x] **health package** — `health/health_test.go` covers all four published metrics landing under their configured refDes, uptime advancing with an injected clock (`Publisher.SetClock`/`SetStartTime`, no sleeping), connection counts tracking the broker's live `DaqConnected`/`WcConnected`, the command-refDes keepalive, and the publish loop stopping on `ctx` cancellation (`Publisher.Run` now takes a `context.Context`).
