# ERAU TC3 RTC Code

Real-time control and monitoring software for **TC3 (Test Cell 3)**, a liquid rocket engine test stand at Embry-Riddle Aeronautical University. The system streams sensor data at ~20 Hz and controls propellant valves, ignition circuits, and actuators from a browser-based interface.

---

## Architecture

```
PXIe Chassis (LabVIEW 2024)
├── NI DAQ Modules  ──────────────────────────────────────────────────┐
│   (thermocouples, pressure transducers, load cells, flow meters)    │
│                                                                      ▼
├── DAQ_msgHandler Main.vi                                      Acquire.vi
│   ├── Acquisition Loop  (DAQmx hardware I/O)                        │
│   ├── Streaming Loop    (network transmission)                       │
│   └── Logging Loop      (TDMS file)                                  │
│                                                                      │
└── CTRnode.vi  ──────────────────────────────────────────────────────┘
    ├── CTR_webSocketConfig_XML-JSON.vi  (XML → JSON config)
    └── WebSocket Server  :8000
             │
             │  JSON over WebSocket (~20 Hz data, config on connect)
             ▼
    Browser (file:// or network)
    └── WebClient/index.html
        ├── app.js   — tabs, live data, graphs, command sending
        └── style.css
```

**Data flow:**
1. LabVIEW reads `nodeConfigs_0.0.2.xml` → converts `<controlList>` to JSON → sends as `config` message on connect
2. Hardware sensors → DAQ acquisition → streaming loop → `data` messages at ~20 Hz
3. Browser command → `cmd` message → LabVIEW → valve/actuator driver

---

## Repository Layout

| Path | Description |
|---|---|
| `nodeConfigs_0.0.2.xml` | Primary system configuration — control definitions + DAQ hardware mapping |
| `CTRsample/` | LabVIEW Control/Routing node (CTRnode.vi + supporting VIs) |
| `DAQsample/` | LabVIEW DAQ node for initial testing (Depricated) (DAQ-node.vi + supporting VIs) |
| `DAQ_msgHandler/` | Message-driven DAQ handler with separate acquisition, streaming, and logging loops. Used for initial testing, currently depricated. |
| `WebClient/` | Browser-based front-end (HTML + vanilla JS + CSS, no build system) |
| `parsedConfigs/` | Generated/parsed config outputs |
| `docs/` | Documentation |

---

## Quick Start

### Open the WebClient

1. Open `WebClient/index.html` directly in a browser (`file://` — no server needed)
2. Enter the IP address or hostname of the PXIe chassis in the connection bar
3. Click **Connect** — the status indicator turns green when the WebSocket handshake succeeds

> **Note:** Graph tabs require Chart.js, which is loaded from CDN. Internet access is needed for graphs to render.

### Connect to the Test Stand

- Default WebSocket endpoint: `ws://<chassis-hostname>:8000`
- If opening locally on the chassis, hostname defaults to `localhost`

---

## Documentation

| Doc | Description |
|---|---|
| [docs/websocket-protocol.md](docs/websocket-protocol.md) | WebSocket message format (config, data, cmd) |
| [docs/webclient-guide.md](docs/webclient-guide.md) | Browser client user guide |
| [docs/xml-config-reference.md](docs/xml-config-reference.md) | nodeConfigs XML format reference |
| [WebClient/TODO.md](WebClient/TODO.md) | Open feature items and known issues |

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

- **Propellants:** LOX (liquid oxygen) + Keroscene or Ethanol fuel
- **DAQ chassis:** NI PXIe running LabVIEW 2024 Q3
- **Communication rate:** 20 Hz continuous
- **Aquisition rate:** 1000 Hz continuous
