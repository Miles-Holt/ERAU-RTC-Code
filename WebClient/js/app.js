// =============================================================================
// RTC WebSocket Client
// =============================================================================
//
// PROTOCOL (all messages are JSON strings):
//
//   LabVIEW -> Browser:
//     Config  { "type": "config", "controls": [ <control>, ... ] }
//     Data    { "type": "data",   "t": <unix_epoch_s>, "d": { "<refDes>": <value>, ... } }
//
//   Browser -> LabVIEW:
//     Command { "type": "cmd", "refDes": "<refDes>", "value": <number|bool> }
//
//   Config control object:
//     {
//       "refDes":      "NV-03",
//       "description": "LOX Press",
//       "type":        "valve",
//       "subType":     "IO-CMD_IO-FB",
//       "details":     { "senseRefDes": "OPT-02" },
//       "channels": [
//         { "refDes": "NV-03-CMD", "role": "cmd-bool", "units": "" },
//         { "refDes": "NV-03-FB",  "role": "sensor",   "units": "" }
//       ]
//     }
//
//   Channel roles: "sensor" | "cmd-bool" | "cmd-pct" | "cmd-float"
//
//   TIMESTAMP NOTE:
//     LabVIEW epoch starts 1904-01-01. Unix epoch starts 1970-01-01.
//     Convert in LabVIEW before sending: unix_t = lv_t - 2082844800
//
// =============================================================================

const CONFIG = {
    wsUrl:              `ws://${window.location.hostname || 'localhost'}:8000`,
    staleThresholdMs:   500,
    reconnect:          { baseMs: 1000, maxMs: 10000, factor: 2 },
    graphBufferMinutes: 15,
    consoleBufferLimit: 500
};

// =============================================================================
// State
// =============================================================================

// --- Connection ---
let ws             = null;
let reconnectDelay = CONFIG.reconnect.baseMs;
let reconnectTimer = null;
let stalenessTimer = null;

// --- Config ---
let configControls = [];
let configApplied  = false;

// --- Tabs ---
let tabs        = [];
let activeTabId = null;
const tabCounts = {};   // { frontPanel: 1, ... } running counter for auto-naming

// --- Graph ---
// graphState[tabId] = { rows, cols, gridEl, cells: [{ cellEl, chart, channels: [{refDes,color,hidden}] }] }
const graphState          = {};
const channelBuffers      = {};         // refDes -> { ts: number[], vals: number[] }
const activeGraphChannels = new Set();

const CHART_COLORS = [
    '#58a6ff', '#3fb950', '#f78166', '#e3b341',
    '#bc8cff', '#56d364', '#79c0ff', '#ffa657',
    '#ff7b72', '#d2a8ff'
];

// --- Dev ---
const devStats = {
    connectedAt:     null,
    msgCount:        0,
    lastWindowCount: 0,
    missedCycles:    0,
    lastDataT:       null,
    avgInterval:     null
};
let devTabs = [];

// --- Console ---
const consoleLog = [];   // [{ time, dir: 'in'|'out', msg }]
let consoleTabs  = [];


// =============================================================================
// WebSocket management
// =============================================================================

function connect() {
    setStatus('connecting', 'Connecting...');
    try {
        ws           = new WebSocket(CONFIG.wsUrl);
        ws.onopen    = onOpen;
        ws.onmessage = onMessage;
        ws.onclose   = onClose;
        ws.onerror   = () => {};
    } catch (e) {
        scheduleReconnect();
    }
}

function onOpen() {
    reconnectDelay       = CONFIG.reconnect.baseMs;
    devStats.connectedAt = Date.now();
    setStatus('connected', 'Connected — waiting for config...');
}

function onMessage(event) {
    devStats.msgCount++;
    let msg;
    try { msg = JSON.parse(event.data); }
    catch { console.warn('Non-JSON message received:', event.data); return; }

    logConsole('in', msg);

    switch (msg.type) {
        case 'config': applyConfig(msg); break;
        case 'data':   applyData(msg);   break;
        default:       console.warn('Unknown message type:', msg.type);
    }
}

function onClose() {
    clearTimeout(stalenessTimer);
    markStale();
    devStats.connectedAt = null;
    setStatus('disconnected', 'Disconnected');
    scheduleReconnect();
}

function scheduleReconnect() {
    if (reconnectTimer) return;
    setStatus('reconnecting', `Reconnecting in ${reconnectDelay / 1000}s...`);
    reconnectTimer = setTimeout(() => {
        reconnectTimer = null;
        connect();
    }, reconnectDelay);
    reconnectDelay = Math.min(reconnectDelay * CONFIG.reconnect.factor, CONFIG.reconnect.maxMs);
}

function sendCommand(refDes, value) {
    if (!ws || ws.readyState !== WebSocket.OPEN) {
        console.warn('Cannot send command: not connected');
        return;
    }
    const msg = { type: 'cmd', refDes, value };
    ws.send(JSON.stringify(msg));
    logConsole('out', msg);
}


// =============================================================================
// Config & data handling
// =============================================================================

function applyConfig(msg) {
    configControls = msg.controls ?? [];
    configApplied  = true;
    for (const tab of tabs) {
        if (tab.type === 'dataView') rebuildDataView(tab);
    }
    setStatus('connected', 'Connected');
}

function applyData(msg) {
    if (!configApplied) return;
    resetStalenessTimer();
    updateTimestamp(msg.t);
    setStatus('connected', 'Connected');
    trackDataTiming(msg.t);
    bufferGraphData(msg.d);

    for (const tab of tabs) {
        if (!tab.channelUpdaters) continue;
        for (const [refDes, value] of Object.entries(msg.d)) {
            tab.channelUpdaters[refDes]?.(value);
        }
    }
}

function resetStalenessTimer() {
    clearTimeout(stalenessTimer);
    stalenessTimer = setTimeout(markStale, CONFIG.staleThresholdMs);
}

