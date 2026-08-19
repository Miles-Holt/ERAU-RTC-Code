// =============================================================================
// Alerts — bottom bar
// =============================================================================
//
// Alert categories:
//   "info"    — blue  ⓘ  (informational, e.g. layout updated)
//   "warning" — yellow ⚠  (non-critical issues)
//   "alarm"   — red   🔔  (requires immediate attention)
//
// Alerts flash until acknowledged. Acking on one client acks for all operators
// via the server broadcasting alert_acked to all /ws/data subscribers.
//
// THIS MODULE IS RENDER-ONLY. Alerts are CREATED by the control node (rule
// alerts from config/alerts/*.alert, per-daqNode template alerts for
// disconnect / reconnect / bad_data / stale, and server notices), and arrive as
// alert / alert_snapshot / alert_acked messages. Do not construct alerts here:
// two browsers would otherwise disagree about what is alarming, and an operator
// acking on one would not clear it for anyone else. The only exceptions are
// faults the server cannot observe — the local control link ('cmd-not-sent' in
// ws.js) and browser-side JS failures (errors.js, pid.js render guards).
// =============================================================================

let _alerts    = [];    // [{ id, category, message, timestamp, acked, resolved,
                         //    channels, node, suppressed, suppressedAt?, description? }]
let _collapsed = true;
let _barEl     = null;
let _listEl    = null;

// _showSuppressed toggles the bottom bar's "View suppressed" filter (item
// 07b). Suppressed alerts have NO row in the default list — not greyed,
// hidden — so this is the only thing that brings them back into the list;
// the standing count badge (see _renderAlerts) is visible either way.
let _showSuppressed   = false;
let _suppressedBadgeEl = null;

// badDataState tracks which channels the server currently reports as out of
// range, for VALUE DISPLAY (the red reading / LED on a widget). It carries no
// alert of its own — the server raises the bad-data alert.
const badDataState = {};   // refDes -> { status, value, validMin, validMax, t }

// =============================================================================
// Public API (called from ws.js)
// =============================================================================

// handleBadData records a bad_data (or bad_data_snapshot entry) message for
// display. Render-only: the matching alert arrives separately from the server.
function handleBadData(msg) {
    if (!msg || !msg.refDes) return;
    if (msg.status === 'ok') {
        delete badDataState[msg.refDes];
        return;
    }
    badDataState[msg.refDes] = {
        status:   msg.status,
        value:    msg.value,
        validMin: msg.validMin,
        validMax: msg.validMax,
        t:        msg.t,
    };
}

// isChannelBad reports whether the server currently flags a channel as out of
// range, for widgets that colour their reading.
function isChannelBad(refDes) {
    return Object.prototype.hasOwnProperty.call(badDataState, refDes);
}

// isChannelAlarmed reports whether an alert concerning this channel is raised
// and NOT acknowledged — which is the alarm axis every front-panel object
// colours from.
//
// "Raised and unacknowledged", not "condition true right now": red LATCHES. A
// pressure spike that came back down on its own still leaves the object red,
// because the fact that it happened is the thing worth knowing. The server
// marks such a row `resolved: true, acked: false`, and it still counts here.
//
// Attribution comes off the alert itself (`channels`, `node`), not from parsing
// the id or the message. A rule alert names the channels its condition reads,
// so an object goes red for a rule about it — which id-sniffing could never
// deliver.
function isChannelAlarmed(refDes) {
    if (!refDes) return false;
    for (const a of _alerts) {
        // Suppressed is a standing operator decision to silence this alert —
        // it must stop contributing to the glow the moment it's set, exactly
        // like acked already does, not just until the rule stops retriggering.
        if (a.acked || a.suppressed) continue;
        if (a.channels && a.channels.indexOf(refDes) >= 0) return true;
    }
    return false;
}

// isNodeAlarmed is the node-level half: a disconnect or stale alert names the
// node rather than listing every channel it owns. An object whose channel lives
// on that node is alarmed by it.
function isNodeAlarmed(nodeRefDes) {
    if (!nodeRefDes) return false;
    for (const a of _alerts) {
        if (a.acked || a.suppressed) continue;
        if (a.node && a.node === nodeRefDes) return true;
    }
    return false;
}

// alertsFor returns the current NON-suppressed alerts attributed to any of
// the given channel refDes or node refDes. Same attribution rule as
// isChannelAlarmed/isNodeAlarmed above — off the alert's own `channels`/
// `node` fields, never id-sniffing — generalized to return the matching
// records themselves rather than a boolean. Used by the object side panel's
// Raised section (item 07) to build one row per alert concerning the
// control currently open; suppressed alerts are excluded here for the same
// reason they're excluded from the glow — the operator asked for them gone,
// not just unglowed.
function alertsFor(channels, nodes) {
    const chSet   = new Set(channels || []);
    const nodeSet = new Set(nodes || []);
    return _alerts.filter(a => {
        if (a.suppressed) return false;
        if (a.channels && a.channels.some(c => chSet.has(c))) return true;
        if (a.node && nodeSet.has(a.node)) return true;
        return false;
    });
}

