// =============================================================================
// PID Layout Editor — standalone page
// =============================================================================
//
// Opened from the front panel via the "Editor" button.
// Receives data via sessionStorage key 'pid_editor_data':
//   { configControls: [...], pidLayouts: {...}, selectedLayout: "filename.yaml" }
//
// The user edits the layout and downloads the updated YAML.
// No WebSocket connection is needed — all data is passed from the main page.
//
// =============================================================================

// Apply saved theme immediately to avoid a flash of the wrong theme.
(function () {
    if (localStorage.getItem('rtc-theme') === 'light')
        document.documentElement.setAttribute('data-theme', 'light');
})();

// ── Constants ────────────────────────────────────────────────────────────────
// PID now lives in pidRender.js (shared with the viewer) so both pages route and
// attach pipes identically. It is loaded before this file via editor.html.

// ── Editor state ─────────────────────────────────────────────────────────────

let edConfigControls = [];
let edLayouts = {};   // filename → { name, filename, content }

// Formats a live channel value for display: an integer shows bare, a
// non-integer number to 3 decimal places, anything else via String().
function fmtEdLiveValue(v) {
    if (typeof v !== 'number') return String(v);
    return Number.isInteger(v) ? String(v) : v.toFixed(3);
}

const FLUIDS       = ['', 'gn2', 'air', 'lox', 'gox', 'fuel', 'hydrogen'];
const FLUID_LABELS = { '': 'None', gn2: 'GN2', air: 'Air', lox: 'LOX', gox: 'GOX', fuel: 'Fuel', hydrogen: 'Hydrogen' };

// Live data WebSocket (/ws/data)
let edWs               = null;
let edWsReconnectDelay = 1000;
let edWsReconnectTimer = null;
let edWsStatusEl       = null;          // dot in header
const edLiveValues     = {};            // refDes → latest value
let edLiveRefDes       = null;          // refDes of the currently selected sensor
let edLiveEl           = null;          // <span> in right sidebar to update
const edLiveStale      = makeStaleTimer(2000, () => { if (edLiveEl) edLiveEl.classList.add('stale'); });

// Control WebSocket (/ws/ctrl) — authenticated, used for set_layout
let edWsCtrl               = null;
let edWsCtrlAuthed         = false;
let edWsCtrlReconnectDelay = 1000;
let edWsCtrlReconnectTimer = null;
let edWsCtrlPending        = null;      // payload queued until auth completes

// Single editor "tab" object — mirrors the tab.pid shape used in the main app.
const tab = {
    channelUpdaters: {},
    pid: {
        editMode: true,
        layoutFilename: '',
        layoutName: '',
        objects: [],
        connections: [],
        selectedId: null,
        connecting: null,
        previewEl: null,
        svgEl: null,
        gGrid: null,
        gConns: null,
        gObjs: null,
        canvasWrap: null,
        lsbEl: null,
        rsbEl: null,
        pickerEl: null,
        routingErrors: [],
        problems: [],
        warnBtnEl: null,
        warnDropdownEl: null,
        selectedConnId: null,
    },
};

// ── YAML serialiser ──────────────────────────────────────────────────────────