function markStale() {
    document.querySelectorAll('.value, .fb-label').forEach(el => el.classList.add('stale'));
    setStatus('stale', 'Data stale');
}

function trackDataTiming(t) {
    if (devStats.lastDataT !== null) {
        const gap = t - devStats.lastDataT;
        if (devStats.avgInterval === null) devStats.avgInterval = gap;
        else devStats.avgInterval = devStats.avgInterval * 0.9 + gap * 0.1;
        if (gap > devStats.avgInterval * 2.5) {
            devStats.missedCycles += Math.round(gap / devStats.avgInterval) - 1;
        }
    }
    devStats.lastDataT = t;
}


// =============================================================================
// Tab management
// =============================================================================

const TAB_TYPE_LABELS = {
    frontPanel: 'Front Panel',
    dataView:   'Data View',
    graph:      'Graph',
    dev:        'Dev',
    console:    'Console'
};

function nextTabName(type) {
    tabCounts[type] = (tabCounts[type] || 0) + 1;
    const n = tabCounts[type];
    return n === 1 ? TAB_TYPE_LABELS[type] : `${TAB_TYPE_LABELS[type]} ${n}`;
}

function addTab(type = 'frontPanel') {
    const id        = `tab-${Date.now()}`;
    const name      = nextTabName(type);
    const contentEl = document.createElement('div');
    contentEl.className = 'tab-content';
    document.getElementById('tab-viewport').appendChild(contentEl);

    const tab = { id, type, name, contentEl, channelUpdaters: {} };
    tabs.push(tab);
    buildTabContent(tab);
    renderTabBar();
    activateTab(id);
    return tab;
}

function buildTabContent(tab) {
    tab.contentEl.innerHTML = '';
    tab.channelUpdaters = {};
    switch (tab.type) {
        case 'frontPanel': buildFrontPanelContent(tab); break;
        case 'dataView':   rebuildDataView(tab);        break;
        case 'graph':      buildGraphContent(tab);      break;
        case 'dev':        buildDevContent(tab);        break;
        case 'console':    buildConsoleContent(tab);    break;
    }
}

function removeTab(id) {
    const idx = tabs.findIndex(t => t.id === id);
    if (idx === -1) return;
    const tab = tabs[idx];

    if (tab.type === 'graph')   cleanupGraphTab(id);
    if (tab.type === 'dev')     devTabs     = devTabs.filter(t => t.id !== id);
    if (tab.type === 'console') consoleTabs = consoleTabs.filter(t => t.id !== id);

    tab.contentEl.remove();
    tabs.splice(idx, 1);

    const next = tabs.length > 0 ? tabs[Math.max(0, idx - 1)].id : null;
    renderTabBar();
    if (next) activateTab(next);
    else      addTab('frontPanel');
}

function changeTabType(id, newType) {
    const tab = tabs.find(t => t.id === id);
    if (!tab || tab.type === newType) return;

    if (tab.type === 'graph')   cleanupGraphTab(id);
    if (tab.type === 'dev')     devTabs     = devTabs.filter(t => t.id !== id);
    if (tab.type === 'console') consoleTabs = consoleTabs.filter(t => t.id !== id);

    tab.type = newType;
    tab.name = nextTabName(newType);
    buildTabContent(tab);
    renderTabBar();
}

function activateTab(id) {
    activeTabId = id;
    for (const tab of tabs) {
        const active = tab.id === id;
        tab.contentEl.style.display = active ? '' : 'none';
        if (active && tab.type === 'graph') {
            setTimeout(() => resizeGraphCharts(id), 0);
        }
    }
    renderTabBar();
}

function renderTabBar() {
    const container = document.getElementById('tabs');
    container.innerHTML = '';
    for (const tab of tabs) {
        const el = document.createElement('div');
        el.className = `tab${tab.id === activeTabId ? ' tab-active' : ''}`;

        const nameEl = document.createElement('span');
        nameEl.className = 'tab-name';
        nameEl.textContent = tab.name;

        const closeEl = document.createElement('button');
        closeEl.className = 'tab-close';
        closeEl.textContent = '✕';
        closeEl.title = 'Close tab';
        closeEl.addEventListener('click', (e) => { e.stopPropagation(); removeTab(tab.id); });

        el.appendChild(nameEl);
        el.appendChild(closeEl);
        el.addEventListener('click', () => activateTab(tab.id));
        el.addEventListener('dblclick', (e) => { e.stopPropagation(); startRename(tab, nameEl); });
        el.addEventListener('contextmenu', (e) => {
            e.preventDefault();
            showContextMenu(e.clientX, e.clientY, [
                { label: 'Front Panel', action: () => changeTabType(tab.id, 'frontPanel') },
                { label: 'Data View',   action: () => changeTabType(tab.id, 'dataView')   },
                { label: 'Graph',       action: () => changeTabType(tab.id, 'graph')      },
                { label: 'Dev',         action: () => changeTabType(tab.id, 'dev')        },
                { label: 'Console',     action: () => changeTabType(tab.id, 'console')    },
            ]);
        });
        container.appendChild(el);
    }
}

function startRename(tab, nameEl) {
    const inp = document.createElement('input');
    inp.className = 'tab-rename-input';
    inp.value = tab.name;
    nameEl.replaceWith(inp);
    inp.focus(); inp.select();
    const commit = () => { tab.name = inp.value.trim() || tab.name; renderTabBar(); };
    inp.addEventListener('blur', commit);
    inp.addEventListener('keydown', (e) => {
        if (e.key === 'Enter')  inp.blur();
        if (e.key === 'Escape') { inp.value = tab.name; inp.blur(); }
    });
}