// getAlert returns the CURRENT record for `id`, or null. Callers (the alarm
// panel) re-look it up on every refresh rather than holding a reference, so
// ack/suppress/resolve/a genuine re-trigger are always read fresh off the
// one place alert state lives.
function getAlert(id) {
    return _alerts.find(x => x.id === id) || null;
}

// _refreshAllAlertViews re-renders every place alert state is visible, after
// ANY mutation of _alerts — an incoming message (ingestAlert) or a local
// optimistic action (ack/suppress/unsuppress). Centralising it here means
// every mutation site stays in sync with the Raised section (item 07) and
// the alarm panel by construction, instead of each call site having to
// remember to notify both. Cross-file forward-reference by bare function
// name, same established pattern as reloadWithTabState below and
// ingestAlert's own callers in ws.js checking `typeof ingestAlert ===
// 'function'` — neither function is guaranteed to exist while THIS file is
// still being parsed, but both exist by the time any of these ever run.
function _refreshAllAlertViews() {
    _renderAlerts();
    if (typeof updateObjectSidebarRaised === 'function') updateObjectSidebarRaised();
    if (typeof updateAlarmSidebar === 'function') updateAlarmSidebar();
}

function ingestAlert(a) {
    const existing = _alerts.findIndex(x => x.id === a.id);
    if (existing >= 0) {
        // Preserve locally-acked state — server may not know about it yet.
        _alerts[existing] = { ...a, acked: _alerts[existing].acked || a.acked };
    } else {
        _alerts.push(a);
    }
    _refreshAllAlertViews();
}

function ackAlertLocally(id) {
    const a = _alerts.find(x => x.id === id);
    if (a) a.acked = true;
    _refreshAllAlertViews();
}

// ackAlert sends the ack to the server (which broadcasts to all clients),
// and optimistically marks it locally.
function ackAlert(id) {
    sendWsCtrl({ type: 'ack_alert', id });
    ackAlertLocally(id);
}

// suppressAlertLocally / unsuppressAlertLocally mirror ackAlertLocally: flip
// the ONE field this action owns on the existing record and re-render.
// Deliberately do NOT touch suppressedAt — that timestamp is the server's to
// set (item 07b's "time-of-suppression" note), and leaving it to arrive on
// the next `alert` broadcast is fine because nothing local reads it; only
// the boolean feeds isChannelAlarmed/isNodeAlarmed/alertsFor.
function suppressAlertLocally(id) {
    const a = _alerts.find(x => x.id === id);
    if (a) a.suppressed = true;
    _refreshAllAlertViews();
}

function unsuppressAlertLocally(id) {
    const a = _alerts.find(x => x.id === id);
    if (a) a.suppressed = false;
    _refreshAllAlertViews();
}

// suppressAlert / unsuppressAlert mirror ackAlert exactly: send the ctrl
// message, then optimistically update locally so the glow and every list
// (bottom bar, Raised section, alarm panel) reflect the decision immediately
// rather than waiting on the server's republished `alert`.
function suppressAlert(id) {
    sendWsCtrl({ type: 'suppress_alert', id });
    suppressAlertLocally(id);
}

function unsuppressAlert(id) {
    sendWsCtrl({ type: 'unsuppress_alert', id });
    unsuppressAlertLocally(id);
}

// =============================================================================
// Build DOM
// =============================================================================

document.addEventListener('DOMContentLoaded', () => {
    _barEl = document.createElement('div');
    _barEl.id = 'alert-bar';
    _barEl.className = 'alert-bar collapsed';

    // Header (always visible)
    const header = document.createElement('div');
    header.className = 'alert-bar-header';

    const counts = document.createElement('div');
    counts.className = 'alert-counts';

    // "View suppressed" badge (item 07b) — a STANDING count, not a pulsing
    // alert, so it must show every render even when the count is unchanged
    // from the last one; it deliberately does not get _badgeHtml's
    // 'pulsing' class. Built once here (like toggleBtn) rather than from the
    // innerHTML string _badgeHtml assembles for the other three badges, so
    // it can carry its own persistent click listener instead of being
    // rebuilt from scratch — and losing that listener — on every render.
    _suppressedBadgeEl = document.createElement('span');
    _suppressedBadgeEl.className = 'alert-badge alert-badge-suppressed';
    _suppressedBadgeEl.addEventListener('click', () => {
        _showSuppressed = !_showSuppressed;
        _renderAlerts();
    });

    const toggleBtn = document.createElement('button');
    toggleBtn.className = 'alert-toggle-btn';
    toggleBtn.title = 'Toggle alert list';
    toggleBtn.addEventListener('click', () => {
        _collapsed = !_collapsed;
        _renderAlerts();
    });

    header.append(counts, toggleBtn);

    // List (shown when expanded)
    _listEl = document.createElement('div');
    _listEl.className = 'alert-list';

    _barEl.append(header, _listEl);
    document.body.appendChild(_barEl);

    _renderAlerts();
});

// =============================================================================
// Render
// =============================================================================

