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
├── Parses nodeConfigs_0.0.2.xml at startup
├── Connects to DAQ nodes over WebSocket (one goroutine per node)
├── Serves WebClient static files (HTTP) + WebSocket server on :8000
├── Broker — fans data to all connected browsers, routes commands to DAQ
└── Health publisher — uptime, loop time, DAQ/WC connection counts

Browser (file:// or http://<chassis>:8000)
└── WebClient/index.html
    ├── js/   — state, websocket, P&ID editor, tabs, graphs, console, auth, …
    └── css/style.css
```

**Data flow:**
1. Control node reads `nodeConfigs_0.0.2.xml` → builds JSON config → sends as `config` message on every browser connect
2. DAQ node streams hardware readings → control node broker → `data` messages to all browsers at configured Hz
3. Browser command → `cmd` message → control node → routed to correct DAQ node → valve/actuator driver
4. Front panel layouts → `.yaml` files on disk → control node reads them → `pid_layout` messages to browsers on connect

---

## Repository Layout

| Path | Description |
|---|---|
| `nodeConfigs_0.0.2.xml` | Primary system configuration — control definitions, channel bounds, DAQ hardware mapping |
| `controlnode/` | Go control node — WebSocket server, broker, DAQ client, XML config parser |
| `WebClient/` | Browser front-end (HTML + vanilla JS + CSS, no build system) |
| `docs/` | Documentation |
| `DAQ_msgHandler/` | Legacy LabVIEW DAQ handler (deprecated) |
| `CTRsample/` | Legacy control node sample (deprecated) |
| `DAQsample/` | Legacy LabVIEW DAQ node sample (deprecated) |

---

## Quick Start

### Run the Control Node

```bash
cd controlnode
go build -o controlnode.exe .
./controlnode.exe --config ../nodeConfigs_0.0.2.xml
```

The control node serves the WebClient at `http://localhost:8000` and the WebSocket at `ws://localhost:8000`.

> To serve a live-edited WebClient instead of the embedded one: `--webroot ../WebClient`

### Open the WebClient

Navigate to `http://<chassis-hostname>:8000` in a browser, or open `WebClient/index.html` directly via `file://` for local development (the WebSocket URL is derived from `window.location.hostname`, defaulting to `localhost`).

> **No internet required.** Chart.js is bundled at `WebClient/js/chart.umd.min.js`.

---

## Documentation

| Doc | Description |
|---|---|
| [docs/websocket-protocol.md](docs/websocket-protocol.md) | WebSocket message format (config, data, cmd, pid_layout) |
| [docs/webclient-guide.md](docs/webclient-guide.md) | Browser client user guide |
| [docs/xml-config-reference.md](docs/xml-config-reference.md) | `nodeConfigs_0.0.2.xml` format reference |
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