function showContextMenu(x, y, items) {
    document.getElementById('context-menu')?.remove();
    const menu = document.createElement('div');
    menu.id = 'context-menu';
    menu.className = 'context-menu';
    menu.style.left = `${x}px`;
    menu.style.top  = `${y}px`;
    for (const item of items) {
        if (item.sep) {
            menu.appendChild(mkEl('div', 'ctx-sep'));
        } else {
            const btn = mkEl('button', 'ctx-item', item.label);
            btn.addEventListener('click', () => { item.action(); menu.remove(); });
            menu.appendChild(btn);
        }
    }
    document.body.appendChild(menu);
    const dismiss = (e) => {
        if (!menu.contains(e.target)) { menu.remove(); document.removeEventListener('mousedown', dismiss); }
    };
    setTimeout(() => document.addEventListener('mousedown', dismiss), 0);
}


// =============================================================================
// Front Panel tab
// =============================================================================

const FP_SVG = `<svg viewBox="0 0 200 400" width="160" height="320" fill="none" stroke="currentColor"
     stroke-width="2" stroke-linecap="round" stroke-linejoin="round" opacity="0.5">
  <!-- Injector dome -->
  <path d="M62,24 C62,6 138,6 138,24"/>
  <!-- Left profile: chamber → converging → bell nozzle -->
  <path d="M62,24 L62,105 C62,132 76,148 80,163 C82,182 22,305 6,390"/>
  <!-- Right profile: mirror -->
  <path d="M138,24 L138,105 C138,132 124,148 120,163 C118,182 178,305 194,390"/>
</svg>`;

function buildFrontPanelContent(tab) {
    tab.contentEl.innerHTML = `<div class="fp-wrapper"></div>`;
}


// =============================================================================
// Data View tab
// =============================================================================

const CMD_TYPES    = new Set(['valve', 'bangBang', 'ignition', 'digitalOut', 'VFD']);
const SENSOR_TYPES = new Set(['pressure', 'temperature', 'flowMeter', 'thrust', 'tank']);

const TYPE_LABELS = {
    pressure:    'Pressures',
    temperature: 'Temperatures',
    flowMeter:   'Flow Meters',
    thrust:      'Thrust',
    tank:        'Tanks',
    valve:       'Valves',
    bangBang:    'Bang-Bang Controllers',
    ignition:    'Ignition',
    digitalOut:  'Digital Outputs',
    VFD:         'VFDs'
};

const TYPE_ORDER = [
    'pressure', 'temperature', 'flowMeter', 'thrust', 'tank',
    'valve', 'bangBang', 'ignition', 'digitalOut', 'VFD'
];

function rebuildDataView(tab) {
    tab.channelUpdaters = {};
    tab.contentEl.innerHTML = '';

    if (!configControls.length) {
        tab.contentEl.appendChild(mkEl('div', 'loading', 'Waiting for configuration from LabVIEW...'));
        return;
    }

    const cmdControls    = configControls.filter(c => CMD_TYPES.has(c.type));
    const sensorControls = configControls.filter(c => SENSOR_TYPES.has(c.type));

    // --- Command cards ---
    if (cmdControls.length) {
        const sec  = mkEl('section', 'dv-section');
        const grid = mkEl('div', 'grid');
        const groups = {};
        for (const c of cmdControls) (groups[c.type] ??= []).push(c);
        for (const type of TYPE_ORDER) {
            if (!groups[type]) continue;
            sec.appendChild(mkEl('h2', 'group-heading', TYPE_LABELS[type] ?? type));
            for (const ctrl of groups[type]) grid.appendChild(buildCard(ctrl, tab));
        }
        sec.appendChild(grid);
        tab.contentEl.appendChild(sec);
    }

    // --- Sensor table ---
    if (sensorControls.length) {
        const sec = mkEl('section', 'dv-section');
        sec.appendChild(mkEl('h2', 'group-heading', 'Sensors'));
        const table = document.createElement('table');
        table.className = 'sensor-table';
        const thead = document.createElement('thead');
        thead.innerHTML = '<tr><th>RefDes</th><th>Description</th><th>Value</th><th>Units</th></tr>';
        table.appendChild(thead);
        const tbody = document.createElement('tbody');

        for (const ctrl of sensorControls) {
            for (const ch of ctrl.channels) {
                const tr     = document.createElement('tr');
                const tdRef  = mkEl('td', 'mono', ch.refDes);
                const tdDesc = mkEl('td', null,   ctrl.description || '');
                const tdVal  = mkEl('td', 'tbl-val stale', '--');
                const tdUnit = mkEl('td', 'muted', ch.units || '');
                tr.append(tdRef, tdDesc, tdVal, tdUnit);
                tbody.appendChild(tr);
                tab.channelUpdaters[ch.refDes] = (v) => {
                    tdVal.textContent = typeof v === 'number' ? v.toFixed(2) : String(v);
                    tdVal.classList.remove('stale');
                };
            }
        }
        table.appendChild(tbody);
        sec.appendChild(table);
        tab.contentEl.appendChild(sec);
    }
}


// =============================================================================
// Graph tab
// =============================================================================

