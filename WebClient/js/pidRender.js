// =============================================================================
// pidRender.js — P&ID render/serialisation helpers shared by the in-app viewer
// (pid.js) and the standalone editor (editor.js).
// =============================================================================
//
// Loaded on BOTH index.html and editor.html, before pid.js / editor.js.
//
// Only functions that render/serialise a P&ID identically in both views live here.
//
// PID (the geometry/tuning constants) is shared here too. It MUST be identical
// between viewer and editor: it drives portPos() and the router, so if the two
// pages disagree the same YAML draws different pipe routes on each. (This used to
// diverge — VALVE_PORT_OFF was 0 in the viewer but 40 in the editor — which made
// valve pipes render differently on the main page than in the editor.)
//
// Tuning knobs to adjust pipe appearance:
//   STUB           — length of the forced straight segment leaving each port
//   VALVE_PORT_OFF — how far a valve's pipe attach point sits from its centre
//   OBS_MARGIN     — clearance padding around objects for routing/collision
//
// NOT shared (they genuinely diverge between viewer and editor):
//   - pidToYaml        — editor emits daqControl `label` and serialises valves in
//                        the generic branch; pid.js's copy was dead and removed
//   - the group builders / renderPidObj / renderPidConn — edit mode adds ports,
//     drag handles and other interactivity, so those stay per-file by design
// =============================================================================

const PID = {
    GRID:        20,    // px per grid cell
    SENSOR_W:    120,   // sensor box width  (6 cells)
    SENSOR_H:    50,    // sensor box height (2.5 cells)
    NODE_R:      5,     // junction dot radius
    PORT_R:      6,     // port hit-circle radius
    PORT_OFF:    20,    // port offset from node centre
    STUB:        20,    // orthogonal routing stub length (forced straight exit)
    CANVAS_W:    2400,
    CANVAS_H:    1800,
    CORNER_R:    8,     // rounded corner radius px
    OBS_MARGIN:  4,     // obstacle clearance margin px
    VALVE_R:     18,    // valve circle radius px
    VALVE_PORT_OFF: 20, // valve pipe attach offset from centre (unified viewer/editor)
    DAQCTRL_W:   200,   // daqControl widget default width px (10 cells)
    DAQCTRL_H:   60,    // daqControl widget default height px (3 cells)
};

// ── Themed color-picker popup (shared by graph.js and editor.js) ────────────
// Default swatch palette for callers that don't have their own (e.g. the P&ID
// editor's pipe color picker). Matches the style used for graph line colors.
const PID_COLOR_PALETTE = [
    '#58a6ff', '#3fb950', '#f78166', '#e3b341',
    '#bc8cff', '#56d364', '#79c0ff', '#ffa657',
    '#ff7b72', '#d2a8ff'
];

// openColorPalette opens the themed swatch-grid + custom-color popup used
// throughout the app (graph line colors, P&ID pipe colors, ...). `palette` is
// the array of preset swatch colors to show; `onSelect(color)` fires with a
// '#rrggbb' string when the user picks a preset or a custom color.
function openColorPalette(anchorEl, currentColor, palette, onSelect) {
    const existing = document.querySelector('.color-palette-popup');
    if (existing) existing.remove();

    const popup = document.createElement('div');
    popup.className = 'color-palette-popup';

    for (const c of (palette && palette.length ? palette : PID_COLOR_PALETTE)) {
        const opt = document.createElement('div');
        opt.className = 'color-palette-option' + (c === currentColor ? ' active' : '');
        opt.style.background = c;
        opt.title = c;
        opt.addEventListener('mousedown', (e) => {
            e.preventDefault();
            popup.remove();
            onSelect(c);
        });
        popup.appendChild(opt);
    }

    // Custom color option
    const customBtn  = document.createElement('div');
    customBtn.className = 'color-palette-custom';
    customBtn.title  = 'Custom color';
    customBtn.textContent = '✎';
    const hiddenInput = document.createElement('input');
    hiddenInput.type  = 'color';
    hiddenInput.value = currentColor || '#000000';
    hiddenInput.style.cssText = 'position:absolute;width:0;height:0;opacity:0;pointer-events:none';
    hiddenInput.addEventListener('input', () => { popup.remove(); onSelect(hiddenInput.value); });
    customBtn.appendChild(hiddenInput);
    customBtn.addEventListener('mousedown', (e) => { e.preventDefault(); hiddenInput.click(); });
    popup.appendChild(customBtn);

    document.body.appendChild(popup);
    const rect = anchorEl.getBoundingClientRect();
    popup.style.top  = (rect.bottom + 4) + 'px';
    popup.style.left = rect.left + 'px';

    const dismiss = (e) => {
        if (!popup.contains(e.target) && e.target !== anchorEl) {
            popup.remove();
            document.removeEventListener('mousedown', dismiss);
        }
    };
    setTimeout(() => document.addEventListener('mousedown', dismiss), 0);
}

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

