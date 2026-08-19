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
        // The pipe attaches to the BUBBLE, not to the old box: the bubble is the
        // instrument and the text block beside it is only its label, so a pipe
        // that met the label would be drawn to the wrong thing. Flipping `side`
        // moves the bubble across the object, and the ports go with it.
        const R = PID_OBJ.R;
        const w = pidSensorBoxW(obj);
        const bx = ((obj.side || 'right') === 'right') ? PID_OBJ.GX : w - PID_OBJ.GX;
        const by = PID_OBJ.CY;
        const off = R + 3;
        if (port === 'top')   return { x: x + bx,       y: y + by - off };
        if (port === 'right') return { x: x + bx + off, y: y + by };
        if (port === 'left')  return { x: x + bx - off, y: y + by };
        // 'bottom' is the only port name old layouts carry, so it stays the
        // fallback as well as a case: every existing pipe must still resolve.
        return { x: x + bx, y: y + by + off };
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
        // Despite the name, this renders as the same bubble-and-text row as a
        // sensor/valve (pidBuildObject type:'machine' — see
        // makeDaqControlGroup / pidGlyphMachine), not the fixed gridW x gridH
        // box this branch assumed. That box hasn't existed on screen since the
        // machine glyph redesign (flagged in editor.js's
        // makeDaqControlGroupEditor comment); ports still sized off it landed
        // on empty space near the object instead of the glyph. Mirror the
        // sensor branch instead.
        const R = PID_OBJ.R;
        const w = pidMachineBoxW(obj);
        const bx = ((obj.side || 'right') === 'right') ? PID_OBJ.GX : w - PID_OBJ.GX;
        const by = PID_OBJ.CY;
        const off = R + 3;
        if (port === 'top')    return { x: x + bx,       y: y + by - off };
        if (port === 'right')  return { x: x + bx + off, y: y + by };
        if (port === 'left')   return { x: x + bx - off, y: y + by };
        if (port === 'bottom') return { x: x + bx,       y: y + by + off };
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

// =============================================================================
// Front-panel object system — shared by viewer and editor
// =============================================================================
// Design: docs/design/sensor-object-options.html
//
// A sensor, a valve and a state machine are ONE construction: an optional
// glyph, a name line and a line of live text. Only the glyph interior differs.
//
// State is two independent axes, never one enum:
//   data condition — 'live' | 'nodata' | 'stale' | 'unbound'
//   alarm          — an alert raised AND unacknowledged
// There is deliberately no 'range' state (out of range is live + alarm) and no
// 'offline' state (node offline is nodata + alarm).
//
// Colours live in CSS (style.css), driven by the classes set here, because the
// app has a light theme that the dark-only design page does not.
// =============================================================================

const PID_OBJ = {
    R:      17,     // glyph radius — one number for all three families
    TK_R:   13,     // limit marks — inside the circle, at the bore-line end
    TK_DOT: 2.9,
    RIN:    13,     // continuous position — inside the circle
    H:      64,     // row height (the selection bloom needs the room)
    GAP:    16,     // glyph edge to text block
    CHAR_W: 10.3,   // monospace advance at the 17px value size
    // GX/CY are the glyph centre's offset from the row's near edge and its
    // vertical centre. Sensor and machine rows draw their bubble here AND sit
    // straight on the grid (no compensating translate, unlike valve — see
    // pid.js's makeValveGroup), so a pipe attaches at (gridX*GRID + GX,
    // gridY*GRID + CY) or the mirror image. Both must land on a PID.GRID
    // multiple or the router's defensive grid-snap (pidRouter.js) yanks the
    // first waypoint away from the real glyph and the pipe leaves at an
    // angle. R + 6 (≈23) and H / 2 (32) did the same job but weren't grid
    // multiples; GX/CY are the same geometry rounded onto the grid, kept as
    // named constants so pidBuildObject, portPos, and pidValveBox can't drift
    // apart from each other again.
    GX:     20,
    CY:     40,
};