function buildGraphContent(tab) {
    graphState[tab.id] = { rows: 1, cols: 1, gridEl: null, cells: [] };

    const wrapper  = mkEl('div', 'graph-tab');
    const toolbar  = mkEl('div', 'graph-toolbar');

    const rowInput = document.createElement('input');
    rowInput.type = 'number'; rowInput.min = 1; rowInput.max = 4; rowInput.value = 1;
    rowInput.className = 'graph-size-input';
    const colInput = document.createElement('input');
    colInput.type = 'number'; colInput.min = 1; colInput.max = 8; colInput.value = 1;
    colInput.className = 'graph-size-input';

    const rowLbl = mkEl('label', 'graph-size-lbl');
    rowLbl.appendChild(mkEl('span', null, 'Rows '));
    rowLbl.appendChild(rowInput);
    const colLbl = mkEl('label', 'graph-size-lbl');
    colLbl.appendChild(mkEl('span', null, 'Cols '));
    colLbl.appendChild(colInput);
    toolbar.appendChild(rowLbl);
    toolbar.appendChild(colLbl);
    wrapper.appendChild(toolbar);

    const gridEl = mkEl('div', 'graph-grid');
    wrapper.appendChild(gridEl);
    tab.contentEl.appendChild(wrapper);
    graphState[tab.id].gridEl = gridEl;

    const applyGrid = () => {
        const r = Math.min(4, Math.max(1, parseInt(rowInput.value) || 1));
        const c = Math.min(8, Math.max(1, parseInt(colInput.value) || 1));
        rowInput.value = r; colInput.value = c;
        resizeGraphGrid(tab.id, r, c);
    };
    rowInput.addEventListener('change', applyGrid);
    colInput.addEventListener('change', applyGrid);

    resizeGraphGrid(tab.id, 1, 1);
}

function resizeGraphGrid(tabId, rows, cols) {
    const state  = graphState[tabId];
    const gridEl = state.gridEl;
    const total  = rows * cols;

    // Destroy excess chart instances
    for (let i = total; i < state.cells.length; i++) {
        state.cells[i].chart?.destroy();
    }

    const preserved = state.cells.slice(0, total);
    while (preserved.length < total) preserved.push({ cellEl: null, chart: null, channels: [] });

    state.rows  = rows;
    state.cols  = cols;
    state.cells = preserved;

    gridEl.innerHTML = '';
    gridEl.style.gridTemplateColumns = `repeat(${cols}, 1fr)`;
    gridEl.style.gridTemplateRows    = `repeat(${rows}, 1fr)`;

    for (let i = 0; i < total; i++) {
        const cell   = state.cells[i];
        const cellEl = buildGraphCell(tabId, i);
        gridEl.appendChild(cellEl);
        cell.cellEl = cellEl;

        const canvas = cellEl.querySelector('canvas');
        cell.chart   = createCellChart(canvas);

        for (const ch of cell.channels) addDatasetToChart(cell.chart, ch.refDes, ch.color, ch.hidden);
        updateCellLegend(tabId, i);
    }

    updateActiveGraphChannels();
}

function buildGraphCell(tabId, cellIdx) {
    const cellEl  = mkEl('div', 'graph-cell');
    const header  = mkEl('div', 'graph-cell-header');

    const searchWrap = mkEl('div', 'graph-search-wrap');
    const searchInput = document.createElement('input');
    searchInput.type        = 'text';
    searchInput.placeholder = 'Add channel (regex)...';
    searchInput.className   = 'graph-search';
    const dropdown = mkEl('div', 'graph-dropdown');
    dropdown.style.display = 'none';
    searchWrap.appendChild(searchInput);
    searchWrap.appendChild(dropdown);

    const legendEl = mkEl('div', 'graph-legend');
    header.appendChild(searchWrap);
    header.appendChild(legendEl);

    const canvas = document.createElement('canvas');
    canvas.className = 'graph-canvas';

    cellEl.appendChild(header);
    cellEl.appendChild(canvas);

    searchInput.addEventListener('input', () => {
        const q = searchInput.value.trim();
        if (!q) { dropdown.style.display = 'none'; return; }
        let re;
        try { re = new RegExp(q, 'i'); } catch { dropdown.style.display = 'none'; return; }
        const all     = configControls.flatMap(c => c.channels.map(ch => ch.refDes));
        const matches = all.filter(r => re.test(r)).slice(0, 20);
        dropdown.innerHTML = '';
        if (!matches.length) { dropdown.style.display = 'none'; return; }
        for (const refDes of matches) {
            const item = mkEl('div', 'graph-dropdown-item', refDes);
            item.addEventListener('mousedown', (e) => {
                e.preventDefault();
                addChannelToCell(tabId, cellIdx, refDes);
                searchInput.value = '';
                dropdown.style.display = 'none';
            });
            dropdown.appendChild(item);
        }
        dropdown.style.display = '';
    });
    searchInput.addEventListener('blur', () => setTimeout(() => { dropdown.style.display = 'none'; }, 150));

    return cellEl;
}

function createCellChart(canvas) {
    return new Chart(canvas, {
        type: 'line',
        data: { datasets: [] },
        options: {
            animation:  false,
            responsive: true,
            maintainAspectRatio: false,
            parsing:    false,
            scales: {
                x: {
                    type: 'linear',
                    ticks: {
                        color: '#6e7681',
                        maxTicksLimit: 6,
                        maxRotation: 0,
                        callback: (v) => new Date(v * 1000).toLocaleTimeString('en-US', { hour12: false })
                    },
                    grid: { color: '#21262d' }
                },
                y: {
                    ticks: { color: '#6e7681' },
                    grid:  { color: '#21262d' }
                }
            },
            plugins: {
                legend:  { display: false },
                tooltip: {
                    mode: 'index', intersect: false,
                    callbacks: {
                        title: (items) => new Date(items[0].parsed.x * 1000)
                            .toLocaleTimeString('en-US', { hour12: false, fractionalSecondDigits: 3 })
                    }
                }
            },
            elements: {
                point: { radius: 0 },
                line:  { borderWidth: 1.5 }
            }
        }
    });
}

function addChannelToCell(tabId, cellIdx, refDes) {
    const cell = graphState[tabId]?.cells[cellIdx];
    if (!cell || cell.channels.some(c => c.refDes === refDes)) return;

    const used  = cell.channels.map(c => c.color);
    const color = CHART_COLORS.find(c => !used.includes(c)) ?? CHART_COLORS[cell.channels.length % CHART_COLORS.length];
    cell.channels.push({ refDes, color, hidden: false });

    if (!channelBuffers[refDes]) channelBuffers[refDes] = { ts: [], vals: [] };
    activeGraphChannels.add(refDes);

    addDatasetToChart(cell.chart, refDes, color, false);
    updateCellLegend(tabId, cellIdx);
}

