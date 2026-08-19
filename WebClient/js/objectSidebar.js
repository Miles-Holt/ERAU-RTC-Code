// =============================================================================
// Object Detail Sidebar
// =============================================================================
// Shown when the user right-clicks a sensor object in a Front Panel tab.
// Displays a graph pre-populated with ALL channels of the parent control,
// using the same chart infrastructure as the Graph tab.
// =============================================================================

const SIDEBAR_TAB_ID  = '__sidebar__';
const SIDEBAR_CELL_IDX = 0;

// The SVG group currently glowing to mark which object the panel is bound to.
// Exactly one object may glow at a time, so this is a single slot, not a set —
// openObjectSidebar clears it before adopting a new target and closeObjectSidebar
// clears it on the way out. Tracked here (rather than reading '.selected' back
// off the DOM) because a caller with no group at all — e.g. a graph object,
// see openObjectSidebarForGraph in pid.js — legitimately opens the panel
// without anything to glow.
let sidebarGlowEl = null;

// sidebarSetGlow retargets the glow to `el` (or clears it for `null`), leaving
// every other class on the outgoing/incoming groups untouched. It deliberately
// does not go through pidSetObjectState: that function also re-derives the
// st-live/st-stale/alarmed axes from arguments this module doesn't have, and
// calling it with placeholder values would clobber real state. 'selected' is
// layered on top of those axes (see pidSetObjectState's own comment), so
// toggling the class directly is the correct, narrower operation.
function sidebarSetGlow(el) {
    if (sidebarGlowEl && sidebarGlowEl !== el) sidebarGlowEl.classList.remove('selected');
    sidebarGlowEl = el || null;
    if (sidebarGlowEl) sidebarGlowEl.classList.add('selected');
}

// The MutationObserver behind the header pill (item 06). Same one-slot pattern
// as sidebarGlowEl: at most one object's classList is being watched at a time.
// Watching the object's OWN classList — rather than recomputing dataCond/alarmed
// from channelBuffers/alerts.js independently — is what makes "same vocabulary
// as the glyph" a guarantee instead of a hope: pidSetObjectState (pid.js,
// pidRender.js) is the one place st-live/st-nodata/st-stale/st-unbound/alarmed
// ever get written, so reading them back is reading the glyph's own answer, not
// a second opinion that could drift from it.
let sidebarPillObserver = null;

// The refDes and decimals of the specific channel the operator right-clicked,
// as opposed to every channel on its control (which the readings table also
// lists). Decimals are a property of the front-panel OBJECT, not the channel —
// see pidFormatValue's comment — so only the row for the clicked channel gets
// them; every other row in the table falls back to pidFormatValue's default.
let sidebarClickedRefDes = null;
let sidebarClickedDecimals = undefined;

// The channel/node lists the Raised section (item 07) is currently querying
// alertsFor() with — i.e. the currently-bound control's own attribution,
// same shape openObjectSidebar already computes for the readings table, just
// held onto so updateObjectSidebarRaised can re-run the same query on a live
// refresh without needing the DOM's `ctrl` object again.
let sidebarRaisedChannels = [];
let sidebarRaisedNodes    = [];