// pidObjStateClass maps the two axes onto the classes CSS keys off.
function pidObjStateClass(dataCond, alarmed) {
    const known = ['live', 'nodata', 'stale', 'unbound'];
    const d = known.indexOf(dataCond) >= 0 ? dataCond : 'live';
    return 'st-' + d + (alarmed ? ' alarmed' : '');
}

// pidEnsureObjDefs installs the selection-glow filter/gradient once per page.
// The ids are document-global, so a second canvas reuses the first one's.
function pidEnsureObjDefs(svgEl) {
    if (!svgEl || document.getElementById('po-glow-blur')) return;
    const defs = svgN('defs', {});
    const f = svgN('filter', {
        id: 'po-glow-blur', x: '-100%', y: '-100%', width: '300%', height: '300%',
    });
    f.appendChild(svgN('feGaussianBlur', { stdDeviation: 3.4 }));
    defs.appendChild(f);
    const grad = svgN('radialGradient', { id: 'po-glow-grad' });
    const stops = [['42%', 0.34], ['72%', 0.12], ['100%', 0]];
    for (const st of stops) {
        grad.appendChild(svgN('stop', {
            offset: st[0], 'stop-color': '#ffffff', 'stop-opacity': st[1],
        }));
    }
    defs.appendChild(grad);
    svgEl.insertBefore(defs, svgEl.firstChild);
}

// The bubble interior is a per-object text field, so it has to hold whatever is
// typed. Empty draws an empty bubble, which makes "no text" a setting rather
// than a second widget.
function pidBubbleFontSize(n) {
    if (n <= 2) return 14;
    if (n === 3) return 11.5;
    if (n === 4) return 9.5;
    if (n === 5) return 8.5;
    return 7.5;
}

// pidDefaultBubbleText derives the value shown until an object sets its own:
// the leading letters of the refDes, so "CPT-01" gives "CPT".
function pidDefaultBubbleText(refDes) {
    const m = String(refDes || '').match(/^[A-Za-z]+/);
    return m ? m[0].slice(0, 4) : '';
}

// ── Geometry ────────────────────────────────────────────────────────────────

const PID_A_SHUT = Math.PI / 4;    // along the 45° shut line
const PID_A_OPEN = 0;              // along the horizontal open line
const PID_FB0    = Math.PI * 0.75; // gauge sweep start
const PID_FBSPAN = Math.PI * 1.5;  // gauge sweep extent

function pidPolar(r, a) { return { x: Math.cos(a) * r, y: Math.sin(a) * r }; }

function pidArcPath(r, a0, a1) {
    const p0 = pidPolar(r, a0), p1 = pidPolar(r, a1);
    const large = (a1 - a0) > Math.PI ? 1 : 0;
    return 'M ' + p0.x + ' ' + p0.y + ' A ' + r + ' ' + r + ' 0 ' + large + ' 1 ' + p1.x + ' ' + p1.y;
}

// ── Glyph interiors ─────────────────────────────────────────────────────────

function pidGlyphSensor(g, spec, refs) {
    const raw = (spec.bubbleText === undefined || spec.bubbleText === null)
        ? pidDefaultBubbleText(spec.name) : String(spec.bubbleText);
    if (raw === '') return;
    const size = pidBubbleFontSize(raw.length);
    const el = svgN('text', { class: 'po-bub', x: 0, y: size * 0.36, 'font-size': size });
    el.textContent = raw;
    refs.bubbleText = el;
    g.appendChild(el);
}

// Shut lies across the bore at 45°, open along it. `rot` turns the bore and the
// limit ticks together for a top-to-bottom run — they have to move together,
// since each tick sits at the end of the line it reports on. A percentage
// figure stays upright: a number read sideways is not a reading.
function pidGlyphValve(g, spec, refs) {
    const l = PID_OBJ.R - 5, d = l * 0.72;
    const rotG = svgN('g', spec.rot ? { transform: 'rotate(' + spec.rot + ')' } : {});
    refs.rotG = rotG;
    if (spec.valveKind === 'pct') {
        pidBuildFbRing(g, refs);                       // inside the circle
        const t = svgN('text', {
            class: 'po-value', x: 0, y: 4, 'font-size': 11, 'text-anchor': 'middle',
        });
        t.textContent = '--';
        refs.pctText = t;
        g.appendChild(t);                              // upright, unrotated
    } else {
        const line = svgN('line', { class: 'po-bore', x1: -d, y1: -d, x2: d, y2: d });
        refs.bore = line;
        refs.boreGeom = { l: l, d: d };
        rotG.appendChild(line);
        if (spec.hasLimits) pidBuildLimitTicks(rotG, refs);
    }
    g.appendChild(rotG);
}