function removeChannelFromCell(tabId, cellIdx, refDes) {
    const cell = graphState[tabId]?.cells[cellIdx];
    if (!cell) return;
    cell.channels = cell.channels.filter(c => c.refDes !== refDes);
    const dsIdx = cell.chart.data.datasets.findIndex(d => d.label === refDes);
    if (dsIdx !== -1) { cell.chart.data.datasets.splice(dsIdx, 1); cell.chart.update('none'); }
    updateCellLegend(tabId, cellIdx);
    updateActiveGraphChannels();
}

function addDatasetToChart(chart, refDes, color, hidden) {
    const buf  = channelBuffers[refDes];
    const data = buf ? buf.ts.map((t, i) => ({ x: t, y: buf.vals[i] })) : [];
    chart.data.datasets.push({
        label:           refDes,
        data,
        borderColor:     color,
        backgroundColor: color + '22',
        hidden,
        fill:            false
    });
    chart.update('none');
}

function updateCellLegend(tabId, cellIdx) {
    const cell   = graphState[tabId]?.cells[cellIdx];
    if (!cell?.cellEl) return;
    const legend = cell.cellEl.querySelector('.graph-legend');
    legend.innerHTML = '';

    for (const ch of cell.channels) {
        const item = mkEl('div', 'legend-item');

        const swatch = document.createElement('input');
        swatch.type  = 'color';
        swatch.value = ch.color;
        swatch.className = 'legend-swatch';
        swatch.title = 'Change color';
        swatch.addEventListener('input', () => {
            ch.color = swatch.value;
            const ds = cell.chart.data.datasets.find(d => d.label === ch.refDes);
            if (ds) { ds.borderColor = ch.color; ds.backgroundColor = ch.color + '22'; }
            cell.chart.update('none');
        });

        const lbl = mkEl('span', `legend-label${ch.hidden ? ' legend-hidden' : ''}`, ch.refDes);
        lbl.title = 'Click to toggle visibility';
        lbl.addEventListener('click', () => {
            ch.hidden = !ch.hidden;
            lbl.classList.toggle('legend-hidden', ch.hidden);
            const ds = cell.chart.data.datasets.find(d => d.label === ch.refDes);
            if (ds) { ds.hidden = ch.hidden; cell.chart.update('none'); }
        });

        const rmBtn = mkEl('button', 'legend-remove', '×');
        rmBtn.title = 'Remove';
        rmBtn.addEventListener('click', () => removeChannelFromCell(tabId, cellIdx, ch.refDes));

        item.appendChild(swatch);
        item.appendChild(lbl);
        item.appendChild(rmBtn);
        legend.appendChild(item);
    }
}

function updateActiveGraphChannels() {
    activeGraphChannels.clear();
    for (const state of Object.values(graphState)) {
        for (const cell of state.cells) {
            for (const ch of cell.channels) activeGraphChannels.add(ch.refDes);
        }
    }
    for (const refDes of Object.keys(channelBuffers)) {
        if (!activeGraphChannels.has(refDes)) delete channelBuffers[refDes];
    }
}

function bufferGraphData(data) {
    if (!activeGraphChannels.size) return;
    const now    = Date.now() / 1000;
    const cutoff = now - CONFIG.graphBufferMinutes * 60;
    for (const refDes of activeGraphChannels) {
        if (!(refDes in data)) continue;
        const buf = channelBuffers[refDes];
        if (!buf) continue;
        buf.ts.push(now);
        buf.vals.push(data[refDes]);
        while (buf.ts.length && buf.ts[0] < cutoff) { buf.ts.shift(); buf.vals.shift(); }
    }
}

function updateAllGraphs() {
    for (const [tabId, state] of Object.entries(graphState)) {
        const tab = tabs.find(t => t.id === tabId);
        if (!tab || tab.contentEl.style.display === 'none') continue;
        for (const cell of state.cells) {
            if (!cell.chart) continue;
            let dirty = false;
            for (const ds of cell.chart.data.datasets) {
                const buf = channelBuffers[ds.label];
                if (!buf) continue;
                ds.data = buf.ts.map((t, i) => ({ x: t, y: buf.vals[i] }));
                dirty = true;
            }
            if (dirty) cell.chart.update('none');
        }
    }
}

function resizeGraphCharts(tabId) {
    const state = graphState[tabId];
    if (!state) return;
    for (const cell of state.cells) cell.chart?.resize();
}

function cleanupGraphTab(tabId) {
    const state = graphState[tabId];
    if (!state) return;
    for (const cell of state.cells) cell.chart?.destroy();
    delete graphState[tabId];
    updateActiveGraphChannels();
}


// =============================================================================
// Console tab
// =============================================================================

function buildConsoleContent(tab) {
    consoleTabs.push(tab);

    const wrapper  = mkEl('div', 'console-tab');
    const toolbar  = mkEl('div', 'console-toolbar');

    const filterChk = document.createElement('input');
    filterChk.type = 'checkbox'; filterChk.checked = true; filterChk.className = 'console-chk';
    filterChk.id = `flt-${tab.id}`;
    const filterLbl = document.createElement('label');
    filterLbl.htmlFor = filterChk.id; filterLbl.textContent = 'Hide data messages'; filterLbl.className = 'console-lbl';

    const limLbl   = mkEl('label', 'console-lbl', 'Buffer: ');
    const limInput = document.createElement('input');
    limInput.type = 'number'; limInput.min = 50; limInput.max = 5000; limInput.value = CONFIG.consoleBufferLimit;
    limInput.className = 'console-buf-input';
    limLbl.appendChild(limInput);

    const clearBtn = mkEl('button', 'btn console-clear', 'Clear');

    toolbar.appendChild(filterChk); toolbar.appendChild(filterLbl);
    toolbar.appendChild(limLbl);
    toolbar.appendChild(clearBtn);

    const logEl = mkEl('div', 'console-log');
    wrapper.appendChild(toolbar);
    wrapper.appendChild(logEl);
    tab.contentEl.appendChild(wrapper);

    tab._consoleLogEl  = logEl;
    tab._filterData    = () => filterChk.checked;
    tab._consoleLimit  = () => parseInt(limInput.value) || CONFIG.consoleBufferLimit;

    for (const entry of consoleLog) appendConsoleEntry(tab, entry);

    filterChk.addEventListener('change', () => reRenderConsole(tab));
    clearBtn.addEventListener('click',   () => { consoleLog.length = 0; logEl.innerHTML = ''; });
}