function pidToYaml(layout) {
    function q(s) {
        s = String(s);
        return /[:#{}[\],&*?|<>=!%@`'"\\]/.test(s) ? '"' + s.replace(/"/g, '\\"') + '"' : s;
    }
    let y = 'name: ' + q(layout.name || 'Untitled') + '\nversion: 1\nobjects:\n';
    for (const o of layout.objects) {
        y += '  - id: '   + q(o.id)  + '\n';
        y += '    type: ' + o.type   + '\n';
        if (o.type === 'graph') {
            if (o.name)             y += '    name: '           + q(o.name)        + '\n';
            y +=                        '    gridX: '           + o.gridX           + '\n';
            y +=                        '    gridY: '           + o.gridY           + '\n';
            y +=                        '    gridW: '           + (o.gridW || 20)   + '\n';
            y +=                        '    gridH: '           + (o.gridH || 10)   + '\n';
            if (o.showName === false)                          y += '    showName: false\n';
            if (o.showLeftSidebar)                             y += '    showLeftSidebar: true\n';
            if (o.legendPosition && o.legendPosition !== 'none') y += '    legendPosition: ' + o.legendPosition + '\n';
            if (o.lines && o.lines.length) {
                y += '    lines:\n';
                for (const l of o.lines) {
                    y += '      - refDes: ' + q(l.refDes) + '\n';
                    if (l.color)           y += '        color: '  + q(l.color)  + '\n';
                    if (l.yAxis && l.yAxis !== 1) y += '        yAxis: ' + l.yAxis + '\n';
                    if (l.hidden)          y += '        hidden: true\n';
                }
            }
        } else if (o.type === 'tank') {
            y +=                              '    gridX: '      + o.gridX            + '\n';
            y +=                              '    gridY: '      + o.gridY            + '\n';
            y +=                              '    gridW: '      + (o.gridW || 5)     + '\n';
            y +=                              '    gridH: '      + (o.gridH || 8)     + '\n';
            if (o.rotation)              y += '    rotation: '   + o.rotation         + '\n';
            if (o.cornerR !== undefined) y += '    cornerR: '    + o.cornerR          + '\n';
            if (o.label)                 y += '    label: '      + q(o.label)         + '\n';
            if (o.showLabel === false)   y += '    showLabel: false\n';
            if (o.labelOffsetX)          y += '    labelOffsetX: ' + o.labelOffsetX   + '\n';
            if (o.labelOffsetY)          y += '    labelOffsetY: ' + o.labelOffsetY   + '\n';
        } else if (o.type === 'daqControl') {
            if (o.daqRefDes)           y += '    daqRefDes: ' + q(o.daqRefDes) + '\n';
            if (o.label)               y += '    label: '     + q(o.label)     + '\n';
            if (o.side && o.side !== 'right') y += '    side: ' + o.side + '\n';
            y +=                           '    gridX: ' + o.gridX + '\n';
            y +=                           '    gridY: ' + o.gridY + '\n';
            if (o.gridW && o.gridW !== 10) y += '    gridW: ' + o.gridW + '\n';
            if (o.gridH && o.gridH !== 3)  y += '    gridH: ' + o.gridH + '\n';
        } else {
            // bubbleText, label, decimals, showUnits (existing) and side are the
            // per-object display fields from docs/design/sensor-object-options.html.
            // Every one of them is absent-safe: a layout saved before they existed
            // must come back out of pidToYaml byte-identical, so each is only
            // written when it differs from the default pidBuildObject already
            // assumes when the key is missing (see pid.js's makeSensorGroup /
            // makeValveGroup for what "missing" resolves to).
            if (o.refDes)              y += '    refDes: '        + q(o.refDes)        + '\n';
            if (o.units)               y += '    units: '         + q(o.units)         + '\n';
            if (o.controlRefDes)       y += '    controlRefDes: ' + q(o.controlRefDes) + '\n';
            if (o.channelRefDes)       y += '    channelRefDes: ' + q(o.channelRefDes) + '\n';
            if (o.showRefDes === false) y += '    showRefDes: false\n';
            if (o.showUnits  === false) y += '    showUnits: false\n';
            if (o.showName   === true)  y += '    showName: true\n';
            if (o.label)                y += '    label: '       + q(o.label)          + '\n';
            // bubbleText: undefined = default (derived from refDes, omit the key);
            // '' is a real, distinct value ("force an empty bubble") and must
            // still round-trip, which is why this checks `!== undefined` rather
            // than truthiness like the plain string fields above.
            if (o.bubbleText !== undefined) y += '    bubbleText: ' + q(o.bubbleText)   + '\n';
            if (typeof o.decimals === 'number' && o.decimals >= 0 && o.decimals <= 6 && o.decimals !== 2)
                                        y += '    decimals: '      + o.decimals          + '\n';
            if (o.side && o.side !== 'right') y += '    side: '   + o.side              + '\n';
            if (o.rotation)            y += '    rotation: '      + o.rotation          + '\n';
            y +=                            '    gridX: '         + o.gridX             + '\n';
            y +=                            '    gridY: '         + o.gridY             + '\n';
            if (o.labelOffsetX)        y += '    labelOffsetX: '  + o.labelOffsetX      + '\n';
            if (o.labelOffsetY)        y += '    labelOffsetY: '  + o.labelOffsetY      + '\n';
        }
    }
    y += 'connections:\n';
    for (const c of layout.connections) {
        y += '  - id: '       + q(c.id)       + '\n';
        y += '    fromId: '   + q(c.fromId)   + '\n';
        y += '    fromPort: ' + c.fromPort     + '\n';
        y += '    toId: '     + q(c.toId)     + '\n';
        y += '    toPort: '   + c.toPort       + '\n';
        if (c.fluid)          y += '    fluid: '    + c.fluid      + '\n';
        if (c.color)          y += '    color: '    + q(c.color)   + '\n';
    }
    return y;
}

// ── YAML parser ──────────────────────────────────────────────────────────────

// pidFromYaml moved to pidRender.js (shared with the viewer).

// ── SVG helpers ──────────────────────────────────────────────────────────────

// svgN, portPos, pidSvgPt moved to pidRender.js (shared with the viewer).

function pidUid(prefix) {
    return prefix + '_' + Date.now() + '_' + Math.floor(Math.random() * 9999);
}

function pidEsc(s) {
    return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

// Valve symbol geometry helpers (_valveSubtypeInfo, _valveLineAttrs,
// _valveArcPath, _valvePtrPos) moved to pidRender.js (shared with the viewer).

// =============================================================================
// Live WebSocket connection (read-only — data only)
// =============================================================================

function edConnect() {
    const url = 'ws://' + (window.location.hostname || 'localhost') + ':8000/ws/data';
    try {
        edWs = new WebSocket(url);
    } catch (e) {
        scheduleEdReconnect();
        return;
    }
    edWs.onopen = () => {
        edWsReconnectDelay = 1000;
        setEdWsStatus(true);
    };
    edWs.onmessage = ev => {
        let msg;
        try { msg = JSON.parse(ev.data); } catch { return; }
        if (msg.type === 'data') edApplyData(msg);
    };
    edWs.onclose = () => {
        edWs = null;
        setEdWsStatus(false);
        scheduleEdReconnect();
    };
    edWs.onerror = () => {};
}

function scheduleEdReconnect() {
    if (edWsReconnectTimer) return;
    edWsReconnectTimer = setTimeout(() => {
        edWsReconnectTimer = null;
        edConnect();
    }, edWsReconnectDelay);
    edWsReconnectDelay = Math.min(edWsReconnectDelay * 2, 10000);
}

// ── Control WebSocket (/ws/ctrl) ──────────────────────────────────────────────

function edConnectCtrl() {
    const url = 'ws://' + (window.location.hostname || 'localhost') + ':8000/ws/ctrl';
    try {
        edWsCtrl = new WebSocket(url);
    } catch (e) {
        scheduleEdCtrlReconnect();
        return;
    }
    edWsCtrl.onopen = () => {
        edWsCtrlReconnectDelay = 1000;
    };
    edWsCtrl.onmessage = ev => {
        let msg;
        try { msg = JSON.parse(ev.data); } catch { return; }
        if (msg.type !== 'auth_response') return;
        if (msg.approved) {
            edWsCtrlAuthed = true;
            hideEdAuthModal();
            if (edWsCtrlPending) {
                edWsCtrl.send(edWsCtrlPending);
                edWsCtrlPending = null;
            }
        } else {
            showEdAuthModal(msg.reason || 'Authentication failed');
        }
    };
    edWsCtrl.onclose = () => {
        edWsCtrl       = null;
        edWsCtrlAuthed = false;
        scheduleEdCtrlReconnect();
    };
    edWsCtrl.onerror = () => {};
}

function scheduleEdCtrlReconnect() {
    if (edWsCtrlReconnectTimer) return;
    edWsCtrlReconnectTimer = setTimeout(() => {
        edWsCtrlReconnectTimer = null;
        edConnectCtrl();
    }, edWsCtrlReconnectDelay);
    edWsCtrlReconnectDelay = Math.min(edWsCtrlReconnectDelay * 2, 10000);
}

// edSendCtrl queues a payload on the control WS, prompting auth if needed.
function edSendCtrl(payload) {
    edWsCtrlPending = payload;
    if (!edWsCtrl || edWsCtrl.readyState !== WebSocket.OPEN) {
        edConnectCtrl();
        showEdAuthModal();
        return;
    }
    if (!edWsCtrlAuthed) {
        showEdAuthModal();
        return;
    }
    edWsCtrl.send(payload);
    edWsCtrlPending = null;
}

// ── Editor auth modal ─────────────────────────────────────────────────────────

let _edAuthModalEl = null;

function showEdAuthModal(errorMsg) {
    if (_edAuthModalEl) {
        if (errorMsg) {
            const status = _edAuthModalEl.querySelector('.ed-auth-status');
            if (status) { status.textContent = errorMsg; status.style.display = ''; }
            const btn = _edAuthModalEl.querySelector('.ed-auth-submit');
            if (btn) btn.disabled = false;
            _edAuthModalEl.querySelector('.ed-auth-pin')?.focus();
        }
        return;
    }
    const overlay = document.createElement('div');
    overlay.className = 'ed-auth-overlay';
    overlay.innerHTML =
        '<div class="ed-auth-modal">' +
            '<div class="ed-auth-title">Authenticate to Save</div>' +
            '<input class="ed-auth-name" type="text" placeholder="Name" maxlength="32" autocomplete="off" spellcheck="false">' +
            '<input class="ed-auth-pin" type="password" placeholder="PIN" maxlength="16" autocomplete="off">' +
            '<div class="ed-auth-status" style="display:none"></div>' +
            '<button class="ed-auth-submit">Authenticate &amp; Save</button>' +
        '</div>';
    document.body.appendChild(overlay);
    _edAuthModalEl = overlay;

    const nameInp = overlay.querySelector('.ed-auth-name');
    const pinInp  = overlay.querySelector('.ed-auth-pin');
    const btn     = overlay.querySelector('.ed-auth-submit');
    if (errorMsg) {
        const status = overlay.querySelector('.ed-auth-status');
        status.textContent = errorMsg;
        status.style.display = '';
    }
    nameInp.focus();

    function submit() {
        const name = nameInp.value.trim();
        const pin  = pinInp.value;
        if (!name || !pin) return;
        btn.disabled = true;
        const status = overlay.querySelector('.ed-auth-status');
        status.textContent = 'Waiting...';
        status.style.display = '';
        if (edWsCtrl && edWsCtrl.readyState === WebSocket.OPEN) {
            edWsCtrl.send(JSON.stringify({ type: 'auth_request', name, pin }));
        } else {
            edConnectCtrl();
            setTimeout(submit, 500);
        }
    }

    btn.addEventListener('click', submit);
    [nameInp, pinInp].forEach(inp =>
        inp.addEventListener('keydown', e => { if (e.key === 'Enter') submit(); })
    );
}

function hideEdAuthModal() {
    _edAuthModalEl?.remove();
    _edAuthModalEl = null;
}

function edApplyData(msg) {
    const d = Array.isArray(msg.d)
        ? Object.fromEntries(msg.d.map(e => [e.r, e.v]))
        : msg.d;
    Object.assign(edLiveValues, d);

    // Push value to the right sidebar if a sensor is currently selected
    if (edLiveRefDes && edLiveEl && d[edLiveRefDes] !== undefined) {
        const v = d[edLiveRefDes];
        edLiveEl.textContent = fmtEdLiveValue(v);
        edLiveEl.classList.remove('stale');
        edLiveStale.bump();
    }
}

function setEdWsStatus(connected) {
    if (!edWsStatusEl) return;
    edWsStatusEl.title = connected ? 'Live data: connected' : 'Live data: disconnected';
    edWsStatusEl.className = 'ed-ws-dot ' + (connected ? 'ed-ws-connected' : 'ed-ws-disconnected');
}

// =============================================================================
// Build editor UI
// =============================================================================

function buildEditorUI(rootEl) {
    rootEl.innerHTML = '';

    // ── Header ──
    const header = document.createElement('div');
    header.className = 'ed-header';

    const title = document.createElement('span');
    title.className = 'ed-title';
    title.textContent = 'PID Layout Editor';

    const picker = document.createElement('select');
    picker.className = 'pid-picker';
    picker.title = 'Select layout';
    picker.innerHTML = '<option value="">-- No layout --</option>';
    Object.values(edLayouts).forEach(l => {
        const o = document.createElement('option');
        o.value = l.filename; o.textContent = l.name;
        picker.appendChild(o);
    });

    const saveBtn = document.createElement('button');
    saveBtn.className = 'pid-save-btn';
    saveBtn.textContent = 'Save YAML';

    const warnBtn = document.createElement('button');
    warnBtn.className = 'pid-warn-btn';
    warnBtn.title = 'Problems';
    warnBtn.style.display = 'none';
    warnBtn.innerHTML =
        '<span class="pid-warn-icon">!</span>' +
        '<span class="pid-warn-count"></span>';
    const warnDropdown = document.createElement('div');
    warnDropdown.className = 'pid-warn-dropdown';
    warnBtn.appendChild(warnDropdown);

    warnBtn.addEventListener('click', e => {
        e.stopPropagation();
        warnBtn.classList.toggle('pid-warn-open');
        if (warnBtn.classList.contains('pid-warn-open')) renderPidWarnDropdown();
    });
    document.addEventListener('click', () => {
        document.querySelectorAll('.pid-warn-btn.pid-warn-open')
                .forEach(b => b.classList.remove('pid-warn-open'));
    });

    const wsDot = document.createElement('span');
    wsDot.className = 'ed-ws-dot ed-ws-disconnected';
    wsDot.title = 'Live data: disconnected';
    edWsStatusEl = wsDot;

    const themeBtn = document.createElement('button');
    themeBtn.id    = 'theme-btn';
    themeBtn.title = 'Toggle light/dark mode';
    themeBtn.setAttribute('aria-label', 'Toggle theme');
    themeBtn.innerHTML =
        '<svg id="ed-theme-moon" viewBox="0 0 16 16" width="14" height="14" fill="currentColor" aria-hidden="true">' +
            '<path d="M6 .278a.768.768 0 0 1 .08.858 7.208 7.208 0 0 0-.878 3.46c0 4.021 3.278 7.277 7.318 7.277.527 0 1.04-.055 1.533-.16a.787.787 0 0 1 .81.316.733.733 0 0 1-.031.893A8.349 8.349 0 0 1 8.344 16C3.734 16 0 12.286 0 7.71 0 4.266 2.114 1.312 5.124.06A.752.752 0 0 1 6 .278z"/>' +
        '</svg>' +
        '<svg id="ed-theme-sun" viewBox="0 0 16 16" width="14" height="14" fill="currentColor" aria-hidden="true" style="display:none">' +
            '<path d="M8 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8zM8 0a.5.5 0 0 1 .5.5v2a.5.5 0 0 1-1 0v-2A.5.5 0 0 1 8 0zm0 13a.5.5 0 0 1 .5.5v2a.5.5 0 0 1-1 0v-2A.5.5 0 0 1 8 13zm8-5a.5.5 0 0 1-.5.5h-2a.5.5 0 0 1 0-1h2a.5.5 0 0 1 .5.5zM3 8a.5.5 0 0 1-.5.5h-2a.5.5 0 0 1 0-1h2A.5.5 0 0 1 3 8zm10.657-5.657a.5.5 0 0 1 0 .707l-1.414 1.415a.5.5 0 1 1-.707-.708l1.414-1.414a.5.5 0 0 1 .707 0zm-9.193 9.193a.5.5 0 0 1 0 .707L3.05 13.657a.5.5 0 0 1-.707-.707l1.414-1.414a.5.5 0 0 1 .707 0zm9.193 2.121a.5.5 0 0 1-.707 0l-1.414-1.414a.5.5 0 0 1 .707-.707l1.414 1.414a.5.5 0 0 1 0 .707zM4.464 4.465a.5.5 0 0 1-.707 0L2.343 3.05a.5.5 0 1 1 .707-.707l1.414 1.414a.5.5 0 0 1 0 .707z"/>' +
        '</svg>';

    // Sync icon to current theme on build
    (function syncThemeIcon() {
        const isLight = document.documentElement.getAttribute('data-theme') === 'light';
        themeBtn.querySelector('#ed-theme-moon').style.display = isLight ? 'none'  : 'block';
        themeBtn.querySelector('#ed-theme-sun').style.display  = isLight ? 'block' : 'none';
    })();

    themeBtn.addEventListener('click', () => {
        const html   = document.documentElement;
        const isLight = html.getAttribute('data-theme') === 'light';
        const next    = isLight ? 'dark' : 'light';
        localStorage.setItem('rtc-theme', next);
        if (next === 'light') {
            html.setAttribute('data-theme', 'light');
            themeBtn.querySelector('#ed-theme-moon').style.display = 'none';
            themeBtn.querySelector('#ed-theme-sun').style.display  = 'block';
        } else {
            html.removeAttribute('data-theme');
            themeBtn.querySelector('#ed-theme-moon').style.display = 'block';
            themeBtn.querySelector('#ed-theme-sun').style.display  = 'none';
        }
    });

    header.append(title, picker, warnBtn, saveBtn, wsDot, themeBtn);

    // ── Body ──
    const body = document.createElement('div');
    body.className = 'pid-body';
    body.style.flex = '1';
    body.style.minHeight = '0';

    // Left sidebar
    const lsb = document.createElement('div');
    lsb.className = 'pid-lsb';
    lsb.innerHTML =
        '<div class="pid-sb-title">Objects</div>' +
        '<div class="pid-obj-item" draggable="true" data-type="sensor">' +
            '<div class="pid-obj-preview">Sensor</div></div>' +
        '<div class="pid-obj-item" draggable="true" data-type="node">' +
            '<div class="pid-obj-preview pid-obj-preview-node">Node</div></div>' +
        '<div class="pid-obj-item" draggable="true" data-type="graph">' +
            '<div class="pid-obj-preview pid-obj-preview-graph">Graph</div></div>' +
        '<div class="pid-obj-item" draggable="true" data-type="valve">' +
            '<div class="pid-obj-preview pid-obj-preview-valve">Valve</div></div>' +
        '<div class="pid-obj-item" draggable="true" data-type="tank">' +
            '<div class="pid-obj-preview pid-obj-preview-tank">Tank</div></div>' +
        '<div class="pid-obj-item" draggable="true" data-type="daqControl">' +
            '<div class="pid-obj-preview pid-obj-preview-daqctrl">DAQ Ctrl</div></div>';

    // Canvas
    const canvasWrap = document.createElement('div');
    canvasWrap.className = 'pid-canvas-wrap';

    const svg = svgN('svg', {
        class: 'pid-svg', width: PID.CANVAS_W, height: PID.CANVAS_H,
        xmlns: 'http://www.w3.org/2000/svg',
    });

    const defs = svgN('defs');
    const pat = svgN('pattern', {
        id: 'pid-grid-editor', x: 0, y: 0,
        width: PID.GRID, height: PID.GRID, patternUnits: 'userSpaceOnUse',
    });
    pat.appendChild(svgN('circle', { cx: 0, cy: 0, r: 0.7, fill: 'var(--border)' }));
    defs.appendChild(pat);
    svg.appendChild(defs);

    const gGrid = svgN('g', { class: 'pid-g-grid' });
    gGrid.appendChild(svgN('rect', {
        x: 0, y: 0, width: PID.CANVAS_W, height: PID.CANVAS_H,
        fill: 'url(#pid-grid-editor)', 'pointer-events': 'none',
    }));
    const gConns = svgN('g', { class: 'pid-g-conns' });
    const gObjs  = svgN('g', { class: 'pid-g-objs'  });
    svg.append(gGrid, gConns, gObjs);
    canvasWrap.appendChild(svg);

    // The selection glow (.selected .po-glow) needs its blur filter/gradient
    // defs installed once; the viewer never selects anything so it never
    // needed this, but the editor shows selection constantly.
    pidEnsureObjDefs(svg);

    // Right sidebar
    const rsb = document.createElement('div');
    rsb.className = 'pid-rsb';

    body.append(lsb, canvasWrap, rsb);

    rootEl.append(header, body);

    // ── Store refs in tab.pid ──
    tab.pid.svgEl         = svg;
    tab.pid.gGrid         = gGrid;
    tab.pid.gConns        = gConns;
    tab.pid.gObjs         = gObjs;
    tab.pid.canvasWrap    = canvasWrap;
    tab.pid.lsbEl         = lsb;
    tab.pid.rsbEl         = rsb;
    tab.pid.pickerEl      = picker;
    tab.pid.warnBtnEl     = warnBtn;
    tab.pid.warnDropdownEl = warnDropdown;

    // ── Events ──
    picker.addEventListener('change', () => {
        const fn = picker.value;
        if (fn && edLayouts[fn]) loadLayout(edLayouts[fn]);
        else                     clearLayout();
    });

    saveBtn.addEventListener('click', savePidYaml);

    lsb.querySelectorAll('[draggable]').forEach(el => {
        el.addEventListener('dragstart', e => e.dataTransfer.setData('pid-type', el.dataset.type));
    });
    svg.addEventListener('dragover', e => e.preventDefault());
    svg.addEventListener('drop',     e => onPidDrop(e));

    svg.addEventListener('pointerdown', e => onPidPointerDown(e));
    svg.addEventListener('pointermove', e => onPidPointerMove(e));
    svg.addEventListener('contextmenu', e => { e.preventDefault(); onPidContextMenu(e); });

    renderPidRsb(null);
}

// =============================================================================
// Layout load / clear / save
// =============================================================================

function loadLayout(record) {
    const parsed = pidFromYaml(record.content);
    tab.pid.layoutFilename = record.filename;
    tab.pid.layoutName     = parsed.name;
    tab.pid.objects        = parsed.objects;
    tab.pid.connections    = parsed.connections;
    tab.pid.selectedId     = null;
    tab.pid.connecting     = null;
    if (tab.pid.pickerEl) tab.pid.pickerEl.value = record.filename;
    renderPidAll();
    renderPidRsb(null);
}

function clearLayout() {
    tab.pid.layoutFilename = '';
    tab.pid.layoutName     = '';
    tab.pid.objects        = [];
    tab.pid.connections    = [];
    tab.pid.selectedId     = null;
    tab.pid.connecting     = null;
    renderPidAll();
    renderPidRsb(null);
}

function savePidYaml() {
    const layout = {
        name:        tab.pid.layoutName || 'Untitled',
        version:     1,
        objects:     tab.pid.objects,
        connections: tab.pid.connections,
    };
    const yaml = pidToYaml(layout);

    // Push to server (saves to disk + re-broadcasts to all front panel clients).
    if (tab.pid.layoutFilename) {
        edSendCtrl(JSON.stringify({
            type:     'set_layout',
            filename: tab.pid.layoutFilename,
            content:  yaml,
        }));
    }

    // Also download locally.
    const dlName = (layout.name.toLowerCase().replace(/[^a-z0-9]+/g, '_') || 'panel') + '.yaml';
    const blob   = new Blob([yaml], { type: 'text/yaml' });
    const url    = URL.createObjectURL(blob);
    const a      = document.createElement('a');
    a.href = url; a.download = dlName; a.click();
    URL.revokeObjectURL(url);
}

// =============================================================================
// Render
// =============================================================================

function renderPidAll() {
    tab.pid.gObjs.innerHTML  = '';
    tab.pid.gConns.innerHTML = '';
    tab.pid.previewEl        = null;
    tab.pid.gGrid.style.display = '';   // always visible in editor

    for (const obj of tab.pid.objects) renderPidObj(obj);

    tab.pid._routedPaths = new Map(); tab.pid.routingErrors = [];
    for (const conn of tab.pid.connections) renderPidConn(conn);
    renderPidWarning();
}

function renderPidObj(obj) {
    const g = obj.type === 'graph'      ? makeGraphGroup(obj)
            : obj.type === 'sensor'     ? makeSensorGroup(obj)
            : obj.type === 'valve'      ? makeValveGroupEditor(obj)
            : obj.type === 'tank'       ? makeTankGroupEditor(obj)
            : obj.type === 'daqControl' ? makeDaqControlGroupEditor(obj)
            : makeNodeGroup(obj);
    tab.pid.gObjs.appendChild(g);
}

function makeGraphGroup(obj) {
    const sel     = (tab.pid.selectedId === obj.id);
    const W       = (obj.gridW || 20) * PID.GRID;
    const H       = (obj.gridH || 10) * PID.GRID;
    const showTitle = obj.showName !== false && obj.name;
    const TB      = showTitle ? 20 : 0;   // title bar height (matches .pid-graph-titlebar ~20px)

    const g = svgN('g', {
        class: 'pid-obj pid-graph' + (sel ? ' pid-selected' : ''),
        'data-pid-id': obj.id,
        transform: 'translate(' + (obj.gridX * PID.GRID) + ',' + (obj.gridY * PID.GRID) + ')',
        cursor: 'grab',
    });

    // Outer border (matches .pid-graph-body border)
    g.appendChild(svgN('rect', {
        x: 0, y: 0, width: W, height: H,
        rx: 4, class: 'pid-graph-rect',
    }));

    // Title bar section (sits inside the outer rect)
    if (showTitle) {
        // Rounded-top only: draw rect + plain rect to square off the bottom corners
        g.appendChild(svgN('rect', { x: 1, y: 1, width: W - 2, height: TB, rx: 3, class: 'pid-graph-titlebar-rect' }));
        g.appendChild(svgN('rect', { x: 1, y: Math.ceil(TB / 2), width: W - 2, height: Math.ceil(TB / 2), class: 'pid-graph-titlebar-rect' }));
        // Separator line between title bar and chart area
        g.appendChild(svgN('line', { class: 'pid-graph-gridline', x1: 0, y1: TB, x2: W, y2: TB }));
        const titleEl = svgN('text', { class: 'pid-graph-titlebar-text', x: 8, y: TB - 5 });
        titleEl.textContent = obj.name;
        g.appendChild(titleEl);
    }

    // Chart area: subtle horizontal grid lines
    const chartTop  = TB;
    const chartH    = H - TB;
    const numLines  = 4;
    for (let i = 1; i <= numLines; i++) {
        const ly = chartTop + Math.round(chartH * i / (numLines + 1));
        g.appendChild(svgN('line', {
            class: 'pid-graph-gridline',
            x1: 0, y1: ly, x2: W, y2: ly,
        }));
    }

    // Centered sublabel (and name label only if no title bar)
    const midY = chartTop + chartH / 2;
    if (!showTitle) {
        const lbl = svgN('text', { class: 'pid-graph-label', x: W / 2, y: midY - 8 });
        lbl.textContent = obj.name || '(no name)';
        g.appendChild(lbl);
    }

    const sub = svgN('text', { class: 'pid-graph-sublabel', x: W / 2, y: showTitle ? midY : midY + 10 });
    sub.textContent = 'Graph \u2022 ' + (obj.lines?.length || 0) + ' line' + (obj.lines?.length === 1 ? '' : 's');
    g.appendChild(sub);

    return g;
}

function makeTankGroupEditor(obj) {
    const sel = (tab.pid.selectedId === obj.id);
    const W   = (obj.gridW  || 5) * PID.GRID;
    const H   = (obj.gridH  || 8) * PID.GRID;
    const rx  = obj.cornerR !== undefined ? obj.cornerR : PID.CORNER_R;
    const rot = obj.rotation || 0;

    const g = svgN('g', {
        class: 'pid-obj pid-tank' + (sel ? ' pid-selected' : ''),
        'data-pid-id': obj.id,
        transform: 'translate(' + (obj.gridX * PID.GRID) + ',' + (obj.gridY * PID.GRID) + ')',
        cursor: 'grab',
    });

    const rect = svgN('rect', {
        x: 0, y: 0, width: W, height: H,
        rx, ry: rx,
        class: 'pid-tank-rect',
    });
    if (rot) rect.setAttribute('transform', 'rotate(' + rot + ',' + (W / 2) + ',' + (H / 2) + ')');
    g.appendChild(rect);

    if (obj.showLabel !== false) {
        const lx  = obj.labelOffsetX || 0;
        const ly  = obj.labelOffsetY || 0;
        const lblG = svgN('g', {
            'data-label-id': obj.id,
            transform: 'translate(' + (W / 2 + lx) + ',' + (H / 2 + ly) + ')',
            style: 'cursor:move',
        });
        const lbl = svgN('text', { class: 'pid-tank-label', x: 0, y: 0 });
        lbl.textContent = obj.label || 'Tank';
        lblG.appendChild(lbl);
        g.appendChild(lblG);
    }

    const tankPorts = {
        top:    [W / 2, 0],
        right:  [W,     H / 2],
        bottom: [W / 2, H],
        left:   [0,     H / 2],
    };
    for (const [pname, [px, py]] of Object.entries(tankPorts)) {
        g.appendChild(svgN('circle', {
            class: 'pid-port',
            'data-obj-id': obj.id, 'data-port': pname,
            cx: px, cy: py, r: PID.PORT_R,
        }));
    }

    return g;
}

// makeSensorGroup mirrors pid.js's makeSensorGroup (the object-system bubble +
// name line + value row) but adds the edit-mode affordances the viewer never
// needs: a full-row hit rectangle to drag by, port circles on all four sides,
// and `.selected` driven off tab.pid.selectedId instead of live data.
function makeSensorGroup(obj) {
    const sel = (tab.pid.selectedId === obj.id);
    const binding = resolveSensorBinding(obj, edConfigControls);
    const refDesText = binding?.ch.refDes || obj.refDes || obj.controlRefDes || '(no refDes)';
    const built = pidBuildObject({
        type:       'sensor',
        name:       obj.label || refDesText,
        showName:   obj.showRefDes !== false,
        units:      obj.units || binding?.ch.units || '',
        showUnits:  obj.showUnits !== false,
        decimals:   obj.decimals,
        bubbleText: obj.bubbleText,
        side:       obj.side || 'right',
        glyph:      obj.showGlyph !== false,
        dataCond:   binding ? 'nodata' : 'unbound',
    });
    const g = built.g;
    // Transient render cache — never serialised (pidToYaml only ever writes
    // named fields), but portPos and the router need to know the drawn width.
    obj._objW = built.refs.width;

    const rot = obj.rotation || 0;
    const ox = obj.gridX * PID.GRID, oy = obj.gridY * PID.GRID;
    const xf = 'translate(' + ox + ',' + oy + ')' +
        (rot ? ' rotate(' + rot + ',' + (built.refs.width / 2) + ',' + (built.refs.height / 2) + ')' : '');
    g.classList.add('pid-obj');
    g.setAttribute('data-pid-id', obj.id);
    g.setAttribute('transform', xf);
    g.style.cursor = 'grab';
    pidSetObjectState(g, binding ? 'nodata' : 'unbound', false, sel);

    // Full-row hit rectangle: the object-system row is mostly unpainted space
    // (text has pointer-events:none per style.css), so without this an
    // unglyphed or blank-area click could not start a drag.
    const hit = svgN('rect', {
        class: 'pid-obj-hit', x: 0, y: 0,
        width: built.refs.width, height: built.refs.height, fill: 'transparent',
    });
    g.insertBefore(hit, g.firstChild);

    for (const pname of ['top', 'right', 'bottom', 'left']) {
        const pp = portPos(obj, pname);
        g.appendChild(svgN('circle', {
            class: 'pid-port',
            'data-obj-id': obj.id, 'data-port': pname,
            cx: pp.x - ox, cy: pp.y - oy, r: PID.PORT_R,
        }));
    }
    return g;
}

function makeNodeGroup(obj) {
    const sel = (tab.pid.selectedId === obj.id);
    const g = svgN('g', {
        class: 'pid-obj pid-node' + (sel ? ' pid-selected' : ''),
        'data-pid-id': obj.id,
        transform: 'translate(' + (obj.gridX * PID.GRID) + ',' + (obj.gridY * PID.GRID) + ')',
        cursor: 'grab',
    });

    g.appendChild(svgN('circle', { class: 'pid-node-dot', cx: 0, cy: 0, r: PID.NODE_R }));

    const ports = { top: [0, -PID.PORT_OFF], right: [PID.PORT_OFF, 0], bottom: [0, PID.PORT_OFF], left: [-PID.PORT_OFF, 0] };
    for (const [pname, [px, py]] of Object.entries(ports)) {
        g.appendChild(svgN('circle', {
            class: 'pid-port',
            'data-obj-id': obj.id, 'data-port': pname,
            cx: px, cy: py, r: PID.PORT_R,
        }));
    }
    return g;
}

// makeValveGroupEditor mirrors pid.js's makeValveGroup (the object-system
// bubble + name line + value row, valve interior) plus the edit-mode
// affordances: a full-row hit rectangle, port circles, and `.selected`.
// GEOMETRY: see the long comment on pid.js's makeValveGroup — the glyph
// centre must land exactly on (gridX*GRID, gridY*GRID) because that's where
// every existing pipe already attaches (portPos is unaware of the row).
function makeValveGroupEditor(obj) {
    const sel   = (tab.pid.selectedId === obj.id);
    const ctrl  = edConfigControls.find(c => c.refDes === obj.controlRefDes);
    const info  = _valveSubtypeInfo(ctrl);
    const cmdCh = ctrl?.channels?.find(c => c.role === 'cmd-bool' || c.role === 'cmd-pct');

    const valveKind = (cmdCh?.role === 'cmd-pct') ? 'pct' : 'io';
    const hasLimits = !!(info.hasFb && !info.fbIsPct);

    const refDesText = obj.controlRefDes || '(no refDes)';
    const built = pidBuildObject({
        type:        'valve',
        valveKind:   valveKind,
        hasLimits:   hasLimits,
        rot:         obj.rotation || 0,
        name:        obj.label || refDesText,
        showName:    obj.showRefDes !== false,
        units:       obj.units || '',
        showUnits:   obj.showUnits !== false,
        decimals:    obj.decimals,
        side:        obj.side || 'right',
        glyph:       obj.showGlyph !== false,
        sampleValue: valveKind === 'pct' ? 'CMD 100%' : 'CLOSED',
        dataCond:    ctrl ? 'nodata' : 'unbound',
    });
    const g = built.g;
    obj._objW = built.refs.width;

    // Shift the row so the GLYPH centre — not the row origin — sits on the
    // grid point (same constraint as pid.js's makeValveGroup).
    const R = PID_OBJ.R;
    const gx = (obj.showGlyph === false) ? 0
             : (((obj.side || 'right') === 'right') ? R + 6 : built.refs.width - (R + 6));
    const gy = PID_OBJ.H / 2;
    const ox = obj.gridX * PID.GRID - gx, oy = obj.gridY * PID.GRID - gy;
    g.classList.add('pid-obj');
    g.setAttribute('data-pid-id', obj.id);
    g.setAttribute('transform', 'translate(' + ox + ',' + oy + ')');
    g.style.cursor = 'grab';
    pidSetObjectState(g, ctrl ? 'nodata' : 'unbound', false, sel);

    const hit = svgN('rect', {
        class: 'pid-obj-hit', x: 0, y: 0,
        width: built.refs.width, height: built.refs.height, fill: 'transparent',
    });
    g.insertBefore(hit, g.firstChild);

    for (const pname of ['top', 'right', 'bottom', 'left']) {
        const pp = portPos(obj, pname);
        g.appendChild(svgN('circle', {
            class: 'pid-port',
            'data-obj-id': obj.id, 'data-port': pname,
            cx: pp.x - ox, cy: pp.y - oy, r: PID.PORT_R,
        }));
    }
    return g;
}

// makeDaqControlGroupEditor mirrors pid.js's makeDaqControlGroup (the
// object-system bubble + name line + value row, machine/diamond interior)
// plus the edit-mode affordances: a full-row hit rectangle, port circles and
// `.selected`. NOTE: portPos's 'daqControl' case still sizes ports off
// obj.gridW/gridH (the old box), which no longer matches this row's drawn
// extent (now text-sized, like sensor/valve) — that mismatch lives in the
// shared portPos() in pidRender.js and predates this change; ports here use
// portPos as the single source of truth the router also uses, so editor and
// viewer still agree with each other even though the dots may sit off the
// visible glyph. Flagged rather than silently patched, since portPos also
// drives the live viewer's routing.
function makeDaqControlGroupEditor(obj) {
    const sel = (tab.pid.selectedId === obj.id);
    const machineName = obj.daqRefDes || '';
    const nameText = obj.label || machineName || '(no refDes)';
    const built = pidBuildObject({
        type:        'machine',
        name:        nameText,
        showName:    obj.showRefDes !== false,
        units:       '',
        showUnits:   false,
        side:        obj.side || 'right',
        glyph:       obj.showGlyph !== false,
        sampleValue: 'autoSequence → autoSequence',
        dataCond:    machineName ? 'nodata' : 'unbound',
    });
    const g = built.g;
    obj._objW = built.refs.width;

    const ox = obj.gridX * PID.GRID, oy = obj.gridY * PID.GRID;
    g.classList.add('pid-obj');
    g.setAttribute('data-pid-id', obj.id);
    g.setAttribute('transform', 'translate(' + ox + ',' + oy + ')');
    g.style.cursor = 'grab';
    pidSetObjectState(g, machineName ? 'nodata' : 'unbound', false, sel);

    const hit = svgN('rect', {
        class: 'pid-obj-hit', x: 0, y: 0,
        width: built.refs.width, height: built.refs.height, fill: 'transparent',
    });
    g.insertBefore(hit, g.firstChild);

    for (const pname of ['top', 'right', 'bottom', 'left']) {
        const pp = portPos(obj, pname);
        g.appendChild(svgN('circle', {
            class: 'pid-port',
            'data-obj-id': obj.id, 'data-port': pname,
            cx: pp.x - ox, cy: pp.y - oy, r: PID.PORT_R,
        }));
    }

    return g;
}

function renderPidConn(conn) {
    const from = tab.pid.objects.find(o => o.id === conn.fromId);
    const to   = tab.pid.objects.find(o => o.id === conn.toId);
    if (!from || !to) return;

    if (!tab.pid._routedPaths) tab.pid._routedPaths = new Map();

    const p1 = portPos(from, conn.fromPort);
    const p2 = portPos(to,   conn.toPort);
    const pipeSegs = pidPipeSegs(tab.pid._routedPaths, conn.id);
    const { d, error, pts } = pidRoute({
        p1, d1: conn.fromPort, p2, d2: conn.toPort,
        objects: tab.pid.objects, pipeSegs,
    });
    tab.pid._routedPaths.set(conn.id, pts);

    if (error) {
        tab.pid.routingErrors.push({
            connId:   conn.id,
            fromId:   conn.fromId,   fromPort: conn.fromPort,
            toId:     conn.toId,     toPort:   conn.toPort,
            message:  error,
        });
    }

    let grp = tab.pid.gConns.querySelector('[data-conn-id="' + conn.id + '"]');
    if (!grp) {
        grp = svgN('g', { 'data-conn-id': conn.id });
        grp.appendChild(svgN('path', { class: 'pid-conn-hit' }));
        grp.appendChild(svgN('path', { class: 'pid-conn-path', 'pointer-events': 'none' }));
        tab.pid.gConns.appendChild(grp);
    }
    const visPath = grp.querySelector('.pid-conn-path');
    const hitPath = grp.querySelector('.pid-conn-hit');
    visPath.setAttribute('d', d);
    hitPath.setAttribute('d', d);
    visPath.classList.toggle('pid-conn-error', !!error);
    grp.classList.toggle('pid-conn-selected', tab.pid.selectedConnId === conn.id);

    grp.className.baseVal = grp.className.baseVal.replace(/\bpid-conn-fluid-\S+/g, '').trim();
    if (conn.fluid) grp.classList.add('pid-conn-fluid-' + conn.fluid);
    // Explicit per-connection color (optional) overrides the fluid-type default;
    // absent = current appearance (fluid class, or the plain default stroke).
    visPath.style.stroke = conn.color || '';
}

function updateConnsTouching() {
    tab.pid._routedPaths = new Map(); tab.pid.routingErrors = [];
    for (const conn of tab.pid.connections) renderPidConn(conn);
    renderPidWarning();
}

// =============================================================================
// Problem list — a DRAWING check, not a config check (design:
// docs/design/sensor-object-options.html, EDITOR).
//
// ONE FLAT LIST. No severities, no categories, no icons per row — with no
// severities an icon could only ever say "problem" on a list where every row
// is a problem. Every row is a location plus a one-line reason, and clicking
// a row selects the offending element.
//
// Fed from two sources: tab.pid.routingErrors (unroutable pipes — the only
// thing this list held before) and every sensor object whose binding
// resolveSensorBinding(...) cannot resolve (both of its null cases: the
// control isn't found, and the control is found but has no readable
// channel) — previously invisible, reaching no list at all.
// =============================================================================

// pidSensorUnboundReason walks the same two paths resolveSensorBinding does
// (pidRender.js) but returns why it failed, for display, rather than null.
function pidSensorUnboundReason(obj, controls) {
    if (obj.controlRefDes) {
        const ctrl = controls.find(c => c.refDes === obj.controlRefDes);
        if (!ctrl) return 'controlRefDes names nothing.';
        const readable = (ctrl.channels ?? []).filter(pidIsReadableChannel);
        if (!readable.length) return 'Control has no readable channel.';
        return null;
    }
    if (obj.refDes) {
        for (const ctrl of controls) {
            if (ctrl.channels?.find(c => c.refDes === obj.refDes)) return null;
        }
        return 'refDes names no channel.';
    }
    return 'Not bound to a control.';
}

function computeEdProblems() {
    const items = [];
    for (const err of tab.pid.routingErrors) {
        const from = tab.pid.objects.find(o => o.id === err.fromId);
        const to   = tab.pid.objects.find(o => o.id === err.toId);
        const fromName = from ? (from.refDes || from.controlRefDes || from.daqRefDes || from.type) : err.fromId;
        const toName   = to   ? (to.refDes   || to.controlRefDes   || to.daqRefDes   || to.type)   : err.toId;
        items.push({
            objId: null, connId: err.connId,
            title: fromName + ':' + err.fromPort + ' → ' + toName + ':' + err.toPort,
            detail: err.message,
        });
    }
    for (const obj of tab.pid.objects) {
        if (obj.type !== 'sensor') continue;
        const reason = pidSensorUnboundReason(obj, edConfigControls);
        if (!reason) continue;
        items.push({
            objId: obj.id, connId: null,
            title: obj.controlRefDes || obj.refDes || obj.label || obj.id,
            detail: reason,
        });
    }
    return items;
}

function renderPidWarning() {
    const btn = tab.pid.warnBtnEl;
    if (!btn) return;
    const items = computeEdProblems();
    tab.pid.problems = items;
    btn.style.display = items.length > 0 ? '' : 'none';
    const countEl = btn.querySelector('.pid-warn-count');
    if (countEl) countEl.textContent = items.length > 1 ? String(items.length) : '';
    if (btn.classList.contains('pid-warn-open')) renderPidWarnDropdown();
}

function renderPidWarnDropdown() {
    const dropdown = tab.pid.warnDropdownEl;
    if (!dropdown) return;
    const items = tab.pid.problems || [];
    dropdown.innerHTML = '';
    if (!items.length) return;

    const title = document.createElement('div');
    title.className = 'pid-warn-title';
    title.textContent = 'Problems (' + items.length + ')';
    dropdown.appendChild(title);

    for (const item of items) {
        const row = document.createElement('div');
        row.className = 'pid-warn-item';
        row.style.cursor = 'pointer';
        row.innerHTML =
            '<div class="pid-warn-conn">' + pidEsc(item.title)  + '</div>' +
            '<div class="pid-warn-msg">'  + pidEsc(item.detail) + '</div>';
        row.addEventListener('click', () => {
            if (item.objId)       selectPidObject(item.objId);
            else if (item.connId) selectPidConn(item.connId);
            tab.pid.warnBtnEl.classList.remove('pid-warn-open');
        });
        dropdown.appendChild(row);
    }
}

// =============================================================================
// Selection & right sidebar
// =============================================================================

function selectPidObject(id) {
    tab.pid.selectedId = id;
    // '.pid-selected' drives the old boxed-object CSS (still used by tank,
    // node, graph); '.selected' drives the object system's glow (sensor,
    // valve, daqControl — see pidSetObjectState in pidRender.js). Both are
    // toggled here so selection works regardless of which system an object
    // uses; each is inert on the other's markup.
    tab.pid.gObjs.querySelectorAll('.pid-selected, .selected').forEach(el => el.classList.remove('pid-selected', 'selected'));
    if (id) {
        const el = tab.pid.gObjs.querySelector('[data-pid-id="' + id + '"]');
        if (el) el.classList.add('pid-selected', 'selected');
    }
    // Clear any pipe selection
    tab.pid.selectedConnId = null;
    tab.pid.gConns.querySelectorAll('.pid-conn-selected').forEach(el => el.classList.remove('pid-conn-selected'));
    renderPidRsb(id);
}

function selectPidConn(connId) {
    // Clear object selection
    tab.pid.selectedId = null;
    tab.pid.gObjs.querySelectorAll('.pid-selected').forEach(el => el.classList.remove('pid-selected'));
    // Clear previous pipe selection
    tab.pid.gConns.querySelectorAll('.pid-conn-selected').forEach(el => el.classList.remove('pid-conn-selected'));

    tab.pid.selectedConnId = connId;
    if (connId) {
        const grp = tab.pid.gConns.querySelector('[data-conn-id="' + connId + '"]');
        if (grp) grp.classList.add('pid-conn-selected');
    }
    renderPidConnRsb(connId);
}

function renderPidConnRsb(connId) {
    edLiveRefDes = null;
    edLiveEl = null;
    edLiveStale.cancel();

    const rsb = tab.pid.rsbEl;
    rsb.innerHTML = '';
    const c = document.createElement('div');
    c.className = 'pid-rsb-content';

    if (!connId) {
        renderPidRsb(null);
        return;
    }

    const conn = tab.pid.connections.find(cn => cn.id === connId);
    if (!conn) { rsb.appendChild(c); return; }

    const fromObj = tab.pid.objects.find(o => o.id === conn.fromId);
    const toObj   = tab.pid.objects.find(o => o.id === conn.toId);
    const fromName = fromObj ? (fromObj.refDes || fromObj.type + ' ' + fromObj.id) : conn.fromId;
    const toName   = toObj   ? (toObj.refDes   || toObj.type   + ' ' + toObj.id)   : conn.toId;

    const fluidOpts = FLUIDS.map(f =>
        '<option value="' + f + '"' + (conn.fluid === f ? ' selected' : '') + '>' + FLUID_LABELS[f] + '</option>'
    ).join('');

    c.innerHTML =
        '<div class="pid-sb-heading">Pipe</div>' +
        '<div class="pid-sb-field"><label>From</label>' +
        '<span class="pid-sb-value">' + pidEsc(fromName) + ' : ' + pidEsc(conn.fromPort) + '</span></div>' +
        '<div class="pid-sb-field"><label>To</label>' +
        '<span class="pid-sb-value">' + pidEsc(toName) + ' : ' + pidEsc(conn.toPort) + '</span></div>' +
        '<div class="pid-sb-field"><label>Fluid</label>' +
        '<select class="pid-fluid-select">' + fluidOpts + '</select></div>' +
        '<div class="pid-sb-field"><label>Color</label>' +
        '<div class="pid-conn-color-row">' +
            '<div class="color-swatch pid-conn-color-swatch" title="Click to set a custom pipe color"></div>' +
            '<button class="pid-conn-color-clear" title="Clear — use default/fluid color">Default</button>' +
        '</div></div>' +
        '<button class="pid-delete-btn">Remove</button>';

    c.querySelector('.pid-fluid-select').addEventListener('change', e => {
        conn.fluid = e.target.value || undefined;
        renderPidConn(conn);
    });

    const colorSwatch = c.querySelector('.pid-conn-color-swatch');
    const updateColorSwatch = () => {
        colorSwatch.style.background = conn.color || 'repeating-linear-gradient(45deg, var(--surface), var(--surface) 4px, var(--border) 4px, var(--border) 8px)';
    };
    updateColorSwatch();
    colorSwatch.addEventListener('click', () => {
        openColorPalette(colorSwatch, conn.color || PID_COLOR_PALETTE[0], PID_COLOR_PALETTE, (newColor) => {
            conn.color = newColor;
            updateColorSwatch();
            renderPidConn(conn);
        });
    });
    c.querySelector('.pid-conn-color-clear').addEventListener('click', () => {
        conn.color = undefined;
        updateColorSwatch();
        renderPidConn(conn);
    });

    c.querySelector('.pid-delete-btn').addEventListener('click', () => deletePidConn(connId));
    rsb.appendChild(c);
}

function deletePidConn(id) {
    tab.pid.connections = tab.pid.connections.filter(c => {
        if (c.id === id) {
            tab.pid.gConns.querySelector('[data-conn-id="' + c.id + '"]')?.remove();
            return false;
        }
        return true;
    });
    selectPidConn(null);
    tab.pid._routedPaths = new Map(); tab.pid.routingErrors = [];
    for (const conn of tab.pid.connections) renderPidConn(conn);
    renderPidWarning();
}

function renderPidRsb(objId) {
    // Clear live tracking whenever the sidebar is rebuilt
    edLiveRefDes = null;
    edLiveEl = null;
    edLiveStale.cancel();

    const rsb = tab.pid.rsbEl;
    rsb.innerHTML = '';
    const c = document.createElement('div');
    c.className = 'pid-rsb-content';

    if (!objId) {
        c.innerHTML =
            '<div class="pid-sb-heading">Layout</div>' +
            '<div class="pid-sb-field"><label>Name</label>' +
            '<input class="pid-name-input" type="text" value="' + pidEsc(tab.pid.layoutName || '') + '" placeholder="Panel name"></div>' +
            '<div class="pid-sb-hint">Save YAML and add the file to the<br>control node config under<br>&lt;frontPanels&gt;.</div>';
        c.querySelector('.pid-name-input').addEventListener('input', e => {
            tab.pid.layoutName = e.target.value;
        });
    } else {
        const obj = tab.pid.objects.find(o => o.id === objId);
        if (!obj) { rsb.appendChild(c); return; }

        if (obj.type === 'sensor') {
            // Objects reference controls, not channels: the primary picker is
            // a control (like the valve object's picker below); a channel
            // sub-picker only appears when that control has more than one
            // readable channel (obj.channelRefDes picks among them — see
            // resolveSensorBinding in pidRender.js). obj.refDes (a bare
            // channel, no owning control field) is the legacy binding form;
            // editing and re-applying a legacy object here migrates it to
            // controlRefDes, which is fine — it's an explicit user edit, not
            // a silent rewrite of untouched layout files.
            const curBinding = resolveSensorBinding(obj, edConfigControls);
            const sensorControls = edConfigControls.filter(ctrl => (ctrl.channels || []).some(pidIsReadableChannel));
            const selectedCtrlRefDes = obj.controlRefDes || curBinding?.ctrl.refDes || '';

            const ctrlOpts = sensorControls.length > 0
                ? sensorControls.map(ctrl =>
                    '<option value="' + pidEsc(ctrl.refDes) + '"' +
                    (ctrl.refDes === selectedCtrlRefDes ? ' selected' : '') + '>' +
                    pidEsc(ctrl.refDes) + (ctrl.description ? ' — ' + pidEsc(ctrl.description) : '') +
                    '</option>'
                  ).join('')
                : null;

            const curRot    = obj.rotation || 0;
            const curSide   = obj.side || 'right';
            const hasBubble = obj.bubbleText !== undefined;
            const curDec    = (typeof obj.decimals === 'number') ? obj.decimals : 2;
            c.innerHTML =
                '<div class="pid-sb-heading">Sensor</div>' +
                '<div class="pid-sb-field"><label>Control</label>' +
                (ctrlOpts
                    ? '<select class="pid-sensor-ctrl-sel"><option value="">-- pick --</option>' + ctrlOpts + '</select>'
                    : '<input class="pid-sensor-ctrl-inp" type="text" value="' + pidEsc(selectedCtrlRefDes) + '" placeholder="e.g. PT-01">') +
                '</div>' +
                '<div class="pid-sb-field pid-sensor-channel-field" style="display:none"><label>Channel</label>' +
                '<select class="pid-sensor-channel-sel"></select></div>' +
                '<div class="pid-sb-field"><label>Units</label>' +
                '<input class="pid-units-inp" type="text" value="' + pidEsc(obj.units || '') + '" placeholder="(auto from channel)"></div>' +
                '<div class="pid-sb-field"><label>Label</label>' +
                '<input class="pid-label-inp" type="text" value="' + pidEsc(obj.label || '') + '" placeholder="(default: refDes)"></div>' +
                '<div class="pid-sb-check"><label><input type="checkbox" class="pid-bubble-custom"' + (hasBubble ? ' checked' : '') + '> Custom bubble text</label></div>' +
                '<div class="pid-sb-field"><label>Bubble text</label>' +
                '<input class="pid-bubble-inp" type="text" value="' + pidEsc(obj.bubbleText || '') + '" placeholder="(default: from refDes)"' + (hasBubble ? '' : ' disabled') + '></div>' +
                '<div class="pid-sb-field"><label>Decimals</label>' +
                '<input class="pid-decimals-inp" type="number" min="0" max="6" value="' + curDec + '"></div>' +
                '<div class="pid-sb-field"><label>Side</label>' +
                '<select class="pid-side-sel">' +
                    '<option value="right"' + (curSide === 'right' ? ' selected' : '') + '>Right</option>' +
                    '<option value="left"'  + (curSide === 'left'  ? ' selected' : '') + '>Left</option>' +
                '</select></div>' +
                '<div class="pid-sb-heading pid-sb-heading--sm">Front Panel Display</div>' +
                '<div class="pid-sb-check"><label><input type="checkbox" class="pid-show-refdes"' + (obj.showRefDes !== false ? ' checked' : '') + '> Show name line</label></div>' +
                '<div class="pid-sb-check"><label><input type="checkbox" class="pid-show-units"'  + (obj.showUnits  !== false ? ' checked' : '') + '> Show units</label></div>' +
                '<div class="pid-sb-field"><label>Rotation</label>' +
                '<select class="pid-sensor-rotation">' +
                    '<option value="0"'   + (curRot === 0   ? ' selected' : '') + '>0\u00b0</option>'   +
                    '<option value="90"'  + (curRot === 90  ? ' selected' : '') + '>90\u00b0</option>'  +
                    '<option value="180"' + (curRot === 180 ? ' selected' : '') + '>180\u00b0</option>' +
                    '<option value="270"' + (curRot === 270 ? ' selected' : '') + '>270\u00b0</option>' +
                '</select></div>' +
                '<button class="pid-apply-btn">Apply</button>' +
                '<button class="pid-delete-btn">Remove</button>';

            const ctrlSel = c.querySelector('.pid-sensor-ctrl-sel');
            const ctrlInp = c.querySelector('.pid-sensor-ctrl-inp');
            const chField = c.querySelector('.pid-sensor-channel-field');
            const chSel   = c.querySelector('.pid-sensor-channel-sel');
            const uinp    = c.querySelector('.pid-units-inp');
            const bubbleChk = c.querySelector('.pid-bubble-custom');
            const bubbleInp = c.querySelector('.pid-bubble-inp');
            bubbleChk.addEventListener('change', () => { bubbleInp.disabled = !bubbleChk.checked; });

            // Rebuild the channel sub-picker for whichever control is
            // currently chosen; shown only when there's a real choice to make
            // (a control with exactly one readable channel needs no picker —
            // that channel is used implicitly).
            function refreshSensorChannelSel() {
                const ctrlRefDes = ctrlSel ? ctrlSel.value : (ctrlInp ? ctrlInp.value.trim() : '');
                const ctrl = edConfigControls.find(cc => cc.refDes === ctrlRefDes);
                const readable = ctrl ? (ctrl.channels || []).filter(pidIsReadableChannel) : [];
                if (readable.length > 1) {
                    chField.style.display = '';
                    chSel.innerHTML = readable.map(ch =>
                        '<option value="' + pidEsc(ch.refDes) + '"' +
                        (ch.refDes === obj.channelRefDes ? ' selected' : '') + '>' +
                        pidEsc(ch.refDes) + '</option>'
                    ).join('');
                } else {
                    chField.style.display = 'none';
                    chSel.innerHTML = '';
                }
            }
            refreshSensorChannelSel();
            if (ctrlSel) ctrlSel.addEventListener('change', refreshSensorChannelSel);
            if (ctrlInp) ctrlInp.addEventListener('input', refreshSensorChannelSel);

            c.querySelector('.pid-apply-btn').addEventListener('click', () => {
                const ctrlRefDes = ctrlSel ? ctrlSel.value : (ctrlInp ? ctrlInp.value.trim() : '');
                if (ctrlRefDes) {
                    obj.controlRefDes = ctrlRefDes;
                    delete obj.refDes;   // migrate away from the legacy channel-only form
                    const chVal = (chField.style.display !== 'none') ? chSel.value : '';
                    if (chVal) obj.channelRefDes = chVal; else delete obj.channelRefDes;
                } else {
                    delete obj.controlRefDes;
                    delete obj.channelRefDes;
                }
                obj.units      = uinp.value.trim();
                const labelVal = c.querySelector('.pid-label-inp').value.trim();
                if (labelVal) obj.label = labelVal; else delete obj.label;
                if (bubbleChk.checked) obj.bubbleText = bubbleInp.value; else delete obj.bubbleText;
                const decVal = parseInt(c.querySelector('.pid-decimals-inp').value, 10);
                if (!isNaN(decVal) && decVal >= 0 && decVal <= 6 && decVal !== 2) obj.decimals = decVal; else delete obj.decimals;
                const sideVal = c.querySelector('.pid-side-sel').value;
                if (sideVal === 'left') obj.side = 'left'; else delete obj.side;
                obj.showRefDes = c.querySelector('.pid-show-refdes').checked;
                obj.showUnits  = c.querySelector('.pid-show-units').checked;
                obj.rotation   = parseInt(c.querySelector('.pid-sensor-rotation').value) || 0;
                // Re-render the object in place to reflect display flag changes
                const existing = tab.pid.gObjs.querySelector('[data-pid-id="' + objId + '"]');
                if (existing) existing.remove();
                renderPidObj(obj);
                // Re-apply selection highlight
                const updated = tab.pid.gObjs.querySelector('[data-pid-id="' + objId + '"]');
                if (updated) updated.classList.add('pid-selected', 'selected');
                // Re-bind live display to the new binding
                const binding = resolveSensorBinding(obj, edConfigControls);
                edLiveRefDes = binding?.ch.refDes || null;
                if (edLiveEl && edLiveRefDes && edLiveValues[edLiveRefDes] !== undefined) {
                    const v = edLiveValues[edLiveRefDes];
                    edLiveEl.textContent = fmtEdLiveValue(v);
                    edLiveEl.classList.remove('stale');
                } else if (edLiveEl) {
                    edLiveEl.textContent = '--';
                }
                // A binding edit can resolve or introduce an unbound-sensor
                // problem row.
                renderPidWarning();
            });

            // ── Live value row ──
            const liveRow = document.createElement('div');
            liveRow.className = 'pid-sb-field ed-live-row';
            const liveLabel = document.createElement('label');
            liveLabel.textContent = 'Live';
            const liveVal = document.createElement('span');
            liveVal.className = 'ed-live-value';
            const initRefDes = curBinding?.ch.refDes || null;
            const initVal = initRefDes ? edLiveValues[initRefDes] : undefined;
            liveVal.textContent = initVal !== undefined ? fmtEdLiveValue(initVal) : '--';
            liveRow.append(liveLabel, liveVal);
            c.appendChild(liveRow);

            // Register for live updates
            edLiveRefDes = initRefDes;
            edLiveEl     = liveVal;

        } else if (obj.type === 'graph') {
            // ── Graph object configuration ────────────────────────────────────

            c.innerHTML =
                '<div class="pid-sb-heading">Graph</div>' +
                '<div class="pid-sb-field"><label>Name</label>' +
                '<input class="pid-graph-name" type="text" value="' + pidEsc(obj.name || '') + '" placeholder="e.g. LOX Pressure"></div>' +
                '<div class="pid-sb-field pid-sb-field--row">' +
                    '<div><label>Width (cells)</label>' +
                    '<input class="pid-graph-w" type="number" min="4" max="100" value="' + (obj.gridW || 20) + '"></div>' +
                    '<div><label>Height (cells)</label>' +
                    '<input class="pid-graph-h" type="number" min="4" max="100" value="' + (obj.gridH || 10) + '"></div>' +
                '</div>' +
                '<div class="pid-sb-check"><label><input type="checkbox" class="pid-graph-show-name"' + (obj.showName !== false ? ' checked' : '') + '> Show title bar</label></div>' +
                '<div class="pid-sb-check"><label><input type="checkbox" class="pid-graph-show-lsb"'  + (obj.showLeftSidebar ? ' checked' : '') + '> Show channel list</label></div>' +
                '<div class="pid-sb-field"><label>Legend</label>' +
                '<select class="pid-graph-legend-pos">' +
                    '<option value="none"'   + ((!obj.legendPosition || obj.legendPosition === 'none')   ? ' selected' : '') + '>None</option>'   +
                    '<option value="bottom"' + (obj.legendPosition === 'bottom' ? ' selected' : '') + '>Bottom</option>' +
                    '<option value="left"'   + (obj.legendPosition === 'left'   ? ' selected' : '') + '>Left</option>'   +
                '</select></div>' +
                '<div class="pid-sb-heading pid-sb-heading--sm">Channels</div>' +
                '<div class="pid-graph-channel-list"></div>' +
                '<div class="pid-sb-field pid-graph-add-row">' +
                    '<input class="pid-graph-add-inp" type="text" placeholder="Add channel (refDes)...">' +
                    '<div class="pid-graph-add-dropdown" style="display:none"></div>' +
                '</div>' +
                '<button class="pid-apply-btn">Apply</button>' +
                '<button class="pid-delete-btn">Remove</button>';

            // Render the channel list
            function renderGraphChannelList() {
                const list = c.querySelector('.pid-graph-channel-list');
                list.innerHTML = '';
                for (let li = 0; li < obj.lines.length; li++) {
                    const line = obj.lines[li];
                    const row = document.createElement('div');
                    row.className = 'pid-graph-ch-row';

                    const swatch = document.createElement('div');
                    swatch.className = 'pid-graph-color-swatch';
                    swatch.style.background = line.color || '#4e9f3d';
                    swatch.title = 'Click to change color';
                    const colorInp = document.createElement('input');
                    colorInp.type = 'color';
                    colorInp.value = line.color || '#4e9f3d';
                    colorInp.style.cssText = 'position:absolute;width:0;height:0;opacity:0;';
                    swatch.appendChild(colorInp);
                    swatch.addEventListener('click', () => colorInp.click());
                    colorInp.addEventListener('input', () => {
                        line.color = colorInp.value;
                        swatch.style.background = colorInp.value;
                    });

                    const badge = document.createElement('span');
                    badge.className = 'pid-graph-y-badge';
                    badge.textContent = 'Y' + (line.yAxis || 1);
                    badge.title = 'Click to cycle Y axis';
                    badge.addEventListener('click', () => {
                        line.yAxis = ((line.yAxis || 1) % 6) + 1;
                        badge.textContent = 'Y' + line.yAxis;
                    });

                    const rdLbl = document.createElement('span');
                    rdLbl.className = 'pid-graph-ch-refdes';
                    rdLbl.textContent = line.refDes;

                    const rmBtn = document.createElement('button');
                    rmBtn.className = 'pid-graph-ch-rm';
                    rmBtn.textContent = '×';
                    rmBtn.addEventListener('click', () => {
                        obj.lines.splice(li, 1);
                        renderGraphChannelList();
                    });

                    row.append(swatch, colorInp, badge, rdLbl, rmBtn);
                    list.appendChild(row);
                }
            }
            renderGraphChannelList();

            // Channel search / add dropdown
            const addInp  = c.querySelector('.pid-graph-add-inp');
            const addDrop = c.querySelector('.pid-graph-add-dropdown');
            const CHART_COLORS_ED = ['#4e9f3d','#4fc3f7','#ff7043','#ffd54f','#ba68c8','#4db6ac','#f06292','#aed581','#ff8a65','#90a4ae'];

            addInp.addEventListener('input', () => {
                const q = addInp.value.trim();
                if (!q) { addDrop.style.display = 'none'; return; }
                let re;
                try { re = new RegExp(q, 'i'); } catch { addDrop.style.display = 'none'; return; }
                const used = new Set(obj.lines.map(l => l.refDes));
                const matches = [];
                for (const ctrl of edConfigControls) {
                    for (const ch of (ctrl.channels || [])) {
                        if (!used.has(ch.refDes) && (re.test(ch.refDes) || re.test(ctrl.description || ''))) {
                            matches.push({ refDes: ch.refDes, desc: ctrl.description || '' });
                        }
                    }
                }
                const trimmed = matches.slice(0, 15);
                addDrop.innerHTML = '';
                if (!trimmed.length) { addDrop.style.display = 'none'; return; }
                for (const { refDes, desc } of trimmed) {
                    const item = document.createElement('div');
                    item.className = 'pid-graph-add-item';
                    item.innerHTML = '<span class="pid-graph-add-rd">' + pidEsc(refDes) + '</span>' +
                                     (desc ? '<span class="pid-graph-add-desc">' + pidEsc(desc) + '</span>' : '');
                    item.addEventListener('mousedown', (ev) => {
                        ev.preventDefault();
                        const usedColors = obj.lines.map(l => l.color);
                        const color = CHART_COLORS_ED.find(c => !usedColors.includes(c)) || CHART_COLORS_ED[obj.lines.length % CHART_COLORS_ED.length];
                        obj.lines.push({ refDes, color, yAxis: 1, hidden: false });
                        addInp.value = '';
                        addDrop.style.display = 'none';
                        renderGraphChannelList();
                    });
                    addDrop.appendChild(item);
                }
                addDrop.style.display = '';
            });
            addInp.addEventListener('blur', () => setTimeout(() => { addDrop.style.display = 'none'; }, 150));

            c.querySelector('.pid-apply-btn').addEventListener('click', () => {
                obj.name          = c.querySelector('.pid-graph-name').value.trim();
                obj.gridW         = parseInt(c.querySelector('.pid-graph-w').value) || 20;
                obj.gridH         = parseInt(c.querySelector('.pid-graph-h').value) || 10;
                obj.showName        = c.querySelector('.pid-graph-show-name').checked;
                obj.showLeftSidebar = c.querySelector('.pid-graph-show-lsb').checked;
                obj.legendPosition  = c.querySelector('.pid-graph-legend-pos').value;
                // Re-render the placeholder to reflect new size and name
                const existing = tab.pid.gObjs.querySelector('[data-pid-id="' + objId + '"]');
                if (existing) existing.remove();
                renderPidObj(obj);
                const updated = tab.pid.gObjs.querySelector('[data-pid-id="' + objId + '"]');
                if (updated) updated.classList.add('pid-selected', 'selected');
                tab.pid._routedPaths = new Map(); tab.pid.routingErrors = [];
                for (const conn of tab.pid.connections) renderPidConn(conn);
                renderPidWarning();
            });

        } else if (obj.type === 'valve') {
            const valveControls = edConfigControls.filter(c => c.type === 'valve');
            const opts = valveControls.length > 0
                ? valveControls.map(ctrl =>
                    '<option value="' + pidEsc(ctrl.refDes) + '"' +
                    (ctrl.refDes === obj.controlRefDes ? ' selected' : '') + '>' +
                    pidEsc(ctrl.refDes) + (ctrl.description ? ' \u2014 ' + pidEsc(ctrl.description) : '') +
                    '</option>'
                  ).join('')
                : null;

            const curRot  = obj.rotation || 0;
            const curSide = obj.side || 'right';
            const curDec  = (typeof obj.decimals === 'number') ? obj.decimals : 2;
            c.innerHTML =
                '<div class="pid-sb-heading">Valve</div>' +
                '<div class="pid-sb-field"><label>Control</label>' +
                (opts
                    ? '<select class="pid-valve-ctrl-sel"><option value="">-- pick --</option>' + opts + '</select>'
                    : '<input class="pid-valve-ctrl-inp" type="text" value="' + pidEsc(obj.controlRefDes || '') + '" placeholder="e.g. NV-03">') +
                '</div>' +
                '<div class="pid-sb-field"><label>Units</label>' +
                '<input class="pid-units-inp" type="text" value="' + pidEsc(obj.units || '') + '" placeholder="(optional)"></div>' +
                '<div class="pid-sb-field"><label>Label</label>' +
                '<input class="pid-label-inp" type="text" value="' + pidEsc(obj.label || '') + '" placeholder="(default: control refDes)"></div>' +
                '<div class="pid-sb-field"><label>Decimals</label>' +
                '<input class="pid-decimals-inp" type="number" min="0" max="6" value="' + curDec + '"></div>' +
                '<div class="pid-sb-field"><label>Side</label>' +
                '<select class="pid-side-sel">' +
                    '<option value="right"' + (curSide === 'right' ? ' selected' : '') + '>Right</option>' +
                    '<option value="left"'  + (curSide === 'left'  ? ' selected' : '') + '>Left</option>' +
                '</select></div>' +
                '<div class="pid-sb-heading pid-sb-heading--sm">Display</div>' +
                '<div class="pid-sb-check"><label><input type="checkbox" class="pid-show-refdes"' + (obj.showRefDes !== false ? ' checked' : '') + '> Show name line</label></div>' +
                '<div class="pid-sb-check"><label><input type="checkbox" class="pid-show-units"'  + (obj.showUnits  !== false ? ' checked' : '') + '> Show units</label></div>' +
                '<div class="pid-sb-field"><label>Rotation</label>' +
                '<select class="pid-valve-rotation">' +
                    '<option value="0"'  + (curRot === 0  ? ' selected' : '') + '>0°</option>'  +
                    '<option value="90"' + (curRot === 90 ? ' selected' : '') + '>90°</option>' +
                '</select></div>' +
                '<button class="pid-apply-btn">Apply</button>' +
                '<button class="pid-delete-btn">Remove</button>';

            c.querySelector('.pid-apply-btn').addEventListener('click', () => {
                const sel = c.querySelector('.pid-valve-ctrl-sel');
                const inp = c.querySelector('.pid-valve-ctrl-inp');
                obj.controlRefDes = sel ? sel.value : (inp ? inp.value.trim() : '');
                obj.units         = c.querySelector('.pid-units-inp').value.trim();
                const labelVal = c.querySelector('.pid-label-inp').value.trim();
                if (labelVal) obj.label = labelVal; else delete obj.label;
                const decVal = parseInt(c.querySelector('.pid-decimals-inp').value, 10);
                if (!isNaN(decVal) && decVal >= 0 && decVal <= 6 && decVal !== 2) obj.decimals = decVal; else delete obj.decimals;
                const sideVal = c.querySelector('.pid-side-sel').value;
                if (sideVal === 'left') obj.side = 'left'; else delete obj.side;
                obj.showRefDes    = c.querySelector('.pid-show-refdes').checked;
                obj.showUnits     = c.querySelector('.pid-show-units').checked;
                obj.rotation      = parseInt(c.querySelector('.pid-valve-rotation').value) || 0;
                const existing = tab.pid.gObjs.querySelector('[data-pid-id="' + objId + '"]');
                if (existing) existing.remove();
                renderPidObj(obj);
                const updated = tab.pid.gObjs.querySelector('[data-pid-id="' + objId + '"]');
                if (updated) updated.classList.add('pid-selected', 'selected');
                tab.pid._routedPaths = new Map(); tab.pid.routingErrors = [];
                for (const conn of tab.pid.connections) renderPidConn(conn);
                renderPidWarning();
            });

        } else if (obj.type === 'tank') {
            const curRot = obj.rotation || 0;
            c.innerHTML =
                '<div class="pid-sb-heading">Tank</div>' +
                '<div class="pid-sb-field pid-sb-field--row">' +
                    '<div><label>Width (cells)</label>' +
                    '<input class="pid-tank-w" type="number" min="1" max="100" value="' + (obj.gridW || 5) + '"></div>' +
                    '<div><label>Height (cells)</label>' +
                    '<input class="pid-tank-h" type="number" min="1" max="100" value="' + (obj.gridH || 8) + '"></div>' +
                '</div>' +
                '<div class="pid-sb-field"><label>Corner radius (px)</label>' +
                '<input class="pid-tank-corner" type="number" min="0" max="100" value="' + (obj.cornerR !== undefined ? obj.cornerR : PID.CORNER_R) + '"></div>' +
                '<div class="pid-sb-field"><label>Rotation</label>' +
                '<select class="pid-tank-rotation">' +
                    '<option value="0"'   + (curRot === 0   ? ' selected' : '') + '>0\u00b0</option>'   +
                    '<option value="90"'  + (curRot === 90  ? ' selected' : '') + '>90\u00b0</option>'  +
                    '<option value="180"' + (curRot === 180 ? ' selected' : '') + '>180\u00b0</option>' +
                    '<option value="270"' + (curRot === 270 ? ' selected' : '') + '>270\u00b0</option>' +
                '</select></div>' +
                '<div class="pid-sb-heading pid-sb-heading--sm">Label</div>' +
                '<div class="pid-sb-field"><label>Text</label>' +
                '<input class="pid-tank-label-inp" type="text" value="' + pidEsc(obj.label || '') + '" placeholder="e.g. LOX Tank"></div>' +
                '<div class="pid-sb-check"><label><input type="checkbox" class="pid-show-label"' + (obj.showLabel !== false ? ' checked' : '') + '> Show label</label></div>' +
                '<button class="pid-reset-label-btn">Reset label position</button>' +
                '<button class="pid-apply-btn">Apply</button>' +
                '<button class="pid-delete-btn">Remove</button>';

            c.querySelector('.pid-reset-label-btn').addEventListener('click', () => {
                obj.labelOffsetX = 0;
                obj.labelOffsetY = 0;
                const existing = tab.pid.gObjs.querySelector('[data-pid-id="' + objId + '"]');
                if (existing) existing.remove();
                renderPidObj(obj);
                const updated = tab.pid.gObjs.querySelector('[data-pid-id="' + objId + '"]');
                if (updated) updated.classList.add('pid-selected', 'selected');
            });

            c.querySelector('.pid-apply-btn').addEventListener('click', () => {
                obj.gridW     = parseInt(c.querySelector('.pid-tank-w').value)       || 5;
                obj.gridH     = parseInt(c.querySelector('.pid-tank-h').value)       || 8;
                obj.cornerR   = parseInt(c.querySelector('.pid-tank-corner').value);
                obj.rotation  = parseInt(c.querySelector('.pid-tank-rotation').value) || 0;
                obj.label     = c.querySelector('.pid-tank-label-inp').value.trim();
                obj.showLabel = c.querySelector('.pid-show-label').checked;
                const existing = tab.pid.gObjs.querySelector('[data-pid-id="' + objId + '"]');
                if (existing) existing.remove();
                renderPidObj(obj);
                const updated = tab.pid.gObjs.querySelector('[data-pid-id="' + objId + '"]');
                if (updated) updated.classList.add('pid-selected', 'selected');
                tab.pid._routedPaths = new Map(); tab.pid.routingErrors = [];
                for (const conn of tab.pid.connections) renderPidConn(conn);
                renderPidWarning();
            });

        } else if (obj.type === 'daqControl') {
            // daqRefDes holds the state MACHINE name (config/machines/<x>.sm,
            // "machine <name>") — the widget binds to SM-<name>-STATE/-TARGET.
            // The editor has no WebSocket, so the machine list is unknown here:
            // it is always a free-text field.
            c.innerHTML =
                '<div class="pid-sb-heading">State Machine Control</div>' +
                '<div class="pid-sb-field"><label>Machine</label>' +
                '<input class="pid-daqctrl-inp" type="text" value="' + pidEsc(obj.daqRefDes || '') + '" placeholder="e.g. fuelSeq">' +
                '</div>' +
                '<div class="pid-sb-field"><label>Label</label>' +
                '<input class="pid-daqctrl-label" type="text" value="' + pidEsc(obj.label || '') + '" placeholder="(optional display name)"></div>' +
                '<div class="pid-sb-field"><label>Side</label>' +
                '<select class="pid-side-sel">' +
                    '<option value="right"' + ((obj.side || 'right') === 'right' ? ' selected' : '') + '>Right</option>' +
                    '<option value="left"'  + (obj.side === 'left'                ? ' selected' : '') + '>Left</option>' +
                '</select></div>' +
                '<div class="pid-sb-field pid-sb-field--row">' +
                    '<div><label>Width (cells)</label>' +
                    '<input class="pid-daqctrl-w" type="number" min="4" max="100" value="' + (obj.gridW || 10) + '"></div>' +
                    '<div><label>Height (cells)</label>' +
                    '<input class="pid-daqctrl-h" type="number" min="2" max="100" value="' + (obj.gridH || 3) + '"></div>' +
                '</div>' +
                '<button class="pid-apply-btn">Apply</button>' +
                '<button class="pid-delete-btn">Remove</button>';

            c.querySelector('.pid-apply-btn').addEventListener('click', () => {
                const inp = c.querySelector('.pid-daqctrl-inp');
                obj.daqRefDes = inp ? inp.value.trim() : '';
                obj.label     = c.querySelector('.pid-daqctrl-label').value.trim();
                const sideVal = c.querySelector('.pid-side-sel').value;
                if (sideVal === 'left') obj.side = 'left'; else delete obj.side;
                obj.gridW     = parseInt(c.querySelector('.pid-daqctrl-w').value) || 10;
                obj.gridH     = parseInt(c.querySelector('.pid-daqctrl-h').value) || 3;
                const existing = tab.pid.gObjs.querySelector('[data-pid-id="' + objId + '"]');
                if (existing) existing.remove();
                renderPidObj(obj);
                const updated = tab.pid.gObjs.querySelector('[data-pid-id="' + objId + '"]');
                if (updated) updated.classList.add('pid-selected', 'selected');
                tab.pid._routedPaths = new Map(); tab.pid.routingErrors = [];
                for (const conn of tab.pid.connections) renderPidConn(conn);
                renderPidWarning();
            });

        } else {
            c.innerHTML =
                '<div class="pid-sb-heading">Junction Node</div>' +
                '<div class="pid-sb-hint">Connects pipes in up to<br>4 directions.</div>' +
                '<button class="pid-delete-btn">Remove</button>';
        }

        c.querySelector('.pid-delete-btn').addEventListener('click', () => deletePidObj(objId));
    }

    rsb.appendChild(c);
}

// =============================================================================
// Object CRUD
// =============================================================================

function createPidObj(type, gridX, gridY) {
    const obj = { id: pidUid(type), type, gridX, gridY };
    // New sensors bind to a control (controlRefDes), consistent with valve
    // objects — obj.refDes (bare channel) is the legacy form, kept only for
    // layouts that already use it (see resolveSensorBinding in pidRender.js).
    if (type === 'sensor') { obj.controlRefDes = ''; obj.units = ''; }
    if (type === 'graph')  {
        obj.name = ''; obj.gridW = 20; obj.gridH = 10;
        obj.showName = true; obj.showLeftSidebar = false; obj.lines = [];
    }
    if (type === 'valve') { obj.controlRefDes = ''; }
    if (type === 'tank')  { obj.gridW = 5; obj.gridH = 8; obj.rotation = 0; obj.label = ''; obj.showLabel = true; }
    if (type === 'daqControl') { obj.daqRefDes = ''; obj.label = ''; obj.gridW = 10; obj.gridH = 3; }
    tab.pid.objects.push(obj);
    renderPidObj(obj);
    tab.pid._routedPaths = new Map(); tab.pid.routingErrors = [];
    for (const conn of tab.pid.connections) renderPidConn(conn);
    renderPidWarning();
    selectPidObject(obj.id);
}

function deletePidObj(id) {
    tab.pid.connections = tab.pid.connections.filter(c => {
        if (c.fromId === id || c.toId === id) {
            tab.pid.gConns.querySelector('[data-conn-id="' + c.id + '"]')?.remove();
            return false;
        }
        return true;
    });
    tab.pid.objects = tab.pid.objects.filter(o => o.id !== id);
    tab.pid.gObjs.querySelector('[data-pid-id="' + id + '"]')?.remove();
    selectPidObject(null);
    tab.pid._routedPaths = new Map(); tab.pid.routingErrors = [];
    for (const conn of tab.pid.connections) renderPidConn(conn);
    renderPidWarning();
}

// =============================================================================
// Drag objects on canvas
// =============================================================================

function startObjDrag(objId, e) {
    const obj = tab.pid.objects.find(o => o.id === objId);
    if (!obj) return;

    const startPt  = pidSvgPt(tab.pid.svgEl, e);
    const startGX  = obj.gridX, startGY = obj.gridY;
    let   moved    = false;
    let   rafPending = false;

    const onMove = em => {
        const pt = pidSvgPt(tab.pid.svgEl, em);
        const dx = pt.x - startPt.x, dy = pt.y - startPt.y;
        if (Math.abs(dx) + Math.abs(dy) > 4) moved = true;
        obj.gridX = Math.max(0, Math.round((startGX * PID.GRID + dx) / PID.GRID));
        obj.gridY = Math.max(0, Math.round((startGY * PID.GRID + dy) / PID.GRID));
        const el = tab.pid.gObjs.querySelector('[data-pid-id="' + objId + '"]');
        if (el) el.setAttribute('transform', 'translate(' + (obj.gridX * PID.GRID) + ',' + (obj.gridY * PID.GRID) + ')');
        if (!rafPending) {
            rafPending = true;
            requestAnimationFrame(() => {
                rafPending = false;
                updateConnsTouching();
            });
        }
    };

    const onUp = eu => {
        tab.pid.svgEl.removeEventListener('pointermove', onMove);
        tab.pid.svgEl.removeEventListener('pointerup',   onUp);
        tab.pid.svgEl.releasePointerCapture(eu.pointerId);
        updateConnsTouching();
        if (!moved) selectPidObject(objId);
    };

    tab.pid.svgEl.setPointerCapture(e.pointerId);
    tab.pid.svgEl.addEventListener('pointermove', onMove);
    tab.pid.svgEl.addEventListener('pointerup',   onUp);
}

function startLabelDrag(objId, e) {
    const obj = tab.pid.objects.find(o => o.id === objId);
    if (!obj) return;

    const startPt  = pidSvgPt(tab.pid.svgEl, e);
    const startLX  = obj.labelOffsetX || 0;
    const startLY  = obj.labelOffsetY || 0;

    // Sensor and valve no longer have an independently-positioned label — the
    // object-system name line lives inside the row pidBuildObject draws, so
    // those two cases are gone. Tank still draws its label as a free-standing,
    // draggable text and keeps this.
    function labelDefaultPos(o) {
        if (o.type === 'tank') {
            const W = (o.gridW || 5) * PID.GRID;
            const H = (o.gridH || 8) * PID.GRID;
            return { dx: W / 2, dy: H / 2 };
        }
        return { dx: 0, dy: 0 };
    }

    const { dx: defX, dy: defY } = labelDefaultPos(obj);

    const onMove = em => {
        const pt = pidSvgPt(tab.pid.svgEl, em);
        obj.labelOffsetX = startLX + (pt.x - startPt.x);
        obj.labelOffsetY = startLY + (pt.y - startPt.y);
        const labelG = tab.pid.gObjs.querySelector('[data-label-id="' + objId + '"]');
        if (labelG) {
            labelG.setAttribute('transform',
                'translate(' + (defX + obj.labelOffsetX) + ',' + (defY + obj.labelOffsetY) + ')');
        }
    };

    const onUp = eu => {
        tab.pid.svgEl.removeEventListener('pointermove', onMove);
        tab.pid.svgEl.removeEventListener('pointerup',   onUp);
        tab.pid.svgEl.releasePointerCapture(eu.pointerId);
    };

    tab.pid.svgEl.setPointerCapture(e.pointerId);
    tab.pid.svgEl.addEventListener('pointermove', onMove);
    tab.pid.svgEl.addEventListener('pointerup',   onUp);
}

// =============================================================================
// Connection drawing
// =============================================================================

function startPidConnect(fromObjId, fromPort, e) {
    tab.pid.connecting = { objId: fromObjId, port: fromPort };
    tab.pid.svgEl && tab.pid.svgEl.classList.add('pid-connecting');
    tab.pid.previewEl = svgN('line', {
        class: 'pid-preview-line', 'pointer-events': 'none',
        x1: 0, y1: 0, x2: 0, y2: 0,
    });
    tab.pid.gConns.appendChild(tab.pid.previewEl);
    const fromObj = tab.pid.objects.find(o => o.id === fromObjId);
    if (fromObj) {
        const pp = portPos(fromObj, fromPort);
        tab.pid.previewEl.setAttribute('x1', pp.x);
        tab.pid.previewEl.setAttribute('y1', pp.y);
    }
}

function completePidConnect(toObjId, toPort) {
    const { objId: fromId, port: fromPort } = tab.pid.connecting;
    const exists = tab.pid.connections.some(
        c => (c.fromId === fromId && c.fromPort === fromPort && c.toId === toObjId && c.toPort === toPort) ||
             (c.fromId === toObjId && c.fromPort === toPort && c.toId === fromId && c.toPort === fromPort)
    );
    if (!exists) {
        const conn = { id: pidUid('conn'), fromId, fromPort, toId: toObjId, toPort };
        tab.pid.connections.push(conn);
        renderPidConn(conn);
    }
    cancelPidConnect();
}

function cancelPidConnect() {
    tab.pid.connecting = null;
    tab.pid.svgEl && tab.pid.svgEl.classList.remove('pid-connecting');
    if (tab.pid.previewEl) { tab.pid.previewEl.remove(); tab.pid.previewEl = null; }
}

// =============================================================================
// Canvas event handlers
// =============================================================================

function onPidPointerDown(e) {
    if (e.button !== 0) return;
    e.stopPropagation();

    const portEl = e.target.closest('.pid-port');
    if (portEl) {
        const fromObjId = portEl.dataset.objId, fromPort = portEl.dataset.port;
        if (tab.pid.connecting) {
            if (fromObjId !== tab.pid.connecting.objId) completePidConnect(fromObjId, fromPort);
            else cancelPidConnect();
        } else {
            startPidConnect(fromObjId, fromPort, e);
        }
        return;
    }

    if (tab.pid.connecting) { cancelPidConnect(); return; }

    // Check for an independently-draggable label group before the parent object
    const labelEl = e.target.closest('[data-label-id]');
    if (labelEl) {
        e.preventDefault();
        startLabelDrag(labelEl.dataset.labelId, e);
        return;
    }

    const objEl = e.target.closest('[data-pid-id]');
    if (objEl) {
        e.preventDefault();
        startObjDrag(objEl.dataset.pidId, e);
        return;
    }

    const connHitEl = e.target.closest('.pid-conn-hit');
    if (connHitEl) {
        const grp = connHitEl.closest('[data-conn-id]');
        if (grp) { selectPidConn(grp.dataset.connId); return; }
    }

    selectPidObject(null);
    startEditorPan(e);
}

function onPidPointerMove(e) {
    if (!tab.pid.connecting || !tab.pid.previewEl) return;
    const pt = pidSvgPt(tab.pid.svgEl, e);
    tab.pid.previewEl.setAttribute('x2', pt.x);
    tab.pid.previewEl.setAttribute('y2', pt.y);
}

function onPidContextMenu(e) {
    const objEl = e.target.closest('[data-pid-id]');
    if (objEl) selectPidObject(objEl.dataset.pidId);
}

function onPidDrop(e) {
    e.preventDefault();
    const type = e.dataTransfer.getData('pid-type');
    if (!type) return;
    const pt = pidSvgPt(tab.pid.svgEl, e);
    const gx = Math.max(0, Math.round(pt.x / PID.GRID));
    const gy = Math.max(0, Math.round(pt.y / PID.GRID));
    createPidObj(type, gx, gy);
}

// =============================================================================
// Canvas pan
// =============================================================================

function startEditorPan(e) {
    const wrap   = tab.pid.canvasWrap;
    const startX = e.clientX + wrap.scrollLeft;
    const startY = e.clientY + wrap.scrollTop;

    wrap.style.cursor = 'grabbing';

    const onMove = em => {
        wrap.scrollLeft = startX - em.clientX;
        wrap.scrollTop  = startY - em.clientY;
    };

    const onUp = () => {
        window.removeEventListener('pointermove', onMove);
        window.removeEventListener('pointerup',   onUp);
        wrap.style.cursor = '';
    };

    window.addEventListener('pointermove', onMove);
    window.addEventListener('pointerup',   onUp);
}

// =============================================================================
// Keyboard shortcuts
// =============================================================================

document.addEventListener('keydown', e => {
    if (e.target.tagName === 'INPUT' || e.target.tagName === 'TEXTAREA' || e.target.tagName === 'SELECT') return;

    if ((e.key === 'Delete' || e.key === 'Backspace') && tab.pid.selectedId) {
        e.preventDefault();
        deletePidObj(tab.pid.selectedId);
    }
    if (e.key === 'Escape') {
        cancelPidConnect();
    }
});

// =============================================================================
// Init — read sessionStorage and build UI
// =============================================================================

(function initEditor() {
    let data = null;
    try {
        const raw = sessionStorage.getItem('pid_editor_data');
        if (raw) data = JSON.parse(raw);
    } catch (e) {
        console.warn('Could not read editor data from sessionStorage:', e);
    }

    if (data) {
        edConfigControls = data.configControls  || [];
        edLayouts        = data.pidLayouts       || {};
    }

    const rootEl = document.getElementById('editor-root');
    if (!rootEl) { console.error('editor-root not found'); return; }

    buildEditorUI(rootEl);
    edConnect();
    edConnectCtrl();

    // Auto-load the layout that was open on the front panel
    const sel = data && data.selectedLayout;
    if (sel && edLayouts[sel]) {
        loadLayout(edLayouts[sel]);
    } else if (Object.keys(edLayouts).length > 0) {
        const first = Object.values(edLayouts)[0];
        loadLayout(first);
    }
})();
