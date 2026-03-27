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
- [ ] **Front panel interactive editor** — in-browser drag-and-drop editor to place, move, resize, and configure front panel objects on the P&ID canvas; object config (position, bound refDes, type) saved to `localStorage` or exported as JSON

---

### Data View Tab
- [ ] **Redesign UI cards** — current cards are functional but visually basic; redesign layouts for each commandable control type (valve, bangBang, ignition, digitalOut); consider consistent sizing, status LED placement, and label hierarchy
- [ ] **Update UI colors** — revise color scheme in `css/style.css`; current palette works but hasn't been intentionally designed
- [ ] **Data View filtering** — allow each Data View tab instance to show a user-selected subset of controls (e.g. "LOX valves only", "all pressures"); filter by control type and/or manually include/exclude individual refDes entries

---

### Graph Tab
- [ ] **Fix graph grid** — canvas height not rendering correctly in some grid size configurations; `.graph-cell` uses flexbox but the canvas inside doesn't respect the available height; investigate `height: 100%` vs explicit pixel constraints on `.graph-canvas` and `.graph-cell`; Chart.js `maintainAspectRatio: false` is already set but the containing element may not have a defined height
- [ ] **Offline Chart.js** — Chart.js currently loaded from CDN (`cdn.jsdelivr.net`); test stand may not have internet; consider downloading and bundling `chart.umd.min.js` locally in `WebClient/js/`

---

### Console Tab
- [ ] **Better filtering** — current filter is only a single "hide data messages" toggle; expand to support: filter by message direction (in / out), filter by `type` field value, and free-text / regex filter on the full serialized message string; filters should be combinable

---

### Dev Tab
- [ ] **Force reconnect button** — add a button that calls `connect()` immediately, bypassing the exponential backoff reconnect timer; useful during development or after a known LabVIEW restart
- [ ] **WebSocket stats** — the following are already implemented: connection state, endpoint URL, uptime timer, total messages received, message rate, missed cycle counter; verify these are accurate and updating correctly
- [ ] **Browser memory** — JS heap used / total via `performance.memory` (Chrome only); already implemented; hidden on non-Chrome browsers

---

## Done

- [x] **Channel roles in XML** — added explicit `<role>` nodes to all `<channel>` elements in `nodeConfigs_0.0.2.xml`; cmd-bool assigned to all command channels; sensor assigned to all read-only channels
- [x] **Update channel roles in app.js** — replaced `role === 'cmd'` checks with `isCmd(ch)` helper covering `cmd-bool`, `cmd-pct`, `cmd-float`; widget type driven from channel role instead of control subType
- [x] **Tab system** — VS Code-style tab bar implemented; `+` adds Front Panel tab; right-click to change type; double-click to rename; ✕ to close; first tab of each type has no number suffix
- [x] **Front Panel tab** — placeholder implemented (empty canvas); boot-time overlay explains tab interactions
- [x] **Data View tab** — commandable controls rendered as cards (top section); sensor-only controls rendered as a live-updating table with refDes / description / value / units (bottom section)
- [x] **Graph tab** — adjustable grid (1–4 rows × 1–8 cols); per-cell regex channel search with dropdown; Chart.js line charts; 15-minute rolling buffer per channel; legend with color picker, click-to-hide, remove button
- [x] **Console tab** — live log of all WS messages; data messages hidden by default (toggle); configurable buffer limit; clear button
- [x] **Dev tab** — WS stats (endpoint, state, uptime, message count, rate, missed cycles) + browser memory (Chrome only)