// Build the sidebar DOM once at load time and register it in graphState.
(function initObjectSidebar() {
    // ── Outer container ──────────────────────────────────────────────────────
    const el = document.createElement('div');
    el.id = 'object-sidebar';
    el.className = 'object-sidebar';
    el.style.display = 'none';

    // ── Header ───────────────────────────────────────────────────────────────
    const header    = mkEl('div', 'object-sidebar-header');
    const titleWrap = mkEl('div', 'object-sidebar-title');
    const refdesEl  = mkEl('span', 'object-sidebar-refdes');
    const descEl    = mkEl('span', 'object-sidebar-desc');
    titleWrap.append(refdesEl, descEl);
    const closeBtn = mkEl('button', 'object-sidebar-close', '×');
    closeBtn.title = 'Close';
    closeBtn.addEventListener('click', closeObjectSidebar);
    header.append(titleWrap, closeBtn);
    el.appendChild(header);

    // ── Status row: data-condition/alarm pill (item 06) ───────────────────────
    const statusRow = mkEl('div', 'object-sidebar-status');
    const pillEl    = mkEl('span', 'object-sidebar-pill');
    statusRow.appendChild(pillEl);
    el.appendChild(statusRow);
    el._statusRow = statusRow;
    el._pillEl    = pillEl;

    // ── Graph cell (column layout: chart on top, panel below) ────────────────
    const cellEl = document.createElement('div');
    cellEl.className = 'graph-cell graph-cell--column';

    // Chart area (top)
    const chartArea = mkEl('div', 'graph-chart-area');
    const canvas    = document.createElement('canvas');
    chartArea.appendChild(canvas);

    // Now / Freeze (item 16). buildGraphCell (graph.js) builds the same pair
    // for Graph tab cells; the sidebar cell is built inline here rather than
    // through that function, so it gets its own copy of the two buttons —
    // same markup and classes, wired directly below since `cell` already
    // exists in this scope (no two-pass build/wire split needed, unlike
    // resizeGraphGrid's grid-rebuild dance).
    //
    // The sidebar chart never had a Now button before this: it reuses the
    // same createCellChart/attachDragPan/attachScrollZoom as the Graph tab
    // (see the file banner comment), which means panning away from live was
    // already possible here too, just with no way back except dragging by
    // hand. Freeze is unusable without a way back to live, so adding Now
    // here is bundled into this item rather than filed separately — see the
    // worklog for the reasoning against leaving it as a silent gap.
    const nowBtn = mkEl('button', 'graph-now-btn', 'Now');
    nowBtn.title = 'Jump to live view';
    nowBtn.style.display = 'none';
    chartArea.appendChild(nowBtn);

    const freezeBtn = mkEl('button', 'graph-freeze-btn', 'Freeze');
    freezeBtn.title = "Stop the window advancing — click again, or Now, to resume";
    chartArea.appendChild(freezeBtn);

    cellEl.appendChild(chartArea);

    // ── Live readings table (item 05) ─────────────────────────────────────────
    // Sits between the chart and the "channels on the chart" management panel
    // below it — the chart answers "what has this been doing", this answers
    // "what is it doing right now", and the panel below manages what the chart
    // plots. Rows are built in openObjectSidebar (one per channel on the
    // control) and repainted by updateObjectSidebarReadings on the same redraw
    // tick as the Graph/DataView tabs (see app.js) rather than a timer of its
    // own.
    const readingsEl  = mkEl('div', 'object-sidebar-readings');
    const readingsLbl = mkEl('div', 'object-sidebar-readings-label', 'Now');
    const readingsRows = mkEl('div', 'object-sidebar-readings-rows');
    readingsEl.append(readingsLbl, readingsRows);
    cellEl.appendChild(readingsEl);
    el._readingsEl  = readingsEl;
    el._readingsRowsEl = readingsRows;
    el._readingRows = {};   // refDes -> { rowEl, valEl }

    // ── Raised alerts (item 07) ────────────────────────────────────────────
    // Sits between "what is it doing right now" (readings, above) and "what
    // can I do to it" (the channel-management panel, below) — matching the
    // mockup's stacking order. Rows only, no inline Ack (spec) — clicking a
    // row opens the alarm detail panel (alarmSidebar.js) scoped to that one
    // alert. Rebuilt wholesale on every open/refresh rather than diffed in
    // place — the same "acceptable for now" tradeoff the readings table and
    // chart already make (see the worklog), and for the same reason: a
    // small, bounded list, not a hot path.
    const raisedEl   = mkEl('div', 'object-sidebar-raised');
    const raisedLbl  = mkEl('div', 'object-sidebar-raised-label', 'Raised');
    const raisedRows = mkEl('div', 'object-sidebar-raised-rows');
    raisedEl.append(raisedLbl, raisedRows);
    cellEl.appendChild(raisedEl);
    el._raisedEl     = raisedEl;
    el._raisedRowsEl = raisedRows;
    el._raisedRows   = {};   // id -> rowEl

    // Channel panel (bottom): list + search bar
    const panel       = mkEl('div', 'graph-cell-panel');
    const channelList = mkEl('div', 'graph-channel-list');
    panel.appendChild(channelList);

    const searchWrap  = mkEl('div', 'graph-search-wrap');
    const searchInput = document.createElement('input');
    searchInput.type        = 'text';
    searchInput.placeholder = 'Add channel (regex)...';
    searchInput.className   = 'graph-search';
    searchWrap.appendChild(searchInput);
    panel.appendChild(searchWrap);
    cellEl.appendChild(panel);

    el.appendChild(cellEl);
    document.getElementById('tab-viewport').appendChild(el);

    // ── Chart (reuses same factory as Graph tab) ─────────────────────────────
    const chart = createCellChart(canvas);

    // ── Cell state object (matches graphState[tabId].cells[i] shape) ─────────
    const cell = {
        cellEl,
        chart,
        channels:      [],
        viewWindowSec: 60,
        viewEnd:       null,
        frozen:        false,
        nowBtn,
        freezeBtn,
    };
    nowBtn.addEventListener('click', () => returnCellToLive(cell));
    freezeBtn.addEventListener('click', () => toggleCellFreeze(cell));

    // Item 04: debounced per-cell history fetch. Built once here (no rebuild
    // path for this cell, unlike the Graph tab's resizeGraphGrid), so a plain
    // assignment is enough — well before openObjectSidebar ever calls
    // addChannelToCell on this cell (that only happens on a later right-click,
    // long after this IIFE has finished running).
    cell._debouncedEnsureHistory = debounce(() => ensureCellHistory(cell), 200);

    // Attach drag-pan, scroll-zoom and proximity tooltip — same as graph tab cells
    attachDragPan(canvas, cell);
    attachScrollZoom(canvas, cell);
    attachProximityTooltip(canvas, cell);

    // ── Search dropdown (appended to body for unambiguous fixed positioning) ──
    const searchDropdown = createChannelSearchDropdown(searchInput, {
        getExcluded: () => new Set(cell.channels.map(c => c.refDes)),
        onPick:      (refDes) => addChannelToCell(SIDEBAR_TAB_ID, SIDEBAR_CELL_IDX, refDes),
        position:    'above',
    });
    cellEl._dropdown = searchDropdown.dropdownEl;   // tracked for potential future cleanup

    // ── Store header element refs on the container for openObjectSidebar ──────
    el._refdesEl = refdesEl;
    el._descEl   = descEl;

    // ── Register in graphState ────────────────────────────────────────────────
    // This lets updateActiveGraphChannels() and updateAllGraphs() handle the
    // sidebar automatically. The '__sidebar__' key is recognised by updateAllGraphs.
    graphState[SIDEBAR_TAB_ID] = {
        rows: 1, cols: 1, gridEl: null,
        cells: [cell],
        sizeBtn: null, showDesc: false, _dismissHandler: null,
    };
})();

