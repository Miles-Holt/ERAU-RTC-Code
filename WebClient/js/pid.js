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

// ── Shared render/serialisation helpers moved to pidRender.js ────────────────
// PID (constants), svgN, pidSvgPt, portPos, pidFromYaml and the valve-symbol
// geometry helpers now live in pidRender.js, shared with the standalone editor
// so the same YAML renders identically on both pages. pidToYaml also lived here
// but was unused by the viewer, so it was removed.

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
        closeMachineDropdown(false);
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
    // Render each object/connection in isolation — one malformed object (e.g. an
    // embedded graph that fails to initialise) must not blank the whole panel.
    // Objects render before connections, so without this a single throwing object
    // would drop every pipe.
    // Browser-local render failures only — the server cannot see them, so these
    // stay client-side (every SYSTEM alert comes from the control node).
    for (const obj of tab.pid.objects) {
        try { renderPidObj(tab, obj); }
        catch (e) {
            console.error('pid: object', obj.id, '(' + obj.type + ') failed to render:', e);
            if (typeof ingestAlert === 'function') ingestAlert({
                id:        'piderr:' + obj.id,   // stable id → replaces, doesn't stack
                category:  'warning',
                message:   'Panel: "' + obj.type + '" object failed to render (' + obj.id + ')',
                timestamp: Date.now(), acked: false,
            });
        }
    }
    for (const conn of tab.pid.connections) {
        try { renderPidConn(tab, conn); }
        catch (e) {
            console.error('pid: connection', conn.id, 'failed to render:', e);
            if (typeof ingestAlert === 'function') ingestAlert({
                id:        'piderr:' + conn.id,
                category:  'warning',
                message:   'Panel: pipe failed to render (' + conn.id + ')',
                timestamp: Date.now(), acked: false,
            });
        }
    }
    rebindPidLiveData(tab);
}

function renderPidObj(tab, obj) {
    const g = obj.type === 'graph'      ? makeGraphGroup(obj, tab)
            : obj.type === 'sensor'     ? makeSensorGroup(obj)
            : obj.type === 'valve'      ? makeValveGroup(obj, tab)
            : obj.type === 'tank'       ? makeTankGroup(obj)
            : obj.type === 'daqControl' ? makeDaqControlGroup(obj, tab)
            : makeNodeGroup(obj);
    tab.pid.gObjs.appendChild(g);
}

// pidObjRefs holds the live SVG nodes for each front-panel object, keyed by the
// group element itself. A WeakMap rather than a field on tab.pid: the groups are
// thrown away and rebuilt on every renderPid, and a map keyed by the element
// cannot outlive them or go stale against a re-rendered layout.
const pidObjRefs = new WeakMap();

// makeSensorGroup builds one front-panel object — a bubble, a name line and a
// line of live text (design: docs/design/sensor-object-options.html). The old
// 120x50 box with stacked centred text is gone, and with it the italic control
// description: the refDes line IS the title now.
function makeSensorGroup(obj) {
    // Objects reference controls, not channels: obj.controlRefDes (+ optional
    // obj.channelRefDes to pick one of several readable channels) is the
    // preferred binding; obj.refDes (a bare channel refDes) is the legacy
    // form kept for layouts saved before controls existed. Both resolve
    // through the same helper so display + live data agree.
    const binding = resolveSensorBinding(obj, configControls);

    // The name line is free text defaulting to the bound refDes, so a layout
    // that sets none draws exactly what it drew before.
    const refDesText = binding?.ch.refDes || obj.refDes || obj.controlRefDes || '(no refDes)';
    const built = pidBuildObject({
        type:       'sensor',
        name:       obj.label || refDesText,
        showName:   obj.showRefDes !== false,
        units:      obj.units || binding?.ch.units || '',
        showUnits:  obj.showUnits !== false,
        decimals:   obj.decimals,
        bubbleText: obj.bubbleText,     // undefined → derived from the refDes
        side:       obj.side || 'right',
        glyph:      obj.showGlyph !== false,
        dataCond:   binding ? 'nodata' : 'unbound',
    });
    const g = built.g;
    pidObjRefs.set(g, built.refs);

    // portPos has to put the pipe on the bubble, and the bubble's position on a
    // left-sided object depends on the width the text block came out at, so the
    // built width is cached back onto the layout object. Transient: pidToYaml
    // serialises named fields only, so it never reaches the file.
    obj._objW = built.refs.width;

    const rot = obj.rotation || 0;
    const xf = 'translate(' + (obj.gridX * PID.GRID) + ',' + (obj.gridY * PID.GRID) + ')' +
        (rot ? ' rotate(' + rot + ',' + (built.refs.width / 2) + ',' + (built.refs.height / 2) + ')' : '');
    g.classList.add('pid-obj');
    g.setAttribute('data-pid-id', obj.id);
    g.setAttribute('transform', xf);

    if (binding) {
        g.style.cursor = 'context-menu';
        g.addEventListener('contextmenu', (e) => {
            e.preventDefault();
            e.stopPropagation();
            openObjectSidebar(binding.ch.refDes, g);
        });
    }

    return g;
}

