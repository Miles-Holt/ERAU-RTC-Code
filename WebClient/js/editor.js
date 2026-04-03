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

// ── Constants (same as pid.js) ───────────────────────────────────────────────

const PID = {
    GRID:        20,
    SENSOR_W:    120,
    SENSOR_H:    50,
    NODE_R:      5,
    PORT_R:      6,
    PORT_OFF:    20,
    STUB:        40,
    CANVAS_W:    2400,
    CANVAS_H:    1800,
    CORNER_R:    8,
    OBS_MARGIN:  6,
    VALVE_R:     25,
    VALVE_PORT_OFF: 40,
};

// ── Editor state ─────────────────────────────────────────────────────────────

let edConfigControls = [];
let edLayouts = {};   // filename → { name, filename, content }

// Live data
let edWs               = null;
let edWsReconnectDelay = 1000;
let edWsReconnectTimer = null;
let edWsStatusEl       = null;          // dot in header
const edLiveValues     = {};            // refDes → latest value
let edLiveRefDes       = null;          // refDes of the currently selected sensor
let edLiveEl           = null;          // <span> in right sidebar to update
let edLiveStaleTimer   = null;

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
            if (o.showName === false)   y += '    showName: false\n';
            if (o.showLeftSidebar)      y += '    showLeftSidebar: true\n';
            if (o.lines && o.lines.length) {
                y += '    lines:\n';
                for (const l of o.lines) {
                    y += '      - refDes: ' + q(l.refDes) + '\n';
                    if (l.color)           y += '        color: '  + q(l.color)  + '\n';
                    if (l.yAxis && l.yAxis !== 1) y += '        yAxis: ' + l.yAxis + '\n';
                    if (l.hidden)          y += '        hidden: true\n';
                }
            }
        } else {
            if (o.refDes)              y += '    refDes: ' + q(o.refDes) + '\n';
            if (o.units)               y += '    units: '  + q(o.units)  + '\n';
            if (o.showRefDes === false) y += '    showRefDes: false\n';
            if (o.showUnits  === false) y += '    showUnits: false\n';
            if (o.showName   === true)  y += '    showName: true\n';
            y +=                           '    gridX: '   + o.gridX    + '\n';
            y +=                           '    gridY: '   + o.gridY    + '\n';
        }
    }
    y += 'connections:\n';
    for (const c of layout.connections) {
        y += '  - id: '       + q(c.id)       + '\n';
        y += '    fromId: '   + q(c.fromId)   + '\n';
        y += '    fromPort: ' + c.fromPort     + '\n';
        y += '    toId: '     + q(c.toId)     + '\n';
        y += '    toPort: '   + c.toPort       + '\n';
    }
    return y;
}

// ── YAML parser ──────────────────────────────────────────────────────────────

