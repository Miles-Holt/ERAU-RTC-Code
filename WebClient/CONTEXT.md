# WebClient — Project Context

This file is intended to be read by an AI assistant at the start of a new session
to quickly understand the project state, architecture, and key decisions.

---

## What This Project Is

**TC3 (Test Cell 3)** is a liquid rocket engine test stand at ERAU. This `WebClient`
folder is a browser-based front-end that connects to a **LabVIEW DAQ system** over
WebSocket to display live sensor data and send commands to valves, ignition circuits,
and other actuators.

The LabVIEW system runs on a PXIe chassis and streams data at ~20 Hz.

---

## Repository Layout

```
ERAU-RTC_Code/
├── nodeConfigs_0.0.2.xml       — system configuration (controls, channels, DAQ mapping)
├── CTRsample/
│   └── Support/
│       └── CTR_webSocketConfig_XML-JSON.vi  — converts XML config to JSON for WebSocket
└── WebClient/
    ├── index.html
    ├── js/app.js               — all client logic (single file, no build system)
    ├── css/style.css
    ├── TODO.md                 — open action items
    └── CONTEXT.md              — this file
```

---

## WebSocket Protocol

All messages are JSON strings over `ws://<host>:8000`.

**LabVIEW → Browser:**
```json
{ "type": "config", "controls": [ <control>, ... ] }
{ "type": "data",   "t": <unix_epoch_s>, "d": { "<refDes>": <value>, ... } }
```

**Browser → LabVIEW:**
```json
{ "type": "cmd", "refDes": "<refDes>", "value": <number|bool>, "user": "<name>" }
```

- `user` field is added when auth is implemented (name entered at login; no server-side validation)
- LabVIEW filters out disabled controls before sending config — browser renders everything it receives
- LabVIEW timestamp is LV epoch (1904); convert before sending: `unix_t = lv_t - 2082844800`
- The browser opens `index.html` directly via `file://` for local dev (hostname falls back to `localhost`)

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
    { "refDes": "NV-03-CMD", "role": "cmd-bool", "units": "" },
    { "refDes": "NV-03-FB",  "role": "sensor",   "units": "" }
  ]
}
```

### Control Types
| type | description |
|---|---|
| `valve` | Solenoid valve with CMD + FB channels |
| `bangBang` | Bang-bang pressure controller (POS + NEG digital outputs) |
| `ignition` | Ignition circuit (CMD + FB channels, ARM/FIRE UI) |
| `digitalOut` | Single digital output (TRIGGER) |
| `pressure` | Pressure transducer (read-only) |
| `temperature` | Thermocouple (read-only) |
| `flowMeter` | Turbine flow meter (read-only) |
| `thrust` | Load cell cluster (read-only) |
| `VFD` | Variable frequency drive |

### Channel Roles
| role | meaning | UI widget |
|---|---|---|
| `sensor` | Read-only (default for all FB, sensor channels) | display only |
| `cmd-bool` | Boolean 1/0 command | OPEN/CLOSE or ON/OFF buttons |
| `cmd-pct` | 0–100% setpoint | slider |
| `cmd-float` | Arbitrary float command | number input |

Roles are defined explicitly in `nodeConfigs_0.0.2.xml` and passed through the VI into the JSON config.
The VI overwrites empty `<role>` nodes to `"sensor"` before serializing.

---

## XML Config (`nodeConfigs_0.0.2.xml`)

- Lives in the repo root (not in WebClient)
- Two top-level sections: `<controlList>` (what the WebClient uses) and `<daqNodes>` (hardware mapping, not sent to browser)
- Each `<channel>` has `<refDes>`, `<role>`, `<description>`, `<units>`, etc.
- `CTR_webSocketConfig_XML-JSON.vi` reads this file and produces the `config` WebSocket message

---

## app.js Architecture

Single file, no build system. Key globals:

| variable | purpose |
|---|---|
| `tabs[]` | Array of `{ id, type, name, contentEl, channelUpdaters }` |
| `configControls[]` | Last received config controls array |
| `graphState{}` | Per-tab graph state: grid dims, cell channel lists, Chart.js instances |
| `channelBuffers{}` | Per-refDes rolling 15-min data buffer (only for channels active in a graph cell) |
| `devStats{}` | WS uptime, message counts, missed cycle tracking |
| `consoleLog[]` | Circular buffer of all non-ignored WebSocket messages |
| `devTabs[]` | References to open Dev tab objects (for live stat updates) |
| `consoleTabs[]` | References to open Console tab objects |

### Tab types
| type | description |
|---|---|
| `frontPanel` | Default tab; placeholder for P&ID overlays (not yet implemented) |
| `dataView` | Hybrid view: commandable controls as cards + sensor data as table |
| `graph` | Grid of Chart.js line charts; adjustable up to 4×8; regex channel search |
| `console` | Live log of WS messages; filter toggle hides `data` type by default |
| `dev` | WS connection stats + browser memory (Chrome only) |

### Tab interactions
- `+` button always adds a `frontPanel` tab
- Right-click tab → context menu to change type (Front Panel / Data View / Graph / Dev / Console)
- Double-click tab name → inline rename
- Hover tab → ✕ button appears to close

### Card builders
All card builders take `(ctrl, tab)` — they register callbacks in `tab.channelUpdaters`
so multiple Data View tabs can each maintain independent live-updating DOM elements.

### Data flow
1. `onMessage` → increments `devStats.msgCount`, logs to console buffer
2. `applyConfig` → stores `configControls`, rebuilds all open Data View tabs
3. `applyData` → fires all `tab.channelUpdaters`, buffers graph data, tracks timing for missed cycles
4. `setInterval(updateAllGraphs, 500)` → pushes buffer data to Chart.js at 2 Hz
5. `setInterval(refreshDevTabs, 2000)` → updates Dev tab stat displays

---

## Key Implementation Decisions

- **No build system** — `index.html` opened directly via `file://`; Chart.js loaded from CDN (requires internet)
- **Per-tab channelUpdaters** — each tab has its own `{ refDes: fn }` map so multiple Data View tabs work independently
- **Graph buffers only for active channels** — `channelBuffers` only exists for refDes values currently selected in a graph cell; cleaned up when channel is removed
- **Auth is client-side only** — no server validation; `user` field on cmd messages is for LabVIEW logging only
- **`enabled` filter is server-side** — LabVIEW strips disabled controls before sending config; browser renders everything it receives (no filter in JS)
- **Roles explicit in XML** — previously inferred from refDes suffixes inside the VI; now `<role>` nodes are set in XML and passed through to JSON

---

## Known Issues / Active Bugs

- **Graph grid rendering** — canvas height not rendering correctly in some grid configurations; CSS height constraints on `.graph-cell` / `.graph-canvas` need investigation
- **Chart.js CDN** — graph tab will not function without internet access; consider bundling for offline/test-stand use

---

## System Hardware Context

- **PXIe chassis** running LabVIEW 2024
- Propellants: LOX (liquid oxygen) + fuel (UDMH or similar)
- Sensors: pressure transducers (PT), thermocouples (OT/FT), load cells (LCC), flow meters (FM)
- Actuators: solenoid valves (NV/OV/FV), bang-bang controllers (NV-01/02), ignition circuit (IG-01), trigger (TRIGGER-01)
- All valve CMD channels are `cmd-bool`; no `cmd-pct` or `cmd-float` channels exist in current config
