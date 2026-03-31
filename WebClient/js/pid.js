// =============================================================================
// P&ID Front Panel — Interactive Editor & Viewer
// =============================================================================
//
// Each Front Panel tab loads one YAML layout.  Layouts arrive via pid_layout
// WebSocket messages and are cached in pidLayouts{}.
//
// YAML layout schema:
//   name: Panel Name
//   version: 1
//   objects:
//     - id: "obj_123"
//       type: sensor        # sensor | node
//       refDes: OPT-01      # channel refDes (sensor only)
//       units: psi          # engineering units (sensor only)
//       gridX: 10           # position in grid cells (1 cell = 20 px)
//       gridY: 5
//   connections:
//     - id: "conn_123"
//       fromId: "obj_1"
//       fromPort: bottom    # top | right | bottom | left
//       toId: "node_1"
//       toPort: top
//
// =============================================================================

const PID = {
    GRID:        20,    // px per grid cell
    SENSOR_W:    120,   // sensor box width  (6 cells)
    SENSOR_H:    50,    // sensor box height (2.5 cells)
    NODE_R:      5,     // junction dot radius
    PORT_R:      6,     // port hit-circle radius
    PORT_OFF:    20,    // port offset from node centre
    STUB:        40,    // orthogonal routing stub length
    CANVAS_W:    2400,
    CANVAS_H:    1800,
    CORNER_R:    8,     // rounded corner radius px
    OBS_MARGIN:  6,     // obstacle clearance margin px
};

// ── YAML serialiser ──────────────────────────────────────────────────────────