// =============================================================================
// State pill (item 06)
// =============================================================================

// _sidebarPillInfo reads the two axes straight off the object's own classList —
// the same classes pidSetObjectState/pidObjStateClass put there for the glyph
// (see the two-axis comment above PID_OBJ in pidRender.js). Returns null for a
// null/detached element so callers can blank the pill rather than guess.
function _sidebarPillInfo(g) {
    if (!g || !g.classList) return null;
    const alarmed = g.classList.contains('alarmed');
    // The design's pill vocabulary (LIVE/STALE/NO DATA/UNBOUND/ALARMING) has
    // five words for a two-axis, eight-combination state. Alarm wins the text
    // when both are true — "decided: the pill reads ALARMING, not ALARM" plus
    // the object turning red is already how alarm-over-stale/live reads on the
    // glyph itself (see .pid-object.alarmed.st-live/.st-stale in style.css).
    if (alarmed) return { text: 'ALARMING', mod: 'alarming' };
    if (g.classList.contains('st-stale'))   return { text: 'STALE',   mod: 'stale' };
    if (g.classList.contains('st-unbound')) return { text: 'UNBOUND', mod: 'unbound' };
    if (g.classList.contains('st-nodata'))  return { text: 'NO DATA', mod: 'nodata' };
    return { text: 'LIVE', mod: 'live' };   // st-live, or no st-* class yet
}

function _sidebarPaintPill(sidebarEl, g) {
    const info = _sidebarPillInfo(g);
    const pillEl = sidebarEl._pillEl;
    if (!info) {
        sidebarEl._statusRow.style.display = 'none';
        return;
    }
    sidebarEl._statusRow.style.display = '';
    pillEl.textContent = info.text;
    pillEl.className = 'object-sidebar-pill object-sidebar-pill--' + info.mod;
}

