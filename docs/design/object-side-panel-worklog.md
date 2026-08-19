# Object Side Panel — work log

Tracking doc for building out the object side panel. The design and the reasoning
behind each decision live in `docs/design/object-side-panel.html` (published as an
Artifact, with the decision threads in its comments). **This file is the status of
the build**, not the design; when the two disagree, the HTML is the design of
record and this file is wrong.

Started 2026-08-18.

## How the pieces fit

- `WebClient/js/objectSidebar.js` — the panel as it exists today (163 lines): a
  chart, every channel on the control, a regex box. Everything below either
  extends it or replaces part of it.
- `WebClient/js/` is the **source**. `controlnode/static/` is a mirrored copy the
  server falls back to when `-webroot` is not pointed at `WebClient/`. It is
  **gitignored**, so it never shows up in `git status` and is never committed —
  which means a change that only lands in `WebClient/` looks complete in the diff
  while the served copy is stale. Copy every touched file across, and do not
  expect git to remind you.
- Alerts are evaluated server-side in `controlnode/alerts/` (`config.go` parses
  the `.alert` DSL, `engine.go` evaluates, `registry.go` holds raised state). The
  browser only renders what the server publishes. Anything on this list that
  changes alert behaviour is a Go change first and a JS change second.
- The wire format is documented in `docs/websocket-protocol.md`, mirrored into
  `controlnode/webclient/embedded/websocket-protocol.md`. Protocol changes touch
  both, plus `controlnode/webclient/protocol_test.go`.

## Bugs

Reported 2026-08-18. Fix these before the menu items.

| # | Bug | Status |
|---|-----|--------|
| B1 | The P&ID **editor** sometimes renders a valve in a different spot from where the code thinks it is. Self-corrects on refresh or on any re-render, so it is a stale-render / transform desync rather than bad geometry. | not started |
| B2 | Pipes do not leave sensors (all four sides) or the machine "SM" block (top and bottom) perpendicular to the edge. | **fixed, awaiting validation** (`7d3933a`). Two causes. (1) The router snaps its first waypoint to the grid, so a port's held coordinate must already be a grid multiple; sensor/machine glyphs sat at `R+6=23` across and `H/2=32` down, neither a multiple of 20. Replaced by named `PID_OBJ.GX=20` / `CY=40`, and `pidObjectWidth` now rounds up to a grid multiple because a left-sided row puts its glyph at `width - GX`. (2) The `daqControl` port case still sized ports off a `gridW x gridH` box that stopped existing when the machine glyph became a bubble-and-text row — ports landed on empty space. Now mirrors the sensor branch, with `pidObstacleRects` following. **Visible side effect to check:** sensor and machine glyphs move 3px left and 8px down within their row. Valves are unchanged. **Regression found and fixed in `3126ae0`:** the first cut rounded *every* row width up to a grid multiple, which widened every obstacle rect by up to a full cell and broke routes on the test panel with "could not route without crossing an object". Only left-sided rows need it. **If routing still fails, the next suspect is the `daqControl` obstacle rect** — it changed from a vestigial 200x60 box to the real measured row, which is correct but genuinely makes machine objects larger obstacles than before, so pipes that used to route through a machine's text now have nowhere to go. That is an honest obstacle, not a bug; the fix would be moving the object or the pipe, not shrinking the rect. |
| B3 | Pinned actuation panels belong to the P&ID tab and correctly survive a tab switch, but they keep painting over the content of whatever tab you switch to. They should hide while P&ID is not the active tab and come back unchanged — still pinned, same binding — on return. | **closed** (`fc39256`) — user-validated 2026-08-18. — the panels are `position:fixed` on `document.body`, not inside a tab pane, so `activateTab`'s display toggle never reached them. Panels now carry their opening tab's id and `activateTab` asserts visibility. |

## Menu items