function pidToYaml(layout) {
    function q(s) {
        s = String(s);
        return /[:#{}[\],&*?|<>=!%@`'"\\]/.test(s) ? '"' + s.replace(/"/g, '\\"') + '"' : s;
    }
    let y = 'name: ' + q(layout.name || 'Untitled') + '\nversion: 1\nobjects:\n';
    for (const o of layout.objects) {
        y += '  - id: '    + q(o.id)    + '\n';
        y += '    type: '  + o.type     + '\n';
        if (o.refDes) y += '    refDes: ' + q(o.refDes) + '\n';
        if (o.units)  y += '    units: '  + q(o.units)  + '\n';
        y += '    gridX: ' + o.gridX    + '\n';
        y += '    gridY: ' + o.gridY    + '\n';
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

// ── YAML parser (handles our exact schema only) ──────────────────────────────

function pidFromYaml(text) {
    const out = { name: 'Untitled', version: 1, objects: [], connections: [] };
    let section = null, cur = null;
    function uq(s) { return s.trim().replace(/^["']|["']$/g, ''); }
    function coerce(v) { return (v !== '' && !isNaN(v)) ? Number(v) : v; }
    function kv(obj, str) {
        const m = str.match(/^(\w+):\s*(.*)/);
        if (m) obj[m[1]] = coerce(uq(m[2]));
    }
    for (const raw of text.split(/\r?\n/)) {
        const t = raw.trim();
        if (!t || t.startsWith('#')) continue;
        const ind = raw.search(/\S/);
        if (ind === 0) {
            const m = t.match(/^(\w+):\s*(.*)/);
            if (!m) continue;
            if      (m[1] === 'name')        out.name    = uq(m[2]);
            else if (m[1] === 'version')     out.version = parseInt(m[2]) || 1;
            else if (m[1] === 'objects')     { section = 'objects';     cur = null; }
            else if (m[1] === 'connections') { section = 'connections'; cur = null; }
        } else {
            if (t.startsWith('- ')) {
                cur = {};
                if (section === 'objects')     out.objects.push(cur);
                if (section === 'connections') out.connections.push(cur);
                kv(cur, t.slice(2));
            } else if (cur) {
                kv(cur, t);
            }
        }
    }
    return out;
}

// ── SVG namespace helper ─────────────────────────────────────────────────────

function svgN(tag, attrs) {
    const el = document.createElementNS('http://www.w3.org/2000/svg', tag);
    if (attrs) for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, String(v));
    return el;
}

// ── Port positions ───────────────────────────────────────────────────────────

function portPos(obj, port) {
    const x = obj.gridX * PID.GRID;
    const y = obj.gridY * PID.GRID;
    if (obj.type === 'sensor') {
        if (port === 'bottom') return { x: x + PID.SENSOR_W / 2, y: y + PID.SENSOR_H };
    }
    if (obj.type === 'node') {
        if (port === 'top')    return { x: x,              y: y - PID.PORT_OFF };
        if (port === 'right')  return { x: x + PID.PORT_OFF, y: y             };
        if (port === 'bottom') return { x: x,              y: y + PID.PORT_OFF };
        if (port === 'left')   return { x: x - PID.PORT_OFF, y: y             };
    }
    return { x, y };
}

// ── Obstacle-aware orthogonal router ────────────────────────────────────────

// Returns inflated bounding rects for all sensor objects not in excludeIds.
function pidObstacleRects(objects, excludeIds) {
    const M = PID.OBS_MARGIN;
    return objects
        .filter(o => !excludeIds.has(o.id) && o.type === 'sensor')
        .map(o => ({
            x1: o.gridX * PID.GRID - M,
            y1: o.gridY * PID.GRID - M,
            x2: o.gridX * PID.GRID + PID.SENSOR_W + M,
            y2: o.gridY * PID.GRID + PID.SENSOR_H + M,
        }));
}

// Returns false if axis-aligned segment passes through any rect interior.
// Strict inequalities allow segments that only touch a border.
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

function pidPathClear(pts, rects) {
    if (!rects.length) return true;
    for (let i = 0; i < pts.length - 1; i++) {
        if (!pidSegClear(pts[i].x, pts[i].y, pts[i+1].x, pts[i+1].y, rects)) return false;
    }
    return true;
}

// Converts orthogonal waypoints to a rounded SVG path string.
// Collinear runs are merged; corners get a quadratic Bezier arc of radius r.
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

// Main routing entry point.
// Tries direct and shifted Z/U-shape candidates; first clear one wins.
// Returns { d: svgPathString, error: string|null }.
function orthRouteAvoiding(p1, d1, p2, d2, objects, fromId, toId) {
    function ext(p, d, dist) {
        if (d === 'top')    return { x: p.x,        y: p.y - dist };
        if (d === 'bottom') return { x: p.x,        y: p.y + dist };
        if (d === 'right')  return { x: p.x + dist, y: p.y };
        return                      { x: p.x - dist, y: p.y };
    }
    const G = PID.GRID, S = PID.STUB, R = PID.CORNER_R;
    const s1 = ext(p1, d1, S), s2 = ext(p2, d2, S);
    const rects = pidObstacleRects(objects, new Set([fromId, toId]));

    const offsets = [0, G, -G, 2*G, -2*G, 3*G, -3*G, 4*G, -4*G, 6*G, -6*G, 8*G, -8*G, 10*G, -10*G];

    // Direct: stubs already aligned
    if (Math.abs(s1.x - s2.x) < 1 || Math.abs(s1.y - s2.y) < 1) {
        const pts = [p1, s1, s2, p2];
        if (pidPathClear(pts, rects)) return { d: pidRoundedPath(pts, R), error: null };
    }

    for (const off of offsets) {
        // Z-shape: horizontal crossover at y = midY
        const my = Math.round((s1.y + s2.y) / 2 / G) * G + off;
        const zPts = [p1, s1, { x: s1.x, y: my }, { x: s2.x, y: my }, s2, p2];
        if (pidPathClear(zPts, rects)) return { d: pidRoundedPath(zPts, R), error: null };

        // U-shape: vertical crossover at x = midX
        const mx = Math.round((s1.x + s2.x) / 2 / G) * G + off;
        const uPts = [p1, s1, { x: mx, y: s1.y }, { x: mx, y: s2.y }, s2, p2];
        if (pidPathClear(uPts, rects)) return { d: pidRoundedPath(uPts, R), error: null };
    }

    // All candidates blocked — fallback with error
    const my0 = Math.round((s1.y + s2.y) / 2 / G) * G;
    const fallPts = [p1, s1, { x: s1.x, y: my0 }, { x: s2.x, y: my0 }, s2, p2];
    return { d: pidRoundedPath(fallPts, R), error: 'Could not route without crossing an object' };
}

// ── SVG coordinate from pointer event ───────────────────────────────────────

function pidSvgPt(svgEl, e) {
    const pt = svgEl.createSVGPoint();
    pt.x = e.clientX; pt.y = e.clientY;
    return pt.matrixTransform(svgEl.getScreenCTM().inverse());
}

// ── Unique ID ────────────────────────────────────────────────────────────────

function pidUid(prefix) {
    return prefix + '_' + Date.now() + '_' + Math.floor(Math.random() * 9999);
}

// ── HTML escape helper ───────────────────────────────────────────────────────

function pidEsc(s) {
    return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
}

// =============================================================================
// buildFrontPanelContent  — called by tabs.js when creating a front-panel tab
// =============================================================================

function buildFrontPanelContent(tab) {
    tab.contentEl.innerHTML = '';
    tab.contentEl.classList.add('tab-content--fixed');

    // ── Root container ──
    const panel = document.createElement('div');
    panel.className = 'pid-panel';

    // ── Toolbar ──
    const toolbar = document.createElement('div');
    toolbar.className = 'pid-toolbar';

    const picker = document.createElement('select');
    picker.className = 'pid-picker';
    picker.title = 'Select layout';
    picker.innerHTML = '<option value="">-- No layout --</option>';
    Object.values(pidLayouts).forEach(l => {
        const o = document.createElement('option');
        o.value = l.filename; o.textContent = l.name;
        picker.appendChild(o);
    });

    const modeWrap = document.createElement('div');
    modeWrap.className = 'pid-mode-toggle';
    const viewBtn = document.createElement('button');
    viewBtn.className = 'pid-mode-btn pid-mode-active'; viewBtn.textContent = 'View';
    const editBtn = document.createElement('button');
    editBtn.className = 'pid-mode-btn'; editBtn.textContent = 'Edit';
    modeWrap.append(viewBtn, editBtn);

    const saveBtn = document.createElement('button');
    saveBtn.className = 'pid-save-btn'; saveBtn.textContent = 'Save YAML';

    // Warning button — shown only when routing errors exist
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
        if (warnBtn.classList.contains('pid-warn-open')) renderPidWarnDropdown(tab);
    });
    // Close all warn dropdowns on outside click (registered once)
    if (!window._pidWarnOutsideHandler) {
        window._pidWarnOutsideHandler = true;
        document.addEventListener('click', () => {
            document.querySelectorAll('.pid-warn-btn.pid-warn-open')
                    .forEach(b => b.classList.remove('pid-warn-open'));
        });
    }

    toolbar.append(picker, modeWrap, warnBtn, saveBtn);
    panel.appendChild(toolbar);

    // ── Body ──
    const body = document.createElement('div');
    body.className = 'pid-body';

    // Left sidebar
    const lsb = document.createElement('div');
    lsb.className = 'pid-lsb pid-lsb-hidden';
    lsb.innerHTML =
        '<div class="pid-sb-title">Objects</div>' +
        '<div class="pid-obj-item" draggable="true" data-type="sensor">' +
            '<div class="pid-obj-preview">Sensor</div></div>' +
        '<div class="pid-obj-item" draggable="true" data-type="node">' +
            '<div class="pid-obj-preview pid-obj-preview-node">Node</div></div>';

    // Canvas
    const canvasWrap = document.createElement('div');
    canvasWrap.className = 'pid-canvas-wrap';

    const svg = svgN('svg', {
        class: 'pid-svg', width: PID.CANVAS_W, height: PID.CANVAS_H,
        xmlns: 'http://www.w3.org/2000/svg',
    });

    // Grid pattern
    const defs = svgN('defs');
    const pat = svgN('pattern', {
        id: 'pid-grid-' + tab.id, x: 0, y: 0,
        width: PID.GRID, height: PID.GRID, patternUnits: 'userSpaceOnUse',
    });
    pat.appendChild(svgN('circle', { cx: 0, cy: 0, r: 0.7, fill: 'var(--border)' }));
    defs.appendChild(pat);
    svg.appendChild(defs);

    const gGrid = svgN('g', { class: 'pid-g-grid' });
    gGrid.appendChild(svgN('rect', {
        x: 0, y: 0, width: PID.CANVAS_W, height: PID.CANVAS_H,
        fill: 'url(#pid-grid-' + tab.id + ')', 'pointer-events': 'none',
    }));
    const gConns = svgN('g', { class: 'pid-g-conns' });
    const gObjs  = svgN('g', { class: 'pid-g-objs'  });
    svg.append(gGrid, gConns, gObjs);
    canvasWrap.appendChild(svg);

    // Right sidebar
    const rsb = document.createElement('div');
    rsb.className = 'pid-rsb';

    body.append(lsb, canvasWrap, rsb);
    panel.appendChild(body);
    tab.contentEl.appendChild(panel);

    // ── Per-tab state ──
    tab.pid = {
        editMode: false,
        layoutFilename: '',
        layoutName: '',
        objects: [],
        connections: [],
        selectedId: null,
        connecting: null,     // { objId, port } while drawing a connection
        previewEl: null,      // dashed preview path
        svgEl: svg,
        gGrid, gConns, gObjs,
        canvasWrap,
        lsbEl: lsb,
        rsbEl: rsb,
        pickerEl: picker,
        routingErrors: [],
        warnBtnEl: warnBtn,
        warnDropdownEl: warnDropdown,
    };

    // ── Toolbar events ──
    picker.addEventListener('change', () => {
        const fn = picker.value;
        if (fn && pidLayouts[fn]) loadPidLayout(tab, pidLayouts[fn]);
        else                      clearPidLayout(tab);
    });
    viewBtn.addEventListener('click', () => { setPidMode(tab, false); viewBtn.classList.add('pid-mode-active'); editBtn.classList.remove('pid-mode-active'); });
    editBtn.addEventListener('click', () => { setPidMode(tab, true);  editBtn.classList.add('pid-mode-active'); viewBtn.classList.remove('pid-mode-active'); });
    saveBtn.addEventListener('click', () => savePidYaml(tab));

    // ── Sidebar drag-to-canvas ──
    lsb.querySelectorAll('[draggable]').forEach(el => {
        el.addEventListener('dragstart', e => e.dataTransfer.setData('pid-type', el.dataset.type));
    });
    svg.addEventListener('dragover', e => e.preventDefault());
    svg.addEventListener('drop', e => onPidDrop(tab, e));

    // ── Canvas interaction ──
    svg.addEventListener('pointerdown', e => onPidPointerDown(tab, e));
    svg.addEventListener('pointermove', e => onPidPointerMove(tab, e));
    svg.addEventListener('contextmenu', e => { e.preventDefault(); onPidContextMenu(tab, e); });

    // ── Initial right sidebar ──
    renderPidRsb(tab, null);
}

// =============================================================================
// Mode, layout, save
// =============================================================================

function setPidMode(tab, editMode) {
    tab.pid.editMode = editMode;
    tab.pid.lsbEl.classList.toggle('pid-lsb-hidden', !editMode);
    cancelPidConnect(tab);
    selectPidObject(tab, null);
    renderPidAll(tab);
}

function loadPidLayout(tab, record) {
    const parsed = pidFromYaml(record.content);
    tab.pid.layoutFilename = record.filename;
    tab.pid.layoutName     = parsed.name;
    tab.pid.objects        = parsed.objects;
    tab.pid.connections    = parsed.connections;
    tab.pid.selectedId     = null;
    tab.pid.connecting     = null;
    if (tab.pid.pickerEl) tab.pid.pickerEl.value = record.filename;
    renderPidAll(tab);
    renderPidRsb(tab, null);
}

function clearPidLayout(tab) {
    tab.pid.layoutFilename = '';
    tab.pid.layoutName     = '';
    tab.pid.objects        = [];
    tab.pid.connections    = [];
    tab.pid.selectedId     = null;
    tab.pid.connecting     = null;
    renderPidAll(tab);
    renderPidRsb(tab, null);
}

function savePidYaml(tab) {
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

// Called by ws.js when a new pid_layout arrives to refresh the layout picker.
function refreshPidLayoutPicker(tab) {
    if (!tab.pid || !tab.pid.pickerEl) return;
    const picker = tab.pid.pickerEl;
    const current = picker.value;
    // Rebuild options
    while (picker.options.length > 1) picker.remove(1);
    Object.values(pidLayouts).forEach(l => {
        const o = document.createElement('option');
        o.value = l.filename; o.textContent = l.name;
        picker.appendChild(o);
    });
    picker.value = current;
}

// =============================================================================
// Render
// =============================================================================

function renderPidAll(tab) {
    tab.pid.gObjs.innerHTML  = '';
    tab.pid.gConns.innerHTML = '';
    tab.pid.previewEl        = null;
    tab.pid.gGrid.style.display = tab.pid.editMode ? '' : 'none';

    for (const obj of tab.pid.objects) renderPidObj(tab, obj);

    tab.pid.routingErrors = [];
    for (const conn of tab.pid.connections) renderPidConn(tab, conn);
    renderPidWarning(tab);

    rebindPidLiveData(tab);
}

function renderPidObj(tab, obj) {
    let g;
    if (obj.type === 'sensor') g = makeSensorGroup(tab, obj);
    else                       g = makeNodeGroup(tab, obj);
    tab.pid.gObjs.appendChild(g);
}

function makeSensorGroup(tab, obj) {
    const sel = (tab.pid.selectedId === obj.id);
    const g = svgN('g', {
        class: 'pid-obj pid-sensor' + (sel ? ' pid-selected' : ''),
        'data-pid-id': obj.id,
        transform: 'translate(' + (obj.gridX * PID.GRID) + ',' + (obj.gridY * PID.GRID) + ')',
        cursor: tab.pid.editMode ? 'grab' : 'default',
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

    // Port — always rendered but visible only in edit mode
    const port = svgN('circle', {
        class: 'pid-port' + (tab.pid.editMode ? '' : ' pid-port-hidden'),
        'data-obj-id': obj.id, 'data-port': 'bottom',
        cx: PID.SENSOR_W / 2, cy: PID.SENSOR_H, r: PID.PORT_R,
    });
    g.appendChild(port);
    return g;
}

function makeNodeGroup(tab, obj) {
    const sel = (tab.pid.selectedId === obj.id);
    const g = svgN('g', {
        class: 'pid-obj pid-node' + (sel ? ' pid-selected' : '') + (tab.pid.editMode ? '' : ' pid-node-hidden'),
        'data-pid-id': obj.id,
        transform: 'translate(' + (obj.gridX * PID.GRID) + ',' + (obj.gridY * PID.GRID) + ')',
        cursor: tab.pid.editMode ? 'grab' : 'default',
    });

    g.appendChild(svgN('circle', { class: 'pid-node-dot', cx: 0, cy: 0, r: PID.NODE_R }));

    if (tab.pid.editMode) {
        const ports = { top: [0, -PID.PORT_OFF], right: [PID.PORT_OFF, 0], bottom: [0, PID.PORT_OFF], left: [-PID.PORT_OFF, 0] };
        for (const [pname, [px, py]] of Object.entries(ports)) {
            g.appendChild(svgN('circle', {
                class: 'pid-port',
                'data-obj-id': obj.id, 'data-port': pname,
                cx: px, cy: py, r: PID.PORT_R,
            }));
        }
    }
    return g;
}

function renderPidConn(tab, conn) {
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

    let el = tab.pid.gConns.querySelector('[data-conn-id="' + conn.id + '"]');
    if (!el) {
        el = svgN('path', { class: 'pid-conn-path', 'data-conn-id': conn.id });
        tab.pid.gConns.appendChild(el);
    }
    el.setAttribute('d', d);
    el.classList.toggle('pid-conn-error', !!error);
}

// Re-routes ALL connections (not just touching ones) because moving/adding any
// object can block or unblock paths that don't connect to it.
function updateConnsTouching(tab, _objId) {
    tab.pid.routingErrors = [];
    for (const conn of tab.pid.connections) renderPidConn(tab, conn);
    renderPidWarning(tab);
}

// =============================================================================
// Routing warning indicator
// =============================================================================

function renderPidWarning(tab) {
    const btn = tab.pid.warnBtnEl;
    if (!btn) return;
    const errs = tab.pid.routingErrors;
    btn.style.display = errs.length > 0 ? '' : 'none';
    const countEl = btn.querySelector('.pid-warn-count');
    if (countEl) countEl.textContent = errs.length > 1 ? String(errs.length) : '';
    if (btn.classList.contains('pid-warn-open')) {
        renderPidWarnDropdown(tab);
    }
}

function renderPidWarnDropdown(tab) {
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
// Live data binding
// =============================================================================

function rebindPidLiveData(tab) {
    tab.channelUpdaters = {};
    if (tab.pid.editMode) return;
    for (const obj of tab.pid.objects) {
        if (obj.type !== 'sensor' || !obj.refDes) continue;
        const id = obj.id;
        tab.channelUpdaters[obj.refDes] = value => {
            const el = tab.pid.svgEl.querySelector('[data-pid-id="' + id + '"] .pid-sensor-value');
            if (!el) return;
            el.textContent = typeof value === 'number'
                ? (Number.isInteger(value) ? String(value) : value.toFixed(2))
                : String(value);
            el.classList.remove('stale');
        };
    }
}

// =============================================================================
// Selection
// =============================================================================

function selectPidObject(tab, id) {
    tab.pid.selectedId = id;
    // Refresh selection highlight
    tab.pid.gObjs.querySelectorAll('.pid-selected').forEach(el => el.classList.remove('pid-selected'));
    if (id) {
        const el = tab.pid.gObjs.querySelector('[data-pid-id="' + id + '"]');
        if (el) el.classList.add('pid-selected');
    }
    renderPidRsb(tab, id);
}

// =============================================================================
// Right sidebar
// =============================================================================

function renderPidRsb(tab, objId) {
    const rsb = tab.pid.rsbEl;
    rsb.innerHTML = '';
    const c = document.createElement('div');
    c.className = 'pid-rsb-content';

    if (!objId) {
        // Layout settings
        c.innerHTML =
            '<div class="pid-sb-heading">Layout</div>' +
            '<div class="pid-sb-field"><label>Name</label>' +
            '<input class="pid-name-input" type="text" value="' + pidEsc(tab.pid.layoutName || '') + '" placeholder="Panel name"></div>' +
            '<div class="pid-sb-hint">Save YAML and add the file to the<br>control node config XML under<br>&lt;frontPanels&gt;.</div>';
        c.querySelector('.pid-name-input').addEventListener('input', e => {
            tab.pid.layoutName = e.target.value;
        });
    } else {
        const obj = tab.pid.objects.find(o => o.id === objId);
        if (!obj) { rsb.appendChild(c); return; }

        if (obj.type === 'sensor') {
            // Build channel list from configControls (sensor roles)
            const chs = [];
            for (const ctrl of configControls) {
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
                obj.refDes = sel ? sel.value : (inp ? inp.value.trim() : '');
                obj.units  = uinp ? uinp.value.trim() : '';
                // Sync label/units text in SVG
                const g = tab.pid.gObjs.querySelector('[data-pid-id="' + objId + '"]');
                if (g) {
                    g.querySelector('.pid-sensor-label').textContent = obj.refDes || '(no refDes)';
                    g.querySelector('.pid-sensor-units').textContent = obj.units || '';
                }
                rebindPidLiveData(tab);
            });

        } else {
            c.innerHTML =
                '<div class="pid-sb-heading">Junction Node</div>' +
                '<div class="pid-sb-hint">Connects pipes in up to<br>4 directions.</div>' +
                '<button class="pid-delete-btn">Remove</button>';
        }

        c.querySelector('.pid-delete-btn').addEventListener('click', () => deletePidObj(tab, objId));
    }

    rsb.appendChild(c);
}

// =============================================================================
// Object CRUD
// =============================================================================

function createPidObj(tab, type, gridX, gridY) {
    const obj = { id: pidUid(type), type, gridX, gridY };
    if (type === 'sensor') { obj.refDes = ''; obj.units = ''; }
    tab.pid.objects.push(obj);
    renderPidObj(tab, obj);
    // New object may block existing routes — re-path everything
    tab.pid.routingErrors = [];
    for (const conn of tab.pid.connections) renderPidConn(tab, conn);
    renderPidWarning(tab);
    selectPidObject(tab, obj.id);
}

function deletePidObj(tab, id) {
    // Remove connections touching this object
    tab.pid.connections = tab.pid.connections.filter(c => {
        if (c.fromId === id || c.toId === id) {
            tab.pid.gConns.querySelector('[data-conn-id="' + c.id + '"]')?.remove();
            return false;
        }
        return true;
    });
    // Remove object
    tab.pid.objects = tab.pid.objects.filter(o => o.id !== id);
    tab.pid.gObjs.querySelector('[data-pid-id="' + id + '"]')?.remove();
    selectPidObject(tab, null);
    // Deleted object may have been blocking routes — re-path remaining connections
    tab.pid.routingErrors = [];
    for (const conn of tab.pid.connections) renderPidConn(tab, conn);
    renderPidWarning(tab);
}

// =============================================================================
// Drag objects on canvas
// =============================================================================

function startObjDrag(tab, objId, e) {
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
        // Move the object element immediately for smooth visual feedback
        const el = tab.pid.gObjs.querySelector('[data-pid-id="' + objId + '"]');
        if (el) el.setAttribute('transform', 'translate(' + (obj.gridX * PID.GRID) + ',' + (obj.gridY * PID.GRID) + ')');
        // Throttle path recalculation to once per animation frame
        if (!rafPending) {
            rafPending = true;
            requestAnimationFrame(() => {
                rafPending = false;
                updateConnsTouching(tab, objId);
            });
        }
    };

    const onUp = eu => {
        tab.pid.svgEl.removeEventListener('pointermove', onMove);
        tab.pid.svgEl.removeEventListener('pointerup',   onUp);
        tab.pid.svgEl.releasePointerCapture(eu.pointerId);
        // Final re-route at resting position
        updateConnsTouching(tab, objId);
        if (!moved) selectPidObject(tab, objId);
    };

    tab.pid.svgEl.setPointerCapture(e.pointerId);
    tab.pid.svgEl.addEventListener('pointermove', onMove);
    tab.pid.svgEl.addEventListener('pointerup',   onUp);
}

// =============================================================================
// Connection drawing
// =============================================================================

function startPidConnect(tab, fromObjId, fromPort, e) {
    tab.pid.connecting = { objId: fromObjId, port: fromPort };
    // Create preview line
    tab.pid.previewEl = svgN('line', {
        class: 'pid-preview-line', 'pointer-events': 'none',
        x1: 0, y1: 0, x2: 0, y2: 0,
    });
    tab.pid.gConns.appendChild(tab.pid.previewEl);
    // Anchor start
    const fromObj = tab.pid.objects.find(o => o.id === fromObjId);
    if (fromObj) {
        const pp = portPos(fromObj, fromPort);
        tab.pid.previewEl.setAttribute('x1', pp.x);
        tab.pid.previewEl.setAttribute('y1', pp.y);
    }
}

function completePidConnect(tab, toObjId, toPort) {
    const { objId: fromId, port: fromPort } = tab.pid.connecting;

    // Avoid duplicate connections on the same port pair
    const exists = tab.pid.connections.some(
        c => (c.fromId === fromId && c.fromPort === fromPort && c.toId === toObjId && c.toPort === toPort) ||
             (c.fromId === toObjId && c.fromPort === toPort && c.toId === fromId && c.toPort === fromPort)
    );
    if (!exists) {
        const conn = { id: pidUid('conn'), fromId, fromPort, toId: toObjId, toPort };
        tab.pid.connections.push(conn);
        renderPidConn(tab, conn);
    }
    cancelPidConnect(tab);
}

function cancelPidConnect(tab) {
    tab.pid.connecting = null;
    if (tab.pid.previewEl) { tab.pid.previewEl.remove(); tab.pid.previewEl = null; }
}

// =============================================================================
// Canvas event handlers
// =============================================================================

function onPidPointerDown(tab, e) {
    if (e.button !== 0) return;
    e.stopPropagation();

    if (tab.pid.editMode) {
        const portEl = e.target.closest('.pid-port');
        if (portEl) {
            const fromObjId = portEl.dataset.objId, fromPort = portEl.dataset.port;
            if (tab.pid.connecting) {
                if (fromObjId !== tab.pid.connecting.objId) completePidConnect(tab, fromObjId, fromPort);
                else cancelPidConnect(tab);
            } else {
                startPidConnect(tab, fromObjId, fromPort, e);
            }
            return;
        }

        if (tab.pid.connecting) { cancelPidConnect(tab); return; }

        const objEl = e.target.closest('[data-pid-id]');
        if (objEl) {
            e.preventDefault();
            startObjDrag(tab, objEl.dataset.pidId, e);
            return;
        }

        selectPidObject(tab, null);
    }

    // Pan the canvas when clicking on the background (both view and edit mode)
    if (!e.target.closest('[data-pid-id]') && !e.target.closest('.pid-port')) {
        startPidPan(tab, e);
    }
}

// =============================================================================
// Canvas pan (click-drag on background)
// =============================================================================

function startPidPan(tab, e) {
    const wrap = tab.pid.canvasWrap;
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

function onPidPointerMove(tab, e) {
    if (!tab.pid.connecting || !tab.pid.previewEl) return;
    const pt = pidSvgPt(tab.pid.svgEl, e);
    tab.pid.previewEl.setAttribute('x2', pt.x);
    tab.pid.previewEl.setAttribute('y2', pt.y);
}

function onPidContextMenu(tab, e) {
    if (!tab.pid.editMode) return;
    const objEl = e.target.closest('[data-pid-id]');
    if (objEl) selectPidObject(tab, objEl.dataset.pidId);
}

function onPidDrop(tab, e) {
    e.preventDefault();
    const type = e.dataTransfer.getData('pid-type');
    if (!type) return;
    const pt = pidSvgPt(tab.pid.svgEl, e);
    const gx = Math.max(0, Math.round(pt.x / PID.GRID));
    const gy = Math.max(0, Math.round(pt.y / PID.GRID));
    createPidObj(tab, type, gx, gy);
}

// =============================================================================
// Global keyboard handler (registered once from app.js init via pid.js load)
// =============================================================================

document.addEventListener('keydown', e => {
    if (e.target.tagName === 'INPUT' || e.target.tagName === 'TEXTAREA' || e.target.tagName === 'SELECT') return;
    const tab = tabs.find(t => t.id === activeTabId && t.type === 'frontPanel');
    if (!tab || !tab.pid) return;

    if ((e.key === 'Delete' || e.key === 'Backspace') && tab.pid.editMode && tab.pid.selectedId) {
        e.preventDefault();
        deletePidObj(tab, tab.pid.selectedId);
    }
    if (e.key === 'Escape') {
        cancelPidConnect(tab);
    }
});
