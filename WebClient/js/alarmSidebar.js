// =============================================================================
// Alarm Detail Sidebar (item 07)
// =============================================================================
// A second, right-of-the-object-panel drill-down: clicking a row in the
// object side panel's Raised section (objectSidebar.js) opens this panel
// scoped to ONE alert — its trend, how long it has been raised, its long
// description (item 07a), and Ack / Suppress (no Reset — see the worklog:
// "Reset" has no defined meaning anywhere and is deliberately not built).
//
// Built as the FOURTH place a "graph cell" gets constructed in this
// codebase (the other three: the Graph tab's buildGraphCell/resizeGraphGrid,
// the object side panel's own IIFE, and pid.js's makeGraphGroup for
// P&ID-embedded graph objects) — mirrors objectSidebar.js's cell-
// construction pattern closely (chart area, Now/Freeze buttons, a `cell`
// object literal carrying `frozen`/`nowBtn`/`freezeBtn`/
// `_debouncedEnsureHistory`, attachDragPan/attachScrollZoom/
// attachProximityTooltip, registered in graphState under its own tab-id
// key) so this one gets item 16 (Freeze/Now) and item 04 (debounced history
// fetch) from the start, unlike the P&ID case, which is a known, documented
// gap (see the worklog's "Found while building, not fixed" note) that is
// NOT this file's job to fix.
// =============================================================================

const ALARM_TAB_ID   = '__alarm_sidebar__';
const ALARM_CELL_IDX = 0;

// The id of the alert this panel is currently showing, or null when closed.
// Single slot — like objectSidebar.js's sidebarGlowEl — because only one
// alarm panel can ever be open at a time.
let alarmAlertId = null;