function logConsole(dir, msg) {
    const entry = { time: Date.now(), dir, msg };
    consoleLog.push(entry);

    const limit = Math.max(CONFIG.consoleBufferLimit,
        ...consoleTabs.map(t => t._consoleLimit?.() ?? CONFIG.consoleBufferLimit));
    while (consoleLog.length > limit) consoleLog.shift();

    for (const tab of consoleTabs) appendConsoleEntry(tab, entry);
}

function appendConsoleEntry(tab, entry) {
    if (tab._filterData?.() && entry.dir === 'in' && entry.msg.type === 'data') return;
    const logEl = tab._consoleLogEl;
    if (!logEl) return;

    const el   = mkEl('div', `console-entry console-${entry.dir}`);
    const time = new Date(entry.time).toLocaleTimeString('en-US', {
        hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit', fractionalSecondDigits: 3
    });
    el.appendChild(mkEl('span', 'console-time', time));
    el.appendChild(mkEl('span', 'console-dir',  entry.dir === 'out' ? '→' : '←'));
    el.appendChild(mkEl('span', 'console-msg',  JSON.stringify(entry.msg)));
    logEl.appendChild(el);
    logEl.scrollTop = logEl.scrollHeight;

    const limit = tab._consoleLimit?.() ?? CONFIG.consoleBufferLimit;
    while (logEl.children.length > limit) logEl.removeChild(logEl.firstChild);
}

function reRenderConsole(tab) {
    if (!tab._consoleLogEl) return;
    tab._consoleLogEl.innerHTML = '';
    for (const entry of consoleLog) appendConsoleEntry(tab, entry);
}


// =============================================================================
// Dev tab
// =============================================================================

function buildDevContent(tab) {
    devTabs.push(tab);
    const hasMem = !!performance.memory;

    tab.contentEl.innerHTML = `
        <div class="dev-tab">
            <div class="dev-section">
                <h3 class="dev-heading">WebSocket</h3>
                <table class="dev-table">
                    <tr><td>Endpoint</td>          <td class="mono">${CONFIG.wsUrl}</td></tr>
                    <tr><td>State</td>              <td class="dev-state">--</td></tr>
                    <tr><td>Uptime</td>             <td class="dev-uptime">--</td></tr>
                    <tr><td>Messages received</td>  <td class="dev-msg-count">0</td></tr>
                    <tr><td>Message rate</td>       <td class="dev-msg-rate">--</td></tr>
                    <tr><td>Missed data cycles</td> <td class="dev-missed">0</td></tr>
                </table>
            </div>
            ${hasMem ? `
            <div class="dev-section">
                <h3 class="dev-heading">Browser Memory</h3>
                <table class="dev-table">
                    <tr><td>JS Heap Used</td>  <td class="dev-heap-used">--</td></tr>
                    <tr><td>JS Heap Total</td> <td class="dev-heap-total">--</td></tr>
                </table>
            </div>` : ''}
        </div>`;

    tab._devEls = {
        state:     tab.contentEl.querySelector('.dev-state'),
        uptime:    tab.contentEl.querySelector('.dev-uptime'),
        msgCount:  tab.contentEl.querySelector('.dev-msg-count'),
        msgRate:   tab.contentEl.querySelector('.dev-msg-rate'),
        missed:    tab.contentEl.querySelector('.dev-missed'),
        heapUsed:  tab.contentEl.querySelector('.dev-heap-used'),
        heapTotal: tab.contentEl.querySelector('.dev-heap-total'),
    };
}

function refreshDevTabs() {
    if (!devTabs.length) return;
    const stateStr  = ws ? (['CONNECTING','OPEN','CLOSING','CLOSED'][ws.readyState] ?? '--') : 'CLOSED';
    const uptime    = devStats.connectedAt ? Math.floor((Date.now() - devStats.connectedAt) / 1000) : null;
    const uptimeStr = uptime !== null ? fmtUptime(uptime) : '--';
    const rate      = ((devStats.msgCount - devStats.lastWindowCount) / 2).toFixed(1);
    devStats.lastWindowCount = devStats.msgCount;

    for (const tab of devTabs) {
        const e = tab._devEls;
        if (!e) continue;
        if (e.state)    e.state.textContent    = stateStr;
        if (e.uptime)   e.uptime.textContent   = uptimeStr;
        if (e.msgCount) e.msgCount.textContent = devStats.msgCount;
        if (e.msgRate)  e.msgRate.textContent  = `${rate} msg/s`;
        if (e.missed)   e.missed.textContent   = devStats.missedCycles;
        if (e.heapUsed && performance.memory) {
            e.heapUsed.textContent  = fmtBytes(performance.memory.usedJSHeapSize);
            e.heapTotal.textContent = fmtBytes(performance.memory.totalJSHeapSize);
        }
    }
}

function fmtUptime(s) {
    const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), sec = s % 60;
    return `${String(h).padStart(2,'0')}:${String(m).padStart(2,'0')}:${String(sec).padStart(2,'0')}`;
}