function pidGlyphMachine(g, spec, refs) {
    const s = 5.5;
    refs.mark = svgN('path', {
        class: 'po-mark',
        d: 'M 0 ' + (-s) + ' L ' + s + ' 0 L 0 ' + s + ' L ' + (-s) + ' 0 Z',
    });
    g.appendChild(refs.mark);
}

// ── Valve feedback ──────────────────────────────────────────────────────────
// Two rules keep the valve's two readings separable now that both live inside
// the circle:
//   FILLED IS FACT, HOLLOW IS INTENT — a made switch fills, an unmade one
//     outlines, and a commanded position is always a hollow ring.
//   A SWITCH IS A PAIR — one dot at each END of the bore line it reports on, so
//     it reads as that line's endpoints rather than as a mark sitting nearby.
//     The command ring is single and rides between them, so shape alone tells
//     them apart; neither weight nor colour has to carry it.

function pidBuildLimitTicks(parent, refs) {
    refs.ticks = {};
    const at = [['shut', PID_A_SHUT], ['open', PID_A_OPEN]];
    for (const pair of at) {
        const key = pair[0];
        const dots = [];
        for (const ang of [pair[1], pair[1] + Math.PI]) {
            const p = pidPolar(PID_OBJ.TK_R, ang);
            const dot = svgN('circle', {
                class: 'po-tick po-tick-' + key, cx: p.x, cy: p.y, r: PID_OBJ.TK_DOT,
            });
            parent.appendChild(dot);
            dots.push(dot);
        }
        refs.ticks[key] = { dots: dots };
    }
}

function pidBuildFbRing(g, refs) {
    refs.fbTrack = svgN('path', {
        class: 'po-fb-track',
        d: pidArcPath(PID_OBJ.RIN, PID_FB0, PID_FB0 + PID_FBSPAN),
    });
    refs.fbLive = svgN('path', { class: 'po-fb-live', d: '' });
    refs.fbCmd  = svgN('circle', { class: 'po-fb-cmd', cx: 0, cy: 0, r: 2.8 });
    refs.fbCmd.style.display = 'none';
    g.appendChild(refs.fbTrack);
    g.appendChild(refs.fbLive);
    g.appendChild(refs.fbCmd);
}

// pidSetValveFeedback drives the ring from the two numbers it carries: the ARC
// is feedback (where the valve is) and the DOT is command (where it was told to
// be). When they agree the dot caps the arc; when they disagree the gap between
// them is the fault, which is the whole reason to draw a ring rather than print
// a number.
function pidSetValveFeedback(refs, fbPct, cmdPct) {
    if (!refs.fbLive) return;
    const fb = typeof fbPct === 'number' ? Math.max(0, Math.min(100, fbPct)) : null;
    refs.fbLive.setAttribute('d', (fb === null || fb <= 0.001) ? ''
        : pidArcPath(PID_OBJ.RIN, PID_FB0, PID_FB0 + PID_FBSPAN * (fb / 100)));
    if (typeof cmdPct === 'number') {
        const c = Math.max(0, Math.min(100, cmdPct));
        const p = pidPolar(PID_OBJ.RIN, PID_FB0 + PID_FBSPAN * (c / 100));
        refs.fbCmd.setAttribute('cx', p.x);
        refs.fbCmd.setAttribute('cy', p.y);
        refs.fbCmd.style.display = '';
    } else {
        refs.fbCmd.style.display = 'none';
    }
    if (refs.pctText) refs.pctText.textContent = (fb === null) ? '--' : String(Math.round(fb));
}

