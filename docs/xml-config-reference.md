# XML Config Reference — `nodeConfigs_0.0.2.xml`

This document is the primary reference for the TC3 system configuration file. The XML file itself contains a `<controlListTemplate>` section as a quick in-file reminder, but this Markdown guide is more complete and easier to navigate.

---

## File Structure

```
<systemConfig>
  <controlListTemplate>  ← in-file schema reminder (not parsed)
  <controlList>          ← parsed by the control node; defines all controls
    <control> ... </control>
    <control> ... </control>
    ...
  </controlList>
  <network>              ← WebSocket port, broadcast rate, connection settings
  <daqNodes>             ← DAQ node IP/port definitions (not sent to browser)
  <frontPanels>          ← P&ID layout YAML files to load and send to browsers
    <panel> ... </panel>
    ...
  </frontPanels>
</systemConfig>
```

The control node parses the entire file at startup. `<controlList>` is converted to JSON and sent as the `config` message on every browser connection. `<daqNodes>` is used only for DAQ node connectivity. `<frontPanels>` tells the control node which YAML layout files to read from disk and send as `pid_layout` messages.

---

## Common Control Fields

Every `<control>` block uses these top-level fields:

| Field | Required | Values | Description |
|---|---|---|---|
| `<refDes>` | Yes | string | Unique reference designator (e.g. `NV-03`, `OPT-01`) |
| `<description>` | No | string | Human-readable label shown in the UI |
| `<enabled>` | Yes | `true` / `false` | If `false`, the control node omits this control from the `config` message — it will not appear in the browser |
| `<type>` | Yes | see [Control Types](#control-types) | Determines UI rendering and channel expectations |
| `<subType>` | Varies | see per-type | Sub-variant that refines behavior (required for some types) |
| `<details>` | Varies | child elements | Type-specific extra configuration |
| `<channels>` | Yes | one or more `<channel>` blocks | Hardware channel bindings |

---

## Control Types

### `temperature` — Thermocouple

Read-only temperature sensor. No subType or details needed.

```xml
<control>
    <refDes required="true">FT-02</refDes>
    <description>Fuel Injector Temperature</description>
    <enabled>true</enabled>
    <type required="true">temperature</type>
    <subType></subType>
    <details></details>
    <channels>
        <channel>
            <refDes required="true">FT-02</refDes>
            <role></role>
            <modelNumber required="true">N/A</modelNumber>
            <serialNumber></serialNumber>
            <refDesDaq required="true">DAQ001</refDesDaq>
            <moduleModelNumber required="true">Thermocouple</moduleModelNumber>
            <channelNumber required="true">ai13</channelNumber>
            <daqMx required="true">
                <taskName required="true">Thermocouple/ai13</taskName>
                <type>K</type>
                <units>Deg F</units>
            </daqMx>
        </channel>
    </channels>
</control>
```

daqMx fields for thermocouple:

| Field | Values | Description |
|---|---|---|
| `<taskName>` | `ModuleName/aiN` | DAQmx task path |
| `<type>` | `K`, `E`, `T` | Thermocouple junction type |
| `<units>` | `Deg F` | Engineering units |

---

### `pressure` — Pressure Transducer

Read-only analog input. `<details>` specifies whether the sensor is absolute or gauge.

```xml
<control>
    <refDes required="true">NPT-01</refDes>
    <description>Bottle Pressure</description>
    <enabled>true</enabled>
    <type required="true">pressure</type>
    <subType></subType>
    <details>
        <absolute>false</absolute>
        <absoluteSensorRefDes></absoluteSensorRefDes>
    </details>
    <channels>
        <channel>
            <refDes required="true">NPT-01</refDes>
            <role></role>
            <modelNumber required="true">PX309</modelNumber>
            <serialNumber></serialNumber>
            <refDesDaq required="true">DAQ001</refDesDaq>
            <moduleModelNumber required="true">Analog-Input</moduleModelNumber>
            <channelNumber required="true">ai02</channelNumber>
            <daqMx required="true">
                <taskName required="true">Analog-Input/ai2</taskName>
                <sensitivity>73943.66197</sensitivity>
                <balance>2.21831</balance>
                <inputTerminalConfiguration>Differential</inputTerminalConfiguration>
                <units>psig</units>
            </daqMx>
        </channel>
    </channels>
</control>
```

`<details>` fields for pressure:

| Field | Values | Description |
|---|---|---|
| `<absolute>` | `true` / `false` | Whether this is an absolute pressure sensor |
| `<absoluteSensorRefDes>` | refDes string or empty | If not absolute and this is empty, a 0 offset is applied on the front panel |

daqMx fields for analog input:

| Field | Values | Description |
|---|---|---|
| `<taskName>` | `ModuleName/aiN` | DAQmx task path |
| `<sensitivity>` | float | Sensor sensitivity (units/Volt or units/mA depending on transducer) |
| `<balance>` | float | Zero/balance offset |
| `<inputTerminalConfiguration>` | `Differential`, `NRSE`, `RSE` | Wiring configuration |
| `<units>` | `psi`, `psig`, `%`, `GPM`, `Volts`, `Amps`, `Watts` | Engineering units |

---

### `valve` — Solenoid Valve

Commandable. Channels depend on the subType.

**subType options:**

| subType | Channels | Description |
|---|---|---|
| `IO-CMD_IO-FB` | CMD (cmd-bool) + FB (sensor) | On/off command + limit switch feedback |
| `IO-CMD` | CMD (cmd-bool) only | On/off command, no feedback device |
| `IO-CMD_POS-FB` | CMD (cmd-bool) + POS (sensor) | On/off command + 4–20 mA position feedback |
| `POS-CMD_POS-FB` | POS-CMD (cmd-pct) + POS-FB (sensor) | % open command + position feedback |

```xml
<control>
    <refDes required="true">NV-03</refDes>
    <description>LOX Press</description>
    <enabled>true</enabled>
    <type required="true">valve</type>
    <subType>IO-CMD_IO-FB</subType>
    <details></details>
    <channels>
        <channel>
            <refDes required="true">NV-03-CMD</refDes>
            <role>cmd-bool</role>
            <modelNumber required="true">N/A</modelNumber>
            <serialNumber></serialNumber>
            <refDesDaq required="true">DAQ001</refDesDaq>
            <moduleModelNumber required="true">Digital-IO</moduleModelNumber>
            <channelNumber required="true">/port3/line0</channelNumber>
            <daqMx required="true">
                <taskName required="true">Digital-IO/port3/line0</taskName>
            </daqMx>
        </channel>
        <channel>
            <refDes required="true">NV-03-FB</refDes>
            <role></role>
            <modelNumber required="true">N/A</modelNumber>
            <serialNumber></serialNumber>
            <refDesDaq required="true">DAQ001</refDesDaq>
            <moduleModelNumber required="true">Digital-IO</moduleModelNumber>
            <channelNumber required="true">/port0/line0</channelNumber>
            <daqMx required="true">
                <taskName required="true">Digital-IO/port0/line0</taskName>
            </daqMx>
        </channel>
    </channels>
</control>
```

---

### `bangBang` — Bang-Bang Pressure Controller

Controls a pressurization system using one or two digital outputs driven by a pressure sensor reference.

**subType options:**

| subType | Channels | Description |
|---|---|---|
| `press` | POS (cmd-bool) | Single press valve only |
| `pressVent` | POS (cmd-bool) + NEG (cmd-bool) | Press and vent valves |

`<details>` for bangBang:

| Field | Description |
|---|---|
| `<senseRefDes>` | refDes of the pressure transducer used as the bang-bang sense input (e.g. `FPT-02`) |

```xml
<control>
    <refDes required="true">NV-02</refDes>
    <description>Fuel Bang Bang</description>
    <enabled>true</enabled>
    <type required="true">bangBang</type>
    <subType>press2</subType>
    <details>
        <senseRefDes>FPT-02</senseRefDes>
    </details>
    <channels>
        <channel>
            <refDes required="true">NV-02-POS</refDes>
            <role>cmd-bool</role>
            ...
        </channel>
        <channel>
            <refDes required="true">NV-02-NEG</refDes>
            <role>cmd-bool</role>
            ...
        </channel>
    </channels>
</control>
```

---

### `ignition` — Ignition Circuit

ARM/FIRE sequence control. Typically has a CMD channel and one or more feedback/command-feedback channels.

```xml
<control>
    <refDes required="true">IG-01</refDes>
    <description>Ignition circuit</description>
    <enabled>true</enabled>
    <type required="true">ignition</type>
    <subType>IO-CMD_VAR-FB-CMD_IO-FB</subType>
    <details></details>
    <channels>
        <channel>
            <refDes required="true">IG-01-CMD</refDes>
            <role>cmd-bool</role>
            <moduleModelNumber required="true">Digital-IO</moduleModelNumber>
            <channelNumber required="true">/port5/line0</channelNumber>
            <daqMx required="true">
                <taskName required="true">Digital-IO/port5/line0</taskName>
            </daqMx>
        </channel>
        <channel>
            <refDes required="true">IG-01-FB</refDes>
            <role></role>
            ...
        </channel>
    </channels>
</control>
```

---

### `digitalOut` — Digital Output

Single digital output with no feedback. Used for triggers and simple on/off signals.

```xml
<control>
    <refDes required="true">TRIGGER-01</refDes>
    <description>Trigger signal</description>
    <enabled>true</enabled>
    <type required="true">digitalOut</type>
    <subType></subType>
    <details></details>
    <channels>
        <channel>
            <refDes required="true">TRIGGER-01</refDes>
            <role>cmd-bool</role>
            <modelNumber required="true">N/A</modelNumber>
            <refDesDaq required="true">DAQ001</refDesDaq>
            <moduleModelNumber required="true">Analog-Output</moduleModelNumber>
            <channelNumber required="true">/port0/line0</channelNumber>
            <daqMx required="true">
                <taskName required="true">Analog-Output/port0/line0</taskName>
            </daqMx>
        </channel>
    </channels>
</control>
```

---

### `thrust` — Load Cell Cluster

Read-only. Supports multiple channels (one per load cell in the cluster) using bridge completion modules.

```xml
<control>
    <refDes required="true">LCC-01</refDes>
    <description>TC3 Load cell cluster</description>
    <enabled>true</enabled>
    <type required="true">thrust</type>
    <subType></subType>
    <details></details>
    <channels>
        <channel>
            <refDes required="true">LCC-01-01</refDes>
            <role></role>
            <moduleModelNumber required="true">Bridge-Completion-2</moduleModelNumber>
            <channelNumber required="true">/ai0</channelNumber>
            <daqMx required="true">
                <taskName required="true">Bridge-Completion-2/ai0</taskName>
                <bridgeConfiguration>Full Bridge</bridgeConfiguration>
                <voltageExcitationSource>Internal</voltageExcitationSource>
                <excitationVoltage>10</excitationVoltage>
                <nominalBridgeResistance>350</nominalBridgeResistance>
                <firstElectricalValue>0.01355</firstElectricalValue>
                <secondElectricalValue>-0.0243</secondElectricalValue>
                <firstPhysicalValue>0</firstPhysicalValue>
                <secondPhysicalValue>25.15</secondPhysicalValue>
                <electricalUnits>mVolts/Volt</electricalUnits>
                <units>Pounds</units>
            </daqMx>
        </channel>
        <!-- repeat for LCC-01-02, LCC-01-03, LCC-01-04 ... -->
    </channels>
</control>
```

daqMx fields for bridge completion:

| Field | Values | Description |
|---|---|---|
| `<taskName>` | `ModuleName/aiN` | DAQmx task path |
| `<bridgeConfiguration>` | `Full Bridge`, `Half Bridge`, `Quarter Bridge` | Wheatstone bridge wiring |
| `<voltageExcitationSource>` | `Internal`, `External` | Excitation voltage source |
| `<excitationVoltage>` | float (V) | Excitation voltage (typically 10 V) |
| `<nominalBridgeResistance>` | float (Ω) | Nominal bridge resistance (typically 350 Ω) |
| `<firstElectricalValue>` | float (mV/V) | First calibration electrical value |
| `<secondElectricalValue>` | float (mV/V) | Second calibration electrical value |
| `<firstPhysicalValue>` | float | Physical value at first electrical point |
| `<secondPhysicalValue>` | float | Physical value at second electrical point |
| `<electricalUnits>` | `mVolts/Volt` | Electrical units |
| `<units>` | `Pounds` | Engineering units for the physical output |

---

### `flowMeter` — Turbine Flow Meter

Read-only analog input. Configure like `pressure` using an analog input daqMx block.

### `VFD` — Variable Frequency Drive

Commandable. Specific daqMx configuration TBD (see `<details>` comment in `<controlListTemplate>`).

### `tank` — Tank

Type is defined but not currently documented in the template. Usage follows the same channel pattern as other sensor types.

---

## Channel Fields

Each `<channel>` inside a `<channels>` block:

| Field | Required | Description |
|---|---|---|
| `<refDes>` | Yes | Channel-level reference designator (e.g. `NV-03-CMD`, `OPT-01`). Convention: parent refDes + suffix |
| `<role>` | Yes | See [Channel Roles](#channel-roles) below |
| `<description>` | No | Optional per-channel label |
| `<modelNumber>` | Yes | Hardware model number; use `N/A` if unknown |
| `<serialNumber>` | No | Hardware serial number |
| `<refDesDaq>` | Yes | Reference to the DAQ node this channel lives on (e.g. `DAQ001`) |
| `<moduleModelNumber>` | Yes | NI module name (e.g. `Thermocouple`, `Analog-Input`, `Digital-IO`) |
| `<channelNumber>` | Yes | Module-relative channel identifier (e.g. `ai02`, `/port3/line0`) |
| `<daqMx>` | Yes | DAQmx task configuration — see type-specific fields above |
| `<validMin>` | No | Lower bound in engineering units. Values below this threshold are flagged as bad data in the WebClient (red LED + red value text). Omit or leave empty to disable the lower bound check. |
| `<validMax>` | No | Upper bound in engineering units. Values above this threshold are flagged as bad data. Omit or leave empty to disable the upper bound check. |

**Bad data detection example** — flag a 4–20 mA pressure transducer as bad when the converted output falls below 0 psi (sensor wire fault) or above 1500 psi (over-range):

```xml
<channel>
    <refDes>OPT-01</refDes>
    <role>sensor</role>
    ...
    <validMin>0</validMin>
    <validMax>1500</validMax>
</channel>
```

The control node passes `validMin` and `validMax` to the browser as part of the `config` message. The Channel List tab's LED turns red and the value text turns red when a live reading falls outside the configured range. If no data has been received recently the LED turns amber (stale) regardless of range.

---

## Channel Roles

The `<role>` field controls how the WebClient renders the channel:

| Role value | UI widget | Notes |
|---|---|---|
| *(empty)* | Read-only display | LabVIEW converts empty → `"sensor"` before sending JSON |
| `sensor` | Read-only display | Explicit read-only; same result as empty |
| `cmd-bool` | OPEN/CLOSE buttons | Sends `1` (on/open) or `0` (off/close) |
| `cmd-pct` | Slider (0–100%) | Sends a percentage setpoint |
| `cmd-float` | Number input field | Sends an arbitrary float |

---

## daqMx Summary by Signal Type

| Signal type | Required fields | Module examples |
|---|---|---|
| Thermocouple | `taskName`, `type` (K/E/T), `units` | `Thermocouple` |
| Analog input | `taskName`, `sensitivity`, `balance`, `inputTerminalConfiguration`, `units` | `Analog-Input` |
| Analog output | `taskName` | `Analog-Output` |
| Bridge completion | `taskName`, `bridgeConfiguration`, `voltageExcitationSource`, `excitationVoltage`, `nominalBridgeResistance`, cal values, `units` | `Bridge-Completion-2` |
| Digital output | `taskName` | `Digital-IO`, `Analog-Output` |
| Digital input | `taskName` | `Digital-IO` |

---

## How to Add a New Channel

1. **Choose the control type** — pick the `<type>` that matches your hardware
2. **Copy an existing entry** of the same type from the file as a starting point
3. **Set `<refDes>`** — must be unique across the entire `<controlList>`
4. **Set `<enabled>true`** — set to `false` to hide from the WebClient without deleting the entry
5. **Configure `<channels>`** — one `<channel>` per physical signal
   - Set `<role>` to the appropriate command/sensor role
   - Set `<refDesDaq>` to the target DAQ node (e.g. `DAQ001`)
   - Set `<moduleModelNumber>` and `<channelNumber>` to match the physical wiring
   - Fill in the `<daqMx>` block using the [daqMx Summary](#daqmx-summary-by-signal-type) table
6. **Save the file** — the control node reads it on startup; restart the control node to pick up changes
7. **Verify in the WebClient** — connect and confirm the new channel appears in the Channel List or can be found via graph channel search

---

## Front Panels

The `<frontPanels>` section lists P&ID layout YAML files that the control node reads from disk and sends to every new browser connection as `pid_layout` WebSocket messages.

```xml
<frontPanels>
    <panel>
        <name>LOX Panel</name>
        <file>lox_panel.yaml</file>
        <enabled>true</enabled>
    </panel>
    <panel>
        <name>Fuel Panel</name>
        <file>fuel_panel.yaml</file>
        <enabled>true</enabled>
    </panel>
</frontPanels>
```

| Field | Required | Description |
|---|---|---|
| `<name>` | Yes | Display name shown in the Front Panel tab's layout picker |
| `<file>` | Yes | Path to the YAML file, relative to the XML config file's directory |
| `<enabled>` | Yes | If `false`, the panel is skipped — not read or sent to browsers |

YAML layout files are created and downloaded from the Front Panel editor in the WebClient, then added to the repository and referenced here. See [webclient-guide.md](webclient-guide.md) for the save workflow.

---

## Naming Conventions

| Prefix | Type | Example |
|---|---|---|
| `OPT` | LOX pressure transducer | `OPT-01` |
| `FPT` | Fuel pressure transducer | `FPT-01` |
| `NPT` | Nitrogen pressure transducer | `NPT-01` |
| `OT` | LOX thermocouple | `OT-01` |
| `FT` | Fuel thermocouple | `FT-01` |
| `NV` | Nitrogen valve | `NV-03` |
| `OV` | LOX valve | `OV-01` |
| `FV` | Fuel valve | `FV-01` |
| `LCC` | Load cell cluster | `LCC-01` |
| `FM` | Flow meter | `FM-01` |
| `IG` | Ignition circuit | `IG-01` |

Channel suffixes: `-CMD` (command), `-FB` (feedback), `-POS` (position), `-NEG` (negative/vent), `-01`/`-02`... (numbered sub-channels).
