# WebSocket Protocol

Every link in the system is **JSON over WebSocket**. There are two of them:

| Link | Who dials | Endpoint | Purpose |
|---|---|---|---|
| Browser ↔ control node | the browser | `ws://<controlnode>:8000/ws/data`, `/ws/ctrl` | telemetry out, commands in |
| Control node ↔ DAQ node | the control node | `ws://<daqnode-ip>:<wsPort>/` | acquisition config, live data, commands, DAQ-local state |

`permessage-deflate` compression is negotiated on both links when the peer
supports it. Telemetry frames repeat the same refDes keys every tick, so the
saving is large; a peer that does not advertise compression simply runs
uncompressed.

This document is generated-adjacent, not generated: the field tables are sourced
from the typed Go builders in `controlnode/webclient/server.go`,
`controlnode/broker/broker.go`, `controlnode/config/yaml.go` and
`controlnode/statemachine/daqlocal.go`, and are asserted by
`controlnode/webclient/protocol_test.go`. A live rendering of the *loaded
configuration* (channels, machines, alerts) is served at
`http://<controlnode>:8000/docs`.

---

# Part 1 — Browser ↔ control node

The browser opens **two** sockets (see `WebClient/js/ws.js`).

| Endpoint | Auth | Direction | Carries |
|---|---|---|---|
| `/ws/data` | anonymous | server → browser only | `config`, `softchan_config`, `state_config`, `pid_layout`, `data`, `state_change`, `alert`, `alert_acked`, `alert_snapshot`, `bad_data`, `bad_data_snapshot`, `err` |
| `/ws/ctrl` | PIN required | browser → server (+ `auth_response` back) | `auth_request`, `cmd`, `ack_alert`, `set_layout` |

A client message on `/ws/data` is drained and ignored. Any `/ws/ctrl` message
other than `auth_request` is rejected until authentication succeeds.

## Connect sequence on `/ws/data`

The server writes these immediately, in this order, then streams broadcasts:

1. `config` — always
2. `softchan_config` — if any software channels are loaded
3. `state_config` — if any state machines are loaded
4. one `pid_layout` per enabled front panel
5. `alert_snapshot` — if any alerts are active
6. `bad_data_snapshot` — if any channel is currently out of range

A reconnect repeats the whole sequence, so the browser can rebuild from scratch.

## `config` — hardware configuration

Built by `config.BuildWebClientConfigJSON`.

```json
{
  "type": "config",
  "broadcastRateHz": 50,
  "channelStaleMs": 2000,
  "controls": [
    {
      "refDes": "NV-03",
      "description": "LOX Press Valve",
      "type": "valve",
      "subType": "IO-CMD_IO-FB",
      "details": {},
      "channels": [
        { "refDes": "NV-03-CMD", "role": "cmd-bool", "units": "", "validMin": null, "validMax": null },
        { "refDes": "NV-03-FB",  "role": "",         "units": "", "validMin": null, "validMax": null }
      ]
    }
  ]
}
```

| Field | Type | Description |
|---|---|---|
| `broadcastRateHz` | number | Data fan-out rate (`system.yaml`). Drives chart render intervals. |
| `channelStaleMs` | number | Milliseconds without a value before the browser shades a channel stale. |
| `controls` | array | One entry per **enabled** control in `controls.yaml`, plus one synthetic `ctrNode` control for control-node health channels. |

Control object:

| Field | Type | Description |
|---|---|---|
| `refDes` | string | Control-level reference designator (`NV-03`, `OPT-01`) |
| `description` | string | Human-readable label |
| `type` | string | `valve`, `bangBang`, `ignition`, `digitalOut`, `pressure`, `temperature`, `flowMeter`, `thrust`, `VFD`, `ctrNode` |
| `subType` | string | Sub-variant, e.g. `IO-CMD_IO-FB` for valves |
| `details` | object | Type-specific: `{absolute, absoluteSensorRefDes}` for `pressure`, `{senseRefDes}` for `bangBang`, `{}` otherwise |
| `channels` | array | Channel objects |

