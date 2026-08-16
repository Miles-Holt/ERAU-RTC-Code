# ERAU TC3 RTC Code

Real-time control and monitoring software for **TC3 (Test Cell 3)**, a liquid rocket engine test stand at Embry-Riddle Aeronautical University. The system streams sensor data at ~20 Hz and controls propellant valves, ignition circuits, and actuators from a browser-based interface.

---

## Architecture

```
NI PXIe Chassis
├── NI DAQ Modules
│   (thermocouples, pressure transducers, load cells, flow meters, valves)
│
└── DAQ Node (LabVIEW or future Go)
    ├── Acquisition Loop  (DAQmx, 1000 Hz)
    ├── Streaming Loop    (WebSocket → Control Node, ~20 Hz)
    └── Logging Loop      (TDMS file)

Control Node  (Go — controlnode/)
├── Parses YAML config from config/ at startup
├── Connects to DAQ nodes over WebSocket (one goroutine per node)
├── Serves WebClient static files (HTTP) + two WebSocket endpoints on :8000
│     /ws/data  (anonymous, server→browser stream)
│     /ws/ctrl  (PIN-authenticated, browser→server commands + auth)
├── Broker — fans data to all connected browsers, routes commands to DAQ
├── State machine — per-DAQ autosequence/abort states (daqnode/statemachine.go)
├── Soft channels + Health publisher — CTR-side virtual channels & metrics
└── Alerts + bad-data detection — server-side range checks pushed to browsers

Browser (file:// or http://<chassis>:8000)
└── WebClient/index.html
    ├── js/   — state, websocket, P&ID editor, tabs, graphs, console, auth, …
    └── css/style.css
```

**Data flow:**
1. Control node reads the YAML config in `config/` → builds JSON config → sends as `config` message on every browser connect
2. DAQ node streams hardware readings → control node broker → `data` messages to all browsers at configured Hz
3. Browser command → `cmd` message → control node → routed to correct DAQ node → valve/actuator driver
4. Front panel layouts → `.yaml` files on disk → control node reads them → `pid_layout` messages to browsers on connect

---

## Repository Layout

| Path | Description |
|---|---|
| `config/` | Primary system configuration (YAML) — controls, channel bounds, DAQ mapping, soft channels, state machine, panel layouts, user auth |
| `controlnode/` | Go control node — WebSocket server, broker, DAQ client, YAML config parser, state machine, soft channels |
| `WebClient/` | Browser front-end (HTML + vanilla JS + CSS, no build system) |
| `docs/` | Documentation |
| `RTC_PXIe-Code v1_9_0/` | LabVIEW DAQ node code (NI PXIe) |

---

## Quick Start

### Run the Control Node

```bash
cd controlnode
build.bat            # copies WebClient into static/ and builds controlnode.exe
./controlnode.exe    # defaults to --config-dir ../config
```

The control node serves the WebClient at `http://localhost:8000` and the WebSocket
endpoints (`/ws/data`, `/ws/ctrl`) on the same port.

> `--config-dir <dir>` selects the YAML config directory (default `../config`).
> `--webroot ../WebClient` serves the live source instead of the embedded build —
> handy while editing the WebClient (no rebuild needed).

### Open the WebClient

Navigate to `http://<chassis-hostname>:8000` in a browser, or open `WebClient/index.html` directly via `file://` for local development (the WebSocket URL is derived from `window.location.hostname`, defaulting to `localhost`).

> **No internet required.** Chart.js is bundled at `WebClient/js/chart.umd.min.js`.

---

## Smoke Tests

Fast Go tests that verify the control node and WebClient serving path boot and
work end-to-end. Run them from `controlnode/`:

```bash
cd controlnode
go test ./...        # or: test.bat  (uses vendored deps, -mod=vendor)
```

| Package | What it smoke-tests |
|---|---|
| `config` | Parsing the real `config/` directory and the browser/DAQ JSON builders |
| `broker` | Data fan-out, web-client → DAQ command routing, bad-data detection |
| `softchan` | Software-channel load, bounds/read-only validation, persistence |
| `webclient` | Auth, the `/ws/data` + `/ws/ctrl` WebSocket flow, and serving every `js/` file referenced by `index.html` |
| `daqnode` | The `config_req` handshake and data bridging against a fake DAQ WebSocket server |

The `webclient` static-file test serves the sibling `WebClient/` directory (it
is skipped if that directory is absent).

---

## Documentation

| Doc | Description |
|---|---|
| [docs/websocket-protocol.md](docs/websocket-protocol.md) | WebSocket message format — two sockets (`/ws/data`, `/ws/ctrl`); config, data, cmd, pid_layout, alerts, bad_data, auth, state/softchan config |
| [docs/webclient-guide.md](docs/webclient-guide.md) | Browser client user guide |
| [docs/xml-config-reference.md](docs/xml-config-reference.md) | **Legacy** XML schema reference — the live config is now YAML under `config/` |
| [docs/TODO.md](docs/TODO.md) | Open feature items and known issues |
| [WebClient/CONTEXT.md](WebClient/CONTEXT.md) | AI/developer context for the WebClient codebase |

---

## Hardware Context

| Component | Examples |
|---|---|
| Pressure transducers | OPT-01/02, FPT-01/02, NPT-01 |
| Thermocouples | OT-01/02, FT-01/02 |
| Solenoid valves | NV-01/05, OV-01/05, FV-01/05 |
| Load cells | LCC-01 (cluster) |
| Flow meters | FM-01/02 |
| Ignition | IG-01 |
| Bang-bang controllers | NV-01/02 (press/vent) |

- **Propellants:** LOX (liquid oxygen) + Kerosene or Ethanol fuel
- **DAQ chassis:** NI PXIe running LabVIEW 2024 Q3
- **Broadcast rate:** ~20 Hz (configurable in XML)
- **Acquisition rate:** 1000 Hz continuous
