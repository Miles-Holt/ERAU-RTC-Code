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
    VALVE_R:     18,    // valve circle radius px
    VALVE_PORT_OFF: 0,  // valve port offset from centre px
    DAQCTRL_W:   200,   // daqControl widget default width px (10 cells)
    DAQCTRL_H:   60,    // daqControl widget default height px (3 cells)
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
            y +=                              '    gridX: '  + o.gridX           + '\n';
            y +=                              '    gridY: '  + o.gridY           + '\n';
            y +=                              '    gridW: '  + (o.gridW  || 5)   + '\n';
            y +=                              '    gridH: '  + (o.gridH  || 8)   + '\n';
            if (o.rotation)              y += '    rotation: ' + o.rotation      + '\n';
            if (o.cornerR !== undefined) y += '    cornerR: '  + o.cornerR       + '\n';
            if (o.label)                 y += '    label: '    + q(o.label)      + '\n';
            if (o.showLabel === false)   y += '    showLabel: false\n';
            if (o.labelOffsetX)          y += '    labelOffsetX: ' + o.labelOffsetX + '\n';
            if (o.labelOffsetY)          y += '    labelOffsetY: ' + o.labelOffsetY + '\n';
        } else if (o.type === 'valve') {
            if (o.controlRefDes)        y += '    controlRefDes: ' + q(o.controlRefDes) + '\n';
            if (o.showRefDes === false)  y += '    showRefDes: false\n';
            if (o.rotation)             y += '    rotation: ' + o.rotation + '\n';
            y +=                             '    gridX: ' + o.gridX + '\n';
            y +=                             '    gridY: ' + o.gridY + '\n';
            if (o.labelOffsetX)         y += '    labelOffsetX: ' + o.labelOffsetX + '\n';
            if (o.labelOffsetY)         y += '    labelOffsetY: ' + o.labelOffsetY + '\n';
        } else if (o.type === 'daqControl') {
            if (o.daqRefDes)           y += '    daqRefDes: ' + q(o.daqRefDes) + '\n';
            y +=                           '    gridX: ' + o.gridX + '\n';
            y +=                           '    gridY: ' + o.gridY + '\n';
            if (o.gridW && o.gridW !== 10) y += '    gridW: ' + o.gridW + '\n';
            if (o.gridH && o.gridH !== 3)  y += '    gridH: ' + o.gridH + '\n';
        } else {
            if (o.refDes)              y += '    refDes: ' + q(o.refDes) + '\n';
            if (o.units)               y += '    units: '  + q(o.units)  + '\n';
            if (o.showRefDes === false) y += '    showRefDes: false\n';
            if (o.showUnits  === false) y += '    showUnits: false\n';
            if (o.showName   === true)  y += '    showName: true\n';
            if (o.rotation)            y += '    rotation: '    + o.rotation      + '\n';
            y +=                           '    gridX: '   + o.gridX    + '\n';
            y +=                           '    gridY: '   + o.gridY    + '\n';
            if (o.labelOffsetX)        y += '    labelOffsetX: ' + o.labelOffsetX + '\n';
            if (o.labelOffsetY)        y += '    labelOffsetY: ' + o.labelOffsetY + '\n';
        }
    }
    y += 'connections:\n';
    for (const c of layout.connections) {
        y += '  - id: '       + q(c.id)       + '\n';
        y += '    fromId: '   + q(c.fromId)   + '\n';
        y += '    fromPort: ' + c.fromPort     + '\n';
        y += '    toId: '     + q(c.toId)     + '\n';
        y += '    toPort: '   + c.toPort       + '\n';
        if (c.fluid)          y += '    fluid: '    + c.fluid        + '\n';
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
    if (obj.type === 'valve') {
        const off = PID.VALVE_PORT_OFF;
        if (port === 'top')    return { x,        y: y - off };
        if (port === 'right')  return { x: x+off, y };
        if (port === 'bottom') return { x,        y: y + off };
        if (port === 'left')   return { x: x-off, y };
    }
    if (obj.type === 'daqControl') {
        const w = (obj.gridW || 10) * PID.GRID;
        const h = (obj.gridH || 3)  * PID.GRID;
        if (port === 'top')    return { x: x + w / 2, y };
        if (port === 'right')  return { x: x + w,     y: y + h / 2 };
        if (port === 'bottom') return { x: x + w / 2, y: y + h };
        if (port === 'left')   return { x,             y: y + h / 2 };
    }
    return { x, y };
}