// _sidebarBindStatePill retargets the pill's MutationObserver to `g` (or tears
// it down for null) and paints once immediately — the observer only fires on
// FUTURE class changes, so without an immediate paint the pill would show
// nothing (or the previous object's state) until the next data tick.
//
// Known limitation, shared with sidebarGlowEl: a config/layout reload rebuilds
// the SVG groups from scratch (renderPidAll), which orphans `g` — the observer
// keeps watching a detached node that will never change again. Same failure
// mode the selection glow already has; not fixed here.
function _sidebarBindStatePill(sidebarEl, g) {
    if (sidebarPillObserver) { sidebarPillObserver.disconnect(); sidebarPillObserver = null; }
    _sidebarPaintPill(sidebarEl, g || null);
    if (!g) return;
    sidebarPillObserver = new MutationObserver(() => _sidebarPaintPill(sidebarEl, g));
    sidebarPillObserver.observe(g, { attributes: true, attributeFilter: ['class'] });
}

// =============================================================================
// Live readings table (item 05)
// =============================================================================

// _sidebarClearReadings empties the table and drops the row index, mirroring
// the channel-buffer teardown already done for the chart. Called on every open
// (before repopulating) and on close, so a retarget never leaves a stale row
// from the previous object behind.
function _sidebarClearReadings(sidebarEl) {
    sidebarEl._readingsRowsEl.innerHTML = '';
    sidebarEl._readingRows = {};
}

// _copyToClipboard writes `text` and reports success/failure via callback,
// falling back to a hidden-textarea + execCommand for contexts where the
// async Clipboard API is unavailable (e.g. non-HTTPS). No existing helper in
// the codebase does this — dataview.js and the graph tab only ever read
// channel names, never copy them.
function _copyToClipboard(text, done) {
    if (navigator.clipboard?.writeText) {
        navigator.clipboard.writeText(text).then(() => done(true), () => done(false));
        return;
    }
    try {
        const ta = document.createElement('textarea');
        ta.value = text;
        ta.style.position = 'fixed';
        ta.style.opacity  = '0';
        document.body.appendChild(ta);
        ta.select();
        const ok = document.execCommand('copy');
        document.body.removeChild(ta);
        done(ok);
    } catch { done(false); }
}

// _sidebarAddReadingRow builds one row for `ch` and appends it to the table.
// Click-to-copy (item 12) copies ch.refDes — the CHANNEL name, never ctrl's
// refDes from the header — because that is what config, alert rules and graph
// cells actually reference; copying the wrong one of the two is its own class
// of bug (see the design doc's rationale for item 12).
//
// The promote button (item 11) is a separate control on the same row rather
// than a second meaning for the row's own click, so "copy the name" and "send
// it to a real graph" stay two distinct, unambiguous actions instead of one
// click doing different things depending on where exactly it lands. It calls
// stopPropagation so clicking it doesn't also fire the row's copy handler.
function _sidebarAddReadingRow(sidebarEl, ctrl, ch) {
    const row   = mkEl('div', 'object-sidebar-reading-row');
    const nameEl = mkEl('span', 'object-sidebar-reading-name', ch.refDes);
    const valEl  = mkEl('span', 'object-sidebar-reading-value', '--');
    const unitsEl = mkEl('span', 'object-sidebar-reading-units', ch.units || '');
    const promoteBtn = mkEl('button', 'object-sidebar-reading-promote', '↗');
    promoteBtn.title = 'Send to Graph tab';
    promoteBtn.addEventListener('click', (e) => {
        e.stopPropagation();
        promoteChannelToGraph(ch.refDes);
    });
    row.append(nameEl, valEl, unitsEl, promoteBtn);
    row.title = 'Click to copy channel name';
    row.addEventListener('click', () => {
        _copyToClipboard(ch.refDes, (ok) => {
            // Minimal confirmation (item 12): flash the row rather than swap its
            // text, so a fast double-click on two different rows can't leave the
            // wrong name displayed if the second click lands mid-flash.
            row.classList.remove('object-sidebar-reading-row--copied', 'object-sidebar-reading-row--copy-failed');
            void row.offsetWidth;   // restart the CSS animation on repeat clicks
            row.classList.add(ok ? 'object-sidebar-reading-row--copied'
                                  : 'object-sidebar-reading-row--copy-failed');
        });
    });
    sidebarEl._readingsRowsEl.appendChild(row);
    sidebarEl._readingRows[ch.refDes] = { rowEl: row, valEl, unitsEl };
}