Channel object:

| Field | Type | Description |
|---|---|---|
| `refDes` | string | Channel-level refDes (`NV-03-CMD`) — the key used in `data` and `cmd` |
| `role` | string | `""` read-only sensor, `cmd-bool`, `cmd-pct`, `cmd-float` |
| `units` | string | Engineering units (`psi`, `Deg F`); may be empty |
| `validMin` | number \| null | Lower bad-data bound; `null` disables that side |
| `validMax` | number \| null | Upper bad-data bound |

Bad-data detection runs **server-side** (`broker.checkBounds`) and is reported
with `bad_data`; the bounds are also sent here so the browser can colour values
without waiting for a transition message.

## `softchan_config` — software channels

Built by `softchan.Store.ConfigJSON`. Software channels live in the control
node's memory and appear in the `data` stream like any other channel.

```json
{
  "type": "softchan_config",
  "channels": [
    { "refDes": "SEQ-CUTOFF-T", "description": "Main-valve cutoff time, absolute from sequence start (burn length = this minus valve-open time)", "units": "ms",
      "role": "cmd-float", "default": 3000, "min": 500, "max": 10000 },
    { "refDes": "PT-FUEL-AVG", "description": "", "units": "", "role": "",
      "default": 0, "min": null, "max": null, "computed": true }
  ]
}
```

| Field | Type | Description |
|---|---|---|
| `refDes` | string | Channel name from the `.chan` file |
| `description` | string | Optional label |
| `units` | string | Optional units |
| `role` | string | `cmd-float` / `cmd-bool` for settable, `""` for read-only |
| `default` | number | Value used when nothing is persisted |
| `min` / `max` | number \| null | Bounds enforced server-side on every set |
| `computed` | bool | Present and `true` for `compute` channels (read-only, recomputed every tick) |

Each loaded state machine adds an auto-generated pair:
`SM-<NAME>-STATE` (read-only, current state **index**) and `SM-<NAME>-TARGET`
(operator-writable target).

## `state_config` — state machines

Built by `webclient.BuildStateConfigJSON`. Omitted entirely when no machines are
loaded.

```json
{
  "type": "state_config",
  "machines": [
    {
      "name": "fuelSeq",
      "targetRefDes": "SM-fuelSeq-TARGET",
      "states": [
        { "name": "safe",          "index": 0, "operator": true },
        { "name": "manualControl", "index": 1, "operator": true },
        { "name": "autoSequence",  "index": 2, "operator": true },
        { "name": "abort",         "index": 3, "operator": true }
      ]
    }
  ]
}
```

| Field | Type | Description |
|---|---|---|
| `machines[].name` | string | Machine name from `machine <name>` in the `.sm` file |
| `machines[].targetRefDes` | string | Channel to write to request a state (`SM-<NAME>-TARGET`) |
| `machines[].states[].name` | string | State name |
| `machines[].states[].index` | number | Position in the file; `0` is the initial state, and the value published on `SM-<NAME>-STATE` |
| `machines[].states[].operator` | bool | `true` if the state carries the `operator` flag — only these may be commanded directly |

## `state_change` — authoritative state broadcast

Built by `webclient.StateChangeJSON`, published on every state entry including
the initial one at startup.

```json
{ "type": "state_change", "machine": "fuelSeq", "state": "autoSequence" }
```

The state **name** travels here; the numeric index arrives in `data` on
`SM-<NAME>-STATE`. The browser should treat `state_change` as authoritative.

## `pid_layout` — front panel layout

One message per enabled panel in `panelLayouts.yaml`, re-sent whenever an
operator saves a layout.

```json
{ "type": "pid_layout", "name": "LOX Panel", "filename": "lox_panel.yaml",
  "content": "name: LOX Panel\nversion: 1\nobjects:\n  ..." }
```

| Field | Type | Description |
|---|---|---|
| `name` | string | Display name from `panelLayouts.yaml` |
| `filename` | string | Base filename; the browser's key, and the handle `set_layout` writes back with |
| `content` | string | Raw YAML text of the layout file |