function fmtBytes(b) {
    return b > 1e6 ? (b / 1e6).toFixed(1) + ' MB' : (b / 1e3).toFixed(1) + ' KB';
}


// =============================================================================
// Card builders
// =============================================================================

const isCmd = ch => ch.role === 'cmd-bool' || ch.role === 'cmd-pct' || ch.role === 'cmd-float';

function buildCard(ctrl, tab) {
    switch (ctrl.type) {
        case 'pressure':
        case 'temperature':
        case 'flowMeter':
        case 'thrust':
        case 'tank':       return buildSensorCard(ctrl, tab);
        case 'valve':      return buildValveCard(ctrl, tab);
        case 'bangBang':   return buildBangBangCard(ctrl, tab);
        case 'ignition':   return buildIgnitionCard(ctrl, tab);
        case 'digitalOut': return buildDigitalOutCard(ctrl, tab);
        case 'VFD':        return buildVFDCard(ctrl, tab);
        default:           return buildSensorCard(ctrl, tab);
    }
}

function buildSensorCard(ctrl, tab) {
    const card = mkEl('div', `card card-sensor card-${ctrl.type}`);
    card.appendChild(mkEl('div', 'card-desc',   ctrl.description || ctrl.refDes));
    card.appendChild(mkEl('div', 'card-refdes', ctrl.refDes));

    for (const ch of ctrl.channels) {
        const row   = mkEl('div', 'sensor-row');
        const valEl = mkEl('span', 'value stale', '--');
        row.appendChild(valEl);
        row.appendChild(mkEl('span', 'units', ch.units ?? ''));
        card.appendChild(row);
        tab.channelUpdaters[ch.refDes] = (v) => {
            valEl.textContent = typeof v === 'number' ? v.toFixed(2) : String(v);
            valEl.classList.remove('stale');
        };
    }
    return card;
}

function buildValveCard(ctrl, tab) {
    const card = mkEl('div', `card card-valve subtype-${ctrl.subType}`);
    card.appendChild(mkEl('div', 'card-desc',   ctrl.description || ctrl.refDes));
    card.appendChild(mkEl('div', 'card-refdes', ctrl.refDes));

    const cmdCh = ctrl.channels.find(c => isCmd(c));
    const fbCh  = ctrl.channels.find(c => !isCmd(c) && c.refDes.endsWith('-FB'));
    const posCh = ctrl.channels.find(c => !isCmd(c) && c.refDes.endsWith('-POS'));

    if (fbCh) {
        const fbRow = mkEl('div', 'fb-row');
        const led   = mkEl('span', 'led led-unknown');
        const lbl   = mkEl('span', 'fb-label stale', 'FB: --');
        fbRow.appendChild(led); fbRow.appendChild(lbl);
        card.appendChild(fbRow);
        tab.channelUpdaters[fbCh.refDes] = (v) => {
            const open = Boolean(v);
            led.className   = `led ${open ? 'led-open' : 'led-closed'}`;
            lbl.textContent = `FB: ${open ? 'OPEN' : 'CLOSED'}`;
            lbl.classList.remove('stale');
        };
    }

    if (posCh) {
        const row   = mkEl('div', 'sensor-row');
        const valEl = mkEl('span', 'value stale', '--');
        row.appendChild(valEl); row.appendChild(mkEl('span', 'units', '%'));
        card.appendChild(row);
        tab.channelUpdaters[posCh.refDes] = (v) => {
            valEl.textContent = typeof v === 'number' ? v.toFixed(1) : String(v);
            valEl.classList.remove('stale');
        };
    }

    if (cmdCh) {
        const btnRow = mkEl('div', 'btn-row');
        if (cmdCh.role === 'cmd-pct') {
            const slider = document.createElement('input');
            slider.type = 'range'; slider.min = 0; slider.max = 100; slider.value = 0;
            slider.className = 'pos-slider';
            const posOut = mkEl('span', 'pos-out', '0%');
            slider.addEventListener('input',  () => posOut.textContent = `${slider.value}%`);
            slider.addEventListener('change', () => sendCommand(cmdCh.refDes, parseFloat(slider.value)));
            btnRow.appendChild(slider); btnRow.appendChild(posOut);
        } else {
            const openBtn  = mkEl('button', 'btn btn-open',  'OPEN');
            const closeBtn = mkEl('button', 'btn btn-close', 'CLOSE');
            openBtn.addEventListener('click',  () => sendCommand(cmdCh.refDes, 1));
            closeBtn.addEventListener('click', () => sendCommand(cmdCh.refDes, 0));
            btnRow.appendChild(openBtn); btnRow.appendChild(closeBtn);
        }
        card.appendChild(btnRow);
    }
    return card;
}

function buildBangBangCard(ctrl, tab) {
    const card = mkEl('div', 'card card-bangbang');
    card.appendChild(mkEl('div', 'card-desc',   ctrl.description || ctrl.refDes));
    card.appendChild(mkEl('div', 'card-refdes', ctrl.refDes));
    if (ctrl.details?.senseRefDes) {
        card.appendChild(mkEl('div', 'sense-label', `Sense: ${ctrl.details.senseRefDes}`));
    }
    for (const ch of ctrl.channels) {
        const row = mkEl('div', 'fb-row');
        const led = mkEl('span', 'led led-unknown');
        const lbl = mkEl('span', 'fb-label stale', `${ch.refDes}: --`);
        row.appendChild(led); row.appendChild(lbl);
        card.appendChild(row);
        tab.channelUpdaters[ch.refDes] = (v) => {
            const on = Boolean(v);
            led.className   = `led ${on ? 'led-open' : 'led-closed'}`;
            lbl.textContent = `${ch.refDes}: ${on ? 'ON' : 'OFF'}`;
            lbl.classList.remove('stale');
        };
    }
    return card;
}