// updateObjectSidebarReadings repaints every row in the currently-open table
// from channelBuffers — the same rolling buffer the chart itself reads,
// already kept current by bufferGraphData() on every 'data' frame (ws.js).
// Called from app.js on the SAME redraw tick as updateAllGraphs/
// updateAllDataViews (item 05's "not a second timer"), not on its own timer.
function updateObjectSidebarReadings() {
    const sidebarEl = document.getElementById('object-sidebar');
    if (!sidebarEl || sidebarEl.style.display === 'none') return;
    for (const [refDes, row] of Object.entries(sidebarEl._readingRows || {})) {
        const buf = channelBuffers[refDes];
        const v = buf && buf.vals.length ? buf.vals[buf.vals.length - 1] : undefined;
        if (v === undefined) {
            // No sample has arrived yet this session — render sanely rather
            // than NaN/undefined, same placeholder pidBuildObject uses.
            row.valEl.textContent = '--';
            row.valEl.classList.remove('bad');
            continue;
        }
        const decimals = refDes === sidebarClickedRefDes ? sidebarClickedDecimals : undefined;
        row.valEl.textContent = typeof v === 'number' ? pidFormatValue(v, decimals) : String(v);
        row.valEl.classList.toggle('bad', typeof isChannelBad === 'function' && isChannelBad(refDes));
    }
}

// _sidebarFindPidObj recovers the layout object behind the SVG group the
// operator right-clicked, by the data-pid-id every front-panel group carries
// (pid.js). Used only to read obj.decimals for the ONE row matching the
// clicked channel — see sidebarClickedDecimals above.
function _sidebarFindPidObj(g) {
    const id = g?.getAttribute('data-pid-id');
    if (!id) return null;
    for (const t of tabs) {
        if (t.type !== 'frontPanel' || !t.pid?.objects) continue;
        const obj = t.pid.objects.find(o => o.id === id);
        if (obj) return obj;
    }
    return null;
}

// =============================================================================
// Raised alerts (item 07)
// =============================================================================

// _sidebarClearRaised empties the Raised list and drops the row index —
// mirrors _sidebarClearReadings exactly, for the same reason: called on
// every open (before repopulating) and on close, so a retarget never leaves
// a stale row from the previous object's alerts behind.
function _sidebarClearRaised(sidebarEl) {
    sidebarEl._raisedRowsEl.innerHTML = '';
    sidebarEl._raisedRows = {};
}

// _sidebarAddRaisedRow builds one row for `alert` and appends it to the
// Raised list. Rows only, no inline Ack (spec) — clicking anywhere on the
// row opens the alarm detail panel (alarmSidebar.js) scoped to this one
// alert; there is nothing else to click. The category icon/colour reuses
// alerts.js's own vocabulary (_categoryIcon, .alert-badge-<category>'s
// colour language) via the .object-sidebar-raised-row-<category> class,
// rather than inventing a second severity language for the same three
// categories.
function _sidebarAddRaisedRow(sidebarEl, alert) {
    const row  = mkEl('div', 'object-sidebar-raised-row object-sidebar-raised-row-' + alert.category);
    const icon = mkEl('span', 'object-sidebar-raised-icon',
        typeof _categoryIcon === 'function' ? _categoryIcon(alert.category) : '•');
    const msg   = mkEl('span', 'object-sidebar-raised-msg', alert.message);
    const chev  = mkEl('span', 'object-sidebar-raised-chevron', '›');
    row.append(icon, msg, chev);
    row.addEventListener('click', () => {
        if (typeof openAlarmSidebar === 'function') openAlarmSidebar(alert.id);
    });
    sidebarEl._raisedRowsEl.appendChild(row);
    sidebarEl._raisedRows[alert.id] = row;
}