Front panel YAML schema:

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

## `data` — live values

Broadcast at `broadcastRateHz`. Only channels that changed since the last tick
are included, so a frame is a delta, not a snapshot.

```json
{ "type": "data", "t": 1711574400.123,
  "d": { "OPT-01": 312.4, "NV-03-FB": 0, "SM-fuelSeq-STATE": 2 } }
```

| Field | Type | Description |
|---|---|---|
| `t` | number | Unix timestamp in seconds (float) |
| `d` | object | Map of channel refDes → current value |

Software channels are re-published in full once a second as a keepalive, so a
newly connected browser converges within ~1 s even for channels nobody is
touching.

## `err` — DAQ or control-node fault

```json
{ "type": "err", "t": 1711574400.5, "daqNode": "DAQ001", "err": "AI task overrun" }
```

| Field | Type | Description |
|---|---|---|
| `t` | number | Unix seconds; `0` when the control node itself raised it |
| `daqNode` | string | The node that reported it, or `CTR` for control-node faults (engine errors, unresolvable `daq_local` payloads) |
| `err` | string | Message text |

## `bad_data` / `bad_data_snapshot` — range violations

`bad_data` is sent on **transitions only** (ok → high/low → ok).

```json
{ "type": "bad_data", "refDes": "OPT-01", "value": 1620.5, "status": "high",
  "validMin": 0, "validMax": 1500, "t": 1711574400.5 }
```

| Field | Type | Description |
|---|---|---|
| `refDes` | string | Offending channel |
| `value` | number | The value that tripped (or cleared) the check |
| `status` | string | `high`, `low`, or `ok` (cleared) |
| `validMin` / `validMax` | number | Present only when configured |
| `t` | number | Unix seconds |

`bad_data_snapshot` carries every currently-bad channel, sent on connect:

```json
{ "type": "bad_data_snapshot",
  "channels": [ { "refDes": "OPT-01", "value": 1620.5, "status": "high",
                  "validMin": 0, "validMax": 1500, "t": 1711574400.5 } ] }
```

A range violation *also* raises an alert through the template's `bad_data`
event — the alert bar is fed by the server, never inferred by the browser.

## `alert`, `alert_snapshot`, `alert_acked`

The control node is the single source of alerts. The browser renders; it never
invents an alert.

```json
{ "type": "alert", "id": "rule:CHAMBER-HIGH", "category": "alarm",
  "message": "Chamber pressure high: 512 psia (limit 450)",
  "timestamp": 1711574400123, "acked": false }
```

| Field | Type | Description |
|---|---|---|
| `id` | string | Stable per source: `rule:<NAME>`, `conn:<NODE>`, `stale:<NODE>`, `bad:<REFDES>`, or a generated id for one-off server notices |
| `category` | string | `info`, `warning`, `alarm` |
| `message` | string | Fully interpolated text ( `{CHANNEL}` / `{node}` / `{refDes}` / `{value}` already substituted) |
| `timestamp` | number | Unix **milliseconds** |
| `acked` | bool | Whether an operator has acknowledged it |

`alert_snapshot` is the full active list — sent on connect and once a second, so
a locally dismissed but still-active alert reappears:

```json
{ "type": "alert_snapshot", "alerts": [ { "id": "...", "category": "...", "message": "...", "timestamp": 0, "acked": false } ] }
```

`alert_acked` is broadcast both when an operator acknowledges an alert and when
the server **resolves** one (a non-latching rule going false, a node
reconnecting). To the browser both mean "stop flashing this row".

```json
{ "type": "alert_acked", "id": "rule:CHAMBER-HIGH" }
```

## `auth_request` / `auth_response` — `/ws/ctrl`

```json
{ "type": "auth_request", "name": "Miles", "pin": "1234" }
{ "type": "auth_response", "approved": true, "name": "Miles", "reason": "" }
```

