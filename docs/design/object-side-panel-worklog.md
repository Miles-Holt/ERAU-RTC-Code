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
  **Correction, 2026-08-19:** a fresh clone does not merely serve a stale
  `static/` — `go build`/`go vet`/`go test` fail outright on a fresh clone,
  every package, with `embed.go:8:12: pattern static: no matching files
  found`. `//go:embed static` needs the directory to exist with at least one
  file in it at compile time; a *missing* directory (not just an empty one)
  is a hard compile error, not a runtime fallback. Fix: `cp -r ../WebClient
  static` from `controlnode/` (same thing `build.bat`'s step 3 does). Still
  gitignored, still untracked, still nothing to diff against — this is
  build-time plumbing, not a step in shipping any item — but it has to exist
  before `go build ./...` will even list the module's packages, so do it
  once per fresh clone/session before trusting a "clean build" claim.
- Alerts are evaluated server-side in `controlnode/alerts/` (`config.go` parses
  the `.alert` DSL, `engine.go` evaluates, `registry.go` holds raised state). The
  browser only renders what the server publishes. Anything on this list that
  changes alert behaviour is a Go change first and a JS change second.
- The wire format is documented in `docs/websocket-protocol.md`, mirrored into
  `controlnode/webclient/embedded/websocket-protocol.md`. Protocol changes touch
  both, plus `controlnode/webclient/protocol_test.go`.
- As of item 04's server half, there's a THIRD source of channel data besides
  the `data` websocket stream: `GET /api/history` (Part 3 of the protocol doc),
  server-side bucketed history from `controlnode/history.Store`. `channelBuffers`
  (WebClient/js) does not fetch from it yet — that's the client half, not yet
  built. Once it is, `channelBuffers` stops being purely a live-stream buffer.

## Bugs

Reported 2026-08-18. Fix these before the menu items.

| # | Bug | Status |
|---|-----|--------|
| B1 | The P&ID **editor** sometimes renders a valve in a different spot from where the code thinks it is. Self-corrects on refresh or on any re-render, so it is a stale-render / transform desync rather than bad geometry. | not started |
| B2 | Pipes do not leave sensors (all four sides) or the machine "SM" block (top and bottom) perpendicular to the edge. | **PARKED 2026-08-18 — do not merge as-is.** The angle fix works (user-confirmed), but the test panel still reports "could not route without crossing an object" on routes that used to build. Commits `7d3933a` + `3126ae0` are on `side-panel-buildout` only; `main` is clean. Two causes. (1) The router snaps its first waypoint to the grid, so a port's held coordinate must already be a grid multiple; sensor/machine glyphs sat at `R+6=23` across and `H/2=32` down, neither a multiple of 20. Replaced by named `PID_OBJ.GX=20` / `CY=40`, and `pidObjectWidth` now rounds up to a grid multiple because a left-sided row puts its glyph at `width - GX`. (2) The `daqControl` port case still sized ports off a `gridW x gridH` box that stopped existing when the machine glyph became a bubble-and-text row — ports landed on empty space. Now mirrors the sensor branch, with `pidObstacleRects` following. **Visible side effect to check:** sensor and machine glyphs move 3px left and 8px down within their row. Valves are unchanged. **Regression found and fixed in `3126ae0`:** the first cut rounded *every* row width up to a grid multiple, which widened every obstacle rect by up to a full cell and broke routes on the test panel with "could not route without crossing an object". Only left-sided rows need it. **If routing still fails, the next suspect is the `daqControl` obstacle rect** — it changed from a vestigial 200x60 box to the real measured row, which is correct but genuinely makes machine objects larger obstacles than before, so pipes that used to route through a machine's text now have nowhere to go. That is an honest obstacle, not a bug; the fix would be moving the object or the pipe, not shrinking the rect. |
| B3 | Pinned actuation panels belong to the P&ID tab and correctly survive a tab switch, but they keep painting over the content of whatever tab you switch to. They should hide while P&ID is not the active tab and come back unchanged — still pinned, same binding — on return. | **closed** (`fc39256`) — user-validated 2026-08-18. — the panels are `position:fixed` on `document.body`, not inside a tab pane, so `activateTab`'s display toggle never reached them. Panels now carry their opening tab's id and `activateTab` asserts visibility. |

## Menu items

Numbering matches the design doc and is deliberately stable — 10 and 13 are cut,
not renumbered, so a reference to an item number means the same thing in the doc,
in the Artifact comment threads, and here.

### Make it behave like the rest of the panel

| # | Item | Status |
|---|------|--------|
| 01 | Glow the object while its panel is open | **done, awaiting validation** (`9f8548a`) — glow was already built and simply never switched on. |
| 02 | Esc and ✕ close it; a right-click elsewhere does not | **done, awaiting validation** (`9f8548a`) — the old document-level contextmenu listener never governed retargeting (object handlers stopPropagation); it only closed the panel on an empty-canvas right-click, which was the defect. |
| 03 | Catch a missing control in the editor, not at right-click. Runtime stays silent; at load time walk the panel's declared controls and warn per key with no config entry, naming the control id and its location | done — the problem-list mechanism (`computeEdProblems`/`pidSensorUnboundReason`/warn dropdown) landed in `3e83f68`; this pass added the grid position to each row's detail text so a dangling binding is identifiable without clicking every row |
| 04 | Send aggregates, not every point — server-side per-bucket **min/max/last** at the requested resolution, raw points only for short windows | **done, awaiting validation** — server half `f114425`, client half `3715e63`. Server: new `controlnode/history` package retains 25 min of raw samples per channel in memory, fed from `broker.Run`'s `dataIn` case at the DAQ's real sample rate; `GET /api/history?refDes=..&from=..&to=..&buckets=N` (repeatable `refDes`) buckets on request into min/max/last/n, omitting empty buckets, no separate raw-points code path (a short window naturally degenerates to 1-sample buckets). Discreteness classification (`last`-only vs min/max meaningful) mirrors `graphChannelIsDiscrete` (items 14/15) exactly, server-side, so client and server can never disagree about which channels are stepped. No auth, matching `/ws/data`. Documented in `docs/websocket-protocol.md` Part 3. Verified with `go test -race`, not just `-count=1`. Client: `ensureCellHistory`/`mergeHistoryBuckets` (graph.js) — per cell, batches every channel whose local buffer doesn't reach the visible window's start into one request, prepends whatever's strictly older than the earliest buffered point, never touches the live tail. Every bucket plots as `last` uniformly (min/max ride the wire, unused for now). Wired at all three places a graph cell is built — Graph tab, the side panel, and P&ID-embedded graph objects (`pid.js`'s `makeGraphGroup`) — since only two of those get item 14/15's treatment for free from shared functions. `CONFIG.graphBufferMinutes` raised 15→25 (`state.js`) to match server retention, a one-line prerequisite bundled in — without it the client would discard fetched history past 15 min on the very next live sample regardless of what the server now retains. No browser/Node available to run any of this — reasoned through by hand, including the merge's ordering/interleaving safety under a fetch racing a client-side trim. |
| ~~10~~ | *Cut.* Command block in the panel — commanding stays in the command panel, one route to a command | — |

