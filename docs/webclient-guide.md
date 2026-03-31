# WebClient User Guide

The WebClient is a browser-based interface for monitoring sensor data, sending commands, and viewing P&ID front panel layouts for the TC3 test stand.

---

## Opening the Client

The control node serves the WebClient at `http://<chassis-hostname>:8000`. You can also open `WebClient/index.html` directly in a browser via `file://` for local development.

> **No internet required.** Chart.js is bundled locally at `js/chart.umd.min.js`.

---

## Operator Name

Before any commands can be sent, you must enter an operator name:

1. Click the **person icon** (top-right of the header)
2. Type your name in the popover field — the icon turns highlighted once a name is set
3. All outgoing `cmd` messages include the `user` field for logging on the DAQ node

Command buttons, sliders, and inputs are **disabled** until a name is entered. Live data is always visible regardless.

---

## Connecting to the Test Stand

The client connects automatically on load. The WebSocket URL is derived from the page's hostname (`ws://<hostname>:8000`), defaulting to `localhost` when opened via `file://`.

The status indicator shows the connection state:

| Indicator | Meaning |
|---|---|
| Blinking blue | Connecting |
| Blinking amber | Reconnecting (exponential backoff) |
| Green | Connected and receiving data |
| Red | Disconnected |
| Amber (steady) | Connected but data stream has gone stale |

On connect, the control node sends a `config` message (all controls) and one `pid_layout` message per configured front panel. If the connection drops, the client reconnects automatically (1 s → 2 s → 4 s → 8 s → 10 s max).

---

## Tab System

The interface is organised into **tabs**, similar to VS Code.

### Managing Tabs

| Action | How |
|---|---|
| Add tab | Click **+** — always opens as Front Panel |
| Change tab type | Right-click the tab → select from context menu |
| Rename tab | Click the active tab name → type → Enter |
| Close tab | Hover the tab → click **✕** |

Tab state is **not** saved between page refreshes — every load opens a single fresh Front Panel tab.

### Tab Types

| Type | Description |
|---|---|
| **Front Panel** | Interactive P&ID editor and viewer |
| **Channel List** | Per-channel live data rows with LEDs and sparklines |
| **Graph** | Adjustable grid of Chart.js line charts |
| **Console** | Raw WebSocket message log with filters |
| **Dev** | Connection statistics and developer tools |

Multiple tabs of the same type can be open simultaneously and update independently.

---

## Front Panel Tab

The Front Panel tab displays a P&ID layout with live sensor values. Layouts are defined as YAML files, stored in the repo, and sent by the control node on connect.

### Toolbar

| Control | Description |
|---|---|
| **Layout picker** (dropdown) | Select a layout to display; populated from `pid_layout` messages received on connect |
| **View / Edit** toggle | Switch between view mode and edit mode |
| **Save YAML** | Download the current layout as a `.yaml` file |

### View Mode

- Objects show their bound channel's live value and units
- Values turn **amber** when stale (no data received)
- Junction nodes are hidden; grid is hidden

### Edit Mode

**Left sidebar — object palette:**

Drag an object type from the sidebar onto the canvas to place it:

| Object | Description |
|---|---|
| **Sensor** | Text box showing a live value; binds to one sensor channel |
| **Node** | Junction point; connects pipes in up to 4 directions |

**Canvas interaction:**

| Action | How |
|---|---|
| Move object | Click and drag |
| Start pipe connection | Click a port dot (appears on objects in edit mode) |
| Complete connection | Click a port dot on another object |
| Cancel connection | Press **Escape** |
| Select object | Click it (or right-click) |
| Delete selected object | Press **Delete** or **Backspace** — also removes its connections |

Objects snap to a **20 px grid**.

**Right sidebar — object config:**

Right-click any object to select it and open its configuration in the right sidebar:

- **Sensor:** pick a channel refDes from the dropdown (populated from the live config) or type one manually; optionally override the units label; click **Apply**
- **Node:** shows a description only; can be removed with the **Remove** button

When nothing is selected, the right sidebar shows the **layout name** field (used as the YAML filename on save).

**Saving a layout:**

