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
// TODO: sensor bounds alerts from alertRules.yaml (server-side)
// TODO: DAQ connect/disconnect alerts (hook into daqnode client)
// TODO: bad-data detection alerts (server-side range checking)
// =============================================================================

let _alerts    = [];    // [{ id, category, message, timestamp, acked }]
let _collapsed = true;
let _barEl     = null;
let _listEl    = null;

// =============================================================================
// Public API (called from ws.js)
// =============================================================================

function ingestAlert(a) {
    const existing = _alerts.findIndex(x => x.id === a.id);
    if (existing >= 0) {
        // Preserve locally-acked state — server may not know about it yet.
        _alerts[existing] = { ...a, acked: _alerts[existing].acked || a.acked };
    } else {
        _alerts.push(a);
    }
    _renderAlerts();
}

function ackAlertLocally(id) {
    const a = _alerts.find(x => x.id === id);
    if (a) a.acked = true;
    _renderAlerts();
}

// ackAlert sends the ack to the server (which broadcasts to all clients),
// and optimistically marks it locally.
function ackAlert(id) {
    sendWsCtrl({ type: 'ack_alert', id });
    ackAlertLocally(id);
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

    const unacked = {
        info:    _alerts.filter(a => a.category === 'info'    && !a.acked).length,
        warning: _alerts.filter(a => a.category === 'warning' && !a.acked).length,
        alarm:   _alerts.filter(a => a.category === 'alarm'   && !a.acked).length,
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

    // Toggle button label
    const toggleBtn = _barEl.querySelector('.alert-toggle-btn');
    toggleBtn.textContent = _collapsed ? '▲' : '▼';

    // List rows (newest first)
    _listEl.innerHTML = '';
    const sorted = [..._alerts].reverse();
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
    row.className = 'alert-row alert-row-' + a.category + (a.acked ? ' acked' : ' unacked');

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