// updateObjectSidebarRaised (item 07) rebuilds the Raised list for whichever
// control the panel is CURRENTLY bound to (sidebarRaisedChannels/Nodes, set
// by openObjectSidebar), from alertsFor() (alerts.js). Hides the whole
// section when there is nothing to show — a "Raised" label over an empty
// list would look broken, not simply quiet.
//
// Called from alerts.js's ingestAlert — every 'alert'/'alert_snapshot'
// message — rather than the periodic redraw tick: Ack/Suppress/resolve/a
// new alert are all EVENTS that arrive as exactly that message, so reacting
// to the message is a tighter, simpler trigger than piggybacking on a
// 500ms poll that has nothing to do with when alert state actually changes.
// (The alarm panel's own live refresh, alarmSidebar.js, makes the same
// choice for its content — see that file's comment — and additionally
// piggybacks its elapsed-time/readings ticking on the periodic tick, which
// this list has no equivalent need for.)
function updateObjectSidebarRaised() {
    const sidebarEl = document.getElementById('object-sidebar');
    if (!sidebarEl || sidebarEl.style.display === 'none') return;
    if (typeof alertsFor !== 'function') return;
    _sidebarClearRaised(sidebarEl);
    const alerts = alertsFor(sidebarRaisedChannels, sidebarRaisedNodes);
    for (const a of alerts) _sidebarAddRaisedRow(sidebarEl, a);
    sidebarEl._raisedEl.style.display = alerts.length ? '' : 'none';
}

// =============================================================================
// Open
// =============================================================================

// objEl is the SVG group the caller right-clicked — passed through so the
// panel can glow it (item 01) and clear the glow again on close or retarget.
// Optional: openObjectSidebarForGraph has no such group and passes nothing.
function openObjectSidebar(refDes, objEl) {
    const sidebarEl = document.getElementById('object-sidebar');
    if (!sidebarEl) return;

    // Sync chart colors to the current theme (chart may have been created before
    // the theme was applied from localStorage on first page load).
    const _state = graphState[SIDEBAR_TAB_ID];
    if (_state?.cells[0]?.chart) applyChartColors(_state.cells[0].chart);

    // Find the parent control that owns this channel refDes
    const ctrl = configControls.find(c => c.channels?.some(ch => ch.refDes === refDes));
    if (!ctrl) return;

    // The alarm panel (item 07) is a drill-down scoped to whichever object
    // this panel is CURRENTLY showing — see alarmSidebar.js. Retargeting to
    // a different object makes whatever alarm it had open belong to the
    // object being left behind, so it closes here rather than silently
    // keep showing an alert for an object the operator has moved on from.
    if (typeof closeAlarmSidebar === 'function') closeAlarmSidebar();

    const state = graphState[SIDEBAR_TAB_ID];
    if (!state) return;
    const cell = state.cells[0];

    // Clear any existing channels
    for (const rd of [...cell.channels.map(c => c.refDes)]) {
        removeChannelFromCell(SIDEBAR_TAB_ID, SIDEBAR_CELL_IDX, rd);
    }

    // A Freeze (item 16) or a manual pan left over from whatever object the
    // panel was PREVIOUSLY bound to must not carry into the new one — the
    // window position and the channels it framed belonged together, and
    // opening a different object with the old one's frozen window silently
    // applied is worse than the panel simply not remembering it at all.
    returnCellToLive(cell);

    // Update header
    sidebarEl._refdesEl.textContent = ctrl.refDes;
    sidebarEl._descEl.textContent   = ctrl.description ?? '';

    // Retarget the glow. Runs after the ctrl lookup above so a right-click on
    // an unbound/misconfigured object (silent failure, item 03) leaves the
    // panel — and whatever it was already glowing — exactly as it was.
    sidebarSetGlow(objEl);

    // Retarget the state pill (item 06) to the same object the glow just
    // bound to, so the two can never disagree about which object they describe.
    _sidebarBindStatePill(sidebarEl, objEl);

    // The clicked channel's own decimals (item 05) — see sidebarClickedDecimals.
    const clickedObj = _sidebarFindPidObj(objEl);
    sidebarClickedRefDes  = refDes;
    sidebarClickedDecimals = clickedObj?.decimals;

    // Replace the readings table wholesale rather than diffing it against the
    // previous control's rows — same "acceptable for now" tradeoff item 04
    // already made for the chart, and for the same reason: a small, bounded
    // list rebuilt on every open, not a hot path.
    _sidebarClearReadings(sidebarEl);
    _sidebarClearRaised(sidebarEl);

    // Add ALL channels from this control
    for (const ch of (ctrl.channels ?? [])) {
        addChannelToCell(SIDEBAR_TAB_ID, SIDEBAR_CELL_IDX, ch.refDes);
        _sidebarAddReadingRow(sidebarEl, ctrl, ch);
    }
    updateObjectSidebarReadings();   // paint immediately, don't wait for the next tick

    // Raised alerts for this object (item 07). Attribution off the SAME
    // channel/node lists isChannelAlarmed/isNodeAlarmed already use for this
    // control — see alertsFor's own comment (alerts.js) — stored so a later
    // live refresh (updateObjectSidebarRaised) can re-run the query without
    // needing `ctrl` again.
    sidebarRaisedChannels = (ctrl.channels ?? []).map(c => c.refDes);
    sidebarRaisedNodes    = [...new Set((ctrl.channels ?? []).map(c => c.node).filter(Boolean))];

    // Set visible BEFORE the paint-immediately calls below: both
    // updateObjectSidebarRaised (and, on a true first-ever open,
    // updateObjectSidebarReadings just above) bail out early while the
    // panel is still display:none, which it still is at this exact point on
    // the very first open of the whole session. Raised's own empty-state
    // handling (hide the section when there is nothing to show) depends on
    // that early-return NOT firing here, so display is flipped first.
    sidebarEl.style.display = '';
    updateObjectSidebarRaised();   // paint immediately, don't wait for the next alert message

    // Trigger an immediate resize so the chart fills its container correctly
    setTimeout(() => cell.chart?.resize(), 0);
}