// Build the alarm sidebar DOM once at load time and register it in
// graphState, mirroring objectSidebar.js's initObjectSidebar IIFE.
(function initAlarmSidebar() {
    // ── Outer container ──────────────────────────────────────────────────────
    // Same top/bottom/width/z-index/background/border-left/box-shadow
    // language as .object-sidebar (see style.css), positioned immediately to
    // its left (right: 320px, the object panel's own width) — "same
    // template, different subject" per the design doc, so new class names
    // throughout rather than reusing .object-sidebar's.
    const el = document.createElement('div');
    el.id = 'alarm-sidebar';
    el.className = 'alarm-sidebar';
    el.style.display = 'none';

    // ── Header ───────────────────────────────────────────────────────────────
    const header    = mkEl('div', 'alarm-sidebar-header');
    const titleWrap = mkEl('div', 'alarm-sidebar-title');
    const nameEl    = mkEl('span', 'alarm-sidebar-name');
    const subEl     = mkEl('span', 'alarm-sidebar-sub', 'alarm');
    titleWrap.append(nameEl, subEl);
    const closeBtn = mkEl('button', 'alarm-sidebar-close', '×');
    closeBtn.title = 'Close';
    closeBtn.addEventListener('click', closeAlarmSidebar);
    header.append(titleWrap, closeBtn);
    el.appendChild(header);

    // ── Scrollable body (everything below the header) ─────────────────────────
    const body = mkEl('div', 'alarm-sidebar-body');
    el.appendChild(body);

    // ── Status row: elapsed time + ack state pills ─────────────────────────────
    const statusRow  = mkEl('div', 'alarm-sidebar-status');
    const raisedPill = mkEl('span', 'alarm-sidebar-pill');
    const ackPill    = mkEl('span', 'alarm-sidebar-pill');
    statusRow.append(raisedPill, ackPill);
    body.appendChild(statusRow);

    // ── Message section (item 07a's long description lives here) ───────────────
    const msgSec = mkEl('div', 'alarm-sidebar-section');
    msgSec.appendChild(mkEl('div', 'alarm-sidebar-label', 'Message'));
    const msgEl  = mkEl('div', 'alarm-sidebar-message');
    const descEl = mkEl('p', 'alarm-sidebar-description');
    msgSec.append(msgEl, descEl);
    body.appendChild(msgSec);

    // ── Graph cell (the trend of the channel(s) that tripped it) ───────────────
    const cellEl = document.createElement('div');
    cellEl.className = 'graph-cell graph-cell--column';

    const chartArea = mkEl('div', 'graph-chart-area');
    const canvas    = document.createElement('canvas');
    chartArea.appendChild(canvas);

    // Now / Freeze (item 16) — same markup/classes as objectSidebar.js's
    // copy, wired the same way below since `cell` already exists in this
    // scope by the time the listeners are attached.
    const nowBtn = mkEl('button', 'graph-now-btn', 'Now');
    nowBtn.title = 'Jump to live view';
    nowBtn.style.display = 'none';
    chartArea.appendChild(nowBtn);

    const freezeBtn = mkEl('button', 'graph-freeze-btn', 'Freeze');
    freezeBtn.title = "Stop the window advancing — click again, or Now, to resume";
    chartArea.appendChild(freezeBtn);

    cellEl.appendChild(chartArea);
    body.appendChild(cellEl);

    // ── Condition / readings (item: one row per channel named on the alert) ────
    const condEl  = mkEl('div', 'alarm-sidebar-section alarm-sidebar-readings');
    const condLbl = mkEl('div', 'alarm-sidebar-label', 'Condition');
    const condRows = mkEl('div', 'alarm-sidebar-readings-rows');
    condEl.append(condLbl, condRows);
    body.appendChild(condEl);

    // ── Actions: Ack / Suppress (no Reset — see the file banner comment) ───────
    const actionsSec = mkEl('div', 'alarm-sidebar-section alarm-sidebar-actions');
    const actionsRow = mkEl('div', 'alarm-sidebar-actions-row');
    const ackBtn      = mkEl('button', 'alarm-sidebar-btn', 'Ack');
    const suppressBtn = mkEl('button', 'alarm-sidebar-btn alarm-sidebar-btn--quiet', 'Suppress');
    actionsRow.append(ackBtn, suppressBtn);
    const suppressNote = mkEl('div', 'alarm-sidebar-note',
        'Suppress hides this alert through any re-trigger, until restart');
    actionsSec.append(actionsRow, suppressNote);
    body.appendChild(actionsSec);

    ackBtn.addEventListener('click', () => {
        if (alarmAlertId && typeof ackAlert === 'function') ackAlert(alarmAlertId);
        // ackAlert's optimistic ackAlertLocally already calls
        // _refreshAllAlertViews (alerts.js), which calls updateAlarmSidebar
        // — no separate repaint needed here.
    });
    suppressBtn.addEventListener('click', () => {
        if (!alarmAlertId) return;
        const a = typeof getAlert === 'function' ? getAlert(alarmAlertId) : null;
        if (a?.suppressed) { if (typeof unsuppressAlert === 'function') unsuppressAlert(alarmAlertId); }
        else               { if (typeof suppressAlert   === 'function') suppressAlert(alarmAlertId); }
    });

    document.getElementById('tab-viewport').appendChild(el);

    // ── Chart (reuses same factory as Graph tab / object sidebar) ────────────
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

    // Item 04: debounced per-cell history fetch — built once here, exactly
    // like objectSidebar.js's copy, for the same reason (no rebuild path for
    // this cell, so a plain assignment is enough; openAlarmSidebar never
    // calls addChannelToCell before this IIFE has finished running).
    cell._debouncedEnsureHistory = debounce(() => ensureCellHistory(cell), 200);

    attachDragPan(canvas, cell);
    attachScrollZoom(canvas, cell);
    attachProximityTooltip(canvas, cell);

    // ── Store element refs on the container for open/paint/close ─────────────
    el._nameEl       = nameEl;
    el._raisedPillEl = raisedPill;
    el._ackPillEl    = ackPill;
    el._msgEl        = msgEl;
    el._descEl       = descEl;
    el._condRowsEl   = condRows;
    el._condRows     = {};   // refDes -> valEl
    el._ackBtn       = ackBtn;
    el._suppressBtn  = suppressBtn;

    // ── Register in graphState ────────────────────────────────────────────────
    // Lets updateAllGraphs() drive this cell's live-follow/pan/zoom exactly
    // like the Graph tab and the object sidebar — see the '__alarm_sidebar__'
    // branch added to updateAllGraphs (graph.js).
    graphState[ALARM_TAB_ID] = {
        rows: 1, cols: 1, gridEl: null,
        cells: [cell],
        sizeBtn: null, showDesc: false, _dismissHandler: null,
    };
})();

// =============================================================================
// Condition / readings mini-list
// =============================================================================

// _alarmClearReadings empties the condition list and drops the row index —
// same pattern as objectSidebar.js's _sidebarClearReadings.
function _alarmClearReadings(el) {
    el._condRowsEl.innerHTML = '';
    el._condRows = {};
}

