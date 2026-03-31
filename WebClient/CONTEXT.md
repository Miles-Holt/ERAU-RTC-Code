# WebClient — Project Context

This file is intended to be read by an AI assistant at the start of a new session
to quickly understand the project state, architecture, and key decisions.

---

## What This Project Is

**TC3 (Test Cell 3)** is a liquid rocket engine test stand at ERAU. The `WebClient`
folder is a browser-based front-end that connects to a **Go control node** over
WebSocket to display live sensor data, send commands to valves and actuators, and
edit/view P&ID front panel layouts.

The Go control node (`controlnode/`) bridges one or more DAQ nodes (LabVIEW) and
all connected browser clients. It serves the WebClient as static files and handles
the WebSocket protocol.

---

## Repository Layout

```
ERAU-RTC-Code/
├── nodeConfigs_0.0.2.xml       — system configuration (controls, channels, DAQ mapping, front panels)
├── controlnode/                — Go control node (WebSocket server, broker, DAQ client, config parser)
│   ├── main.go
│   ├── broker/broker.go        — data fan-out + command routing
│   ├── config/xml.go           — XML parser + JSON builders for browser and DAQ configs
│   ├── webclient/server.go     — HTTP + WebSocket server for browsers
│   ├── daqnode/client.go       — persistent WebSocket client to each DAQ node
│   └── health/health.go        — control node health metrics publisher
└── WebClient/
    ├── index.html
    ├── js/
    │   ├── state.js            — CONFIG constant + all global state variables
    │   ├── utils.js            — mkEl, debounce, setStatus, updateTimestamp
    │   ├── ws.js               — WebSocket management; config/data/pid_layout handlers; markStale
    │   ├── pid.js              — Front Panel P&ID editor + viewer (SVG canvas, YAML parse/serialise)
    │   ├── tabs.js             — tab CRUD, renderTabBar, buildTabContent dispatcher
    │   ├── dataview.js         — Channel List tab (search bar, per-channel rows with LED/sparkline/value)
    │   ├── graph.js            — Graph tab (grid, Chart.js, buffering, zoom/scroll/tooltip)
    │   ├── console.js          — Console tab (direction/type/regex filters, buffer)
    │   ├── dev.js              — Dev tab; forceReconnect; refreshDevTabs; fmtUptime/fmtBytes
    │   ├── cards.js            — card builders for commandable controls (valve, ignition, VFD, …)
    │   ├── auth.js             — operator name popover; updateCommandWidgets (disables cmd widgets without name)
    │   ├── app.js              — init: addTab, intervals, connect(), boot overlay
    │   ├── sim.js              — simulation mode (fake data, independent of WebSocket)
    │   └── chart.umd.min.js    — Chart.js bundled locally (no CDN)
    ├── css/style.css
    ├── CONTEXT.md              — this file
    └── (no TODO.md here — see docs/TODO.md in repo root)
```

Scripts load via `<script>` tags in the order above. All state is shared through
globals — no ES modules (required for `file://` compatibility). `state.js` must
load first; `app.js` must load last.

---

## WebSocket Protocol

All messages are JSON strings over `ws://<host>:8000`.

**Control Node → Browser:**
```json
{ "type": "config",     "broadcastRateHz": 20, "controls": [ … ] }
{ "type": "data",       "t": <unix_epoch_s>, "d": { "<refDes>": <value>, … } }
{ "type": "pid_layout", "name": "LOX Panel", "filename": "lox_panel.yaml", "content": "<raw YAML>" }
```

**Browser → Control Node:**
```json
{ "type": "cmd", "refDes": "<refDes>", "value": <number|bool>, "user": "<name>" }
```

- `config` is sent first on every new connection, followed by one `pid_layout` message per configured front panel
- `data` is broadcast at `broadcastRateHz` (default 20 Hz) by the broker tick
- `cmd` is routed by the broker to the correct DAQ node based on `refDesMap`
- `user` field is required; command widgets are disabled in the browser until an operator name is entered

---

## Config Object Shape

```json
{
  "refDes":      "NV-03",
  "description": "LOX Press Valve",
  "type":        "valve",
  "subType":     "IO-CMD_IO-FB",
  "details":     { "senseRefDes": "", "absolute": false, "absoluteSensorRefDes": "" },
  "channels": [
    { "refDes": "NV-03-CMD", "role": "cmd-bool", "units": "",    "validMin": null, "validMax": null },
    { "refDes": "NV-03-FB",  "role": "sensor",   "units": "",    "validMin": null, "validMax": null }
  ]
}
```