// pidSetValveBore turns the bore line: across the bore at 45° when shut, along
// it when open.
//
// `confirmed` decides whether the line may take its live colour at all. On a
// limit-switched valve green is earned by the SWITCH, not by the command: asking
// for open tells you only that you asked, so the body stays grey until the open
// pair lights. Mid-travel neither switch is made, which is grey for the same
// reason rather than as a special case. A valve with no feedback fitted passes
// confirmed=true, because there the command is the only fact there is.
function pidSetValveBore(refs, isOpen, confirmed) {
    if (!refs.bore) return;
    const l = refs.boreGeom.l, d = refs.boreGeom.d;
    const a = isOpen ? { x1: -l, y1: 0, x2: l, y2: 0 }
                     : { x1: -d, y1: -d, x2: d, y2: d };
    for (const k of Object.keys(a)) refs.bore.setAttribute(k, a[k]);
    refs.bore.classList.toggle('po-bore-open', !!isOpen);
    refs.bore.classList.toggle('po-confirmed', confirmed !== false);
}

// pidValvePositionConfirmed reports whether feedback agrees with the command,
// which is what earns the live colour. `made` is 'shut' | 'open' | null.
function pidValvePositionConfirmed(hasLimits, isOpen, made) {
    if (!hasLimits) return true;
    return made === (isOpen ? 'open' : 'shut');
}

// pidSetLimitSwitch lights one of the two ticks. `made` is 'shut', 'open', or
// null for neither — travelling between the stops, or a failed switch. Unlit is
// information, so null is a legitimate value rather than a missing one.
function pidSetLimitSwitch(refs, made) {
    if (!refs.ticks) return;
    for (const key of ['shut', 'open']) {
        for (const dot of refs.ticks[key].dots) {
            dot.classList.toggle('po-tick-made', made === key);
        }
    }
}

// ── The object row ──────────────────────────────────────────────────────────
// [glyph?] + name line + reading. The name line IS the title. Flipping `side`
// mirrors the whole block — glyph crosses over, text right-aligns, and the unit
// moves to the outer edge — so the number always hugs the bubble.

function pidObjectWidth(spec) {
    const valChars = Math.max(4, String(spec.sampleValue || '888.88').length);
    const unitW = (spec.units && spec.showUnits !== false)
        ? String(spec.units).length * 5.2 + 6 : 0;
    const nameW = (spec.showName === false) ? 0 : String(spec.name || '').length * 6.4;
    const textW = Math.max(valChars * PID_OBJ.CHAR_W + unitW, nameW);
    const glyphW = (spec.glyph === false) ? 0 : 2 * PID_OBJ.R + PID_OBJ.GAP;
    const raw = Math.ceil(glyphW + textW + 8);
    // Only a LEFT-sided row needs its width on the grid: it mirrors the glyph to
    // `width - PID_OBJ.GX`, so an off-grid width drags that port off-grid and
    // the router's snap bends the first segment. A right-sided row puts its
    // glyph at PID_OBJ.GX no matter how wide the row is, so its width is left
    // exactly as measured — rounding every row up widened every obstacle rect
    // in pidObstacleRects by up to a full grid cell, which closed routing
    // corridors that had fit for as long as the drawing had existed.
    if ((spec.side || 'right') === 'right') return raw;
    return Math.ceil(raw / PID.GRID) * PID.GRID;
}

// pidSensorBoxW is the object's drawn width. The renderer caches the measured
// value onto the layout object as `_objW` (transient, never serialised); before
// a first render — editor drag previews, the router on load — it falls back to
// estimating from the raw fields. Both portPos and the router go through here so
// there is one answer rather than three.
function pidSensorBoxW(obj) {
    if (obj && typeof obj._objW === 'number') return obj._objW;
    return pidObjectWidth({
        name: (obj && (obj.label || obj.refDes || obj.controlRefDes)) || '',
        units: obj && obj.units,
        showUnits: !obj || obj.showUnits !== false,
        showName: !obj || obj.showRefDes !== false,
        glyph: !obj || obj.showGlyph !== false,
        // side decides whether the width gets grid-rounded, so the estimate has
        // to carry it or a left-sided row's port and its drawn glyph disagree.
        side: obj && obj.side,
    });
}