Numbering matches the design doc and is deliberately stable — 10 and 13 are cut,
not renumbered, so a reference to an item number means the same thing in the doc,
in the Artifact comment threads, and here.

### Make it behave like the rest of the panel

| # | Item | Status |
|---|------|--------|
| 01 | Glow the object while its panel is open — wire the existing `.selected` class on open, clear on close | not started |
| 02 | Esc and ✕ close it; a right-click on another object retargets rather than dismisses. **No pin** — side panels do not pin | not started |
| 03 | Catch a missing control in the editor, not at right-click. Runtime stays silent; at load time walk the panel's declared controls and warn per key with no config entry, naming the control id and its location | not started |
| 04 | Send aggregates, not every point — server-side per-bucket **min/max/last** at the requested resolution, raw points only for short windows | not started |
| ~~10~~ | *Cut.* Command block in the panel — commanding stays in the command panel, one route to a command | — |

### Show what the object is doing

| # | Item | Status |
|---|------|--------|
| 05 | Live readings table under the chart — every channel with current value, units, per-object decimals | not started |
| 06 | State line in the header — `LIVE / STALE / NO DATA / UNBOUND / ALARMING` as a pill, same vocabulary as the glyph | not started |
| 07 | Raised alerts as rows, **no inline Ack**; a row opens a second right-side panel scoped to the alarm (plot, time-in-alarm, long description, Ack / Reset / Suppress) | not started |
| 07a | A long description on the alert definition — optional `describe "…"` beside `message` in `config/alerts/alerts.alert`, same placeholder interpolation, rendered in the alarm panel | not started |
| 07b | Somewhere to see what is suppressed — filter the alerts list by state, keep a suppressed count always visible, greyed row with a `Suppressed · until restart` pill | not started |
| 08 | Which node owns it, and is that node up — `DAQ001 · connected 4m` in the header | not started |
| 09 | A `channels` section on the alert declaring what its plot draws (`plot <ch>`, `line <value-or-channel> "<label>"`); the `bad_data` template becomes one consumer, emitting lines at validMin/validMax | not started |

### Let the operator act

| # | Item | Status |
|---|------|--------|
| 11 | Promote a channel to the Graph tab | not started |
| 12 | Copy the **channel name** (not the refDes) from the readings table | not started |

### Chart

| # | Item | Status |
|---|------|--------|
| 14 | Plot `SM-<NAME>-STATE` as a step trace and render the index through `state_config` in the tooltip, so it reads `autoSequence` not `3` | not started |
| 15 | Command and feedback as a pair — CMD and FB on one axis so lag is a visible gap | not started |
| 16 | Freeze — stop the window advancing so a reading can be studied | not started |
| ~~13~~ | *Cut.* Time presets — scroll-zoom is enough | — |

## Decisions already made

These were settled in the Artifact comment threads. Do not re-litigate them
without going back to the user.

- **Side panels never pin.** Pinning exists on the command panels; the side panel
  does not get it.
- **A control on the panel but not in the config fails silently at runtime.** The
  editor is where that gets caught, at load time.
- **Rebuilding the chart on every open is acceptable.** The refill is slow because
  it ships raw points, so the fix is item 04 on the wire, not a client-side cache.
  Do not build a warm-chart cache.
- **Suppress is rule-level and volatile** — the rule stops evaluating until
  someone unsuppresses it or the control node restarts.
- **The valid band is not a feature of its own.** It is one alert declaring what
  its plot needs (item 09).
- **State transitions are a channel, not an annotation layer** (item 14), so they
  pan, zoom, bucket and export like everything else.

## Open questions

Raised but not answered. Each blocks part of an item.

1. **Does suppressing a currently-raised rule also resolve the standing alert?**
   Leaving it standing keeps the record honest, but then Suppress alone does not
   clear the red — and an operator will press it expecting exactly that. Blocks
   the Suppress half of 07.
