# WebSocket Protocol

All communication between the LabVIEW backend and the browser client uses **JSON over WebSocket** on port 8000.

**Endpoint:** `ws://<chassis-hostname>:8000`

---

## Message Types

| Direction | Type | When |
|---|---|---|
| LabVIEW → Browser | `config` | Once on connect (or reconnect); defines all controls |
| LabVIEW → Browser | `data` | 20 Hz continuously; live sensor values |
| Browser → LabVIEW | `cmd` | On user action; sends a command to an actuator |

---

## `config` — Control Configuration

Sent by LabVIEW immediately after the WebSocket connection is established. Defines every control/sensor the browser should render.

```json
{
  "type": "config",
  "controls": [
    {
      "refDes":      "NV-03",
      "description": "LOX Press Valve",
      "type":        "valve",
      "subType":     "IO-CMD_IO-FB",
      "details":     {},
      "channels": [
        { "refDes": "NV-03-CMD", "role": "cmd-bool", "units": "" },
        { "refDes": "NV-03-FB",  "role": "sensor",   "units": "" }
      ]
    },
    {
      "refDes":      "OPT-01",
      "description": "LOX Tank Pressure",
      "type":        "pressure",
      "subType":     "",
      "details":     { "absolute": true, "absoluteSensorRefDes": "" },
      "channels": [
        { "refDes": "OPT-01", "role": "sensor", "units": "psi" }
      ]
    }
  ]
}
```

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

### Server-side Filtering

LabVIEW strips controls where `<enabled>false</enabled>` before building the JSON. The browser renders everything it receives — there is no client-side filter.

---

## `data` — Live Sensor Data

Sent at **20 Hz** by LabVIEW. Contains a map of all active channel refDes values to their current readings.

> **Note:** Hardware acquisition runs at **1000 Hz**. The streaming loop decimates to 20 Hz before sending over WebSocket.

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

LabVIEW's internal clock uses the **LV epoch (1904-01-01)**. Before sending, the VI converts to Unix epoch:

```
unix_t = lv_t - 2082844800
```

The browser receives standard Unix timestamps and does not need to convert.

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
| `user` | string | Operator name (entered on the client); used by LabVIEW for logging — no server-side auth |

---

## Connection Behavior

### Reconnect

The browser uses **exponential backoff** on disconnect:

| Parameter | Value |
|---|---|
| Initial delay | 1 000 ms |
| Backoff factor | 2× |
| Maximum delay | 10 000 ms |

For example: 1 s → 2 s → 4 s → 8 s → 10 s → 10 s → ...

### Staleness Detection

If no `data` message is received within **500 ms**, the browser marks channels as stale (values shown with reduced opacity). This threshold is configurable via `CONFIG.staleThresholdMs` in `app.js`.

### On Reconnect

When the connection is re-established, LabVIEW re-sends the `config` message. The browser rebuilds all Data View tabs from the new config.
