# WebSocket Protocol

All communication between the **Go control node** and browser clients uses **JSON over WebSocket** on port 8000.

**Endpoint:** `ws://<chassis-hostname>:8000`

---

## Message Types

| Direction | Type | When |
|---|---|---|
| Control Node → Browser | `config` | Once on every new connection; defines all controls |
| Control Node → Browser | `data` | At `broadcastRateHz` (default 20 Hz); live sensor values |
| Control Node → Browser | `pid_layout` | Once per configured front panel, sent after `config` on every new connection |
| Browser → Control Node | `cmd` | On user action; sends a command to an actuator |

---

## `config` — Control Configuration

Sent by the control node immediately after the WebSocket connection is established. Defines every control and sensor the browser should render.

```json
{
  "type": "config",
  "broadcastRateHz": 20,
  "controls": [
    {
      "refDes":      "NV-03",
      "description": "LOX Press Valve",
      "type":        "valve",
      "subType":     "IO-CMD_IO-FB",
      "details":     {},
      "channels": [
        { "refDes": "NV-03-CMD", "role": "cmd-bool", "units": "", "validMin": null, "validMax": null },
        { "refDes": "NV-03-FB",  "role": "sensor",   "units": "", "validMin": null, "validMax": null }
      ]
    },
    {
      "refDes":      "OPT-01",
      "description": "LOX Tank Pressure",
      "type":        "pressure",
      "subType":     "",
      "details":     { "absolute": true, "absoluteSensorRefDes": "" },
      "channels": [
        { "refDes": "OPT-01", "role": "sensor", "units": "psi", "validMin": 0, "validMax": 1500 }
      ]
    }
  ]
}
```

### Top-Level Fields

