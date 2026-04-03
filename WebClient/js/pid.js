// =============================================================================
// P&ID Front Panel — View-only renderer
// =============================================================================
//
// Each Front Panel tab loads one YAML layout.  Layouts arrive via pid_layout
// WebSocket messages and are cached in pidLayouts{}.
//
// Editing is handled in a separate editor page (editor.html).
// Click the "Editor" button in the toolbar to open it.
//
// YAML layout schema:
//   name: Panel Name
//   version: 1
//   objects:
//     - id: "obj_123"
//       type: sensor        # sensor | node | graph
//       refDes: OPT-01      # channel refDes (sensor only)
//       units: psi          # engineering units (sensor only)
//       showRefDes: true    # sensor: show refDes label (default true)
//       showUnits: true     # sensor: show units label (default true)
//       showName: false     # sensor: show ctrl description (default false)
//                           # graph:  show title bar     (default true)
//       gridX: 10           # position in grid cells (1 cell = 20 px)
//       gridY: 5
//       name: "LOX History" # graph only: display title
//       gridW: 20           # graph only: width in grid cells (default 20)
//       gridH: 10           # graph only: height in grid cells (default 10)
//       showLeftSidebar: false # graph only: show channel list panel (default false)
//       lines:              # graph only: pre-configured channels
//         - refDes: OPT-01
//           color: "#4e9f3d"
//           yAxis: 1
//           hidden: false
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