| Field | Type | Description |
|---|---|---|
| `name` | string | Operator name; echoed back on success and attached to every `cmd` |
| `pin` | string | Validated against `config/userAuth.yaml` (`webclient/auth.go`) |
| `approved` | bool | Authorisation result for this socket |
| `reason` | string | `"Invalid credentials"` on failure, empty on success |

If no `userAuth.yaml` is present the server logs it and accepts any
credentials — a development convenience, not a deployment mode.

## `cmd` — command a channel

```json
{ "type": "cmd", "refDes": "NV-03-CMD", "value": 1, "user": "Miles" }
```

| Field | Type | Description |
|---|---|---|
| `refDes` | string | Target channel; must have a `cmd-*` role |
| `value` | number \| bool \| string | `1`/`0` for `cmd-bool`, `0`–`100` for `cmd-pct`, float for `cmd-float`; a **state name string** for `SM-<NAME>-TARGET` |
| `user` | string | Authenticated operator name, forwarded to the DAQ node for logging |

Routing, in order:

1. `SM-<NAME>-TARGET` → the state machine engine (`RequestTarget`). Only
   `operator`-flagged states are accepted; a rejection comes back as a `warning`
   alert, not an error message.
2. A refDes in the restart list (any control-node command containing `restart`)
   → the control node exits so a supervisor can restart it.
3. A software channel → validated against its min/max and stored.
4. Anything else → forwarded to the owning DAQ node as a `cmd` (see Part 2).

An unknown refDes, or a command for a disconnected node, is dropped with a log
line.

## `ack_alert`

```json
{ "type": "ack_alert", "id": "rule:CHAMBER-HIGH" }
```

Marks the alert acknowledged in the server registry, which broadcasts
`alert_acked`. An unknown id is relayed anyway, so a client holding a stale row
can clear it.

## `set_layout`

```json
{ "type": "set_layout", "filename": "lox_panel.yaml", "content": "name: ...", "user": "Miles" }
```

The filename must match a panel from `panelLayouts.yaml`; the file is written to
disk, re-broadcast as `pid_layout` to every client, and an `info` alert records
who saved it.

## Connection behaviour

**Reconnect (browser side):** exponential backoff, 1 s → 2 s → 4 s → 8 s → 10 s
(capped), then the full connect sequence again.

**Staleness (browser side):** a channel with no new value for `channelStaleMs`
is shown dimmed. This is per channel, not per node.

---

# Part 2 — Control node ↔ DAQ node

The control node **dials** each enabled node from `config/daqNodes/*.yaml` at
`ws://<ip>:<wsPort>/` and reconnects every 2 s on any error
(`controlnode/daqnode/client.go`). Today's DAQ node is a LabVIEW application;
this is the complete contract it must satisfy.

## Message inventory

| Direction | Type | When |
|---|---|---|
| DAQ → CTR | `config_req` | first message after the socket opens |
| CTR → DAQ | `config` | answer to `config_req` |
| DAQ → CTR | `data` | at the configured sample rate |
| DAQ → CTR | `err` | acquisition or hardware fault |
| CTR → DAQ | `cmd` | operator or sequence writes a channel |
| CTR → DAQ | `state_update` | the engine **enters** a `daq_local` state |
| DAQ → CTR | `state_req` | node wants the current `daq_local` state re-sent |
| DAQ → CTR | `abort_triggered` | a cached `abort_rule` tripped; the node already ran `exit_sequence` |
| DAQ → CTR | `sequence_complete` | the cached `entry_sequence` ran to the end |

## Handshake

```
DAQ  → { "type": "config_req", "refDes": "DAQ001" }
CTR  → { "type": "config", ... }
```

The control node waits up to **3 s** for `config_req` after dialling; anything
else, or a timeout, drops the connection and retries. The `refDes` field is
logged but the node identity is taken from the config entry the control node
dialled.

## `config` — CTR → DAQ

Built by `config.BuildDaqNodeConfigJSON` from `controls.yaml` +
`daqNodes/<node>.yaml`. It contains only the channels whose `daqNode` matches
this node, and only enabled modules and controls.

