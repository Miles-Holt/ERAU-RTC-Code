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

// ── Shared render/serialisation helpers moved to pidRender.js ────────────────
// svgN, pidSvgPt, portPos, pidFromYaml (and the valve-symbol geometry helpers
// below) now live in pidRender.js, shared with the standalone editor.
// pidToYaml also lived here but was unused by the viewer, so it was removed.
// PID (above) stays per-file: VALVE_PORT_OFF differs between viewer and editor.

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
    tab.pid._routedPaths     = new Map();   // connId -> Array<{x,y}>; feeds pipe-overlap avoidance
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

    // Top row: label (or DAQ refDes fallback) + connection status (right)
    const labelEl = svgN('text', { class: 'pid-daqctrl-label', x: 8, y: 18 });
    labelEl.textContent = obj.label || daqRef || 'DAQ???';
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
        markCmdWidget(sel);

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
// Valve symbol geometry helpers (_valveLineAttrs, _valveArcPath, _valvePtrPos,
// _valveSubtypeInfo) moved to pidRender.js — shared with the editor.

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

    // Attach drag-pan, scroll-zoom and proximity tooltip
    attachDragPan(canvas, cell);
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

    if (!tab.pid._routedPaths) tab.pid._routedPaths = new Map();

    const p1 = portPos(from, conn.fromPort);
    const p2 = portPos(to,   conn.toPort);
    const pipeSegs = pidPipeSegs(tab.pid._routedPaths, conn.id);
    const { d, pts } = pidRoute({
        p1, d1: conn.fromPort, p2, d2: conn.toPort,
        objects: tab.pid.objects, pipeSegs,
    });
    tab.pid._routedPaths.set(conn.id, pts);

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