// =============================================================================
// Close
// =============================================================================

// Esc closes the panel, matching the command panels (machinePanel.js,
// valveDropdown.js). Side panels never pin — settled in the design doc — so
// there is no "only if unpinned" guard to carry over from those.
//
// A right-click elsewhere does NOT close it any more. That used to be a
// document-level 'contextmenu' listener that closed on any right-click whose
// target wasn't inside the panel. Every object's own contextmenu handler
// already calls e.stopPropagation(), so a right-click on ANOTHER object never
// reached this listener at all — retargeting worked only because that handler
// calls openObjectSidebar(refDes, g) directly (see pid.js) and overwrites the
// panel in place, not because of anything this listener did. What the listener
// actually did was close the panel on a right-click on empty canvas, blank UI,
// or any other object type with no handler of its own — which is the real
// defect item 02 fixes. Removing it, and keeping the per-object retarget call
// and the new sidebarSetGlow() below, is the whole fix.
// The alarm panel (item 07, alarmSidebar.js) is a drill-down scoped to
// whichever object THIS panel currently has open — Esc must close only the
// topmost one. Checked here, inside this SAME listener, rather than as a
// second independent document-level 'keydown' listener registered from
// alarmSidebar.js: two independent listeners on the same event both fire
// regardless of registration order or stopPropagation (only
// stopImmediatePropagation prevents a later-registered listener from running
// at all, which would make script load order in index.html load-bearing for
// correctness) — checking the alarm panel first, inside one listener,
// sidesteps that question entirely instead of relying on it.
document.addEventListener('keydown', (e) => {
    if (e.key !== 'Escape') return;
    const alarmEl = document.getElementById('alarm-sidebar');
    if (alarmEl && alarmEl.style.display !== 'none') {
        if (typeof closeAlarmSidebar === 'function') closeAlarmSidebar();
        return;
    }
    const sidebarEl = document.getElementById('object-sidebar');
    if (sidebarEl && sidebarEl.style.display !== 'none') closeObjectSidebar();
});

function closeObjectSidebar() {
    const sidebarEl = document.getElementById('object-sidebar');
    if (!sidebarEl) return;

    // The alarm panel is a child of this one (Part C) — closing the parent
    // closes the child. The reverse is deliberately not true: closing the
    // alarm panel must not touch this one (see closeAlarmSidebar).
    if (typeof closeAlarmSidebar === 'function') closeAlarmSidebar();

    sidebarEl.style.display = 'none';
    sidebarSetGlow(null);
    _sidebarBindStatePill(sidebarEl, null);
    _sidebarClearReadings(sidebarEl);
    _sidebarClearRaised(sidebarEl);
    sidebarClickedRefDes   = null;
    sidebarClickedDecimals = undefined;
    sidebarRaisedChannels  = [];
    sidebarRaisedNodes     = [];

    const state = graphState[SIDEBAR_TAB_ID];
    if (!state) return;
    const cell = state.cells[0];

    // Remove all channels so their buffers are freed
    for (const rd of [...cell.channels.map(c => c.refDes)]) {
        removeChannelFromCell(SIDEBAR_TAB_ID, SIDEBAR_CELL_IDX, rd);
    }
}