// makeDaqControlGroup builds one front-panel object for a state machine: the
// same glyph + name line + live-text construction the sensor/valve use, with
// a machine interior (design: docs/design/sensor-object-options.html,
// MACHINE). The old box-with-embedded-<select> widget is gone.
function makeDaqControlGroup(obj, tab) {
    // daqRefDes holds the state MACHINE name (e.g. "fuelSeq"), matching
    // state_config machines[].name.  Layouts that predate machines have none.
    const machineName = obj.daqRefDes || '';
    if (!machineName) {
        console.warn('pid: daqControl object "' + obj.id +
            '" has no daqRefDes (state machine name) — widget is unbound and cannot command transitions');
    }
    const machineConfig = machineName ? machineStateConfig[machineName] : null;

    const nameText = obj.label || machineName || '(no refDes)';
    const built = pidBuildObject({
        type:        'machine',
        name:        nameText,
        showName:    obj.showRefDes !== false,
        units:       '',
        showUnits:   false,
        side:        obj.side || 'right',
        glyph:       obj.showGlyph !== false,
        sampleValue: _machineSampleValue(machineConfig),
        dataCond:    machineName ? 'nodata' : 'unbound',
    });
    const g = built.g;
    pidObjRefs.set(g, built.refs);
    obj._objW = built.refs.width;

    const xf = 'translate(' + (obj.gridX * PID.GRID) + ',' + (obj.gridY * PID.GRID) + ')';
    g.classList.add('pid-obj');
    g.setAttribute('data-pid-id', obj.id);
    g.setAttribute('transform', xf);
    g.style.cursor = machineName ? 'pointer' : 'default';

    // Stop left-click from reaching the SVG-level pan handler, same as valve.
    g.addEventListener('pointerdown', e => { if (e.button === 0) e.stopPropagation(); });

    g.addEventListener('click', (e) => {
        e.stopPropagation();
        if (!machineName) return;   // unbound: nothing to command
        // The panel opens BELOW THE GLYPH, same rule as the valve panel.
        const rect = (built.refs.glyphG || g).getBoundingClientRect();
        openMachineDropdown(obj, e.clientX, e.clientY, rect, tab.id);
    });

    return g;
}