1. Set the layout name in the right sidebar (no object selected)
2. Click **Save YAML** — a `.yaml` file downloads to your computer
3. Add the file to the repo and reference it in `nodeConfigs_0.0.2.xml` under `<frontPanels>` — see [xml-config-reference.md](xml-config-reference.md) for details

---

## Channel List Tab

The Channel List tab shows live data for individually selected channels.

### Adding Channels

Type a channel refDes or regex pattern in the search bar (e.g. `OPT-.*` matches all LOX pressure channels). Matching channels appear in a dropdown — click one to add a row.

### Row Layout

Each row contains:

| Section | Content |
|---|---|
| **LED** | Status indicator — see states below |
| **Left** | refDes + control description |
| **Centre** | 15-second sparkline |
| **Right** | Live value + units (sensor) or command input + Send button (commandable) |
| **✕** | Remove this row |

### LED States

| LED | Meaning |
|---|---|
| Green | Live data received; value within valid range |
| **Red** | Live data received; value outside `validMin` / `validMax` configured in XML |
| Amber | No data received in the last 2 seconds (stale) |

Valid-range bounds are optional per channel — channels without configured bounds never show red.

---

## Graph Tab

The Graph tab displays Chart.js line charts with a rolling **15-minute data buffer** per channel.

### Grid Layout

Click the **grid size button** (e.g. `1 × 1`) in the toolbar to open a preset picker. Supported sizes: 1–4 rows × 1–8 columns.

### Adding Channels to a Cell

1. Type a channel refDes or regex in the search box at the bottom of a cell's left panel
2. Matching channels appear in a dropdown — click to add

### Per-Channel Controls

| Control | Description |
|---|---|
| **Y-axis badge** | Left-click to cycle up, right-click to cycle down through 6 independent Y-axes |
| **Color swatch** | Click to open a color picker |
| **Channel name** | Click to toggle visibility (hide/show without removing) |
| **× button** | Remove channel from cell |

### Chart Behavior

- Charts update at the broadcast rate (default 2 Hz render, 20 Hz buffer)
- X-axis: rolling time window; scroll to zoom (30 s – 20 min), anchored to cursor
- Y-axis: auto-scales unless locked (y-axis lock not yet implemented)
- **Auto-scroll:** the chart follows live data; scrolling left pauses it; it snaps back to live when the view edge is within 5% of the right boundary

---

## Console Tab

The Console tab logs all incoming and outgoing WebSocket messages in real time.

### Filters (Row 1)

Toggle buttons — click to enable/disable each filter. All filters combine:

| Group | Buttons | Default |
|---|---|---|
| **Dir** | `← in`, `→ out` | both active |
| **Type** | `data`, `config`, `cmd`, `other` | all active |

### Text Search (Row 2)

Free-text filter matches against the full serialised JSON of each message (case-insensitive). Wrap the pattern in `/…/` to use a regex:

- `NV-03` — show any message containing `NV-03`
- `/OPT-0[12]/` — regex match

### Buffer

The last N messages are kept (default 500, configurable in the row 2 input). Older messages are discarded as new ones arrive. Click **Clear** to empty the current view.

---

## Dev Tab

The Dev tab shows live connection diagnostics and developer tools.

### WebSocket Stats

| Stat | Description |
|---|---|
| Endpoint | WebSocket URL |
| State | `CONNECTING` / `OPEN` / `CLOSING` / `CLOSED` |
| Uptime | Time since connection was established |
| Messages received | Total count since connect |
| Message rate | Messages per second (averaged over last 2 s window) |
| Missed data cycles | Gaps > 2.5× the expected data interval |

Stats refresh every **2 seconds**.

### Browser Memory *(Chrome only)*

JS heap used / total via `performance.memory`. May show a clamped minimum value depending on Chrome's privacy settings.

### Dev Mode

Enable **Dev mode** (checkbox) to reveal:

| Control | Description |
|---|---|
| **Force reconnect** | Immediately closes the current socket and reconnects, bypassing the backoff timer |
| **Start / Stop Sim** | Toggles the built-in data simulator (generates fake sensor values without a real control node) |