// _alarmBuildReadings builds one row per channel named on the alert. Built
// once per open (see openAlarmSidebar) rather than every paint — an alert's
// `channels` come from its definition, not the data that tripped it, so they
// don't change over the alert's lifetime; only the VALUES need repainting on
// a tick (_alarmUpdateReadings). An alert with no channels (a node-level
// alert) legitimately produces an empty list — the whole section hides
// rather than show an empty label with nothing under it.
function _alarmBuildReadings(el, channels) {
    _alarmClearReadings(el);
    for (const refDes of channels) {
        const row   = mkEl('div', 'alarm-sidebar-reading-row');
        const nameEl = mkEl('span', 'alarm-sidebar-reading-name', refDes);
        const valEl  = mkEl('span', 'alarm-sidebar-reading-value', '--');
        row.append(nameEl, valEl);
        el._condRowsEl.appendChild(row);
        el._condRows[refDes] = valEl;
    }
    const condEl = el._condRowsEl.closest('.alarm-sidebar-readings');
    if (condEl) condEl.style.display = channels.length ? '' : 'none';
}

// _alarmUpdateReadings repaints every row's value from channelBuffers — the
// same rolling buffer updateObjectSidebarReadings reads — with
// pidFormatValue's default decimals (no per-object decimals context exists
// for an alarm panel, which isn't bound to any one front-panel object).
function _alarmUpdateReadings(el) {
    for (const [refDes, valEl] of Object.entries(el._condRows || {})) {
        const buf = channelBuffers[refDes];
        const v = buf && buf.vals.length ? buf.vals[buf.vals.length - 1] : undefined;
        if (v === undefined) {
            valEl.textContent = '--';
            valEl.classList.remove('bad');
            continue;
        }
        valEl.textContent = typeof v === 'number' ? pidFormatValue(v) : String(v);
        valEl.classList.toggle('bad', typeof isChannelBad === 'function' && isChannelBad(refDes));
    }
}

// =============================================================================
// Formatting helpers
// =============================================================================

// _alarmDisplayName strips the id-type prefix (`rule:`, `bad:`, `sensor:`,
// `conn:`, `stale:`, `notice:` — see docs/websocket-protocol.md's `alert`
// section) up to and including the first ':' for a cleaner header, matching
// the mockup's "OV-05-STUCK". Falls back to the full id when there's no ':'
// (a generated id for a one-off server notice, per the same doc).
function _alarmDisplayName(id) {
    const i = id.indexOf(':');
    return i >= 0 ? id.slice(i + 1) : id;
}

// _alarmElapsed formats milliseconds-since-`timestamp` as "HH:MM:SS". Called
// on every paint (including the periodic tick — see updateAlarmSidebar) so
// it live-ticks while the panel is open, not just when the underlying alert
// changes.
function _alarmElapsed(timestampMs) {
    let sec = Math.max(0, Math.floor((Date.now() - timestampMs) / 1000));
    const h = Math.floor(sec / 3600); sec -= h * 3600;
    const m = Math.floor(sec / 60);   sec -= m * 60;
    const pad = n => String(n).padStart(2, '0');
    return `${pad(h)}:${pad(m)}:${pad(sec)}`;
}

// _alarmAckPillInfo maps the three-state acked/resolved combination
// (docs/websocket-protocol.md's `alert` table) to pill text and a colour
// modifier. Active and resolved-but-unacknowledged share the SAME red
// styling — the protocol doc is explicit that a recovered-on-its-own alert
// "stays on the board and the object stays red" — with different wording so
// the two remain distinguishable; only acknowledged gets the calmer colour.
function _alarmAckPillInfo(a) {
    if (a.acked) return { text: 'Acknowledged', mod: 'acked' };
    if (a.resolved) return { text: 'Resolved — unacknowledged', mod: 'unacked' };
    return { text: 'Unacknowledged', mod: 'unacked' };
}

// =============================================================================
// Paint
// =============================================================================

// _paintAlarmSidebar repaints every part of the panel from the given CURRENT
// record. Cheap full repaint of one small panel rather than a diff — same
// tradeoff the rest of this build makes elsewhere (see the worklog).
function _paintAlarmSidebar(a) {
    const el = document.getElementById('alarm-sidebar');
    if (!el) return;

    el._nameEl.textContent = _alarmDisplayName(a.id);

    el._raisedPillEl.textContent = 'Raised ' + _alarmElapsed(a.timestamp);
    el._raisedPillEl.className   = 'alarm-sidebar-pill alarm-sidebar-pill--raised';

    const ackInfo = _alarmAckPillInfo(a);
    el._ackPillEl.textContent = ackInfo.text;
    el._ackPillEl.className   = 'alarm-sidebar-pill alarm-sidebar-pill--' + ackInfo.mod;

    el._msgEl.textContent = a.message;

    // Long description (item 07a) — only rendered when present; an alert
    // with no `describe "…"` on its definition gets no paragraph at all,
    // not an empty one (most alerts, and every template/sensor/bad-data
    // alert, never carry one).
    if (a.description) {
        el._descEl.textContent = a.description;
        el._descEl.style.display = '';
    } else {
        el._descEl.textContent = '';
        el._descEl.style.display = 'none';
    }

    _alarmUpdateReadings(el);

    el._ackBtn.style.display = a.acked ? 'none' : '';
    el._suppressBtn.textContent = a.suppressed ? 'Unsuppress' : 'Suppress';
    el._suppressBtn.classList.toggle('alarm-sidebar-btn--active', !!a.suppressed);
}

