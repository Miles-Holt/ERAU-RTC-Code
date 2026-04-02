# controlNode TODO

## Open

### documentation
- [ ] **controlNode -> daqNode JSON messages** - no documentation currently exists for what the controlNode sends to the daqNode

### console
- [ ] **Clean up** remove "retrying in 2s" from the console log while waiting for a connection
- [ ] **Future proof** make the re-try for connect a list of nodes that the control node is attempting to connect to. Provides future proof against lots of nodes congesting the console log.

### dataHealth
- [ ] **Bad data detection** — define "bad data" criteria (out-of-range, sensor fault flag) based on the channel definition in the config file. Send message to websocket when data becomes "bad" (e.g. a 4-20mA sensor reading 1mA). May require new fields in the config file.

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
- [ ] **Auth rejects incorrect logins** — login is not currently validated against the auth YAML; incorrect credentials are accepted without rejection

### Front Panel Tab
- [X] **P&ID background** — load a P&ID image or SVG as the canvas background; support multiple P&ID views selectable per tab (e.g. LOX panel, fuel panel, engine)
- [X] **Redo edit mode entry** — rethink how the user enters edit mode; current UX is not acceptable
- [ ] **Pipe colors** — add color support to pipe/connection segments on the P&ID canvas
- [ ] **Objects reference controls, not channels** — P&ID objects should bind to a control's `refDes` (all channels under that valve/control are implicitly included), not individual channels
- [ ] **Rework sensor P&ID object** — current sensor object design is not working well; needs a full rethink

---

### Channel List Tab
- [ ] **Bad data detection** — define "bad data" criteria (out-of-range, sensor fault flag, etc.) and wire up `.dv-led-bad` (red LED) state on channel rows

---

### Graph Tab
- [ ] **data not collected when tab/window isnt focused**
- [ ] **data lines snap at chart boundary** — rather than smoothly entering/exiting the viewable x-range, line segments snap in/out at the chart edges; likely a Chart.js clipping issue with explicit `x.min`/`x.max` bounds
- [x] **Data tooltip position** — tooltip is not rendering next to the user mouse correctly
- [ ] **Y-axis lock** — add feature to lock y-axis min or max and input custom min or max by clicking the min or max value on a specific y-axis

---

### Dev Tab
- [ ] **Browser memory accuracy** — JS heap via `performance.memory` always reads ~10 MB; investigate whether Chrome is clamping the value or whether the read timing is wrong

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