// pidMachineBoxW is pidSensorBoxW's counterpart for daqControl (state-machine)
// objects: same "cached width first, estimate from raw fields otherwise"
// contract, but the machine row has no units and its value text is a state
// name rather than a reading. The fallback only matters before a first render,
// since makeDaqControlGroup caches the real measured width onto obj._objW right
// after building.
function pidMachineBoxW(obj) {
    if (obj && typeof obj._objW === 'number') return obj._objW;
    return pidObjectWidth({
        name: (obj && (obj.label || obj.daqRefDes)) || '',
        showUnits: false,
        showName: !obj || obj.showRefDes !== false,
        glyph: !obj || obj.showGlyph !== false,
        side: obj && obj.side,
        // Matches _machineSampleValue's no-states fallback in pid.js exactly.
        // Guessing wider here inflates the obstacle rect past what is drawn and
        // blocks routes around an object that is not really that big.
        sampleValue: 'autoSequence',
    });
}

// pidValveBox returns the drawn extent of a valve object relative to its grid
// point. A valve is positioned by its GLYPH CENTRE rather than by the row's
// corner — its pipes attach around that point and a panel is mostly valve
// pipes, so moving it would reroute every existing drawing. The row therefore
// hangs off the glyph, and the router needs to know where it actually lands.
function pidValveBox(obj) {
    const w = pidSensorBoxW(obj);
    const gx = ((obj && obj.side) || 'right') === 'right'
        ? PID_OBJ.GX : w - PID_OBJ.GX;
    return { dx: -gx, dy: -PID_OBJ.CY, w: w, h: PID_OBJ.H };
}

// pidBuildObject returns { g, refs }. `g` is an SVG group positioned by the
// caller; `refs` holds the live nodes so updates never re-parse markup.
function pidBuildObject(spec) {
    const W = spec.width || pidObjectWidth(spec);
    const H = PID_OBJ.H;
    // The glyph centre (gx, cy) is NOT H / 2 — see PID_OBJ.GX/CY's comment:
    // sensor and machine rows sit flush on the grid with no compensating
    // translate, so this is also the pixel portPos() hands the router, and it
    // has to land on a grid multiple or pipes leave the glyph at an angle.
    const cy = PID_OBJ.CY;
    const hasGlyph = spec.glyph !== false;
    const textRight = (spec.side || 'right') === 'right';
    const R = PID_OBJ.R;

    const g = svgN('g', { class: 'pid-object ' + pidObjStateClass(spec.dataCond, spec.alarmed) });
    const refs = { width: W, height: H, textRight: textRight };

    const gx = !hasGlyph ? 0 : (textRight ? PID_OBJ.GX : W - PID_OBJ.GX);
    const edge = hasGlyph ? (textRight ? 2 * R + PID_OBJ.GAP : W - (2 * R + PID_OBJ.GAP))
                          : (textRight ? 4 : W - 4);

    // Selection glow, layered UNDER everything: an alarming object stays red
    // while selected, which a state-based highlight could not manage.
    const glow = svgN('g', { class: 'po-glow' });
    if (hasGlyph) {
        glow.appendChild(svgN('circle', { class: 'po-glow-bloom', cx: gx, cy: cy, r: R + 9 }));
        glow.appendChild(svgN('circle', { class: 'po-glow-blur',  cx: gx, cy: cy, r: R + 2 }));
        glow.appendChild(svgN('circle', { class: 'po-glow-core',  cx: gx, cy: cy, r: R + 1.5 }));
    } else {
        const box = { x: 4, y: 9, width: W - 8, height: H - 18, rx: 4 };
        glow.appendChild(svgN('rect', Object.assign({ class: 'po-glow-blur' }, box)));
        glow.appendChild(svgN('rect', Object.assign({ class: 'po-glow-core' }, box)));
    }
    g.appendChild(glow);

    if (hasGlyph) {
        const gg = svgN('g', { transform: 'translate(' + gx + ',' + cy + ')' });
        // An unreached target adds a concentric accent ring, and nothing at all
        // is added when the machine is where it was told to be.
        if (spec.type === 'machine') {
            refs.target = svgN('circle', { class: 'po-target', cx: 0, cy: 0, r: R + 4 });
            refs.target.style.display = 'none';
            gg.appendChild(refs.target);
        }
        refs.ring = svgN('circle', { class: 'po-ring', cx: 0, cy: 0, r: R });
        gg.appendChild(refs.ring);
        if (spec.type === 'valve')        pidGlyphValve(gg, spec, refs);
        else if (spec.type === 'machine') pidGlyphMachine(gg, spec, refs);
        else                              pidGlyphSensor(gg, spec, refs);
        g.appendChild(gg);
        refs.glyphG = gg;
    }

    const anchor = textRight ? 'start' : 'end';
    if (spec.showName !== false) {
        refs.name = svgN('text', {
            class: 'po-name', x: edge, y: cy - 6, 'text-anchor': anchor,
        });
        refs.name.textContent = spec.name || '';
        g.appendChild(refs.name);
    }
    refs.value = svgN('text', {
        class: 'po-value', x: edge, y: (spec.showName === false ? cy + 7 : cy + 15),
        'text-anchor': anchor,
    });
    refs.value.textContent = '--';
    g.appendChild(refs.value);

    if (spec.units && spec.showUnits !== false) {
        refs.units = svgN('text', {
            class: 'po-units', y: refs.value.getAttribute('y'), 'text-anchor': anchor,
        });
        refs.units.textContent = spec.units;
        refs.unitsEdge = edge;
        g.appendChild(refs.units);
    }
    pidSetObjectValue(refs, '--');
    return { g: g, refs: refs };
}