| Field | Type | Description |
|---|---|---|
| `broadcastRateHz` | number | Data broadcast rate in Hz (default 20). Drives chart render intervals in the browser. |
| `controls` | array | Array of control objects — see [Control Object Fields](#control-object-fields) |

### Control Object Fields

| Field | Type | Description |
|---|---|---|
| `refDes` | string | Reference designator — unique ID for this control (e.g. `NV-03`, `LCC-01`) |
| `description` | string | Human-readable label |
| `type` | string | Control category — see [Control Types](#control-types) |
| `subType` | string | Sub-variant — see per-type notes in [XML Config Reference](xml-config-reference.md) |
| `details` | object | Type-specific extra fields (e.g. `senseRefDes` for bangBang, `absolute` for pressure) |
| `channels` | array | One or more channel objects — see [Channel Object](#channel-object) |

### Channel Object

| Field | Type | Description |
|---|---|---|
| `refDes` | string | Channel-level reference designator (e.g. `NV-03-CMD`, `NV-03-FB`) |
| `role` | string | How this channel is used — see [Channel Roles](#channel-roles) |
| `units` | string | Engineering units string (e.g. `psi`, `Deg F`, `GPM`); may be empty |
| `validMin` | number \| null | Lower bound for bad-data detection; `null` if not configured |
| `validMax` | number \| null | Upper bound for bad-data detection; `null` if not configured |

Bad-data detection is performed client-side: if a live value falls outside `[validMin, validMax]`, the Channel List tab shows a red LED and red value text. Either bound being `null` disables that side of the check.

### Channel Roles

| Role | UI Widget | Description |
|---|---|---|
| `sensor` | Read-only display | Feedback or measurement channel; no user input |
| `cmd-bool` | OPEN/CLOSE buttons | Boolean command — sends `1` (open/on) or `0` (close/off) |
| `cmd-pct` | Slider (0–100%) | Percentage setpoint command |
| `cmd-float` | Number input | Arbitrary float command |

### Control Types

| Type | Description |
|---|---|
| `valve` | Solenoid valve with command and feedback channels |
| `bangBang` | Bang-bang pressure controller (press / vent digital outputs) |
| `ignition` | Ignition circuit (ARM + FIRE sequence) |
| `digitalOut` | Single digital output (e.g. trigger) |
| `pressure` | Pressure transducer (read-only) |
| `temperature` | Thermocouple (read-only) |
| `flowMeter` | Turbine flow meter (read-only) |
| `thrust` | Load cell cluster (read-only) |
| `VFD` | Variable frequency drive |
| `ctrNode` | Control node health sensors and commands (auto-appended by the control node) |

### Server-Side Filtering

The control node strips controls where `<enabled>false</enabled>` before building the JSON config. The browser renders everything it receives — there is no client-side filter.

---

## `data` — Live Sensor Data

Broadcast by the control node at `broadcastRateHz` (default 20 Hz). Contains a map of all active channel refDes values and their current readings.

> **Note:** Hardware acquisition on the DAQ node runs at **1000 Hz**. The DAQ streaming loop decimates to the configured broadcast rate before forwarding to the control node.

```json
{
  "type": "data",
  "t": 1711574400.123,
  "d": {
    "OPT-01":    312.4,
    "FPT-01":    45.1,
    "OT-01":     -182.5,
    "NV-03-FB":  0,
    "LCC-01-01": 1024.7,
    "FM-01":     2.3
  }
}
```

| Field | Type | Description |
|---|---|---|
| `t` | number | Unix timestamp in seconds (float, sub-second precision) |
| `d` | object | Map of channel `refDes` → current value |

### Timestamp Note

The DAQ node's LabVIEW clock uses the **LV epoch (1904-01-01)**. Before forwarding, the DAQ node converts to Unix epoch:

```
unix_t = lv_t - 2082844800
```

The browser receives standard Unix timestamps and does not need to convert.

---

## `pid_layout` — Front Panel Layout

Sent by the control node after the `config` message on every new browser connection — one message per `<panel>` entry in `<frontPanels>` of the XML config. Delivers the raw YAML content of each P&ID layout file.

```json
{
  "type":     "pid_layout",
  "name":     "LOX Panel",
  "filename": "lox_panel.yaml",
  "content":  "name: LOX Panel\nversion: 1\nobjects:\n  - id: obj_1\n    type: sensor\n    ..."
}
```

| Field | Type | Description |
|---|---|---|
| `name` | string | Display name for the layout (from `<name>` in XML) |
| `filename` | string | YAML filename (from `<file>` in XML); used as a unique key in the browser |
| `content` | string | Full raw YAML content of the layout file |

The browser stores all received layouts in `pidLayouts{}` (keyed by `filename`). Each Front Panel tab can load any received layout via the toolbar picker. Layouts can be edited in the browser and downloaded as `.yaml` files via the Save YAML button.

### Front Panel YAML Schema

```yaml
name: LOX Panel
version: 1
objects:
  - id: "obj_1234"
    type: sensor        # sensor | node
    refDes: OPT-01      # channel refDes (sensor objects only)
    units: psi          # overrides config units if set
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

## `cmd` — Command

Sent by the browser when a user interacts with a commandable control (button, slider, or input).

```json
{
  "type":   "cmd",
  "refDes": "NV-03-CMD",
  "value":  1,
  "user":   "Miles"
}
```

| Field | Type | Description |
|---|---|---|
| `type` | string | Always `"cmd"` |
| `refDes` | string | Channel-level refDes of the target channel (must have a `cmd-*` role) |
| `value` | number or bool | Command value — `1`/`0` for `cmd-bool`, `0`–`100` for `cmd-pct`, float for `cmd-float` |
| `user` | string | Operator name entered in the browser; forwarded to the DAQ node for logging — no server-side auth |

The control node routes each `cmd` message to the appropriate DAQ node based on the channel's `refDesDaq` mapping from the XML config. Command widgets in the browser are disabled until the operator enters a name.

---

## Connection Behavior

### On Connect

When a browser connects, the control node immediately sends:
1. One `config` message (all enabled controls)
2. One `pid_layout` message per enabled `<panel>` in `<frontPanels>` of the XML config

### Reconnect

The browser uses **exponential backoff** on disconnect:

| Parameter | Value |
|---|---|
| Initial delay | 1 000 ms |
| Backoff factor | 2× |
| Maximum delay | 10 000 ms |

Sequence: 1 s → 2 s → 4 s → 8 s → 10 s → 10 s → …

### Staleness Detection

If no `data` message is received within **500 ms** of the expected interval, the browser marks all channels as stale — values are shown with reduced opacity and amber color. The threshold is derived from `broadcastRateHz` and configurable via `CONFIG.staleThresholdMs` in `js/state.js`.

### On Reconnect

When the connection is re-established, the control node re-sends the `config` message followed by all `pid_layout` messages. The browser rebuilds all Channel List tabs from the new config and refreshes all open Front Panel layout pickers.