// ── Sensor object binding resolution (shared by viewer + editor) ────────────
// Sensor P&ID objects can bind either to a control (preferred — "objects
// reference controls, not channels") or, for backward compatibility, directly
// to a channel refDes (the legacy form). Both pages need the exact same
// resolution logic so a layout renders/edits identically either place.

// pidIsReadableChannel reports whether a channel is safe to display as a
// sensor value (i.e. not a command channel). Duplicates utils.js's isCmd
// check locally because pidRender.js is also loaded on editor.html, which has
// no utils.js.
function pidIsReadableChannel(ch) {
    return ch.role !== 'cmd-bool' && ch.role !== 'cmd-pct' && ch.role !== 'cmd-float';
}

// resolveSensorBinding resolves a sensor object to the { ctrl, ch } pair that
// supplies its live value, or null if nothing resolves. `controls` is the
// config-controls array (configControls in the viewer, edConfigControls in
// the editor — the two pages don't share globals).
//
//   - obj.controlRefDes (preferred): binds to a control. The channel shown is
//     obj.channelRefDes if it names one of that control's readable channels,
//     else the control's first readable (non-command) channel — this is what
//     keeps "all channels under the control are implicitly included" true
//     without a multi-value widget.
//   - obj.refDes (legacy): binds directly to a channel; the owning control is
//     found by scanning `controls`. Existing layouts saved before controls
//     existed use this form and must keep resolving exactly as before.
function resolveSensorBinding(obj, controls) {
    if (obj.controlRefDes) {
        const ctrl = controls.find(c => c.refDes === obj.controlRefDes);
        if (!ctrl) return null;
        const readable = (ctrl.channels ?? []).filter(pidIsReadableChannel);
        const ch = (obj.channelRefDes && readable.find(c => c.refDes === obj.channelRefDes)) || readable[0];
        return ch ? { ctrl, ch } : null;
    }
    if (obj.refDes) {
        for (const ctrl of controls) {
            const ch = ctrl.channels?.find(c => c.refDes === obj.refDes);
            if (ch) return { ctrl, ch };
        }
    }
    return null;
}

// ── Stale-timer helper (shared by every live-data updater on both pages) ────
// Consolidates the repeated `clearTimeout(timer); timer = setTimeout(markStale,
// ms)` idiom used by the card/pid/dataview builders. Call bump() every time
// fresh data arrives for the thing being tracked (cancels any pending stale
// callback and reschedules it after `ms`); call cancel() to stop tracking
// without scheduling a new callback (e.g. when the UI element being tracked is
// torn down/replaced). `ms` is passed explicitly (rather than read from
// CONFIG) since the editor page has no CONFIG global.
function makeStaleTimer(ms, onStale) {
    let timer = null;
    return {
        bump()   { clearTimeout(timer); timer = setTimeout(onStale, ms); },
        cancel() { clearTimeout(timer); timer = null; },
    };
}

// ── Port positions (reads shared PID, incl. PID.VALVE_PORT_OFF) ──────────────

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

// ── Valve symbol geometry (reads shared PID.VALVE_R) ─────────────────────────

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
