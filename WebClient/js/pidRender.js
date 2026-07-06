// =============================================================================
// pidRender.js — P&ID render/serialisation helpers shared by the in-app viewer
// (pid.js) and the standalone editor (editor.js).
// =============================================================================
//
// Loaded on BOTH index.html and editor.html, before pid.js / editor.js.
//
// Only functions that are byte-identical between the viewer and editor live here.
// These read the page-local `PID` constant at call time, so the intentional
// per-page differences in PID (notably VALVE_PORT_OFF: 0 in the viewer vs 40 in
// the editor, which offsets valve ports so they are clickable in edit mode) are
// preserved — `PID` itself is deliberately NOT shared and stays defined in each
// of pid.js / editor.js.
//
// NOT shared (they genuinely diverge between viewer and editor):
//   - PID              — VALVE_PORT_OFF differs (0 vs 40)
//   - pidToYaml        — editor emits daqControl `label` and serialises valves in
//                        the generic branch; pid.js's copy was dead and removed
//   - the group builders / renderPidObj / renderPidConn — edit mode adds ports,
//     drag handles and other interactivity, so those stay per-file by design
// =============================================================================

// ── SVG namespace helper ─────────────────────────────────────────────────────

function svgN(tag, attrs) {
    const el = document.createElementNS('http://www.w3.org/2000/svg', tag);
    if (attrs) for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, String(v));
    return el;
}

// ── SVG coordinate from pointer event ────────────────────────────────────────

function pidSvgPt(svgEl, e) {
    const pt = svgEl.createSVGPoint();
    pt.x = e.clientX; pt.y = e.clientY;
    return pt.matrixTransform(svgEl.getScreenCTM().inverse());
}

// ── Port positions (reads page-local PID, incl. PID.VALVE_PORT_OFF) ──────────

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
    if (obj.type === 'tank') {
        const w = (obj.gridW || 5) * PID.GRID;
        const h = (obj.gridH || 8) * PID.GRID;
        if (port === 'top')    return { x: x + w / 2, y };
        if (port === 'right')  return { x: x + w,     y: y + h / 2 };
        if (port === 'bottom') return { x: x + w / 2, y: y + h };
        if (port === 'left')   return { x,             y: y + h / 2 };
    }
    return { x, y };
}

// ── YAML parser (handles our exact P&ID schema only) ─────────────────────────

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

// ── Valve symbol geometry (reads page-local PID.VALVE_R) ─────────────────────

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