`validMin` / `validMax` are `null` when not configured in XML, or a float64 when set.
The Channel List tab uses them to drive bad-data detection (red LED + red value text).

### Control Types
| type | description |
|---|---|
| `valve` | Solenoid valve with CMD + FB channels |
| `bangBang` | Bang-bang pressure controller (POS + NEG digital outputs) |
| `ignition` | Ignition circuit (CMD + FB channels, ARM/FIRE UI) |
| `digitalOut` | Single digital output |
| `pressure` | Pressure transducer (read-only) |
| `temperature` | Thermocouple (read-only) |
| `flowMeter` | Turbine flow meter (read-only) |
| `thrust` | Load cell cluster (read-only) |
| `VFD` | Variable frequency drive |
| `ctrNode` | Control node health sensors + commands (auto-appended by config builder) |

### Channel Roles
| role | meaning | UI widget |
|---|---|---|
| `sensor` | Read-only measurement or feedback | display only |
| `cmd-bool` | Boolean 1/0 command | OPEN/CLOSE or ON/OFF buttons |
| `cmd-pct` | 0–100% setpoint | slider |
| `cmd-float` | Arbitrary float command | number input |

---

## pid_layout Message

Sent after `config` on every new browser connection — one message per `<panel>` entry in `<frontPanels>` of the XML config.

```json
{
  "type":     "pid_layout",
  "name":     "LOX Panel",
  "filename": "lox_panel.yaml",
  "content":  "name: LOX Panel\nversion: 1\nobjects:\n  …"
}
```

The browser stores received layouts in `pidLayouts{}` (keyed by `filename`). Each Front Panel tab can load one layout via its toolbar picker. Layouts are edited in-browser and downloaded as `.yaml` via the Save YAML button.

### Front Panel YAML schema
```yaml
name: LOX Panel
version: 1
objects:
  - id: "obj_1234"
    type: sensor        # sensor | node
    refDes: OPT-01      # channel refDes (sensor only)
    units: psi
    gridX: 10           # position in 20 px grid cells
    gridY: 5
connections:
  - id: "conn_5678"
    fromId: "obj_1234"
    fromPort: bottom    # top | right | bottom | left
    toId: "node_9012"
    toPort: top
```

---

## JavaScript Architecture

No build system — opened directly via `file://` or served by the control node. Key globals (all defined in `state.js`):

| variable | purpose |
|---|---|
| `tabs[]` | Array of `{ id, type, name, contentEl, channelUpdaters, … }` |
| `activeTabId` | ID of the currently visible tab |
| `configControls[]` | Controls array from the last received `config` message |
| `configApplied` | `true` after first `config` received |
| `pidLayouts{}` | Map of `filename → { name, filename, content }` from `pid_layout` messages |
| `graphState{}` | Per-tab graph state: grid dims, cell channel lists, Chart.js instances |
| `channelBuffers{}` | Per-refDes rolling 15-min data buffer (only for channels active in a graph cell) |
| `devStats{}` | WS uptime, message counts, missed-cycle tracking |
| `consoleLog[]` | Circular buffer of all WebSocket messages |
| `devTabs[]` | References to open Dev tab objects (for live stat updates) |
| `consoleTabs[]` | References to open Console tab objects |
| `operatorName` | Operator name string; empty string disables command widgets |
| `simActive` | `true` when simulation mode is running |

### Tab types
| type key | label | description |
|---|---|---|
| `frontPanel` | Front Panel | P&ID editor + viewer; loads YAML layouts; binds live channel data in View mode |
| `dataView` | Channel List | Per-channel rows with status LED, sparkline, live value; search bar with regex |
| `graph` | Graph | Grid of Chart.js line charts; adjustable up to 4×8; regex channel search per cell |
| `console` | Console | Live log of WS messages; direction / type / regex filters; configurable buffer |
| `dev` | Dev | WS stats; Force reconnect (Dev Mode); browser memory (Chrome only) |

Tabs do **not** persist across page refresh — every load opens a single fresh Front Panel tab.

### Tab interactions
- `+` button always adds a `frontPanel` tab
- Right-click tab → context menu to change type
- Click active tab name → inline rename (Enter to commit, Escape to cancel)
- Hover tab → ✕ to close

