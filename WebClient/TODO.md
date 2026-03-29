# WebClient TODO

See `CONTEXT.md` for full project/architecture context.

---

## Open

### Auth
- [ ] **Login / access control** — on load, prompt for a name (no password) before command widgets are enabled; store name in a session variable; attach `user` field to every outgoing `cmd` message (`{ type: "cmd", refDes, value, user }`); unauthenticated users can view all live data but all command buttons/inputs are disabled or hidden

---

### Tab System
- [ ] **Tab persistence** — currently resets to one Front Panel tab on every page load; consider saving tab layout (type + name) to `localStorage` so the workspace is restored on reload

---

### Front Panel Tab
- [ ] **P&ID background** — load a P&ID image or SVG as the canvas background; support multiple P&ID views selectable per tab (e.g. LOX panel, fuel panel, engine)
- [ ] **Front panel objects** — interactive overlay components (valve symbol, sensor readout, pipe segment, tank level) that sit on top of the P&ID; each object is bound to one or more `refDes` channels for live data and commands
- [ ] **Front panel interactive editor** — in-browser drag-and-drop editor to place, move, resize, and configure front panel objects on the P&ID canvas; object config (position, bound refDes, type) exported as JSON to user storage. the user can upload the JSON to the repo and will be sent by the websocket manager for each new webclient connection in the labview CTR node similar to the config JSON.

---

### Data View Tab
- [ ] **complete restructure** — rather than displaying all data, make this a top regex search bar where rows can be added below. Each row will be read only OR commanable depending on the type. each row shall include the refDes and description stacked vertically justified left, and a line graph of the current value including the previous 10 seconds with either the value readout or a commandable object justfied right.

---

### Graph Tab
- [ ] **data not collected when tab/window isnt focused**
- [ ] **data lines snap at chart boundary** — rather than smoothly entering/exiting the viewable x-range, line segments snap in/out at the chart edges; likely a Chart.js clipping issue with explicit `x.min`/`x.max` bounds

---

### Console Tab
- [ ] **Better filtering** — current filter is only a single "hide data messages" toggle; expand to support: filter by message direction (in / out), filter by `type` field value, and free-text / regex filter on the full serialized message string; filters should be combinable

---

### Dev Tab
- [ ] **Force reconnect button** — add a button that calls `connect()` immediately, bypassing the exponential backoff reconnect timer; useful during development or after a known restart
- [ ] **WebSocket stats** — the following are already implemented: connection state, endpoint URL, uptime timer, total messages received, message rate, missed cycle counter; verify these are accurate and updating correctly
- [ ] **Browser memory** — JS heap used / total via `performance.memory` (Chrome only); already implemented; hidden on non-Chrome browsers. not 100% sure this is working, always stuck at 10MB

---

## Done
- [x] **Offline Chart.js** — `chart.umd.min.js` bundled locally in `WebClient/js/`; no CDN dependency
- [x] **Fix graph grid** — canvas height now renders correctly; `maintainAspectRatio: false` + explicit flex constraints on `.graph-canvas` and `.graph-cell`
- [x] **Channel roles in XML** — added explicit `<role>` nodes to all `<channel>` elements in `nodeConfigs_0.0.2.xml`; cmd-bool assigned to all command channels; sensor assigned to all read-only channels
- [x] **Update channel roles in app.js** — replaced `role === 'cmd'` checks with `isCmd(ch)` helper covering `cmd-bool`, `cmd-pct`, `cmd-float`; widget type driven from channel role instead of control subType
- [x] **Tab system** — VS Code-style tab bar implemented; `+` adds Front Panel tab; right-click to change type (with clickable shortcuts in boot overlay); double-click to rename; ✕ to close; first tab of each type has no number suffix
- [x] **Front Panel tab** — placeholder implemented (empty canvas); boot-time overlay explains tab interactions with clickable type shortcuts
- [x] **Data View tab** — commandable controls rendered as cards (top section); sensor-only controls rendered as a live-updating table with refDes / description / value / units (bottom section)
- [x] **Graph tab** — adjustable grid (1–4 rows × 1–8 cols); per-cell regex channel search with body-appended dropdown (appears above search bar); Chart.js line charts; 15-minute rolling buffer per channel; left-side panel with channel list; up to 6 independent Y-axes (left-click badge to increment, right-click to decrement); custom themed color picker popup; scroll-to-zoom (30s–20min, anchored to cursor, smooth exponential); live-follow auto-scroll with 5s snap-back tolerance; relative time x-axis; proximity tooltip (28px threshold)
- [x] **Console tab** — live log of all WS messages; data messages hidden by default (toggle); configurable buffer limit; clear button
- [x] **Dev tab** — WS stats (endpoint, state, uptime, message count, rate, missed cycles) + browser memory (Chrome only)