function buildIgnitionCard(ctrl, tab) {
    const card = mkEl('div', 'card card-ignition');
    card.appendChild(mkEl('div', 'card-desc',   ctrl.description || ctrl.refDes));
    card.appendChild(mkEl('div', 'card-refdes', ctrl.refDes));

    for (const ch of ctrl.channels.filter(c => !isCmd(c))) {
        const row = mkEl('div', 'fb-row');
        const led = mkEl('span', 'led led-unknown');
        const lbl = mkEl('span', 'fb-label stale', `${ch.refDes}: --`);
        row.appendChild(led); row.appendChild(lbl);
        card.appendChild(row);
        tab.channelUpdaters[ch.refDes] = (v) => {
            const active = Boolean(v);
            led.className   = `led ${active ? 'led-active' : 'led-inactive'}`;
            lbl.textContent = `${ch.refDes}: ${active ? 'ACTIVE' : 'INACTIVE'}`;
            lbl.classList.remove('stale');
        };
    }

    const cmdCh = ctrl.channels.find(c => isCmd(c));
    if (cmdCh) {
        const btnRow = mkEl('div', 'btn-row ignition-row');
        const armId  = `arm-${ctrl.refDes}-${tab.id}`;
        const armBox = document.createElement('input');
        armBox.type = 'checkbox'; armBox.id = armId; armBox.className = 'arm-checkbox';
        const armLbl = document.createElement('label');
        armLbl.htmlFor = armId; armLbl.textContent = 'ARM'; armLbl.className = 'arm-label';
        const fireBtn = mkEl('button', 'btn btn-fire', 'FIRE');
        fireBtn.disabled = true;
        armBox.addEventListener('change', () => { fireBtn.disabled = !armBox.checked; });
        fireBtn.addEventListener('click', () => {
            if (armBox.checked) {
                sendCommand(cmdCh.refDes, 1);
                armBox.checked = false; fireBtn.disabled = true;
            }
        });
        btnRow.appendChild(armBox); btnRow.appendChild(armLbl); btnRow.appendChild(fireBtn);
        card.appendChild(btnRow);
    }
    return card;
}

function buildDigitalOutCard(ctrl, _tab) {
    const card = mkEl('div', 'card card-digital');
    card.appendChild(mkEl('div', 'card-desc',   ctrl.description || ctrl.refDes));
    card.appendChild(mkEl('div', 'card-refdes', ctrl.refDes));
    const cmdCh = ctrl.channels.find(c => isCmd(c)) ?? ctrl.channels[0];
    if (cmdCh) {
        const btnRow = mkEl('div', 'btn-row');
        const onBtn  = mkEl('button', 'btn btn-open',  'ON');
        const offBtn = mkEl('button', 'btn btn-close', 'OFF');
        onBtn.addEventListener('click',  () => sendCommand(cmdCh.refDes, 1));
        offBtn.addEventListener('click', () => sendCommand(cmdCh.refDes, 0));
        btnRow.appendChild(onBtn); btnRow.appendChild(offBtn);
        card.appendChild(btnRow);
    }
    return card;
}

function buildVFDCard(ctrl, _tab) {
    const card = mkEl('div', 'card card-vfd');
    card.appendChild(mkEl('div', 'card-desc',   ctrl.description || ctrl.refDes));
    card.appendChild(mkEl('div', 'card-refdes', ctrl.refDes));
    const cmdCh = ctrl.channels.find(c => isCmd(c));
    if (cmdCh) {
        const row   = mkEl('div', 'btn-row');
        const input = document.createElement('input');
        input.type = 'number'; input.min = 0; input.max = 60; input.value = 0;
        input.className = 'vfd-input';
        const sendBtn = mkEl('button', 'btn', 'Set Hz');
        sendBtn.addEventListener('click', () => sendCommand(cmdCh.refDes, parseFloat(input.value)));
        row.appendChild(input); row.appendChild(sendBtn);
        card.appendChild(row);
    }
    return card;
}


// =============================================================================
// Status bar & timestamp
// =============================================================================

function setStatus(state, text) {
    document.getElementById('status-indicator').className = `status-indicator status-${state}`;
    document.getElementById('status-text').textContent = text;
}

function updateTimestamp(unixSeconds) {
    const el = document.getElementById('timestamp');
    if (!el) return;
    el.textContent = new Date(unixSeconds * 1000).toLocaleTimeString('en-US', {
        hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit', fractionalSecondDigits: 3
    });
}


// =============================================================================
// Helpers
// =============================================================================

function mkEl(tag, className, text) {
    const e = document.createElement(tag);
    if (className) e.className = className;
    if (text !== undefined) e.textContent = text;
    return e;
}


// =============================================================================
// Init
// =============================================================================

document.getElementById('tab-add').addEventListener('click', () => addTab('frontPanel'));

addTab('frontPanel');                               // default tab on load
setInterval(updateAllGraphs,  500);                // graph refresh at 2 Hz
setInterval(refreshDevTabs,  2000);                // dev stats refresh every 2s
connect();

// One-time boot hint overlay
(function showBootHint() {
    const overlay = document.createElement('div');
    overlay.className = 'boot-overlay';
    overlay.innerHTML = `
        <div class="boot-hint">
            <div>Right-click any tab to change its type</div>
            <div><span class="boot-hint-types">Front Panel &nbsp;·&nbsp; Data View &nbsp;·&nbsp; Graph &nbsp;·&nbsp; Console &nbsp;·&nbsp; Dev</span></div>
            <div style="margin-top:10px"><span class="boot-hint-dismiss">Double-click a tab to rename &nbsp;·&nbsp; Click anywhere to dismiss</span></div>
        </div>`;
    document.body.appendChild(overlay);
    overlay.addEventListener('click', () => overlay.remove(), { once: true });
    document.addEventListener('keydown', () => overlay.remove(), { once: true });
}());
