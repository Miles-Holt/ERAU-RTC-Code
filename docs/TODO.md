# controlNode TODO

## Open

### documentation
- [ ] **controlNode -> daqNode JSON messages** - no documentation currently exists for what the controlNode sends to the daqNode

### console
- [ ] **Clean up** remove "retrying in 2s" from the console log while waiting for a connection
- [ ] **Future proof** make the re-try for connect a list of nodes that the control node is attempting to connect to. Provides future proof against lots of nodes congesting the console log.

### dataHealth
- [x] **Bad data detection** — server-side range checks (`broker.checkBounds`) emit `bad_data` / `bad_data_snapshot` when a value leaves `[validMin, validMax]`; the browser shows a red LED and an alert-bar alarm. Bounds come from the YAML config.

### configFile
- [ ] **quality of life** - remove the unused sections from the configuration file

### commandability
- [ ] **Autosequence and Aborts** - add a websocket message that sends the array required for auto sequence or aborts. Make a method to edit, upload, and send the config through the webclient. Configs should be YAML based but convert to JSON when going through the webClient, controlNode, and arriving at the daqNode.

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
- [ ] **Pipe colors** — add color support to pipe/connection segments on the P&ID canvas
- [ ] **Objects reference controls, not channels** — P&ID objects should bind to a control's `refDes` (all channels under that valve/control are implicitly included), not individual channels
- [ ] **Rework sensor P&ID object** — current sensor object design is not working well; needs a full rethink

---

### Channel List Tab
- [x] **Bad data detection** — `.dv-led-bad` (red LED) + red value text now driven by `validMin`/`validMax`; server `bad_data` messages also raise an alert-bar alarm.

---

### Graph Tab
- [ ] **data not collected when tab/window isnt focused**
- [ ] **data lines snap at chart boundary** — rather than smoothly entering/exiting the viewable x-range, line segments snap in/out at the chart edges; likely a Chart.js clipping issue with explicit `x.min`/`x.max` bounds
- [x] **Data tooltip position** — tooltip is not rendering next to the user mouse correctly
- [ ] **Y-axis lock** — add feature to lock y-axis min or max and input custom min or max by clicking the min or max value on a specific y-axis

---

### Dev Tab
- [ ] **Browser memory accuracy** — JS heap via `performance.memory` always reads ~10 MB; investigate whether Chrome is clamping the value or whether the read timing is wrong
- [x] **Comms instrumentation** — Dev tab now shows data throughput (bytes/s) and average frame size; useful for gauging broadcast volume before/after tuning. (Measures decoded payload, so it reflects channel-count/state-volume, not on-the-wire compression.)

---

## Cleanup / tech debt (from usability + efficiency pass)

Deferred deliberately during the usability pass — extracted here so they aren't lost:

- [~] **Unify P&ID renderer (partial)** — the byte-identical pure helpers (`svgN`, `pidSvgPt`, `portPos`, `pidFromYaml`, and the valve-symbol geometry helpers) are now shared in `js/pidRender.js`, loaded by both `index.html` and `editor.html`. `PID` (differs: `VALVE_PORT_OFF` 0 vs 40) and `pidToYaml` (editor-only; viewer copy was dead, removed) stay per-file. **Still deferred:** the group builders / `renderPidObj` genuinely diverge (edit mode adds ports + drag), and the editor keeps its own WS/auth stack + separate `editor.html` page (kept intentionally — a standalone editor window is desired). Verify the viewer + editor in a browser after this change.
- [ ] **Extract shared channel-search dropdown** — the regex-search-dropdown is copy-pasted ~4× (graph cell, object sidebar, channel list, in-panel graph). Consolidate into one helper.
- [ ] **Extract stale-timer helper** — `clearTimeout; setTimeout(add 'stale', channelStaleMs)` is repeated across card/pid/dataview builders.
- [ ] **Dead code: `cards.js`** — `buildCard` and all `build*Card` helpers are unreachable (the Data View card tab was replaced). Delete the file, or revive it for a future dashboard/card tab. (`isCmd` already moved to `utils.js`.)
- [ ] **Alert bar sources** — still TODO in spirit: sensor-bounds alerts sourced from an `alertRules.yaml`, and explicit DAQ connect/disconnect alerts. (DAQ `err` messages now surface as alerts; bad-data range alerts are done.)
- [ ] **State machine safe-state sequence** — `config/control/daq001_control.yaml` `safe` state entry sequence is incomplete (`# TODO: add all the other valves`).

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
- [ ] **WebClient behavioral tests** — Node.js + `jsdom`: load `ws.js`/`cards.js`/`pid.js` against a fake DOM, feed synthetic `data`/`config`/`bad_data` messages, assert the UI state updates. Pairs with the Go-side contract test to cover both ends of the wire. *Requires Node.js + npm.*
- [ ] **`-race` in CI** — run `CGO_ENABLED=1 go test -race ./...` on the build server. The broker is goroutine-dense; the race detector needs a C compiler (gcc), absent on the current Windows dev box. *Requires a build server with gcc/cgo.*
- [ ] **Reconnection coverage** — exercise `daqnode.Client.Run()` reconnect loop (RegisterDaq/deregister, `DaqConnected` decrement on drop) and WebClient `scheduleReconnect`, beyond the single-cycle `connect()` smoke test.
- [ ] **`set_layout` / `ack_alert` paths** — cover the file-writing `set_layout` handler and `ack_alert` in `webclient/server.go` (currently untested).
- [ ] **health package** — no tests yet for uptime / loopTime / connection-count publishing.