```json
{
  "type": "config",
  "sampleRateHz": 50,
  "managementRateHz": 1,
  "modules": [
    { "moduleModelNumber": "Thermocouple", "sampleRateHz": 1000 },
    { "moduleModelNumber": "Digital-IO",   "sampleRateHz": 1000 }
  ],
  "channels": [
    { "refDes": "OT-01", "moduleModelNumber": "Thermocouple", "channelNumber": "ai0",
      "taskName": "TC", "type": "K", "units": "Deg F" },
    { "refDes": "OPT-01", "moduleModelNumber": "Analog-Input", "channelNumber": "ai05",
      "taskName": "AI", "sensitivity": 0.001, "balance": 0.0,
      "inputTerminalConfiguration": "Differential", "units": "psi" },
    { "refDes": "NV-03-CMD", "moduleModelNumber": "Digital-IO", "channelNumber": "/port3/line0",
      "taskName": "DO" }
  ]
}
```

| Field | Type | Description |
|---|---|---|
| `sampleRateHz` | number | The rate the node should stream `data` at (`broadcastRateHz`) |
| `managementRateHz` | number | Keepalive / connection-management rate |
| `modules[]` | array | Enabled modules: `moduleModelNumber`, `sampleRateHz` |
| `channels[]` | array | Every channel this node owns |

Channel entry — all NI-DAQmx fields are `omitempty`, so only the ones relevant
to the module type appear:

| Field | Type | Applies to |
|---|---|---|
| `refDes` | string | all — the key used in `data` and `cmd` |
| `moduleModelNumber` | string | all |
| `channelNumber` | string | all — DAQmx channel string (`ai05`, `/port3/line0`) |
| `taskName` | string | all |
| `type` | string | thermocouple type: `K`, `E`, `T` |
| `units` | string | engineering units |
| `sensitivity`, `balance`, `inputTerminalConfiguration` | number/string | analog input |
| `bridgeConfiguration`, `voltageExcitationSource`, `excitationVoltage`, `nominalBridgeResistance`, `firstElectricalValue`, `secondElectricalValue`, `firstPhysicalValue`, `secondPhysicalValue`, `electricalUnits` | number/string | bridge completion |

## `data` — DAQ → CTR

```json
{ "type": "data", "t": 1711574400.123, "d": { "OPT-01": 312.4, "OT-01": -182.5 } }
```

Identical in shape to the browser `data` message. Hardware acquisition runs at
the module rate (typically 1000 Hz) and the node decimates to `sampleRateHz`
before sending. Every `data` message also re-arms the server-side stale timer
for this node.

**Timestamps:** LabVIEW's clock uses the LV epoch (1904-01-01). The node
converts before sending: `unix_t = lv_t - 2082844800`.

## `err` — DAQ → CTR

```json
{ "type": "err", "t": 1711574400.5, "err": "AI task overrun" }
```

Republished to browsers as an `err` message with `daqNode` filled in.

## `cmd` — CTR → DAQ

```json
{ "type": "cmd", "refDes": "NV-03-CMD", "value": 1 }
```

The operator name is **not** forwarded on this link (it is logged control-node
side). Commands are dropped, with a log line, if the node is disconnected.

## `state_update` — CTR → DAQ

The compiled form of a `daq_local` state. Built by
`statemachine.resolveDaqLocalState` (see `daqlocal.go`).

**`state_update` means "enter this state now."** It is sent when the engine
enters the `daq_local` state, and re-sent in answer to `state_req`. It is *not*
sent at connect time, because re-sending a running state would re-fire its
sequence from t=0 and re-command valves and igniters.

```json
{
  "type": "state_update",
  "state": "abort",
  "runId": 7,
  "entry_sequence": [
    { "t_ms": 0,   "refDes": "FV-02-CMD", "value": 0 },
    { "t_ms": 0,   "refDes": "OV-05-CMD", "value": 0 },
    { "t_ms": 100, "refDes": "VENT-CMD",  "value": 1 }
  ],
  "exit_sequence": [
    { "t_ms": 0, "refDes": "FV-02-CMD", "value": 0 },
    { "t_ms": 0, "refDes": "OV-05-CMD", "value": 0 },
    { "t_ms": 0, "refDes": "VENT-CMD",  "value": 1 }
  ],
  "abort_rules": [
    { "if": "CPT-01 > 850", "t_ms_on": 0, "t_ms_off": 20000 }
  ]
}
```