2. **`channels` on the `every_daqnode` template needs `{refDes}` interpolation**,
   since the template does not statically know which channel tripped. Blocks the
   template half of 09.
3. **What does an alert with a multi-channel condition plot by default?**
   Suggested: make the `channels` section optional and fall back to the channels
   named in the condition, so simple alerts need no config. Blocks 09.
4. **Attribution does not exist.** `controlnode/alerts/registry.go` records only
   `acked` and `resolved` — no operator identity, and the browser connects
   anonymously. "Acked by" and "suppressed by" cannot be built without adding
   identity first. Affects 07 and 07b.

## Later — raised, not scheduled

| # | Item |
|---|------|
| L1 | **A viewer for data-stream cadence.** Watching the console log, the data stream does not look constant. Build something that shows whether it actually is, rather than judging it by eye from a scrolling log. |

Notes for whoever picks up L1:

- **The console is not a fair instrument for this, and may be the whole finding.**
  `logConsole` in `WebClient/js/console.js` stamps `Date.now()` on arrival, which
  is the right moment — but it then appends every entry synchronously to the DOM
  for every open console tab. At any real sample rate that is enough layout work
  to stutter the main thread, and a stuttering main thread makes a steady socket
  *look* bursty. Rule that out before concluding the stream itself is irregular.
- The default buffer is 500 entries (`CONFIG.consoleBufferLimit`,
  `WebClient/js/state.js`), so at a few hundred messages a second the visible
  window is a couple of seconds. Judging constancy from that window is judging it
  from a keyhole.
- `Date.now()` is millisecond resolution. For anything at or above ~100 Hz,
  timestamps want `performance.now()` instead, or the quantisation itself shows up
  as fake jitter.
- What the viewer should actually show: inter-arrival time per channel — not just
  aggregate message rate — as a histogram or a strip of gaps over time, plus the
  count of intervals beyond some multiple of the expected period. "Constant" is a
  claim about the tail, not the mean, so a mean rate of 100 Hz tells you nothing
  about whether it ever stalled for 300 ms.
- Worth separating three candidate causes before designing the fix: the DAQ node
  not sampling evenly, the control node batching or coalescing before it
  publishes, and the browser being too busy to service the socket promptly. Those
  are three different bugs and the viewer should be able to tell them apart —
  which probably means timestamping at the source and carrying that through,
  rather than only measuring arrival in the browser.
- This interacts with item 04: once history is bucketed server-side, arrival
  cadence and sample cadence stop being the same question, and a viewer that
  conflates them will mislead. Build it before 04 lands, or make it explicit
  about which of the two it is measuring.

## Known flakes

- `controlnode/integration` `TestStateUpdateUndeliverableWhileDisconnected` failed
  once inside a full-suite run and passed alone, then passed three consecutive
  times under `-race`. Timing-sensitive and unrelated to the panel work, but it
  is real — do not assume a one-off failure there is your change.

## Notes for a future session

- Item 14 needs nothing new on the wire: `SM-<NAME>-STATE` already publishes the
  state index in `data`, and `state_config` already ships `states[].name` with
  `states[].index`. The enum-to-name rendering in the tooltip is the only new
  work. The trace must **step, not interpolate** — a linear segment between index
  3 and index 4 draws a state that never existed — it needs its own axis, and its
  bucketing wants last-per-bucket rather than min/max.
- Item 09's `line` value should be allowed to be a **channel**, not only a
  literal. `LIM-CPT01-HIGH` is operator-settable, so a drawn limit that reads the
  channel moves when the operator moves it and cannot disagree with the rule that
  fired.
- Item 04 and item 14 interact: state indices want last-per-bucket, ordinary
  analogue channels want min/max-per-bucket. Bucketing needs to be per-channel,
  not one global policy.
- Item 04 and the cut of item 13 interact: with no time presets, the aggregate
  request resolution has to come from the current pan/zoom window. That is the
  right dependency — bucket size follows what is actually on screen.
