# WebClient User Guide

The WebClient is a browser-based interface for monitoring sensor data and sending commands to the TC3 test stand.

---

## Opening the Client

The client is a static HTML file — no web server is required.

1. Navigate to `WebClient/` in the repository
2. Open `index.html` directly in a browser (double-click, or `File → Open` in browser)
3. The URL bar will show something like `file:///C:/path/to/WebClient/index.html`

> **No internet required.** Chart.js is bundled locally at `js/chart.umd.min.js`.

---

## Connecting to the Test Stand

1. In the **connection bar** at the top of the page, enter the IP address or hostname of the PXIe chassis
   - Example: `192.168.1.100` or `tc3-pxie.local`
   - If opening the client on the chassis itself, `localhost` is used automatically
2. Click **Connect**
3. The status indicator shows the connection state:

| Indicator | Meaning |
|---|---|
| Blinking yellow | Connecting / reconnecting |
| Green | Connected and receiving data |
| Red | Disconnected |

When connected, LabVIEW sends a `config` message and the interface populates automatically. If the connection drops, the client reconnects automatically with exponential backoff (1 s → 2 s → 4 s → 8 s → 10 s max).

---

## Tab System

The interface is organized into **tabs**, similar to a browser or VS Code.

### Managing Tabs

| Action | How |
|---|---|
| Add tab | Click **+** (always opens as Front Panel) |
| Change tab type | Right-click the tab → select type from context menu |
| Rename tab | Double-click the tab name → type new name → press Enter |
| Close tab | Hover the tab → click **✕** |

### Tab Types

| Type | Description |
|---|---|
| **Front Panel** | Default / placeholder — reserved for a future P&ID overlay |
| **Data View** | Live sensor readings + command controls |
| **Graph** | Adjustable grid of Chart.js line charts |
| **Console** | Raw WebSocket message log |
| **Dev** | Connection statistics and browser memory usage |

Multiple tabs of the same type can be open simultaneously and update independently.

---

## Data View Tab

The Data View tab is split into two sections:

### Command Cards (top)

Each commandable control (valves, ignition, bang-bang, VFD, digital outputs) appears as a card. The UI widget depends on the channel's role:

| Role | Widget |
|---|---|
| `cmd-bool` | OPEN / CLOSE buttons (or ON / OFF for non-valves) |
| `cmd-pct` | Slider with 0–100% range |
| `cmd-float` | Number input field |

Click or interact with the widget to send a command. Commands are sent immediately over WebSocket as a `cmd` message.

### Sensor Table (bottom)

Read-only sensors (pressure transducers, thermocouples, load cells, flow meters) are displayed as a live-updating table. Values go **stale** (dimmed) if no data has been received within 500 ms.

---

## Graph Tab

The graph tab displays Chart.js line charts with a rolling **15-minute data buffer** per channel.

### Grid Layout

- Right-click inside the graph area to adjust the grid dimensions (rows × columns)
- Grid size: 1–4 rows, 1–8 columns

### Adding Channels to a Cell

1. Click a graph cell to select it
2. Type a **channel refDes** or **regex pattern** in the search box (e.g. `OPT-.*` matches all LOX pressure channels)
3. Matching channels appear in a list — click to add them to the cell

### Per-Channel Controls

| Control | Description |
|---|---|
| Color swatch | Click to open a color picker |
| Eye icon | Toggle channel visibility (hide/show without removing) |
| ✕ button | Remove channel from the cell |

### Chart Behavior

- Charts update at **2 Hz** (every 500 ms) regardless of the 20 Hz WebSocket stream rate
- Data is buffered in memory; the buffer is cleared when a channel is removed from all cells
- X-axis shows a rolling time window; Y-axis auto-scales to the data range

---

## Console Tab

The Console tab logs all incoming and outgoing WebSocket messages in real time.

| Control | Description |
|---|---|
| **Filter toggle** | When enabled, hides high-frequency `data` messages (default: on) |
| Scroll area | Chronological message log with timestamps and direction indicators |

The buffer holds the last **500 messages** (configurable in `app.js` via `CONFIG.consoleBufferLimit`). Older messages are discarded as new ones arrive.

---

## Dev Tab

The Dev tab shows live connection diagnostics.

| Stat | Description |
|---|---|
| Endpoint | WebSocket URL currently connected to |
| Uptime | Time since connection was established |
| Messages received | Total count since connect |
| Message rate | Average messages per second over the last window |
| Missed cycles | Count of gaps > 1.5× expected interval between `data` messages |
| Browser memory | JS heap usage (Chrome only; not available in other browsers) |

Stats refresh every **2 seconds**.

---

## Operator Name

Some builds include a name field — enter your name before sending commands. The name is included in the `cmd` message payload for LabVIEW to log, but there is no server-side authentication. Any name or blank is accepted.