// ── Obstacle-aware orthogonal router ────────────────────────────────────────

function pidObstacleRects(objects, excludeIds) {
    const M = PID.OBS_MARGIN;
    return objects
        .filter(o => !excludeIds.has(o.id) && (o.type === 'sensor' || o.type === 'graph' || o.type === 'valve' || o.type === 'tank' || o.type === 'daqControl'))
        .map(o => {
            if (o.type === 'tank') {
                const rot = o.rotation || 0;
                const W = (o.gridW || 5) * PID.GRID;
                const H = (o.gridH || 8) * PID.GRID;
                // For 90/270 rotations swap W and H for bounding box
                const bW = (rot === 90 || rot === 270) ? H : W;
                const bH = (rot === 90 || rot === 270) ? W : H;
                const cx = o.gridX * PID.GRID + W / 2;
                const cy = o.gridY * PID.GRID + H / 2;
                return { x1: cx - bW/2 - M, y1: cy - bH/2 - M, x2: cx + bW/2 + M, y2: cy + bH/2 + M };
            }
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
            if (o.type === 'daqControl') {
                return {
                    x1: o.gridX * PID.GRID - M,
                    y1: o.gridY * PID.GRID - M,
                    x2: o.gridX * PID.GRID + (o.gridW || 10) * PID.GRID + M,
                    y2: o.gridY * PID.GRID + (o.gridH || 3) * PID.GRID + M,
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
        closeValveDropdown(false);
        e.stopPropagation();
        startPidPan(tab, e);
    });

    // ── Suppress browser context menu on canvas ──
    svg.addEventListener('contextmenu', e => {
        e.preventDefault();
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
    const g = obj.type === 'graph'      ? makeGraphGroup(obj, tab)
            : obj.type === 'sensor'     ? makeSensorGroup(obj)
            : obj.type === 'valve'      ? makeValveGroup(obj)
            : obj.type === 'tank'       ? makeTankGroup(obj)
            : obj.type === 'daqControl' ? makeDaqControlGroup(obj)
            : makeNodeGroup(obj);
    tab.pid.gObjs.appendChild(g);
}

function makeSensorGroup(obj) {
    const showRefDes = obj.showRefDes !== false;
    const showUnits  = obj.showUnits  !== false;
    const showName   = obj.showName   === true;
    const rot        = obj.rotation   || 0;

    const xf = 'translate(' + (obj.gridX * PID.GRID) + ',' + (obj.gridY * PID.GRID) + ')' +
        (rot ? ' rotate(' + rot + ',' + (PID.SENSOR_W / 2) + ',' + (PID.SENSOR_H / 2) + ')' : '');
    const g = svgN('g', {
        class: 'pid-obj pid-sensor',
        'data-pid-id': obj.id,
        transform: xf,
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
    const lx = obj.labelOffsetX || 0;
    const ly = obj.labelOffsetY || 0;
    for (let i = 0; i < items.length; i++) {
        const item = items[i];
        const y = Math.round(step * (i + 1));
        let cls;
        if      (item.type === 'name')   cls = 'pid-sensor-name';
        else if (item.type === 'refdes') cls = 'pid-sensor-label';
        else if (item.type === 'value')  cls = 'pid-sensor-value stale';
        else                             cls = 'pid-sensor-units';
        if (item.type === 'refdes') {
            // Wrap refDes label in its own group so it can be moved independently
            const lblG = svgN('g', {
                'data-label-id': obj.id,
                transform: 'translate(' + (PID.SENSOR_W / 2 + lx) + ',' + (y + ly) + ')',
            });
            const el = svgN('text', { class: cls, x: 0, y: 0 });
            el.textContent = item.text;
            lblG.appendChild(el);
            g.appendChild(lblG);
        } else {
            const el = svgN('text', { class: cls, x: PID.SENSOR_W / 2, y });
            el.textContent = item.text;
            g.appendChild(el);
        }
    }

    return g;
}

function makeDaqControlGroup(obj) {
    const W = (obj.gridW || 10) * PID.GRID;
    const H = (obj.gridH || 3)  * PID.GRID;
    const daqRef = obj.daqRefDes || '';
    const cfg = daqControlConfig[daqRef];

    const g = svgN('g', {
        class: 'pid-obj pid-daqctrl',
        'data-pid-id': obj.id,
        transform: 'translate(' + (obj.gridX * PID.GRID) + ',' + (obj.gridY * PID.GRID) + ')',
    });

    // Background rect
    g.appendChild(svgN('rect', {
        class: 'pid-daqctrl-bg', x: 0, y: 0, width: W, height: H, rx: 4,
    }));

    // Top row: DAQ refDes label (left) + connection status (right)
    const labelEl = svgN('text', { class: 'pid-daqctrl-label', x: 8, y: 18 });
    labelEl.textContent = daqRef || 'DAQ???';
    g.appendChild(labelEl);

    const connEl = svgN('text', { class: 'pid-daqctrl-conn', x: W - 8, y: 18 });
    connEl.textContent = '---';
    g.appendChild(connEl);

    // Bottom row: state label (left) + dropdown (right via foreignObject)
    const stateEl = svgN('text', { class: 'pid-daqctrl-state', x: 8, y: H - 12 });
    stateEl.textContent = 'State: ---';
    g.appendChild(stateEl);

    // Dropdown in foreignObject
    const foW = Math.min(110, W - 120);
    if (foW > 40) {
        const fo = svgN('foreignObject', { x: W - foW - 8, y: H - 30, width: foW, height: 24 });
        const sel = document.createElement('select');
        sel.className = 'pid-daqctrl-select';
        sel.setAttribute('data-daqctrl-select', '');
        sel.style.width = '100%';

        // Populate with initial placeholder
        const placeholder = document.createElement('option');
        placeholder.value = '';
        placeholder.textContent = '-- transition --';
        placeholder.disabled = true;
        placeholder.selected = true;
        sel.appendChild(placeholder);

        sel.addEventListener('change', () => {
            const target = sel.value;
            if (!target) return;
            // Send command to request state transition
            // Use SYS-TARGET-STATE or a per-DAQ channel
            sendCommand('SYS-TARGET-STATE', target);
            sel.selectedIndex = 0;
        });

        fo.appendChild(sel);
        g.appendChild(fo);
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

function makeTankGroup(obj) {
    const W   = (obj.gridW  || 5) * PID.GRID;
    const H   = (obj.gridH  || 8) * PID.GRID;
    const rx  = obj.cornerR !== undefined ? obj.cornerR : PID.CORNER_R;
    const rot = obj.rotation || 0;

    const g = svgN('g', {
        class: 'pid-obj pid-tank',
        'data-pid-id': obj.id,
        transform: 'translate(' + (obj.gridX * PID.GRID) + ',' + (obj.gridY * PID.GRID) + ')',
    });

    const rect = svgN('rect', {
        x: 0, y: 0, width: W, height: H,
        rx, ry: rx,
        class: 'pid-tank-rect',
    });
    if (rot) rect.setAttribute('transform', 'rotate(' + rot + ',' + (W / 2) + ',' + (H / 2) + ')');
    g.appendChild(rect);

    if (obj.showLabel !== false && obj.label) {
        const lx = obj.labelOffsetX || 0;
        const ly = obj.labelOffsetY || 0;
        const lbl = svgN('text', {
            class: 'pid-tank-label',
            x: W / 2 + lx,
            y: H / 2 + ly,
        });
        lbl.textContent = obj.label;
        g.appendChild(lbl);
    }

    return g;
}

// =============================================================================
// Valve helpers
// =============================================================================

// Returns SVG line attributes for the IO-CMD center line.
// open (truthy) = horizontal (0°), closed (falsy) = vertical (90°).
function _valveLineAttrs(isOpen) {
    const L = PID.VALVE_R - 3;
    return isOpen
        ? { x1: -L, y1: 0,  x2: L, y2: 0  }
        : { x1: 0,  y1: -L, x2: 0, y2: L  };
}

// Returns SVG arc path for POS-FB feedback.
// pct 100 = open (pointer at 180°), pct 0 = closed (pointer at 90°).
function _valveArcPath(pct) {
    const R = PID.VALVE_R + 7;
    const endAngle = Math.PI - (Math.max(0, Math.min(100, pct)) / 100) * (Math.PI / 2);
    const startAngle = Math.PI;
    if (Math.abs(startAngle - endAngle) < 0.01) return '';
    const x1 = Math.cos(startAngle) * R, y1 = Math.sin(startAngle) * R;
    const x2 = Math.cos(endAngle)   * R, y2 = Math.sin(endAngle)   * R;
    return 'M ' + x1 + ' ' + y1 + ' A ' + R + ' ' + R + ' 0 0 1 ' + x2 + ' ' + y2;
}

// Returns {cx, cy} for the POS-FB pointer dot.
function _valvePtrPos(pct) {
    const R = PID.VALVE_R + 7;
    const angle = Math.PI - (Math.max(0, Math.min(100, pct)) / 100) * (Math.PI / 2);
    return { cx: Math.cos(angle) * R, cy: Math.sin(angle) * R };
}

// Determines command/feedback type from ctrl.subType string.
function _valveSubtypeInfo(ctrl) {
    if (!ctrl) return { hasCmd: false, cmdRole: null, hasFb: false, fbIsPct: false };
    const st = (ctrl.subType || '').toUpperCase();
    const hasCmd  = st.includes('IO-CMD') || st.includes('POS-CMD');
    const cmdRole = st.includes('POS-CMD') ? 'cmd-pct' : (hasCmd ? 'cmd-bool' : null);
    const hasFb   = st.includes('IO-FB') || st.includes('POS-FB');
    const fbIsPct = st.includes('POS-FB');
    return { hasCmd, cmdRole, hasFb, fbIsPct };
}

function makeValveGroup(obj) {
    const ctrl  = configControls.find(c => c.refDes === obj.controlRefDes);
    const info  = _valveSubtypeInfo(ctrl);
    const cmdCh = ctrl?.channels?.find(c => c.role === 'cmd-bool' || c.role === 'cmd-pct');
    const fbCh  = ctrl?.channels?.find(c => c.role === '' || c.role === 'sensor');
    const L     = PID.VALVE_R - 3;
    const showRefDes = obj.showRefDes !== false;

    const g = svgN('g', {
        class:         'pid-obj pid-valve',
        'data-pid-id': obj.id,
        transform:     'translate(' + (obj.gridX * PID.GRID) + ',' + (obj.gridY * PID.GRID) + ')',
        cursor:        'pointer',
    });

    // Invisible hit area (not rotated — valve is circular so rotation doesn't affect hit area)
    g.appendChild(svgN('circle', { r: PID.VALVE_R, fill: 'none', 'pointer-events': 'all' }));

    // Visual sub-group — rotated to orient the valve symbol
    const rot = obj.rotation || 0;
    const vis = svgN('g', rot ? { transform: 'rotate(' + rot + ')' } : {});
    // Background fill to block pipe line behind valve
    vis.appendChild(svgN('circle', { class: 'pid-valve-bg', r: PID.VALVE_R }));
    // Outer ring (starts stale until data arrives)
    vis.appendChild(svgN('circle', { class: 'pid-valve-ring stale', r: PID.VALVE_R }));

    if (!ctrl) {
        // Unconfigured: -45° dashed diagonal
        vis.appendChild(svgN('line', { class: 'pid-valve-uncfg', x1: -L, y1: L, x2: L, y2: -L }));
    } else {
        // POS-FB arc + pointer (drawn first so center content is on top)
        if (info.hasFb && info.fbIsPct) {
            vis.appendChild(svgN('path',   { class: 'pid-valve-arc', 'data-vfb-arc': '' }));
            vis.appendChild(svgN('circle', { class: 'pid-valve-ptr', r: 4, 'data-vfb-ptr': '' }));
        }

        // IO-CMD center line
        if (info.hasCmd && cmdCh?.role === 'cmd-bool') {
            const la = _valveLineAttrs(false); // default: closed
            vis.appendChild(svgN('line', {
                class: 'pid-valve-line', 'data-vcmd-line': '',
                x1: la.x1, y1: la.y1, x2: la.x2, y2: la.y2,
            }));
            // IO-FB: dots on line ends
            if (info.hasFb && !info.fbIsPct) {
                vis.appendChild(svgN('circle', { class: 'pid-valve-dot', r: 4, cx: 0, cy: -L, 'data-vfb-dot-a': '' }));
                vis.appendChild(svgN('circle', { class: 'pid-valve-dot', r: 4, cx: 0, cy:  L, 'data-vfb-dot-b': '' }));
            }
        }

        // POS-CMD center text
        if (info.hasCmd && cmdCh?.role === 'cmd-pct') {
            const t = svgN('text', { class: 'pid-valve-pct', 'data-vcmd-pct': '' });
            t.textContent = '--';
            vis.appendChild(t);
        }
    }
    g.appendChild(vis);

    // Label — NOT rotated; moveable independently via labelOffsetX/Y
    if (ctrl && showRefDes) {
        const lx = obj.labelOffsetX || 0;
        const ly = obj.labelOffsetY || 0;
        const lblG = svgN('g', {
            'data-label-id': obj.id,
            transform: 'translate(' + lx + ',' + (PID.VALVE_R + 12 + ly) + ')',
        });
        const lbl = svgN('text', { class: 'pid-valve-label', x: 0, y: 0 });
        lbl.textContent = obj.controlRefDes || '';
        lblG.appendChild(lbl);
        g.appendChild(lblG);
    }

    // Stop left-click from reaching the SVG-level pan handler
    g.addEventListener('pointerdown', e => { if (e.button === 0) e.stopPropagation(); });

    g.addEventListener('contextmenu', (e) => {
        e.preventDefault();
        e.stopPropagation();
        const refDes = fbCh?.refDes || cmdCh?.refDes;
        if (refDes) openObjectSidebar(refDes);
    });

    g.addEventListener('click', (e) => {
        e.stopPropagation();
        const rect = g.getBoundingClientRect();
        openValveDropdown(obj, rect.left + rect.width / 2, rect.top + rect.height / 2);
    });

    return g;
}

// SVG update helpers called from rebindPidLiveData
function _updateValveCmdSvg(svgEl, id, role, value) {
    const g = svgEl.querySelector('[data-pid-id="' + id + '"]');
    if (!g) return;
    if (role === 'cmd-bool') {
        const line = g.querySelector('[data-vcmd-line]');
        if (line) {
            const la = _valveLineAttrs(value);
            line.setAttribute('x1', la.x1); line.setAttribute('y1', la.y1);
            line.setAttribute('x2', la.x2); line.setAttribute('y2', la.y2);
        }
        // IO-FB dots follow the line angle
        const dotA = g.querySelector('[data-vfb-dot-a]');
        const dotB = g.querySelector('[data-vfb-dot-b]');
        if (dotA && dotB) {
            const L = PID.VALVE_R - 3;
            if (value) {
                dotA.setAttribute('cx', -L); dotA.setAttribute('cy', 0);
                dotB.setAttribute('cx',  L); dotB.setAttribute('cy', 0);
            } else {
                dotA.setAttribute('cx', 0); dotA.setAttribute('cy', -L);
                dotB.setAttribute('cx', 0); dotB.setAttribute('cy',  L);
            }
        }
    } else if (role === 'cmd-pct') {
        const txt = g.querySelector('[data-vcmd-pct]');
        if (txt) txt.textContent = (typeof value === 'number' ? Math.round(value) : '--') + '%';
    }
}

function _updateValveFbSvg(svgEl, id, subType, value) {
    const g = svgEl.querySelector('[data-pid-id="' + id + '"]');
    if (!g) return;
    const st = (subType || '').toUpperCase();
    if (st.includes('POS-FB')) {
        const pct = typeof value === 'number' ? value : 0;
        const arc = g.querySelector('[data-vfb-arc]');
        const ptr = g.querySelector('[data-vfb-ptr]');
        if (arc) arc.setAttribute('d', _valveArcPath(pct));
        if (ptr) {
            const pos = _valvePtrPos(pct);
            ptr.setAttribute('cx', pos.cx);
            ptr.setAttribute('cy', pos.cy);
        }
    } else if (st.includes('IO-FB')) {
        const line = g.querySelector('[data-vcmd-line]');
        if (line) {
            const la = _valveLineAttrs(value);
            line.setAttribute('x1', la.x1); line.setAttribute('y1', la.y1);
            line.setAttribute('x2', la.x2); line.setAttribute('y2', la.y2);
        }
        const dotA = g.querySelector('[data-vfb-dot-a]');
        const dotB = g.querySelector('[data-vfb-dot-b]');
        if (dotA && dotB) {
            const L = PID.VALVE_R - 3;
            if (value) {
                dotA.setAttribute('cx', -L); dotA.setAttribute('cy', 0);
                dotB.setAttribute('cx',  L); dotB.setAttribute('cy', 0);
            } else {
                dotA.setAttribute('cx', 0); dotA.setAttribute('cy', -L);
                dotB.setAttribute('cx', 0); dotB.setAttribute('cy',  L);
            }
        }
    }
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

        const searchWrap  = document.createElement('div');
        searchWrap.className = 'graph-search-wrap';
        const searchInput = document.createElement('input');
        searchInput.type        = 'text';
        searchInput.placeholder = 'Add channel (regex)...';
        searchInput.className   = 'graph-search';
        const dropdown = document.createElement('div');
        dropdown.className   = 'graph-dropdown';
        dropdown.style.display = 'none';
        document.body.appendChild(dropdown);
        searchWrap.appendChild(searchInput);
        panel.appendChild(searchWrap);

        const handlePidSearch = debounce(() => {
            const q = searchInput.value.trim();
            if (!q) { dropdown.style.display = 'none'; return; }
            let re;
            try { re = new RegExp(q, 'i'); } catch { dropdown.style.display = 'none'; return; }
            const selected = new Set(cell.channels.map(c => c.refDes));
            const matches = [];
            for (const ctrl of configControls) {
                for (const ch of (ctrl.channels ?? [])) {
                    if (!selected.has(ch.refDes) && (re.test(ch.refDes) || re.test(ctrl.description || ''))) {
                        matches.push({ refDes: ch.refDes, desc: ctrl.description || '' });
                    }
                }
            }
            const trimmed = matches.slice(0, 20);
            dropdown.innerHTML = '';
            if (!trimmed.length) { dropdown.style.display = 'none'; return; }
            for (const { refDes, desc } of trimmed) {
                const item = document.createElement('div');
                item.className = 'graph-dropdown-item';
                const rdSpan = document.createElement('span');
                rdSpan.className = 'graph-dropdown-refdes';
                rdSpan.textContent = refDes;
                item.appendChild(rdSpan);
                if (desc) {
                    const dsSpan = document.createElement('span');
                    dsSpan.className = 'graph-dropdown-desc';
                    dsSpan.textContent = desc;
                    item.appendChild(dsSpan);
                }
                item.addEventListener('mousedown', (e) => {
                    e.preventDefault();
                    addChannelToCell(GRAPH_TAB_ID, 0, refDes);
                    searchInput.focus();
                    handlePidSearch();
                });
                dropdown.appendChild(item);
            }
            const r = searchInput.getBoundingClientRect();
            dropdown.style.left    = r.left + 'px';
            dropdown.style.width   = r.width + 'px';
            dropdown.style.top     = '-9999px';
            dropdown.style.bottom  = '';
            dropdown.style.display = '';
            const h = dropdown.offsetHeight;
            dropdown.style.top = Math.max(4, r.top - h) + 'px';
        }, 150);

        searchInput.addEventListener('input', handlePidSearch);
        searchInput.addEventListener('blur', () => setTimeout(() => { dropdown.style.display = 'none'; }, 150));

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

    // Apply legend position if configured
    if (obj.legendPosition && obj.legendPosition !== 'none') {
        chart.options.plugins.legend.display  = true;
        chart.options.plugins.legend.position = obj.legendPosition;
    }

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

    let wrap = tab.pid.gConns.querySelector('[data-conn-id="' + conn.id + '"]');
    if (!wrap) {
        wrap = svgN('g', { 'data-conn-id': conn.id });
        wrap.append(svgN('path', { class: 'pid-conn-hit' }), svgN('path', { class: 'pid-conn-path' }));
        tab.pid.gConns.appendChild(wrap);
    }
    wrap.children[0].setAttribute('d', d);
    wrap.children[1].setAttribute('d', d);
    wrap.className.baseVal = wrap.className.baseVal.replace(/\bpid-conn-fluid-\S+/g, '').trim();
    if (conn.fluid) wrap.classList.add('pid-conn-fluid-' + conn.fluid);
}

// =============================================================================
// DAQ Control helpers
// =============================================================================

function _updateDaqControlState(svgEl, id, daqRef, stateValue) {
    const cfg = daqControlConfig[daqRef];
    if (!cfg) return;
    const stateNames = Object.keys(cfg.states);
    const stateIdx = typeof stateValue === 'number' ? Math.round(stateValue) : parseInt(stateValue, 10);
    const stateName = stateNames[stateIdx] || ('state_' + stateValue);

    // Update state text
    const stateEl = svgEl.querySelector('[data-pid-id="' + id + '"] .pid-daqctrl-state');
    if (stateEl) stateEl.textContent = 'State: ' + stateName;

    // Update dropdown with valid operator transitions from current state
    const sel = svgEl.querySelector('[data-pid-id="' + id + '"] [data-daqctrl-select]');
    if (!sel) return;

    const stDef = cfg.states[stateName];
    const opTransitions = stDef
        ? stDef.transitions.filter(t => t.on === 'operator_request' || t.on === 'operator_abort')
        : [];

    // Rebuild dropdown options
    sel.innerHTML = '';
    const placeholder = document.createElement('option');
    placeholder.value = '';
    placeholder.textContent = opTransitions.length ? '-- transition --' : '(no transitions)';
    placeholder.disabled = true;
    placeholder.selected = true;
    sel.appendChild(placeholder);

    for (const t of opTransitions) {
        const opt = document.createElement('option');
        const targetIdx = stateNames.indexOf(t.target);
        opt.value = targetIdx >= 0 ? String(targetIdx) : t.target;
        opt.textContent = t.target;
        sel.appendChild(opt);
    }
    sel.disabled = opTransitions.length === 0;
}

// =============================================================================
// Live data binding
// =============================================================================

function rebindPidLiveData(tab) {
    tab.channelUpdaters = {};
    for (const obj of tab.pid.objects) {
        if (obj.type === 'sensor' && obj.refDes) {
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
        if (obj.type === 'valve' && obj.controlRefDes) {
            const ctrl = configControls.find(c => c.refDes === obj.controlRefDes);
            if (!ctrl) continue;
            const cmdCh = ctrl.channels?.find(c => c.role === 'cmd-bool' || c.role === 'cmd-pct');
            const fbCh  = ctrl.channels?.find(c => c.role === '' || c.role === 'sensor');
            const id = obj.id;
            let fbStaleTimer = null;

            if (cmdCh) {
                tab.channelUpdaters[cmdCh.refDes] = value => {
                    _updateValveCmdSvg(tab.pid.svgEl, id, cmdCh.role, value);
                    updateValveDropdownValue(id, cmdCh.refDes, value);
                };
            }
            if (fbCh) {
                tab.channelUpdaters[fbCh.refDes] = value => {
                    _updateValveFbSvg(tab.pid.svgEl, id, ctrl.subType, value);
                    updateValveDropdownValue(id, fbCh.refDes, value);
                    const bad = typeof value === 'number' &&
                        ((fbCh.validMin !== null && fbCh.validMin !== undefined && value < fbCh.validMin) ||
                         (fbCh.validMax !== null && fbCh.validMax !== undefined && value > fbCh.validMax));
                    const ring = tab.pid.svgEl.querySelector('[data-pid-id="' + id + '"] .pid-valve-ring');
                    if (ring) {
                        ring.classList.toggle('bad', bad);
                        ring.classList.remove('stale');
                    }
                    clearTimeout(fbStaleTimer);
                    fbStaleTimer = setTimeout(() => {
                        const r = tab.pid.svgEl.querySelector('[data-pid-id="' + id + '"] .pid-valve-ring');
                        if (r && !r.classList.contains('bad')) r.classList.add('stale');
                    }, CONFIG.channelStaleMs);
                };
            }
        }

        // ── daqControl: bind SYS-STATE + connection staleness ──────────────
        if (obj.type === 'daqControl' && obj.daqRefDes) {
            const id = obj.id;
            const daqRef = obj.daqRefDes;
            const cfg = daqControlConfig[daqRef];
            const stateNames = cfg ? Object.keys(cfg.states) : [];
            let connStaleTimer = null;

            // Listen for SYS-STATE to update current state + dropdown
            tab.channelUpdaters['SYS-STATE'] = value => {
                _updateDaqControlState(tab.pid.svgEl, id, daqRef, value);
            };

            // Track connection via CTR001-daqConnected
            tab.channelUpdaters['CTR001-daqConnected'] = value => {
                const connEl = tab.pid.svgEl.querySelector('[data-pid-id="' + id + '"] .pid-daqctrl-conn');
                if (!connEl) return;
                connEl.textContent = value >= 1 ? 'Connected' : 'Disconnected';
                clearTimeout(connStaleTimer);
                connStaleTimer = setTimeout(() => {
                    if (connEl) connEl.textContent = 'Stale';
                }, CONFIG.channelStaleMs);
            };
        }
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
// Canvas centering
// =============================================================================

function centerCanvasView(tab) {
    const wrap = tab.pid.canvasWrap;
    if (!wrap) return;
    
    // Get viewport dimensions
    const viewportW = wrap.clientWidth;
    const viewportH = wrap.clientHeight;
    
    // Canvas is 2400x1800
    const canvasW = PID.CANVAS_W;
    const canvasH = PID.CANVAS_H;
    
    // Center the canvas in the viewport
    wrap.scrollLeft = (canvasW - viewportW) / 2;
    wrap.scrollTop  = (canvasH - viewportH) / 2;
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