function _renderAlerts() {
    if (!_barEl) return;

    // Suppressed alerts are excluded from the unacked counts (and so from
    // the header's pulse) the same way they're excluded from the glow
    // (isChannelAlarmed/isNodeAlarmed, item 07): suppressing is defined to
    // clear the red immediately, and a suppressed-but-technically-unacked
    // alarm still pulsing the header would contradict that everywhere except
    // the object it came from.
    const unacked = {
        info:    _alerts.filter(a => a.category === 'info'    && !a.acked && !a.suppressed).length,
        warning: _alerts.filter(a => a.category === 'warning' && !a.acked && !a.suppressed).length,
        alarm:   _alerts.filter(a => a.category === 'alarm'   && !a.acked && !a.suppressed).length,
    };
    const anyUnacked = unacked.info + unacked.warning + unacked.alarm > 0;

    _barEl.classList.toggle('collapsed', _collapsed);
    _barEl.classList.toggle('has-unacked', anyUnacked);

    // Counts in header
    const counts = _barEl.querySelector('.alert-counts');
    counts.innerHTML =
        _badgeHtml('info',    unacked.info)    +
        _badgeHtml('warning', unacked.warning) +
        _badgeHtml('alarm',   unacked.alarm);

    // View-suppressed badge (item 07b). Re-appended every render because the
    // innerHTML assignment above just wiped .alert-counts' children — this
    // is the SAME element each time (built once in DOMContentLoaded), so its
    // click listener survives.
    const suppressedCount = _alerts.filter(a => a.suppressed).length;
    _suppressedBadgeEl.textContent = '🔕 ' + suppressedCount;
    _suppressedBadgeEl.title = _showSuppressed ? 'Hide suppressed alerts' : 'View suppressed alerts';
    _suppressedBadgeEl.classList.toggle('active', _showSuppressed);
    counts.appendChild(_suppressedBadgeEl);

    // Toggle button label
    const toggleBtn = _barEl.querySelector('.alert-toggle-btn');
    toggleBtn.textContent = _collapsed ? '▲' : '▼';

    // List rows (newest first). Suppressed alerts have NO row here by
    // default (item 07b: "no row in the default list, not greyed — hidden")
    // — the badge above is the only thing that reveals them, each marked
    // with .alert-row-suppressed (see _makeRow) rather than getting a
    // separate list of their own.
    _listEl.innerHTML = '';
    const visible = _showSuppressed ? _alerts : _alerts.filter(a => !a.suppressed);
    const sorted = [...visible].reverse();
    for (const a of sorted) {
        _listEl.appendChild(_makeRow(a));
    }

    // Adjust tab-viewport bottom padding
    const viewport = document.getElementById('tab-viewport');
    if (viewport) {
        viewport.style.paddingBottom = _collapsed
            ? _barEl.querySelector('.alert-bar-header').offsetHeight + 'px'
            : _barEl.offsetHeight + 'px';
    }
}

function _badgeHtml(category, count) {
    const icon  = _categoryIcon(category);
    const pulse = count > 0 ? ' pulsing' : '';
    return `<span class="alert-badge alert-badge-${category}${pulse}">${icon} ${count}</span>`;
}

function _categoryIcon(category) {
    if (category === 'info')    return 'ℹ️';
    if (category === 'warning') return '⚠️';
    if (category === 'alarm')   return '🚨';
    return '•';
}

function _makeRow(a) {
    const row = document.createElement('div');
    row.className = 'alert-row alert-row-' + a.category + (a.acked ? ' acked' : ' unacked')
        + (a.suppressed ? ' alert-row-suppressed' : '');

    const ts   = new Date(a.timestamp);
    const time = ts.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });

    const icon = document.createElement('span');
    icon.className = 'alert-row-icon';
    icon.textContent = _categoryIcon(a.category);

    const timeEl = document.createElement('span');
    timeEl.className = 'alert-row-time';
    timeEl.textContent = time;

    const msg = document.createElement('span');
    msg.className = 'alert-row-msg';
    msg.textContent = a.message;

    const actions = document.createElement('div');
    actions.className = 'alert-row-actions';

    // "Reload" button — only on layout-update info alerts
    if (a.category === 'info' && a.message.startsWith('Layout') && typeof reloadWithTabState === 'function') {
        const reloadBtn = document.createElement('button');
        reloadBtn.className = 'alert-reload-btn';
        reloadBtn.textContent = 'Reload';
        reloadBtn.addEventListener('click', reloadWithTabState);
        actions.appendChild(reloadBtn);
    }

    if (!a.acked) {
        const ackBtn = document.createElement('button');
        ackBtn.className = 'alert-ack-btn';
        ackBtn.textContent = 'Ack';
        ackBtn.addEventListener('click', () => ackAlert(a.id));
        actions.appendChild(ackBtn);
    }

    const closeBtn = document.createElement('button');
    closeBtn.className = 'alert-close-btn';
    closeBtn.title = 'Dismiss';
    closeBtn.textContent = '✕';
    closeBtn.addEventListener('click', () => {
        const idx = _alerts.findIndex(x => x.id === a.id);
        if (idx >= 0) _alerts.splice(idx, 1);
        _renderAlerts();
    });
    actions.appendChild(closeBtn);

    row.append(icon, timeEl, msg, actions);
    return row;
}