### Show what the object is doing

| # | Item | Status |
|---|------|--------|
| 05 | Live readings table under the chart — every channel with current value, units, per-object decimals | **done, awaiting validation** — table renders under the chart, repainted from `channelBuffers` on the same interval as the DataView tab (`updateAllDataViews`/app.js), no timer of its own. Per-object decimals apply to the row for the specifically-clicked channel via `pidFormatValue`/`_sidebarFindPidObj`; other rows use its default. No browser available to confirm rendering. |
| 06 | State line in the header — `LIVE / STALE / NO DATA / UNBOUND / ALARMING` as a pill, same vocabulary as the glyph | **done, awaiting validation** — the pill reads the SAME classList the glyph is colored from (`st-live`/`st-nodata`/`st-stale`/`st-unbound`/`alarmed`, via a `MutationObserver` on the clicked object's `<g>`), so it cannot disagree with the glyph by construction rather than by re-deriving state independently. Shares the glow's known limitation: a config/layout reload rebuilds the SVG groups and orphans the observed node. |
| 07 | Raised alerts as rows, **no inline Ack**; a row opens a second right-side panel scoped to the alarm (plot, time-in-alarm, long description, Ack / Reset / Suppress) | **server half done** (`5ad7106`) — `Registry.Suppress`/`Unsuppress` (registry.go), `Record.Suppressed`/`SuppressedAt`. The load-bearing fix: `RaiseFor` now carries `Suppressed`/`SuppressedAt` forward from any existing record onto a fresh re-raise — without it, suppression would silently break on a rule's very first re-trigger, since `evalRules` only calls `RaiseFor` on the rising edge and always runs `Resolve`/`ResolveAndAck` first, guaranteeing every re-trigger builds a brand-new `Record`. Verified at both the registry-unit level and the ctrl-message-end-to-end level. **"Reset" was deliberately NOT built** — no design text, worklog entry, or comment thread anywhere gives it a meaning distinct from Ack; guessing at a control-action's behavior was judged not worth it. See the open question below. **Client half not started** — no "Raised" section on the object panel, no alarm detail panel yet. |
| 07a | A long description on the alert definition — optional `describe "…"` beside `message` in `config/alerts/alerts.alert`, same placeholder interpolation, rendered in the alarm panel | **server half done** (`5ad7106`) — new DSL token `TOK_DESCRIBE` (deliberately distinct from the pre-existing `TOK_DESCRIPTION`, a different keyword used by `.chan` files), optional (unlike `message`), same placeholder validation, flows through `RaiseFor`'s new `description` param onto `Record.Description`. Documented in `docs/dsl-guide.md`. **Client half not started** — nothing renders it yet. |
| 07b | Somewhere to see what is suppressed — a "View suppressed" filter plus a standing count; suppressed alerts have **no row** in the default list (not greyed — hidden) | **not started** — purely client-side (the server already has everything needed via `Record.Suppressed`); depends on 07's client half landing first. |
| 08 | Which node owns it, and is that node up — `DAQ001 · connected 4m` in the header | **not implemented — half the data doesn't exist on the wire.** Node *ownership* is genuinely published: `controlnode/config/yaml.go`'s `webclientChannel.Node` (`json:"node"`) is set from `ch.RefDesDaq` and reaches the browser as `channel.node` in the `config` message (the browser stores it in `channelIndex[refDes].ch.node` — `pid.js` already reads it for `isNodeAlarmed`). But *node up/down and "connected 4m"* have no wire representation at all: there is no node-status message, and `lastSeen`/`everConnected` (`controlnode/alerts/engine.go`) never leave the server. The only browser-visible proxy is a disconnect/reconnect **alert**, and that's not a substitute — it depends on a `disconnect`/`reconnect` rule actually being configured per node (engine_test.go: "no disconnect rule configured, so nothing should be raised"), it conflates connectivity with ack state (an acked disconnect alert reads as not-alarmed even if the node is still down), and it carries no duration. Building "connected 4m" from that would be guessing dressed up as a feature. Left unimplemented per instruction; would need a new node-status message (refDes, connected bool, since-timestamp) built from the engine's existing `lastSeen`/`everConnected` maps — a protocol change, not a client one. |
| 09 | A `channels` section on the alert declaring what its plot draws (`plot <ch>`, `line <value-or-channel> "<label>"`); the `bad_data` template becomes one consumer, emitting lines at validMin/validMax | not started |

### Let the operator act

| # | Item | Status |
|---|------|--------|
| 11 | Promote a channel to the Graph tab | **done, awaiting validation** (`cb1ecee`) — a hover-revealed button per readings-table row calls `promoteChannelToGraph` (graph.js): reuse the active tab if it's already a Graph tab, else the first existing one, else create one, add the channel to its cell 0, activate. No tab/cell picker — same "not worth the chrome" reasoning as the cut item 13. No browser available to confirm rendering or click behaviour. |
| 12 | Copy the **channel name** (not the refDes) from the readings table | **done, awaiting validation** — clicking a row in the new readings table copies `ch.refDes` (never the header's control-level refDes) via `navigator.clipboard`, with an `execCommand` fallback, and flashes the row green/red as confirmation instead of swapping its text. |

### Chart

| # | Item | Status |
|---|------|--------|
| 14 | Plot `SM-<NAME>-STATE` as a step trace and render the index through `state_config` in the tooltip | **done, awaiting validation** (`c96af5a`). Nothing new on the wire — the channel and state_config already carried what was needed. Own y-axis by default, tooltip falls back to the raw index when state_config hasn't arrived or the index is stale. |
| 15 | Command and feedback as a pair — CMD and FB on one axis so lag is a visible gap | **done, awaiting validation** (`c48c4fd`) — the "one axis" half was already true by construction (`graphDefaultYAxisId` only special-cases state channels); what was missing was stepped rendering. Generalised item 14's stepped-line check into `graphChannelIsDiscrete` (graph.js): true for state channels, any `cmd-bool` channel, and a valve's feedback channel when `ctrl.subType` says IO-FB (not POS-FB). Not runnable here — reasoned by hand against the four documented valve subTypes and the wire's 1/0 cmd-bool encoding. |
| 16 | Freeze — stop the window advancing so a reading can be studied | **done, awaiting validation** (`a07cc3e`) — per-cell toggle shared by the Graph tab and the sidebar chart (`toggleCellFreeze`/`returnCellToLive`/`_cellSyncLiveControls` in graph.js). Turning it on pins `cell.viewEnd` to the live edge if not already panned; turning it off just removes the guard that stops the existing "snap back within 10%" auto-live-follow from misfiring at the freeze moment. Also gave the sidebar chart its first-ever Now button — it never had one despite sharing the Graph tab's pan/zoom, which would have made Freeze a dead end there (see "Found while building" note below for why this was folded in rather than filed separately). Not runnable here — reasoned through by hand, including the drag/zoom/Now interaction paths. |
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
- **Suppress targets the alert, not the rule.** A rule and its alert are one
  identity already — `Registry.RaiseFor` (registry.go) keys by rule id and
  re-raising while the existing record is unacked/unresolved is a no-op — so
  a retrigger of a suppressed rule re-asserts the same suppressed alert rather
  than raising a new one. Suppressing clears the red immediately and the alert
  stays gone through any number of re-triggers, dropping out of every list
  except a "View suppressed" filter, until unsuppressed or the control node
  restarts. (Reverses an earlier rule-level version — see the open-questions
  note below for why.)
- **The valid band is not a feature of its own.** It is one alert declaring what
  its plot needs (item 09).
- **State transitions are a channel, not an annotation layer** (item 14), so they
  pan, zoom, bucket and export like everything else.

## Open questions

Raised but not answered. Each blocks part of an item.

1. ~~Does suppressing a currently-raised rule also resolve the standing alert?~~
   **Closed 2026-08-19.** Settled by moving Suppress to the alert instead of the
   rule: suppressing an alert clears its red immediately by definition, and the
   rule keeps evaluating underneath, so a fresh trigger raises a fresh alert.
   The question doesn't arise in the alert-level model.
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
5. **What does "Reset" do?** The design mockup's alarm panel shows an
   "ACK / RESET / SUPPRESS" button row, but no design text, comment thread, or
   worklog entry anywhere gives Reset a meaning distinct from Ack — it is a
   label with no specified behaviour. Not built (2026-08-19, item 07's server
   half) rather than guessed: this system controls a rocket test stand, and a
   wrong guess on a control action is a worse outcome than a missing button.
   The alarm panel ships with Ack and Suppress/Unsuppress only. Needs an
   answer from the user before it can be built at all.
6. **Should the existing bottom alert bar (`WebClient/js/alerts.js`) also open
   the new alarm panel when a row is clicked?** As specified, item 07's alarm
   panel is reachable ONLY from the object side panel's new "Raised" list —
   but not every alert is attributable to a single front-panel object with a
   right-click menu (a node-level connect/disconnect alert, or a rule whose
   condition spans channels on several different objects), so those alerts
   have no path at all to Suppress or the long description. Left as an open
   question rather than decided either way — see item 07's client-half work
   for where this was noticed.

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

## B2 — where to pick it up

The angular-exit fix is confirmed working. The outstanding problem is that
routes which used to build now fail. Two things make this hard to chase and
should be fixed first:

1. **There is no way to run the router outside a browser on this machine.**
   `pidRouter.js` is pure functions with no DOM and no globals but `PID`, so it
   is trivially testable — except Node is not installed (checked both the bash
   PATH and Windows `Get-Command`). Installing Node and writing a harness that
   loads `pidRender.js` + `pidRouter.js`, parses `config/test_panel.yaml`, and
   prints every connection that fails is maybe an hour of work and turns this
   from "ask the user to look" into a regression test. Do that before touching
   the routing code again — this regression reached the user precisely because
   it could not be exercised locally.
2. **The obstacle change for `daqControl` is a real behaviour change, not a
   bug.** It went from a vestigial 200x60 box to the measured row. Pipes that
   previously routed through a machine's name and reading will now correctly
   fail to route. Distinguish those from genuine regressions before "fixing"
   anything: shrinking the rect back would restore the old lie.

Suspects not yet ruled out, in order:

- `PID_OBJ.CY` moved the glyph from 32 to 40 within a 64px row, so the top port
  sits at `gridY*20 + 20` and the bottom at `+60`. Both are still inside the
  object's own obstacle rect (0..64), which the router skips for the endpoint's
  own component — but check that the *other* endpoint's rect, and any
  neighbouring object's rect, are not now clipping the first stub.
- `OBS_MARGIN` is 4px on every side. With the glyph 8px lower, the bottom port's
  20px stub now ends 4px from the rect edge instead of 12px away.
- Whether `_objW` is cached at the time the router first runs. If routing happens
  before the first render, every width comes from the fallback estimate, and the
  estimate and the measured width no longer have to agree.

## Adjacent work landed here

Not side-panel items, but done in the same push and worth knowing about.

- **`+=` / `-=` in the state-machine DSL** (`ea64494`). Real AST node
  (`CompoundAssignStmt`) rather than parse-time desugaring, because `format.go`
  re-emits from the AST and desugaring would rewrite a user's `+=` into longhand.
  Execution is in `statemachine/engine.go` and `sequence.go`, not `dsl/eval.go`,
  which only evaluates expressions.
- **Negative literals in `default` / `min` / `max`** (`ea64494`). The expression
  parser wraps a sign in a `UnaryExpr`, and those fields asserted a bare
  `*LiteralExpr`, so `default -60.0` was rejected outright. Folded for numeric
  operands only.
- **`controlnode/dsl/testdata/smokeTest.sm`** — one fixture covering the grammar,
  with a parse -> format -> reparse round-trip test. Deliberately in `testdata/`
  and NOT in `config/machines/`, which `main.go` loads wholesale at runtime.
- `shippedconfig_test.go` follows the `daq001.sm` -> `engineControl.sm` rename and
  stays pointed at live config on purpose: it exists to make a change to the real
  firing sequence fail loudly.

## Found while building, not fixed

- **`docs/websocket-protocol.md`'s channel field table omits `node`.** It IS
  published (`config.controls[].channels[].node`, from `webclientChannel.Node`
  in `config/yaml.go`) and already consumed by `pid.js`. Doc-only fix, unrelated
  to any item.


- **The channel search dropdown can't find a softchan.** `createChannelSearchDropdown`
  (`WebClient/js/utils.js`) only searches `configControls[].channels`, never
  `softchanConfigMap`. `SM-<NAME>-STATE` — and every other softchan — can only be
  charted via a saved graph-layout YAML or a P&ID graph object's configured
  `lines[]`, never by typing into the search box. Pre-existing, not introduced by
  item 14, and not specific to state channels. Worth its own small fix.

- **P&ID-embedded graph objects never got item 16's Freeze/Now buttons.**
  Found while wiring item 04's client half, which needed to reach all three
  places a "graph cell" gets built (`buildGraphCell`/`resizeGraphGrid`,
  `objectSidebar.js`, and `pid.js`'s `makeGraphGroup`) — items 14 and 15 reach
  all three for free because their logic lives inside functions all three
  already share (`addDatasetToChart`, `createCellChart`), but item 16's
  Freeze/Now buttons are per-cell UI that only got wired into the Graph tab
  and the side panel. `makeGraphGroup` already calls
  `attachDragPan`/`attachScrollZoom` on its cell, so a P&ID-embedded graph
  object can be panned away from live exactly like the other two, with no
  button saying so and no way back except dragging by hand. Introduced by
  item 16's own commit (`a07cc3e`), not by item 04. Small — same two buttons,
  same wiring pattern, third call site.

## Known flakes

- `controlnode/integration` `TestStateUpdateUndeliverableWhileDisconnected` failed
  once inside a full-suite run and passed alone, then passed three consecutive
  times under `-race`. Timing-sensitive and unrelated to the panel work, but it
  is real — do not assume a one-off failure there is your change.

## Pre-existing failures on this fresh clone (2026-08-19, not flakes — deterministic)

Both reproduce on every `go test -count=1 ./...` run with zero side-panel
changes present (no Go files touched yet at the point these were found), so
neither is caused by this branch's work. Recorded so a future session doesn't
burn time attributing them to whatever it just changed.

- **`controlnode/softchan` `TestSoftchan_SetDoesNotBlockReaders`** — fails
  every time in this container (`nproc` = 4), not intermittently: "readers
  were starved entirely during 2000 Sets". The test spins a reader goroutine
  against a tight non-blocking loop of 2000 back-to-back `Set` calls on the
  main goroutine and asserts the reader got scheduled at least once. Plausible
  cause is this sandbox's scheduler, not the store: a very small core count
  under a possibly-throttled/virtualized CPU can let a hot, allocation-light
  writer loop run to completion before the Go scheduler preempts it in favour
  of the reader goroutine, especially with GOMAXPROCS contention from other
  test packages. Did not touch `softchan/` to chase this — out of scope for
  every remaining item, and "make a starvation test pass by changing
  scheduling assumptions" is exactly the kind of fix that wants its own
  investigation, not a drive-by.
- **`controlnode/statemachine` `TestShippedConfig_*`** (`AutoSequenceSchedule`,
  `IgnitionOrdering`, `CutoffBeforeMainsRefused`) — all fail identically:
  `load ../../config/machines/engineControl.sm: ... no such file or
  directory`. The adjacent-work note above (`ea64494`) says these tests
  "follow the `daq001.sm` -> `engineControl.sm` rename", but `config/machines/`
  in this clone still only has `daq001.sm` — the rename referenced there
  happened on whichever machine has the "real config with local in-flight
  edits" the top-level instructions describe, and never landed in a commit on
  this branch (`git log --oneline -- config/machines` shows nothing). Per
  instruction, `config/` is off limits this session regardless — left
  untouched and unrenamed.

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
- ~~Item 04 and item 14 interact: state indices want last-per-bucket, ordinary
  analogue channels want min/max-per-bucket. Bucketing needs to be per-channel,
  not one global policy.~~ **Done, server half (`f114425`)** — `/api/history`'s
  per-refDes `discrete` classification (mirrors `graphChannelIsDiscrete`
  exactly) is exactly this per-channel policy: a discrete channel's `last` is
  the one meaningful value, same as items 14/15's stepped rendering already
  decided client-side. Still applies to the CLIENT half not yet built: when it
  merges a bucket into a chart line, it must plot `last` for a `discrete:true`
  channel, not average/interpolate min and max.
- Item 04 and the cut of item 13 interact: with no time presets, the aggregate
  request resolution has to come from the current pan/zoom window. That is the
  right dependency — bucket size follows what is actually on screen.