// pidSetObjectValue writes the reading and repositions the unit, which always
// sits on the OUTER edge of the row whichever side the text is on.
function pidSetObjectValue(refs, text) {
    if (!refs.value) return;
    refs.value.textContent = text;
    if (!refs.units) return;
    const w = String(text).length * PID_OBJ.CHAR_W;
    refs.units.setAttribute('x', refs.textRight ? refs.unitsEdge + w + 6
                                                : refs.unitsEdge - w - 6);
}

// pidFormatValue applies the per-object decimal places. Display only: the value
// on the wire, in the rolling buffer and on the graph is never rounded, so two
// objects bound to one channel may legitimately show it differently.
function pidFormatValue(v, decimals) {
    if (typeof v !== 'number' || !isFinite(v)) return '--';
    const dp = (typeof decimals === 'number' && decimals >= 0 && decimals <= 6) ? decimals : 2;
    return v.toFixed(dp);
}

// pidSetObjectState applies the two axes plus selection. Selection is not a
// state: it layers on top of whatever the object already is.
function pidSetObjectState(g, dataCond, alarmed, selected) {
    if (!g) return;
    g.classList.remove('st-live', 'st-nodata', 'st-stale', 'st-unbound');
    const known = ['live', 'nodata', 'stale', 'unbound'];
    g.classList.add('st-' + (known.indexOf(dataCond) >= 0 ? dataCond : 'live'));
    g.classList.toggle('alarmed', !!alarmed);
    if (selected !== undefined) g.classList.toggle('selected', !!selected);
}

// pidMarkAllObjectsStale drops every front-panel object to the stale data
// condition. Used when the control link itself goes down: nothing is arriving,
// so no object can still claim its reading is current. The alarm axis is
// preserved — a link failure does not clear an alert that was already raised.
function pidMarkAllObjectsStale() {
    document.querySelectorAll('.pid-object').forEach(g => {
        pidSetObjectState(g, 'stale', g.classList.contains('alarmed'));
    });
}

// pidSetMachineTarget shows the accent ring only while a commanded target has
// not been reached.
function pidSetMachineTarget(refs, pending) {
    if (refs.target) refs.target.style.display = pending ? '' : 'none';
}