### Front Panel tab (pid.js)
- **View mode** (default): objects show live channel values; LED/stale inherited from `channelUpdaters`; junction nodes hidden; grid hidden
- **Edit mode**: left sidebar with draggable Sensor / Node object types; 20 px grid overlay; drag objects to move; click port to start connection → click another port to complete; right-click object → right sidebar for refDes/units config; Delete key removes selected object; Escape cancels in-progress connection
- **Save YAML**: generates YAML from current objects/connections and triggers a browser download
- Layout picker in toolbar lists all `pidLayouts` received from the control node

### Channel List tab (dataview.js)
- Search bar with regex match against refDes and description
- Each added channel gets a row: status LED | refDes + description | 15 s sparkline | value + units (or command input)
- LED states: green (live, in range), red (live, out of range per `validMin`/`validMax`), amber (stale)
- Value text: red when out of range, amber+dimmed when stale

### Console tab (console.js)
- Two toolbar rows:
  - Row 1: Dir toggles (`← in` / `→ out`) + Type toggles (`data` / `config` / `cmd` / `other`) + Clear
  - Row 2: free-text / regex search (wrap in `/…/` for regex) + buffer size input
- All filters are combinable; re-renders the full log on any change

### Dev tab (dev.js)
- WS stats refresh every 2 s: endpoint, state, uptime, message count, rate, missed cycles, JS heap (Chrome only)
- **Dev Mode** toggle (checkbox) — reveals Force Reconnect button and Sim toggle button
- Force reconnect: clears backoff timer, closes existing socket, calls `connect()` immediately

### Data flow
1. `ws.onmessage` → increments `devStats.msgCount`, logs to console buffer
2. `applyConfig(msg)` → stores `configControls`, rebuilds all open Channel List tabs, calls `setLiveUpdateRate`
3. `applyData(msg)` → normalises array or object format, calls all `tab.channelUpdaters`, buffers graph data, tracks timing
4. `applyPidLayout(msg)` → stores in `pidLayouts`, refreshes picker on open Front Panel tabs, reloads if tab already shows that layout
5. `setInterval(updateAllGraphs, <rate_ms>)` → pushes buffer data to Chart.js
6. `setInterval(updateAllDataViews, <rate_ms>)` → pushes buffer data to Channel List sparklines
7. `setInterval(refreshDevTabs, 2000)` → updates Dev tab stat displays

---

## Key Implementation Decisions

- **No build system** — `index.html` opened directly via `file://` or served from the control node; Chart.js bundled locally (no internet required)
- **Go control node** — `controlnode/` is a Go binary that serves the WebClient and bridges DAQ nodes
- **Per-tab channelUpdaters** — each tab owns `{ refDes: fn }` so multiple tabs update independently from the same data stream
- **Graph buffers only for active channels** — `channelBuffers` is allocated when a channel is added to any graph cell and freed when removed from all cells
- **Auth is client-side only** — no server validation; `user` field on `cmd` messages is for DAQ node logging only; command widgets are disabled until a name is entered
- **`enabled` filter is server-side** — the control node strips disabled controls before sending `config`; the browser renders everything it receives
- **Tab persistence removed** — `localStorage` tab save/restore was deleted; every page load starts with a single Front Panel tab to avoid stale layout bugs
- **Bad-data detection is client-side** — `validMin`/`validMax` come from the control node in the `config` message; the browser computes the bad state on each value update

---

## Known Issues / Active Bugs

- **Graph data when unfocused** — data is not buffered when the browser tab/window loses focus (browser background throttling)
- **Graph line snap at boundary** — line segments snap in/out at chart edges rather than smoothly; likely a Chart.js clipping issue with explicit `x.min`/`x.max`
- **Graph data tooltip position** — tooltip does not track the mouse correctly
- **Browser memory stat** — `performance.memory` (Chrome only) reports ~10 MB; may be clamped by the browser

---

## System Hardware Context

- **NI PXIe chassis** running LabVIEW 2024
- Propellants: LOX (liquid oxygen) + Kerosene or Ethanol fuel
- Sensors: pressure transducers (OPT/FPT/NPT), thermocouples (OT/FT), load cells (LCC), flow meters (FM)
- Actuators: solenoid valves (NV/OV/FV), bang-bang controllers, ignition circuit (IG-01)
- All valve CMD channels are `cmd-bool`; no `cmd-pct` or `cmd-float` channels exist in the current config