// _machineSampleValue sizes the object for the longest realistic text it will
// ever show: "<current> → <target>". Falls back to a generic placeholder
// before state_config has arrived (config always precedes the first render
// that needs it — applyStateConfig re-renders every front-panel tab).
function _machineSampleValue(machineConfig) {
    const states = machineConfig?.states || [];
    if (!states.length) return 'autoSequence';
    let longest = '';
    for (const s of states) if (s.name.length > longest.length) longest = s.name;
    return longest + ' → ' + longest;
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

// Valve symbol geometry helpers (_valveLineAttrs, _valveArcPath, _valvePtrPos,
// _valveSubtypeInfo) live in pidRender.js. Only _valveSubtypeInfo is still read
// from here — the rest belong to the editor's own valve symbol, which has not
// been redesigned yet.

// makeValveGroup builds one front-panel object for a valve: the same glyph +
// name line + live-text construction the sensor uses, with a valve interior
// (design: docs/design/sensor-object-options.html, VALVE).
//
// GEOMETRY: a valve's pipe ports are computed around its CENTRE at
// (gridX*GRID, gridY*GRID) by portPos, and most of a P&ID is valve pipes, so
// that centre must not move. pidBuildObject lays the row out from its own
// origin with the GLYPH centre at (PID_OBJ.GX, PID_OBJ.CY) for a right-sided
// object (mirrored to W - PID_OBJ.GX for a left-sided one), so the group is
// translated by the negative of that offset: the glyph centre then lands
// exactly on the grid point every existing pipe already attaches to, and
// portPos is untouched.
function makeValveGroup(obj, tab) {
    const ctrl  = configControls.find(c => c.refDes === obj.controlRefDes);
    const info  = _valveSubtypeInfo(ctrl);
    const cmdCh = ctrl?.channels?.find(c => c.role === 'cmd-bool' || c.role === 'cmd-pct');
    const fbCh  = ctrl?.channels?.find(c => c.role === '' || c.role === 'sensor');

    // 'pct' is a continuously-positioned valve — arc for feedback, dot for
    // command. Everything else is a two-position valve with a bore line.
    const valveKind = (cmdCh?.role === 'cmd-pct') ? 'pct' : 'io';
    // Limit switches exist only where the feedback is discrete. POS-FB is a
    // number, not a pair of switches, so it gets the ring instead.
    const hasLimits = !!(info.hasFb && !info.fbIsPct);

    const refDesText = obj.controlRefDes || '(no refDes)';
    const built = pidBuildObject({
        type:        'valve',
        valveKind:   valveKind,
        hasLimits:   hasLimits,
        // The bore and its limit ticks turn with the run; a percentage figure
        // stays upright, which pidGlyphValve handles internally. So the row
        // itself is never rotated — only the glyph interior is.
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
    pidObjRefs.set(g, built.refs);
    obj._objW = built.refs.width;

    // Shift the row so the GLYPH centre — not the row origin — sits on the grid
    // point. This is the constraint above: valve ports do not move.
    const gx = (obj.showGlyph === false) ? 0
             : (((obj.side || 'right') === 'right') ? PID_OBJ.GX : built.refs.width - PID_OBJ.GX);
    const gy = PID_OBJ.CY;
    g.classList.add('pid-obj');
    g.setAttribute('data-pid-id', obj.id);
    g.setAttribute('transform', 'translate(' +
        (obj.gridX * PID.GRID - gx) + ',' + (obj.gridY * PID.GRID - gy) + ')');
    g.style.cursor = 'pointer';

    // Stop left-click from reaching the SVG-level pan handler
    g.addEventListener('pointerdown', e => { if (e.button === 0) e.stopPropagation(); });

    g.addEventListener('contextmenu', (e) => {
        e.preventDefault();
        e.stopPropagation();
        const refDes = fbCh?.refDes || cmdCh?.refDes;
        if (refDes) openObjectSidebar(refDes, g);
    });

    g.addEventListener('click', (e) => {
        e.stopPropagation();
        // The panel opens BELOW THE GLYPH, not on the pointer, so the valve
        // being commanded stays visible under its own panel. clientX/clientY
        // are passed through only as a fallback anchor point.
        const rect = (built.refs.glyphG || g).getBoundingClientRect();
        openValveDropdown(obj, e.clientX, e.clientY, rect, tab.id);
    });

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

        const searchWrap  = document.createElement('div');
        searchWrap.className = 'graph-search-wrap';
        const searchInput = document.createElement('input');
        searchInput.type        = 'text';
        searchInput.placeholder = 'Add channel (regex)...';
        searchInput.className   = 'graph-search';
        searchWrap.appendChild(searchInput);
        panel.appendChild(searchWrap);

        createChannelSearchDropdown(searchInput, {
            getExcluded: () => new Set(cell.channels.map(c => c.refDes)),
            onPick:      (refDes) => addChannelToCell(GRAPH_TAB_ID, 0, refDes),
            position:    'above',
        });

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
    // Explicit per-connection color (optional) overrides the fluid-type default;
    // absent = current appearance (fluid class, or the plain default stroke).
    wrap.children[1].style.stroke = conn.color || '';
}

// =============================================================================
// DAQ Control helpers
// =============================================================================

// _updateDaqControlState renders the machine's current state into every
// rendered front-panel object bound to it (glyph text + accent ring) and, if
// a machinePanel.js dropdown is open for this machine, syncs its rows too.
// stateValue accepts BOTH shapes the server produces:
//   • a string  — the state NAME, from the authoritative state_change message
//   • a number  — the state INDEX, from the SM-<MACHINE>-STATE data channel,
//                 resolved through machineStateConfig[].states[].index
function _updateDaqControlState(machineName, stateValue) {
    const machineConfig = machineStateConfig[machineName];
    if (!machineConfig) return;

    // Resolve state name
    let stateName;
    if (typeof stateValue === 'number') {
        const stIdx = Math.round(stateValue);
        const states = machineConfig.states || [];
        stateName = (states.find(s => s.index === stIdx) || states[stIdx])?.name
            || ('state_' + stateValue);
    } else {
        stateName = String(stateValue);
    }
    machineCurrentState[machineName] = stateName;

    // A pending target (see machinePendingTarget in state.js) is cleared the
    // moment the machine reports the state we asked for.
    const pendBefore = machinePendingTarget[machineName];
    if (pendBefore && pendBefore.target === stateName) {
        clearTimeout(pendBefore.timer);
        delete machinePendingTarget[machineName];
    }

    _repaintMachineWidgets(machineName, stateName);
}

// _repaintMachineWidgets redraws every rendered daqControl object bound to
// `machineName`, across all front-panel tabs, from the two already-known
// facts — machineCurrentState and machinePendingTarget — WITHOUT resolving or
// mutating either. Kept separate from _updateDaqControlState so a repaint
// triggered by pidRequestMachineTarget (before any state_change has arrived)
// can never overwrite machineCurrentState with a guess.
function _repaintMachineWidgets(machineName, knownStateName) {
    const stateName = knownStateName !== undefined ? knownStateName : machineCurrentState[machineName];
    const pending = machinePendingTarget[machineName];
    for (const tab of tabs) {
        if (tab.type !== 'frontPanel' || !tab.pid || !tab.pid.svgEl) continue;
        for (const obj of tab.pid.objects) {
            if (obj.type !== 'daqControl' || obj.daqRefDes !== machineName) continue;
            const g = tab.pid.svgEl.querySelector('[data-pid-id="' + obj.id + '"]');
            const refs = g ? pidObjRefs.get(g) : null;
            if (!g || !refs) continue;
            const text = stateName === undefined ? '--'
                : (pending ? (stateName + ' → ' + pending.target) : stateName);
            pidSetObjectValue(refs, text);
            pidSetMachineTarget(refs, !!pending);
        }
    }
    if (typeof updateMachineDropdownState === 'function') updateMachineDropdownState(machineName);
}

// pidRequestMachineTarget sends a state-machine transition request and marks
// it PENDING on this browser until a matching state_change arrives (or the
// timeout below fires). See the machinePendingTarget comment in state.js for
// why this is inferred client-side rather than read off the wire.
const MACHINE_PENDING_TIMEOUT_MS = 15000;

function pidRequestMachineTarget(machineName, targetRefDes, stateName) {
    sendCommand(targetRefDes, stateName);   // string, not index — see server.go handleStateMachineTarget

    const prior = machinePendingTarget[machineName];
    if (prior) clearTimeout(prior.timer);
    machinePendingTarget[machineName] = {
        target: stateName,
        timer: setTimeout(() => {
            // No matching state_change arrived in time — most likely the
            // request was rejected (out-of-gate, unknown state) and the only
            // trace of that is a server pushAlert the operator may not have
            // seen. Drop the accent ring rather than leave it lit forever on
            // a command that silently failed.
            delete machinePendingTarget[machineName];
            _repaintMachineWidgets(machineName);
        }, MACHINE_PENDING_TIMEOUT_MS),
    };
    // Repaint immediately so the accent ring appears without waiting for the
    // next state_change/data tick.
    _repaintMachineWidgets(machineName);
}

// =============================================================================
// Live data binding
// =============================================================================

function rebindPidLiveData(tab) {
    tab.channelUpdaters = {};
    for (const obj of tab.pid.objects) {
        if (obj.type === 'sensor') {
            const g = tab.pid.svgEl.querySelector('[data-pid-id="' + obj.id + '"]');
            const refs = g ? pidObjRefs.get(g) : null;
            if (!g || !refs) continue;
            const binding = resolveSensorBinding(obj, configControls);
            if (!binding) {
                // Names nothing in the config. Grey, dashed and italic — not an
                // alarm, because a drawing mistake is not a process fault.
                pidSetObjectState(g, 'unbound', false);
                pidSetObjectValue(refs, '--');
                continue;
            }

            // The alarm axis is an alert raised AND unacknowledged — real
            // alert state, latching, attributed to this channel by the server.
            // Two ways an object is alarmed: an alert naming its channel, or a
            // node-level alert (disconnect, stale link) naming the node that
            // owns it. The second is how "node offline" reaches the object at
            // all — the alert lists the node, never the channels beneath it.
            const alarmed = () => typeof isChannelAlarmed === 'function'
                && (isChannelAlarmed(binding.ch.refDes)
                 || isNodeAlarmed(binding.ch.node));

            const stale = makeStaleTimer(CONFIG.channelStaleMs,
                () => pidSetObjectState(g, 'stale', alarmed()));
            // Nothing published since startup is its own condition, distinct
            // from a reading that has gone quiet.
            pidSetObjectState(g, 'nodata', alarmed());
            tab.channelUpdaters[binding.ch.refDes] = value => {
                pidSetObjectValue(refs, typeof value === 'number'
                    ? pidFormatValue(value, obj.decimals) : String(value));
                pidSetObjectState(g, 'live', alarmed());
                stale.bump();
            };
        }
        if (obj.type === 'valve') {
            const g = tab.pid.svgEl.querySelector('[data-pid-id="' + obj.id + '"]');
            const refs = g ? pidObjRefs.get(g) : null;
            if (!g || !refs) continue;
            const ctrl = obj.controlRefDes
                ? configControls.find(c => c.refDes === obj.controlRefDes) : null;
            if (!ctrl) {
                // Names nothing in the config. Grey, dashed and italic — not an
                // alarm, because a drawing mistake is not a process fault.
                pidSetObjectState(g, 'unbound', false);
                pidSetObjectValue(refs, '--');
                continue;
            }
            const info  = _valveSubtypeInfo(ctrl);
            const cmdCh = ctrl.channels?.find(c => c.role === 'cmd-bool' || c.role === 'cmd-pct');
            const fbCh  = ctrl.channels?.find(c => c.role === '' || c.role === 'sensor');
            const id = obj.id;
            const hasLimits = !!(info.hasFb && !info.fbIsPct);
            const isPct = cmdCh?.role === 'cmd-pct';

            // The alarm axis is an alert raised AND unacknowledged — real
            // alert state, latching, attributed to this channel by the server.
            // A valve has two channels and either may be flagged, so either
            // raises the one alarm axis the object has.
            // A valve has two channels and either can be alarmed, plus the node
            // that owns them.
            const alarmed = () => typeof isChannelAlarmed === 'function'
                && ((!!cmdCh && isChannelAlarmed(cmdCh.refDes))
                 || (!!fbCh  && isChannelAlarmed(fbCh.refDes))
                 || isNodeAlarmed((cmdCh || fbCh || {}).node));

            const stale = makeStaleTimer(CONFIG.channelStaleMs,
                () => pidSetObjectState(g, 'stale', alarmed()));
            // Nothing published since startup is its own condition, distinct
            // from a reading that has gone quiet.
            pidSetObjectState(g, 'nodata', alarmed());

            // Command and feedback arrive on separate channels at separate
            // times, and the drawing needs both at once: the bore's colour is
            // command AND switch together, and the ring is feedback AND command
            // together. So the last of each is kept. They are kept as SEPARATE
            // numbers — drawing one of them twice was the old bug.
            const last = { isOpen: false, made: null, fbPct: null, cmdPct: null };

            const repaintIo = () => {
                pidSetLimitSwitch(refs, last.made);
                // Green is earned by the SWITCH, not by the command; a valve
                // with no feedback fitted counts as confirmed, because there
                // the command is the only fact there is.
                pidSetValveBore(refs, last.isOpen,
                    pidValvePositionConfirmed(hasLimits, last.isOpen, last.made));
                pidSetObjectValue(refs, last.isOpen ? 'OPEN' : 'CLOSED');
            };
            const repaintPct = () => {
                pidSetValveFeedback(refs, last.fbPct, last.cmdPct);
                pidSetObjectValue(refs, last.cmdPct === null
                    ? 'CMD --' : 'CMD ' + Math.round(last.cmdPct) + '%');
            };

            if (cmdCh) {
                tab.channelUpdaters[cmdCh.refDes] = value => {
                    if (isPct) {
                        last.cmdPct = typeof value === 'number' ? value : null;
                        repaintPct();
                    } else {
                        last.isOpen = !!value;
                        repaintIo();
                    }
                    pidSetObjectState(g, 'live', alarmed());
                    stale.bump();
                    updateValveDropdownValue(id, cmdCh.refDes, value);
                };
            }
            if (fbCh) {
                tab.channelUpdaters[fbCh.refDes] = value => {
                    if (info.fbIsPct) {
                        last.fbPct = typeof value === 'number' ? value : null;
                        repaintPct();
                    } else {
                        // The IO feedback channel is a single boolean, and the
                        // old drawing code read it exactly this way: truthy
                        // moved the dots onto the open line, falsy onto the shut
                        // one. One boolean cannot express "neither switch made",
                        // so mid-travel is not representable on the wire today
                        // and reads as shut. A non-numeric, non-boolean value
                        // (nothing known) is the one case that maps to null.
                        last.made = (value === null || value === undefined)
                            ? null : (value ? 'open' : 'shut');
                        repaintIo();
                    }
                    pidSetObjectState(g, 'live', alarmed());
                    stale.bump();
                    updateValveDropdownValue(id, fbCh.refDes, value);
                };
            }
        }

        // ── daqControl: bind SM-<MACHINE>-STATE ──────────────────────────────────
        // The old widget also surfaced CTR001-daqConnected as a separate
        // "Connected/Disconnected" line; that doesn't map onto the two-axis
        // state vocabulary (design: STATES) and has no equivalent here — node
        // connectivity now folds into the same nodata/stale/alarm axes every
        // other object uses, not a bespoke third indicator.
        if (obj.type === 'daqControl') {
            const g = tab.pid.svgEl.querySelector('[data-pid-id="' + obj.id + '"]');
            const refs = g ? pidObjRefs.get(g) : null;
            if (!g || !refs) continue;
            const machineName = obj.daqRefDes;
            if (!machineName) {
                // Names no machine. Grey, dashed and italic — not an alarm,
                // because a drawing mistake is not a process fault.
                pidSetObjectState(g, 'unbound', false);
                pidSetObjectValue(refs, '--');
                continue;
            }
            const smStateRefDes = 'SM-' + machineName + '-STATE';

            // The alarm axis is an alert raised AND unacknowledged. Until the
            // alert side of the redesign lands there is no such source, so
            // this reads the server's bad_data flag on the state channel —
            // see the identical comment on the sensor/valve branches above.
            // Channel-level only. A machine's owning node is NOT on the wire —
            // `state_config`'s machine entries carry name, targetRefDes and
            // states, and no node — so a node-level disconnect cannot reach a
            // machine object the way it reaches a sensor or valve. Adding a
            // `node` field to stateConfigMachine would close it; guessing the
            // node from the machine name would not.
            const alarmed = () => typeof isChannelAlarmed === 'function'
                && isChannelAlarmed(smStateRefDes);

            const stale = makeStaleTimer(CONFIG.channelStaleMs,
                () => pidSetObjectState(g, 'stale', alarmed()));
            pidSetObjectState(g, 'nodata', alarmed());

            // Listen for SM-<MACHINE>-STATE (numeric index) to update current
            // state.  state_change (state NAME) drives the same function from
            // ws.js — whichever arrives first wins.
            tab.channelUpdaters[smStateRefDes] = value => {
                _updateDaqControlState(machineName, value);
                pidSetObjectState(g, 'live', alarmed());
                stale.bump();
            };

            // A re-render (new layout, new state_config) must not blank the
            // widget: repaint the last known state immediately.
            if (machineCurrentState[machineName] !== undefined) {
                _updateDaqControlState(machineName, machineCurrentState[machineName]);
                pidSetObjectState(g, 'live', alarmed());
                stale.bump();
            }
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

    // A graph object has no glyph and no .po-glow element — it's a foreignObject
    // embedding a live chart, not a pidBuildObject() row — so there is nothing
    // to glow. Still must clear whatever the panel WAS glowing: otherwise
    // retargeting from a sensor/valve to a graph object leaves that sensor lit
    // while the panel it supposedly marks has moved on to different content.
    sidebarSetGlow(null);

    // Same reasoning for the state pill (item 06) and readings table (item
    // 05): a graph object is a multi-channel plot, not a single object with a
    // data-condition/alarm axis, so it gets neither — but retargeting from a
    // sensor/valve must not leave that object's pill or rows showing.
    _sidebarBindStatePill(sidebarEl, null);
    _sidebarClearReadings(sidebarEl);
    sidebarClickedRefDes   = null;
    sidebarClickedDecimals = undefined;

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