function pidFromYaml(text) {
    const out = { name: 'Untitled', version: 1, objects: [], connections: [] };
    let section = null, cur = null, subSection = null, subCur = null;
    function uq(s) { return s.trim().replace(/^["']|["']$/g, ''); }
    function coerce(v) {
        const u = uq(v);
        if (u === 'true')  return true;
        if (u === 'false') return false;
        return (u !== '' && !isNaN(u)) ? Number(u) : u;
    }
    function kv(obj, str) {
        const m = str.match(/^([\w]+):\s*(.*)/);
        if (m) obj[m[1]] = coerce(m[2]);
    }
    for (const raw of text.split(/\r?\n/)) {
        const t = raw.trim();
        if (!t || t.startsWith('#')) continue;
        const ind = raw.search(/\S/);
        if (ind === 0) {
            subSection = null; subCur = null;
            const m = t.match(/^(\w+):\s*(.*)/);
            if (!m) continue;
            if      (m[1] === 'name')        out.name    = uq(m[2]);
            else if (m[1] === 'version')     out.version = parseInt(m[2]) || 1;
            else if (m[1] === 'objects')     { section = 'objects';     cur = null; }
            else if (m[1] === 'connections') { section = 'connections'; cur = null; }
        } else if (ind <= 3) {
            subSection = null; subCur = null;
            if (t.startsWith('- ')) {
                cur = {};
                if (section === 'objects')     out.objects.push(cur);
                if (section === 'connections') out.connections.push(cur);
                kv(cur, t.slice(2));
            } else if (cur) {
                kv(cur, t);
            }
        } else if (ind <= 5) {
            if (cur) {
                const m = t.match(/^([\w]+):\s*(.*)/);
                if (m) {
                    if (m[2] === '' || m[2].trim() === '') {
                        subSection = m[1];
                        if (!cur[subSection]) cur[subSection] = [];
                        subCur = null;
                    } else {
                        subSection = null; subCur = null;
                        cur[m[1]] = coerce(m[2]);
                    }
                }
            }
        } else {
            if (t.startsWith('- ') && subSection && cur) {
                subCur = {};
                cur[subSection].push(subCur);
                kv(subCur, t.slice(2));
            } else if (subCur) {
                kv(subCur, t);
            }
        }
    }
    return out;
}

// ── SVG helpers ──────────────────────────────────────────────────────────────

function svgN(tag, attrs) {
    const el = document.createElementNS('http://www.w3.org/2000/svg', tag);
    if (attrs) for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, String(v));
    return el;
}

function portPos(obj, port) {
    const x = obj.gridX * PID.GRID;
    const y = obj.gridY * PID.GRID;
    if (obj.type === 'sensor') {
        if (port === 'bottom') return { x: x + PID.SENSOR_W / 2, y: y + PID.SENSOR_H };
    }
    if (obj.type === 'node') {
        return { x, y };
    }
    if (obj.type === 'valve') {
        const off = PID.VALVE_PORT_OFF;
        if (port === 'top')    return { x,        y: y - off };
        if (port === 'right')  return { x: x+off, y };
        if (port === 'bottom') return { x,        y: y + off };
        if (port === 'left')   return { x: x-off, y };
    }
    return { x, y };
}

function pidSvgPt(svgEl, e) {
    const pt = svgEl.createSVGPoint();
    pt.x = e.clientX; pt.y = e.clientY;
    return pt.matrixTransform(svgEl.getScreenCTM().inverse());
}

function pidUid(prefix) {
    return prefix + '_' + Date.now() + '_' + Math.floor(Math.random() * 9999);
}

function pidEsc(s) {
    return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

// ── Obstacle-aware orthogonal router ─────────────────────────────────────────

function pidObstacleRects(objects, excludeIds) {
    const M = PID.OBS_MARGIN;
    return objects
        .filter(o => !excludeIds.has(o.id) && (o.type === 'sensor' || o.type === 'graph' || o.type === 'valve'))
        .map(o => {
            if (o.type === 'graph') {
                return {
                    x1: o.gridX * PID.GRID - M,
                    y1: o.gridY * PID.GRID - M,
                    x2: o.gridX * PID.GRID + (o.gridW || 20) * PID.GRID + M,
                    y2: o.gridY * PID.GRID + (o.gridH || 10) * PID.GRID + M,
                };
            }
            if (o.type === 'valve') {
                const x = o.gridX * PID.GRID, y = o.gridY * PID.GRID, R = PID.VALVE_R;
                return { x1: x-R-M, y1: y-R-M, x2: x+R+M, y2: y+R+M };
            }
            return {
                x1: o.gridX * PID.GRID - M,
                y1: o.gridY * PID.GRID - M,
                x2: o.gridX * PID.GRID + PID.SENSOR_W + M,
                y2: o.gridY * PID.GRID + PID.SENSOR_H + M,
            };
        });
}

function pidSegClear(ax, ay, bx, by, rects) {
    if (ax === bx && ay === by) return true;
    for (const r of rects) {
        if (ay === by) {
            const lo = Math.min(ax, bx), hi = Math.max(ax, bx);
            if (ay > r.y1 && ay < r.y2 && hi > r.x1 && lo < r.x2) return false;
        } else if (ax === bx) {
            const lo = Math.min(ay, by), hi = Math.max(ay, by);
            if (ax > r.x1 && ax < r.x2 && hi > r.y1 && lo < r.y2) return false;
        }
    }
    return true;
}

function pidRoundedPath(pts, r) {
    const s = [pts[0]];
    for (let i = 1; i < pts.length - 1; i++) {
        const prev = s[s.length - 1], curr = pts[i], next = pts[i + 1];
        const dx1 = Math.sign(curr.x - prev.x), dy1 = Math.sign(curr.y - prev.y);
        const dx2 = Math.sign(next.x - curr.x), dy2 = Math.sign(next.y - curr.y);
        if (dx1 !== dx2 || dy1 !== dy2) s.push(curr);
    }
    s.push(pts[pts.length - 1]);
    if (s.length < 2) return '';

    let d = 'M ' + s[0].x + ' ' + s[0].y;
    for (let i = 1; i < s.length; i++) {
        const prev = s[i - 1], curr = s[i], next = i < s.length - 1 ? s[i + 1] : null;
        if (next) {
            const dx1 = Math.sign(curr.x - prev.x), dy1 = Math.sign(curr.y - prev.y);
            const dx2 = Math.sign(next.x - curr.x), dy2 = Math.sign(next.y - curr.y);
            if (dx1 !== dx2 || dy1 !== dy2) {
                const len1 = Math.abs(curr.x - prev.x) + Math.abs(curr.y - prev.y);
                const len2 = Math.abs(next.x - curr.x) + Math.abs(next.y - curr.y);
                const rr   = Math.min(r, len1 / 2, len2 / 2);
                d += ' L ' + (curr.x - dx1 * rr) + ' ' + (curr.y - dy1 * rr);
                d += ' Q ' + curr.x + ' ' + curr.y + ' ' + (curr.x + dx2 * rr) + ' ' + (curr.y + dy2 * rr);
                continue;
            }
        }
        d += ' L ' + curr.x + ' ' + curr.y;
    }
    return d;
}

function pidCandidateClear(pts, noFromRects, noToRects, allRects) {
    const last = pts.length - 2;
    for (let i = 0; i <= last; i++) {
        const rects = i === 0 ? noFromRects : i === last ? noToRects : allRects;
        if (!pidSegClear(pts[i].x, pts[i].y, pts[i+1].x, pts[i+1].y, rects)) return false;
    }
    return true;
}

function orthRouteAvoiding(p1, d1, p2, d2, objects, fromId, toId) {
    function ext(p, d, dist) {
        if (d === 'top')    return { x: p.x,         y: p.y - dist };
        if (d === 'bottom') return { x: p.x,         y: p.y + dist };
        if (d === 'right')  return { x: p.x + dist,  y: p.y };
        return                      { x: p.x - dist,  y: p.y };
    }
    const G = PID.GRID, S = PID.STUB, R = PID.CORNER_R;
    const s1 = ext(p1, d1, S), s2 = ext(p2, d2, S);

    const allRects    = pidObstacleRects(objects, new Set());
    const noFromRects = pidObstacleRects(objects, new Set([fromId]));
    const noToRects   = pidObstacleRects(objects, new Set([toId]));

    function zOk(my) {
        if (d1 === 'bottom' && my < s1.y) return false;
        if (d1 === 'top'    && my > s1.y) return false;
        if (d2 === 'bottom' && my < s2.y) return false;
        if (d2 === 'top'    && my > s2.y) return false;
        return true;
    }
    function uOk(mx) {
        if (d1 === 'right' && mx < s1.x) return false;
        if (d1 === 'left'  && mx > s1.x) return false;
        if (d2 === 'right' && mx < s2.x) return false;
        if (d2 === 'left'  && mx > s2.x) return false;
        return true;
    }

    const offsets = [0, G, -G, 2*G, -2*G, 3*G, -3*G, 4*G, -4*G, 6*G, -6*G, 8*G, -8*G, 10*G, -10*G];

    if (Math.abs(s1.x - s2.x) < 1 || Math.abs(s1.y - s2.y) < 1) {
        const pts = [p1, s1, s2, p2];
        if (pidCandidateClear(pts, noFromRects, noToRects, allRects))
            return { d: pidRoundedPath(pts, R), error: null };
    }

    // Try L-shapes: single corner after stubs — simpler than Z/U when unobstructed
    function lOk1() { // corner at (s2.x, s1.y): s1 goes horizontal, then vertical to s2
        if (d1 === 'right'  && s2.x < s1.x) return false;
        if (d1 === 'left'   && s2.x > s1.x) return false;
        if (d2 === 'bottom' && s1.y > s2.y) return false;
        if (d2 === 'top'    && s1.y < s2.y) return false;
        return true;
    }
    function lOk2() { // corner at (s1.x, s2.y): s1 goes vertical, then horizontal to s2
        if (d1 === 'bottom' && s2.y < s1.y) return false;
        if (d1 === 'top'    && s2.y > s1.y) return false;
        if (d2 === 'right'  && s1.x < s2.x) return false;
        if (d2 === 'left'   && s1.x > s2.x) return false;
        return true;
    }
    if (lOk1()) {
        const lPts = [p1, s1, { x: s2.x, y: s1.y }, s2, p2];
        if (pidCandidateClear(lPts, noFromRects, noToRects, allRects))
            return { d: pidRoundedPath(lPts, R), error: null };
    }
    if (lOk2()) {
        const lPts = [p1, s1, { x: s1.x, y: s2.y }, s2, p2];
        if (pidCandidateClear(lPts, noFromRects, noToRects, allRects))
            return { d: pidRoundedPath(lPts, R), error: null };
    }

    for (const off of offsets) {
        const my = Math.round((s1.y + s2.y) / 2 / G) * G + off;
        if (zOk(my)) {
            const zPts = [p1, s1, { x: s1.x, y: my }, { x: s2.x, y: my }, s2, p2];
            if (pidCandidateClear(zPts, noFromRects, noToRects, allRects))
                return { d: pidRoundedPath(zPts, R), error: null };
        }

        const mx = Math.round((s1.x + s2.x) / 2 / G) * G + off;
        if (uOk(mx)) {
            const uPts = [p1, s1, { x: mx, y: s1.y }, { x: mx, y: s2.y }, s2, p2];
            if (pidCandidateClear(uPts, noFromRects, noToRects, allRects))
                return { d: pidRoundedPath(uPts, R), error: null };
        }
    }

    let fallPts = null;
    for (const off of [0, G, -G, 2*G, -2*G, 4*G, -4*G, 6*G, -6*G]) {
        const my = Math.round((s1.y + s2.y) / 2 / G) * G + off;
        if (zOk(my)) {
            fallPts = [p1, s1, { x: s1.x, y: my }, { x: s2.x, y: my }, s2, p2];
            break;
        }
        const mx = Math.round((s1.x + s2.x) / 2 / G) * G + off;
        if (uOk(mx)) {
            fallPts = [p1, s1, { x: mx, y: s1.y }, { x: mx, y: s2.y }, s2, p2];
            break;
        }
    }
    if (!fallPts) fallPts = [p1, s1, s2, p2];
    return { d: pidRoundedPath(fallPts, R), error: 'Could not route without crossing an object' };
}

// =============================================================================
// Live WebSocket connection (read-only — data only)
// =============================================================================

function edConnect() {
    const url = 'ws://' + (window.location.hostname || 'localhost') + ':8000';
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

function edApplyData(msg) {
    const d = Array.isArray(msg.d)
        ? Object.fromEntries(msg.d.map(e => [e.r, e.v]))
        : msg.d;
    Object.assign(edLiveValues, d);

    // Push value to the right sidebar if a sensor is currently selected
    if (edLiveRefDes && edLiveEl && d[edLiveRefDes] !== undefined) {
        const v = d[edLiveRefDes];
        edLiveEl.textContent = typeof v === 'number'
            ? (Number.isInteger(v) ? String(v) : v.toFixed(3))
            : String(v);
        edLiveEl.classList.remove('stale');
        clearTimeout(edLiveStaleTimer);
        edLiveStaleTimer = setTimeout(() => { if (edLiveEl) edLiveEl.classList.add('stale'); }, 2000);
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
    warnBtn.title = 'Routing warnings';
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
            '<div class="pid-obj-preview pid-obj-preview-valve">Valve</div></div>';

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
    const yaml     = pidToYaml(layout);
    const filename = (layout.name.toLowerCase().replace(/[^a-z0-9]+/g, '_') || 'panel') + '.yaml';
    const blob     = new Blob([yaml], { type: 'text/yaml' });
    const url      = URL.createObjectURL(blob);
    const a        = document.createElement('a');
    a.href = url; a.download = filename; a.click();
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

    tab.pid.routingErrors = [];
    for (const conn of tab.pid.connections) renderPidConn(conn);
    renderPidWarning();
}

function renderPidObj(obj) {
    const g = obj.type === 'graph'  ? makeGraphGroup(obj)
            : obj.type === 'sensor' ? makeSensorGroup(obj)
            : obj.type === 'valve'  ? makeValveGroupEditor(obj)
            : makeNodeGroup(obj);
    tab.pid.gObjs.appendChild(g);
}

function makeGraphGroup(obj) {
    const sel = (tab.pid.selectedId === obj.id);
    const W = (obj.gridW || 20) * PID.GRID;
    const H = (obj.gridH || 10) * PID.GRID;

    const g = svgN('g', {
        class: 'pid-obj pid-graph' + (sel ? ' pid-selected' : ''),
        'data-pid-id': obj.id,
        transform: 'translate(' + (obj.gridX * PID.GRID) + ',' + (obj.gridY * PID.GRID) + ')',
        cursor: 'grab',
    });

    g.appendChild(svgN('rect', {
        x: 0, y: 0, width: W, height: H,
        rx: 4, class: 'pid-graph-rect',
    }));

    const lbl = svgN('text', { class: 'pid-graph-label', x: W / 2, y: H / 2 - 8 });
    lbl.textContent = obj.name || '(no name)';
    g.appendChild(lbl);

    const sub = svgN('text', { class: 'pid-graph-sublabel', x: W / 2, y: H / 2 + 10 });
    sub.textContent = 'Graph \u2022 ' + (obj.lines?.length || 0) + ' line' + (obj.lines?.length === 1 ? '' : 's');
    g.appendChild(sub);

    return g;
}

function makeSensorGroup(obj) {
    const sel = (tab.pid.selectedId === obj.id);
    const g = svgN('g', {
        class: 'pid-obj pid-sensor' + (sel ? ' pid-selected' : ''),
        'data-pid-id': obj.id,
        transform: 'translate(' + (obj.gridX * PID.GRID) + ',' + (obj.gridY * PID.GRID) + ')',
        cursor: 'grab',
    });

    g.appendChild(svgN('rect', {
        x: 0, y: 0, width: PID.SENSOR_W, height: PID.SENSOR_H,
        rx: 3, class: 'pid-sensor-rect',
    }));

    const lbl = svgN('text', { class: 'pid-sensor-label', x: PID.SENSOR_W / 2, y: 14 });
    lbl.textContent = obj.refDes || '(no refDes)';
    g.appendChild(lbl);

    const val = svgN('text', { class: 'pid-sensor-value', x: PID.SENSOR_W / 2, y: 33 });
    val.textContent = '--';
    g.appendChild(val);

    const unt = svgN('text', { class: 'pid-sensor-units', x: PID.SENSOR_W / 2, y: 44 });
    unt.textContent = obj.units || '';
    g.appendChild(unt);

    const port = svgN('circle', {
        class: 'pid-port',
        'data-obj-id': obj.id, 'data-port': 'bottom',
        cx: PID.SENSOR_W / 2, cy: PID.SENSOR_H, r: PID.PORT_R,
    });
    g.appendChild(port);
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

function makeValveGroupEditor(obj) {
    const sel  = (tab.pid.selectedId === obj.id);
    const ctrl = edConfigControls.find(c => c.refDes === obj.controlRefDes);
    const L    = PID.VALVE_R - 3;

    const g = svgN('g', {
        class:         'pid-obj pid-valve' + (sel ? ' pid-selected' : ''),
        'data-pid-id': obj.id,
        transform:     'translate(' + (obj.gridX * PID.GRID) + ',' + (obj.gridY * PID.GRID) + ')',
        cursor:        'grab',
    });

    g.appendChild(svgN('circle', { class: 'pid-valve-ring', r: PID.VALVE_R }));

    if (!ctrl) {
        g.appendChild(svgN('line', { class: 'pid-valve-uncfg', x1: -L, y1: L, x2: L, y2: -L }));
    } else {
        g.appendChild(svgN('line', { class: 'pid-valve-line', x1: -L, y1: 0, x2: L, y2: 0 }));
    }

    const lbl = svgN('text', { class: 'pid-valve-label', x: 0, y: PID.VALVE_R + 12 });
    lbl.textContent = obj.controlRefDes || '(no control)';
    g.appendChild(lbl);

    const off = PID.VALVE_PORT_OFF;
    const valvePorts = { top: [0, -off], right: [off, 0], bottom: [0, off], left: [-off, 0] };
    for (const [pname, [px, py]] of Object.entries(valvePorts)) {
        g.appendChild(svgN('circle', {
            class: 'pid-port',
            'data-obj-id': obj.id, 'data-port': pname,
            cx: px, cy: py, r: PID.PORT_R,
        }));
    }
    return g;
}

function renderPidConn(conn) {
    const from = tab.pid.objects.find(o => o.id === conn.fromId);
    const to   = tab.pid.objects.find(o => o.id === conn.toId);
    if (!from || !to) return;

    const p1 = portPos(from, conn.fromPort);
    const p2 = portPos(to,   conn.toPort);
    const { d, error } = orthRouteAvoiding(
        p1, conn.fromPort, p2, conn.toPort,
        tab.pid.objects, conn.fromId, conn.toId
    );

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
}

function updateConnsTouching() {
    tab.pid.routingErrors = [];
    for (const conn of tab.pid.connections) renderPidConn(conn);
    renderPidWarning();
}

// =============================================================================
// Routing warning indicator
// =============================================================================

function renderPidWarning() {
    const btn = tab.pid.warnBtnEl;
    if (!btn) return;
    const errs = tab.pid.routingErrors;
    btn.style.display = errs.length > 0 ? '' : 'none';
    const countEl = btn.querySelector('.pid-warn-count');
    if (countEl) countEl.textContent = errs.length > 1 ? String(errs.length) : '';
    if (btn.classList.contains('pid-warn-open')) renderPidWarnDropdown();
}

function renderPidWarnDropdown() {
    const dropdown = tab.pid.warnDropdownEl;
    if (!dropdown) return;
    const errs = tab.pid.routingErrors;
    if (!errs.length) { dropdown.innerHTML = ''; return; }

    let html = '<div class="pid-warn-title">Routing errors (' + errs.length + ')</div>';
    for (const err of errs) {
        const from     = tab.pid.objects.find(o => o.id === err.fromId);
        const to       = tab.pid.objects.find(o => o.id === err.toId);
        const fromName = from ? (from.refDes || from.type) : err.fromId;
        const toName   = to   ? (to.refDes   || to.type)  : err.toId;
        html +=
            '<div class="pid-warn-item">' +
                '<div class="pid-warn-conn">' +
                    pidEsc(fromName) + ':' + err.fromPort + ' → ' +
                    pidEsc(toName)   + ':' + err.toPort +
                '</div>' +
                '<div class="pid-warn-msg">' + pidEsc(err.message) + '</div>' +
            '</div>';
    }
    dropdown.innerHTML = html;
}

// =============================================================================
// Selection & right sidebar
// =============================================================================

function selectPidObject(id) {
    tab.pid.selectedId = id;
    tab.pid.gObjs.querySelectorAll('.pid-selected').forEach(el => el.classList.remove('pid-selected'));
    if (id) {
        const el = tab.pid.gObjs.querySelector('[data-pid-id="' + id + '"]');
        if (el) el.classList.add('pid-selected');
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
    clearTimeout(edLiveStaleTimer);

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

    c.innerHTML =
        '<div class="pid-sb-heading">Pipe</div>' +
        '<div class="pid-sb-field"><label>From</label>' +
        '<span class="pid-sb-value">' + pidEsc(fromName) + ' : ' + pidEsc(conn.fromPort) + '</span></div>' +
        '<div class="pid-sb-field"><label>To</label>' +
        '<span class="pid-sb-value">' + pidEsc(toName) + ' : ' + pidEsc(conn.toPort) + '</span></div>' +
        '<button class="pid-delete-btn">Remove</button>';

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
    tab.pid.routingErrors = [];
    for (const conn of tab.pid.connections) renderPidConn(conn);
    renderPidWarning();
}

function renderPidRsb(objId) {
    // Clear live tracking whenever the sidebar is rebuilt
    edLiveRefDes = null;
    edLiveEl = null;
    clearTimeout(edLiveStaleTimer);

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
            const chs = [];
            for (const ctrl of edConfigControls) {
                for (const ch of ctrl.channels) {
                    if (ch.role === 'sensor' || ch.role === '') {
                        chs.push({ refDes: ch.refDes, units: ch.units, desc: ctrl.description });
                    }
                }
            }

            const opts = chs.length > 0
                ? chs.map(ch =>
                    '<option value="' + pidEsc(ch.refDes) + '"' +
                    ' data-units="' + pidEsc(ch.units || '') + '"' +
                    (ch.refDes === obj.refDes ? ' selected' : '') + '>' +
                    pidEsc(ch.refDes) + (ch.desc ? ' — ' + pidEsc(ch.desc) : '') + '</option>'
                  ).join('')
                : null;

            c.innerHTML =
                '<div class="pid-sb-heading">Sensor</div>' +
                '<div class="pid-sb-field"><label>Channel refDes</label>' +
                (opts
                    ? '<select class="pid-refdes-sel"><option value="">-- pick --</option>' + opts + '</select>'
                    : '<input class="pid-refdes-inp" type="text" value="' + pidEsc(obj.refDes || '') + '" placeholder="e.g. OPT-01">') +
                '</div>' +
                '<div class="pid-sb-field"><label>Units</label>' +
                '<input class="pid-units-inp" type="text" value="' + pidEsc(obj.units || '') + '" placeholder="psi"></div>' +
                '<div class="pid-sb-heading pid-sb-heading--sm">Front Panel Display</div>' +
                '<div class="pid-sb-check"><label><input type="checkbox" class="pid-show-refdes"' + (obj.showRefDes !== false ? ' checked' : '') + '> Show refDes</label></div>' +
                '<div class="pid-sb-check"><label><input type="checkbox" class="pid-show-units"'  + (obj.showUnits  !== false ? ' checked' : '') + '> Show units</label></div>' +
                '<div class="pid-sb-check"><label><input type="checkbox" class="pid-show-name"'   + (obj.showName   === true  ? ' checked' : '') + '> Show name (description)</label></div>' +
                '<button class="pid-apply-btn">Apply</button>' +
                '<button class="pid-delete-btn">Remove</button>';

            const sel  = c.querySelector('.pid-refdes-sel');
            const inp  = c.querySelector('.pid-refdes-inp');
            const uinp = c.querySelector('.pid-units-inp');

            if (sel) sel.addEventListener('change', () => {
                const opt = sel.options[sel.selectedIndex];
                if (opt && opt.dataset.units && !uinp.value) uinp.value = opt.dataset.units;
            });

            c.querySelector('.pid-apply-btn').addEventListener('click', () => {
                obj.refDes    = sel ? sel.value : (inp ? inp.value.trim() : '');
                obj.units     = uinp ? uinp.value.trim() : '';
                obj.showRefDes = c.querySelector('.pid-show-refdes').checked;
                obj.showUnits  = c.querySelector('.pid-show-units').checked;
                obj.showName   = c.querySelector('.pid-show-name').checked;
                // Re-render the object in place to reflect display flag changes
                const existing = tab.pid.gObjs.querySelector('[data-pid-id="' + objId + '"]');
                if (existing) existing.remove();
                renderPidObj(obj);
                // Re-apply selection highlight
                const updated = tab.pid.gObjs.querySelector('[data-pid-id="' + objId + '"]');
                if (updated) updated.classList.add('pid-selected');
                // Re-bind live display to the new refDes
                edLiveRefDes = obj.refDes || null;
                if (edLiveEl && edLiveRefDes && edLiveValues[edLiveRefDes] !== undefined) {
                    const v = edLiveValues[edLiveRefDes];
                    edLiveEl.textContent = typeof v === 'number'
                        ? (Number.isInteger(v) ? String(v) : v.toFixed(3))
                        : String(v);
                    edLiveEl.classList.remove('stale');
                } else if (edLiveEl) {
                    edLiveEl.textContent = '--';
                }
            });

            // ── Live value row ──
            const liveRow = document.createElement('div');
            liveRow.className = 'pid-sb-field ed-live-row';
            const liveLabel = document.createElement('label');
            liveLabel.textContent = 'Live';
            const liveVal = document.createElement('span');
            liveVal.className = 'ed-live-value';
            const initVal = obj.refDes ? edLiveValues[obj.refDes] : undefined;
            liveVal.textContent = initVal !== undefined
                ? (typeof initVal === 'number'
                    ? (Number.isInteger(initVal) ? String(initVal) : initVal.toFixed(3))
                    : String(initVal))
                : '--';
            liveRow.append(liveLabel, liveVal);
            c.appendChild(liveRow);

            // Register for live updates
            edLiveRefDes = obj.refDes || null;
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
                obj.showName      = c.querySelector('.pid-graph-show-name').checked;
                obj.showLeftSidebar = c.querySelector('.pid-graph-show-lsb').checked;
                // Re-render the placeholder to reflect new size and name
                const existing = tab.pid.gObjs.querySelector('[data-pid-id="' + objId + '"]');
                if (existing) existing.remove();
                renderPidObj(obj);
                const updated = tab.pid.gObjs.querySelector('[data-pid-id="' + objId + '"]');
                if (updated) updated.classList.add('pid-selected');
                tab.pid.routingErrors = [];
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

            c.innerHTML =
                '<div class="pid-sb-heading">Valve</div>' +
                '<div class="pid-sb-field"><label>Control</label>' +
                (opts
                    ? '<select class="pid-valve-ctrl-sel"><option value="">-- pick --</option>' + opts + '</select>'
                    : '<input class="pid-valve-ctrl-inp" type="text" value="' + pidEsc(obj.controlRefDes || '') + '" placeholder="e.g. NV-03">') +
                '</div>' +
                '<button class="pid-apply-btn">Apply</button>' +
                '<button class="pid-delete-btn">Remove</button>';

            c.querySelector('.pid-apply-btn').addEventListener('click', () => {
                const sel = c.querySelector('.pid-valve-ctrl-sel');
                const inp = c.querySelector('.pid-valve-ctrl-inp');
                obj.controlRefDes = sel ? sel.value : (inp ? inp.value.trim() : '');
                const existing = tab.pid.gObjs.querySelector('[data-pid-id="' + objId + '"]');
                if (existing) existing.remove();
                renderPidObj(obj);
                const updated = tab.pid.gObjs.querySelector('[data-pid-id="' + objId + '"]');
                if (updated) updated.classList.add('pid-selected');
                tab.pid.routingErrors = [];
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
    if (type === 'sensor') { obj.refDes = ''; obj.units = ''; }
    if (type === 'graph')  {
        obj.name = ''; obj.gridW = 20; obj.gridH = 10;
        obj.showName = true; obj.showLeftSidebar = false; obj.lines = [];
    }
    if (type === 'valve') { obj.controlRefDes = ''; }
    tab.pid.objects.push(obj);
    renderPidObj(obj);
    tab.pid.routingErrors = [];
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
    tab.pid.routingErrors = [];
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

// =============================================================================
// Connection drawing
// =============================================================================

function startPidConnect(fromObjId, fromPort, e) {
    tab.pid.connecting = { objId: fromObjId, port: fromPort };
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

    // Auto-load the layout that was open on the front panel
    const sel = data && data.selectedLayout;
    if (sel && edLayouts[sel]) {
        loadLayout(edLayouts[sel]);
    } else if (Object.keys(edLayouts).length > 0) {
        const first = Object.values(edLayouts)[0];
        loadLayout(first);
    }
})();