| Field | Type | Description |
|---|---|---|
| `state` | string | Name of the state being entered |
| `runId` | number | Monotonic id for this entry. Echo it back on `sequence_complete` so the control node can correlate the report with the run it belongs to. |
| `entry_sequence` | array | Timed set-points to execute locally, starting at t=0 on receipt |
| `exit_sequence` | array | Timed set-points to execute locally **when an abort rule trips**; always present (empty array when the state declares no `abort_sequence`) |
| `abort_rules` | array | Conditions to monitor locally while the entry sequence runs |

Step object (`entry_sequence` / `exit_sequence`):

| Field | Type | Description |
|---|---|---|
| `t_ms` | number | Absolute milliseconds from sequence start (already accumulated from the DSL's sequential `sleep`s) |
| `refDes` | string | Channel to write |
| `value` | number | Value to write (booleans are already 1/0) |

Abort rule object:

| Field | Type | Description |
|---|---|---|
| `if` | string | `"<refDes> <op> <number>"`, e.g. `"CPT-01 > 850"`. The threshold is a resolved number, never a channel name. |
| `t_ms_on` | number | Rule arms at this offset from sequence start |
| `t_ms_off` | number | Rule disarms at this offset |

**Send-time resolution:** thresholds, sleep durations and window bounds may be
written in the `.sm` file as soft-channel names or constant arithmetic over them
(`sleep 2 - SEQ-IGN-LEAD`, in the DSL's base unit of seconds). They are folded to
numbers each time the payload is built, so operator-tuned values are always
current, and only then converted to the milliseconds this payload carries on the
wire. An unresolvable reference, or a sleep that resolves negative, **refuses
the payload** and raises an alarm — it is never silently sent as 0.

## `state_req` — DAQ → CTR

```json
{ "type": "state_req" }
```

Asks for the current `daq_local` state to be re-sent (a node that rebooted or
lost its cache). The control node answers with a freshly resolved
`state_update` — but only if a machine is *currently* in a `daq_local` state on
that node. If none is, the request is logged and nothing is sent: there is no
state to give.

## `abort_triggered` — DAQ → CTR

```json
{ "type": "abort_triggered" }
```

Means: a cached `abort_rule` tripped, and the node **has already run**
`exit_sequence` locally (sub-millisecond, no network involved). The control node
then moves the machine to the abort destination declared by the trailing
`transition` of the `abort_sequence` block. Nothing about the safing action
depends on this message arriving.

## `sequence_complete` — DAQ → CTR

```json
{ "type": "sequence_complete", "runId": 7 }
```

Means the cached `entry_sequence` ran to its last step. The control node then
applies the state's **completion transition** (the trailing `transition` of the
`daq_local` sequence).

`runId` is optional but strongly recommended: echoing the `runId` from the
`state_update` lets the control node discard a completion that belongs to a
superseded run (aborted, re-entered, or from before a reconnect). When `runId`
is absent or `0` the control node falls back to matching on state and epoch,
which cannot distinguish two runs of the same state.

## Reconnect behaviour

On reconnect, for every machine with `daq_local` states on this node:

- **Machine not in a `daq_local` state here** → nothing is sent. The node holds
  no armed sequence.
- **Machine *is* in a `daq_local` state here** → the state is treated as
  **uncertain**. The node's timeline after a drop is unknowable (did the burn
  finish? did the abort rule trip while the link was down?), so the control node
  does *not* re-send `state_update`. It fires the state's declared **abort
  destination** and raises an alarm.

This is why `state_update` is an imperative ("enter now") rather than a cache
sync: re-sending it after a drop would restart a burn.