// ── YAML parser (handles our exact schema only) ──────────────────────────────

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
            // Section item: "  - id: ..."
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
            // Object property at indent 4: "    key: value" or "    lines:"
            if (cur) {
                const m = t.match(/^([\w]+):\s*(.*)/);
                if (m) {
                    if (m[2] === '' || m[2].trim() === '') {
                        // Subsection header (e.g. "    lines:")
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
            // Subsection item or property at indent 6+
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
        return { x, y };
    }
    return { x, y };
}

// ── Obstacle-aware orthogonal router ────────────────────────────────────────

function pidObstacleRects(objects, excludeIds) {
    const M = PID.OBS_MARGIN;
    return objects
        .filter(o => !excludeIds.has(o.id) && (o.type === 'sensor' || o.type === 'graph'))
        .map(o => {
            if (o.type === 'graph') {
                return {
                    x1: o.gridX * PID.GRID - M,
                    y1: o.gridY * PID.GRID - M,
                    x2: o.gridX * PID.GRID + (o.gridW || 20) * PID.GRID + M,
                    y2: o.gridY * PID.GRID + (o.gridH || 10) * PID.GRID + M,
                };
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
        if (d === 'top')    return { x: p.x,        y: p.y - dist };
        if (d === 'bottom') return { x: p.x,        y: p.y + dist };
        if (d === 'right')  return { x: p.x + dist, y: p.y };
        return                      { x: p.x - dist, y: p.y };
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

// ── SVG coordinate from pointer event ───────────────────────────────────────

function pidSvgPt(svgEl, e) {
    const pt = svgEl.createSVGPoint();
    pt.x = e.clientX; pt.y = e.clientY;
    return pt.matrixTransform(svgEl.getScreenCTM().inverse());
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

    // (no toolbar — editor button lives in the layout sidebar)

    // ── Body ──
    const body = document.createElement('div');
    body.className = 'pid-body';

    // Layout panel (left sidebar)
    const layoutPanel = document.createElement('div');
    layoutPanel.className = 'pid-layout-panel';
    const panelTitle = document.createElement('div');
    panelTitle.className = 'pid-sb-title';
    panelTitle.textContent = 'Layouts';
    const panelItems = document.createElement('div');
    panelItems.className = 'pid-layout-items';
    const editorBtn = document.createElement('button');
    editorBtn.className = 'pid-editor-btn';
    editorBtn.textContent = 'Editor';
    editorBtn.title = 'Open layout editor';
    layoutPanel.append(panelTitle, panelItems, editorBtn);

    // Canvas
    const canvasWrap = document.createElement('div');
    canvasWrap.className = 'pid-canvas-wrap';

    const svg = svgN('svg', {
        class: 'pid-svg', width: PID.CANVAS_W, height: PID.CANVAS_H,
        xmlns: 'http://www.w3.org/2000/svg',
    });

    // Grid pattern (defined but never shown in view mode)
    const defs = svgN('defs');
    const pat = svgN('pattern', {
        id: 'pid-grid-' + tab.id, x: 0, y: 0,
        width: PID.GRID, height: PID.GRID, patternUnits: 'userSpaceOnUse',
    });
    pat.appendChild(svgN('circle', { cx: 0, cy: 0, r: 0.7, fill: 'var(--border)' }));
    defs.appendChild(pat);
    svg.appendChild(defs);

    const gConns = svgN('g', { class: 'pid-g-conns' });
    const gObjs  = svgN('g', { class: 'pid-g-objs'  });
    svg.append(gConns, gObjs);
    canvasWrap.appendChild(svg);

    body.append(layoutPanel, canvasWrap);
    panel.appendChild(body);
    tab.contentEl.appendChild(panel);

    // ── Per-tab state ──
    tab.pid = {
        layoutFilename: '',
        layoutName: '',
        objects: [],
        connections: [],
        svgEl: svg,
        gConns,
        gObjs,
        canvasWrap,
        layoutPanelEl: panelItems,
    };

    // Populate layout panel with any layouts already received
    buildLayoutPanelItems(tab);

    editorBtn.addEventListener('click', () => openPidEditor(tab));

    // ── Canvas pan ──
    svg.addEventListener('pointerdown', e => {
        if (e.button !== 0) return;
        e.stopPropagation();
        startPidPan(tab, e);
    });
}

// =============================================================================
// Open editor page
// =============================================================================

function openPidEditor(tab) {
    const data = {
        configControls:  configControls,
        pidLayouts:      pidLayouts,
        selectedLayout:  tab.pid.layoutFilename,
    };
    try {
        sessionStorage.setItem('pid_editor_data', JSON.stringify(data));
    } catch (e) {
        console.warn('Could not store editor data in sessionStorage:', e);
    }
    window.open('editor.html', '_blank');
}

// =============================================================================
// Layout load / clear
// =============================================================================

function loadPidLayout(tab, record) {
    const parsed = pidFromYaml(record.content);
    tab.pid.layoutFilename = record.filename;
    tab.pid.layoutName     = parsed.name;
    tab.pid.objects        = parsed.objects;
    tab.pid.connections    = parsed.connections;
    buildLayoutPanelItems(tab);
    renderPidAll(tab);
}

function clearPidLayout(tab) {
    tab.pid.layoutFilename = '';
    tab.pid.layoutName     = '';
    tab.pid.objects        = [];
    tab.pid.connections    = [];
    buildLayoutPanelItems(tab);
    renderPidAll(tab);
}

// Rebuilds the layout panel item list; called on load and when new layouts arrive.
function buildLayoutPanelItems(tab) {
    const el = tab.pid.layoutPanelEl;
    if (!el) return;
    el.innerHTML = '';
    const layouts = Object.values(pidLayouts);
    if (layouts.length === 0) {
        const empty = document.createElement('div');
        empty.className = 'pid-layout-empty';
        empty.textContent = 'No layouts received';
        el.appendChild(empty);
        return;
    }
    layouts.forEach(l => {
        const item = document.createElement('div');
        item.className = 'pid-layout-item' +
            (l.filename === tab.pid.layoutFilename ? ' pid-layout-active' : '');
        item.dataset.fn  = l.filename;
        item.textContent = l.name;
        item.title       = l.name;
        item.addEventListener('click', () => {
            if (tab.pid.layoutFilename === l.filename) return;
            loadPidLayout(tab, pidLayouts[l.filename]);
        });
        el.appendChild(item);
    });
}

// Called by ws.js when a new pid_layout arrives.
function refreshPidLayoutPicker(tab) {
    if (!tab.pid || !tab.pid.layoutPanelEl) return;
    buildLayoutPanelItems(tab);
}

// =============================================================================
// Render (view-only — no grid, no ports, no selection)
// =============================================================================

function renderPidAll(tab) {
    // Clean up any existing embedded graph chart states for this tab
    const pfx = '__pid_graph_' + tab.id + '_';
    for (const key of Object.keys(graphState)) {
        if (!key.startsWith(pfx)) continue;
        const st = graphState[key];
        for (const cell of st.cells) {
            for (const ch of [...cell.channels]) removeChannelFromCell(key, 0, ch.refDes);
            cell.chart?.destroy();
        }
        delete graphState[key];
    }

    tab.pid.gObjs.innerHTML  = '';
    tab.pid.gConns.innerHTML = '';
    for (const obj of tab.pid.objects) renderPidObj(tab, obj);
    for (const conn of tab.pid.connections) renderPidConn(tab, conn);
    rebindPidLiveData(tab);
}

function renderPidObj(tab, obj) {
    const g = obj.type === 'graph'  ? makeGraphGroup(obj, tab)
            : obj.type === 'sensor' ? makeSensorGroup(obj)
            : makeNodeGroup(obj);
    tab.pid.gObjs.appendChild(g);
}

function makeSensorGroup(obj) {
    const showRefDes = obj.showRefDes !== false;
    const showUnits  = obj.showUnits  !== false;
    const showName   = obj.showName   === true;

    const g = svgN('g', {
        class: 'pid-obj pid-sensor',
        'data-pid-id': obj.id,
        transform: 'translate(' + (obj.gridX * PID.GRID) + ',' + (obj.gridY * PID.GRID) + ')',
    });

    g.appendChild(svgN('rect', {
        x: 0, y: 0, width: PID.SENSOR_W, height: PID.SENSOR_H,
        rx: 3, class: 'pid-sensor-rect',
    }));

    if (obj.refDes) {
        g.style.cursor = 'context-menu';
        g.addEventListener('contextmenu', (e) => {
            e.preventDefault();
            e.stopPropagation();
            openObjectSidebar(obj.refDes);
        });
    }

    // Dynamic Y layout: value is always shown; other elements are optional
    // Box height = 50px. Layout items from top: name(opt), refDes(opt), value, units(opt)
    const items = [];
    if (showName) {
        const desc = (configControls.find(c => c.channels?.some(ch => ch.refDes === obj.refDes)))?.description || '';
        items.push({ type: 'name', text: desc });
    }
    if (showRefDes) items.push({ type: 'refdes', text: obj.refDes || '(no refDes)' });
    items.push({ type: 'value', text: '--' });
    if (showUnits)  items.push({ type: 'units',  text: obj.units || '' });

    const step = PID.SENSOR_H / (items.length + 1);
    for (let i = 0; i < items.length; i++) {
        const item = items[i];
        const y = Math.round(step * (i + 1));
        let cls;
        if      (item.type === 'name')   cls = 'pid-sensor-name';
        else if (item.type === 'refdes') cls = 'pid-sensor-label';
        else if (item.type === 'value')  cls = 'pid-sensor-value stale';
        else                             cls = 'pid-sensor-units';
        const el = svgN('text', { class: cls, x: PID.SENSOR_W / 2, y });
        el.textContent = item.text;
        g.appendChild(el);
    }

    return g;
}

function makeNodeGroup(obj) {
    // Junction nodes are invisible in view mode — they only serve as
    // connection routing waypoints.
    const g = svgN('g', {
        class: 'pid-obj pid-node pid-node-hidden',
        'data-pid-id': obj.id,
        transform: 'translate(' + (obj.gridX * PID.GRID) + ',' + (obj.gridY * PID.GRID) + ')',
    });
    g.appendChild(svgN('circle', { class: 'pid-node-dot', cx: 0, cy: 0, r: PID.NODE_R }));
    return g;
}

function makeGraphGroup(obj, tab) {
    const W = (obj.gridW || 20) * PID.GRID;
    const H = (obj.gridH || 10) * PID.GRID;
    const GRAPH_TAB_ID = '__pid_graph_' + tab.id + '_' + obj.id + '__';

    const g = svgN('g', {
        class: 'pid-obj pid-graph',
        'data-pid-id': obj.id,
        transform: 'translate(' + (obj.gridX * PID.GRID) + ',' + (obj.gridY * PID.GRID) + ')',
    });

    // foreignObject embeds an HTML subtree (including canvas) inside the SVG
    const fo = svgN('foreignObject', { x: 0, y: 0, width: W, height: H });

    const body = document.createElementNS('http://www.w3.org/1999/xhtml', 'div');
    body.style.cssText = 'width:' + W + 'px;height:' + H + 'px;overflow:hidden;display:flex;flex-direction:column;box-sizing:border-box;';
    body.className = 'pid-graph-body';

    // Optional title bar
    if (obj.showName !== false && obj.name) {
        const titleBar = document.createElement('div');
        titleBar.className = 'pid-graph-titlebar';
        titleBar.textContent = obj.name;
        body.appendChild(titleBar);
    }

    // Cell row: optional left panel + chart area
    const cellWrap = document.createElement('div');
    cellWrap.style.cssText = 'flex:1;min-height:0;display:flex;overflow:hidden;';

    // Optional left channel-list panel (same structure as graph tab)
    if (obj.showLeftSidebar) {
        const panel       = document.createElement('div');
        panel.className   = 'graph-cell-panel';
        const channelList = document.createElement('div');
        channelList.className = 'graph-channel-list';
        panel.appendChild(channelList);
        cellWrap.appendChild(panel);
    }

    // Chart area
    const chartArea = document.createElement('div');
    chartArea.className = 'graph-chart-area';
    chartArea.style.cssText = 'flex:1;min-width:0;';
    const canvas = document.createElement('canvas');
    chartArea.appendChild(canvas);
    cellWrap.appendChild(chartArea);
    body.appendChild(cellWrap);

    fo.appendChild(body);
    g.appendChild(fo);

    // Create chart using the shared factory from graph.js
    const chart = createCellChart(canvas);
    applyChartColors(chart);

    // Cell state object matching graphState[tabId].cells[i] shape
    const cell = {
        cellEl:        body,
        chart,
        channels:      [],
        viewWindowSec: 60,
        viewEnd:       null,
    };

    // Register in graphState so updateAllGraphs() and updateActiveGraphChannels() pick it up
    graphState[GRAPH_TAB_ID] = {
        rows: 1, cols: 1, gridEl: null,
        cells: [cell],
        sizeBtn: null, showDesc: false, _dismissHandler: null,
    };

    // Pre-populate configured channels
    for (const line of (obj.lines || [])) {
        addChannelToCell(GRAPH_TAB_ID, 0, line.refDes);
        // Apply saved color if present
        if (line.color) {
            const ch = cell.channels.find(c => c.refDes === line.refDes);
            const ds = cell.chart.data.datasets.find(d => d.label === line.refDes);
            if (ch) ch.color = line.color;
            if (ds) { ds.borderColor = line.color; ds.backgroundColor = line.color + '22'; }
        }
        if (line.yAxis && line.yAxis !== 1) {
            const ch = cell.channels.find(c => c.refDes === line.refDes);
            const ds = cell.chart.data.datasets.find(d => d.label === line.refDes);
            if (ch) ch.yAxisId = line.yAxis;
            if (ds) ds.yAxisID = 'y' + line.yAxis;
            syncYAxisVisibility(cell);
        }
        if (line.hidden) {
            const ch = cell.channels.find(c => c.refDes === line.refDes);
            const ds = cell.chart.data.datasets.find(d => d.label === line.refDes);
            if (ch) ch.hidden = true;
            if (ds) ds.hidden = true;
        }
    }
    if (obj.lines?.length) cell.chart.update('none');

    // Attach scroll-zoom and proximity tooltip
    attachScrollZoom(canvas, cell);
    attachProximityTooltip(canvas, cell);

    // Right-click opens the object sidebar showing the graph's channels
    g.style.cursor = 'context-menu';
    g.addEventListener('contextmenu', (e) => {
        e.preventDefault();
        e.stopPropagation();
        openObjectSidebarForGraph(obj);
    });

    setTimeout(() => chart?.resize(), 0);
    return g;
}

function renderPidConn(tab, conn) {
    const from = tab.pid.objects.find(o => o.id === conn.fromId);
    const to   = tab.pid.objects.find(o => o.id === conn.toId);
    if (!from || !to) return;

    const p1 = portPos(from, conn.fromPort);
    const p2 = portPos(to,   conn.toPort);
    const { d } = orthRouteAvoiding(
        p1, conn.fromPort, p2, conn.toPort,
        tab.pid.objects, conn.fromId, conn.toId
    );

    let el = tab.pid.gConns.querySelector('[data-conn-id="' + conn.id + '"]');
    if (!el) {
        el = svgN('path', { class: 'pid-conn-path', 'data-conn-id': conn.id });
        tab.pid.gConns.appendChild(el);
    }
    el.setAttribute('d', d);
}

// =============================================================================
// Live data binding
// =============================================================================

function rebindPidLiveData(tab) {
    tab.channelUpdaters = {};
    for (const obj of tab.pid.objects) {
        if (obj.type !== 'sensor' || !obj.refDes) continue;
        const id = obj.id;
        let staleTimer = null;
        tab.channelUpdaters[obj.refDes] = value => {
            const el = tab.pid.svgEl.querySelector('[data-pid-id="' + id + '"] .pid-sensor-value');
            if (!el) return;
            el.textContent = typeof value === 'number'
                ? (Number.isInteger(value) ? String(value) : value.toFixed(2))
                : String(value);
            el.classList.remove('stale');
            clearTimeout(staleTimer);
            staleTimer = setTimeout(() => el.classList.add('stale'), CONFIG.channelStaleMs);
        };
    }
    rebuildActivePidChannels();
}

// =============================================================================
// Object sidebar helpers
// =============================================================================

// Open the object sidebar pre-populated with a graph object's channels.
// Used when right-clicking a graph object in view mode.
function openObjectSidebarForGraph(obj) {
    const sidebarEl = document.getElementById('object-sidebar');
    if (!sidebarEl) return;

    const _state = graphState[SIDEBAR_TAB_ID];
    if (_state?.cells[0]?.chart) applyChartColors(_state.cells[0].chart);

    const state = graphState[SIDEBAR_TAB_ID];
    if (!state) return;
    const cell = state.cells[0];

    // Clear any existing channels
    for (const rd of [...cell.channels.map(c => c.refDes)]) {
        removeChannelFromCell(SIDEBAR_TAB_ID, SIDEBAR_CELL_IDX, rd);
    }

    // Header shows the graph's name
    sidebarEl._refdesEl.textContent = obj.name || 'Graph';
    sidebarEl._descEl.textContent   = '';

    // Add the graph's configured channels
    for (const line of (obj.lines || [])) {
        addChannelToCell(SIDEBAR_TAB_ID, SIDEBAR_CELL_IDX, line.refDes);
    }

    sidebarEl.style.display = '';
    setTimeout(() => cell.chart?.resize(), 0);
}

// =============================================================================
// Canvas pan
// =============================================================================

function startPidPan(tab, e) {
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