// =============================================================================
// Open / close / live refresh
// =============================================================================

// openAlarmSidebar (item 07) shows the alarm detail panel for `alertId`.
// Looks the record up fresh via getAlert (alerts.js) rather than taking one
// as a parameter, so open and every later repaint (updateAlarmSidebar) share
// one source of truth. If the alert can't be found — aged out of the
// registry's trimmed history (the worklog's registry.go note), or a stale
// id — this does nothing rather than throw or show broken content.
function openAlarmSidebar(alertId) {
    const el = document.getElementById('alarm-sidebar');
    if (!el) return;
    const a = typeof getAlert === 'function' ? getAlert(alertId) : null;
    if (!a) return;

    alarmAlertId = alertId;

    const state = graphState[ALARM_TAB_ID];
    const cell  = state?.cells[0];
    if (cell?.chart) applyChartColors(cell.chart);

    if (cell) {
        // Clear any channels left over from a previously-shown alert.
        for (const rd of [...cell.channels.map(c => c.refDes)]) {
            removeChannelFromCell(ALARM_TAB_ID, ALARM_CELL_IDX, rd);
        }
        // Same reasoning as objectSidebar.js's openObjectSidebar: a Freeze
        // or manual pan left over from a DIFFERENT alert's window must not
        // carry into this one.
        returnCellToLive(cell);
        // May be empty (a node-level alert) — an empty chart with no
        // channels is the correct, honest result; nothing is substituted.
        for (const refDes of (a.channels ?? [])) {
            addChannelToCell(ALARM_TAB_ID, ALARM_CELL_IDX, refDes);
        }
    }

    _alarmBuildReadings(el, a.channels ?? []);

    el.style.display = '';
    _paintAlarmSidebar(a);

    setTimeout(() => cell?.chart?.resize(), 0);
}

// closeAlarmSidebar hides the panel and frees its channel buffers. Does NOT
// touch the object side panel — it is the parent (objectSidebar.js's
// closeObjectSidebar/openObjectSidebar close this on the way out/on
// retarget), never the other way around.
function closeAlarmSidebar() {
    const el = document.getElementById('alarm-sidebar');
    if (!el) return;

    el.style.display = 'none';
    alarmAlertId = null;

    const state = graphState[ALARM_TAB_ID];
    const cell  = state?.cells[0];
    if (cell) {
        for (const rd of [...cell.channels.map(c => c.refDes)]) {
            removeChannelFromCell(ALARM_TAB_ID, ALARM_CELL_IDX, rd);
        }
    }
    _alarmClearReadings(el);
}

// updateAlarmSidebar re-renders the panel from the CURRENT record for
// whatever alert it's showing. Called from two places, deliberately not
// just one:
//   - alerts.js's ingestAlert (via _refreshAllAlertViews), on every
//     'alert'/'alert_snapshot' message — the same live-refresh mechanism
//     objectSidebar.js's updateObjectSidebarRaised uses (item 07), so
//     ack/suppress/resolve/a genuine re-trigger with a new message reflect
//     immediately without waiting on a poll;
//   - the periodic redraw tick (dataview.js's updateAllDataViews, alongside
//     updateObjectSidebarReadings — see app.js), because the elapsed-time
//     pill and the condition readings are both a function of the CURRENT
//     wall clock / rolling buffer, not of the alert record changing at all,
//     and need to tick even when nothing about the alert itself has.
// Both call the same full-repaint function, so there is exactly one
// rendering path to reason about rather than two that could drift.
//
// If the alert this panel is showing is no longer findable via getAlert
// (aged out of the registry's trimmed history), the panel closes rather
// than show stale content.
function updateAlarmSidebar() {
    const el = document.getElementById('alarm-sidebar');
    if (!el || el.style.display === 'none' || !alarmAlertId) return;
    const a = typeof getAlert === 'function' ? getAlert(alarmAlertId) : null;
    if (!a) { closeAlarmSidebar(); return; }
    _paintAlarmSidebar(a);
}
