// =============================================================================
// Graph tab
// =============================================================================

// Custom tooltip positioner: snaps to the active data point closest to the cursor
Chart.Tooltip.positioners.datapoint = (elements, eventPosition) => {
    if (!elements.length) return false;
    let closest = elements[0].element;
    let minDy = Math.abs(closest.y - eventPosition.y);
    for (const { element } of elements) {
        const dy = Math.abs(element.y - eventPosition.y);
        if (dy < minDy) { minDy = dy; closest = element; }
    }
    return { x: closest.x, y: closest.y };
};

function graphGetDesc(refDes) {
    return lookupChannel(refDes)?.ctrl.description || '';
}

function graphGetUnits(refDes) {
    return lookupChannel(refDes)?.ch.units || '';
}

// =============================================================================
// State-machine channels on the chart (design item 14)
// =============================================================================
//
// SM-<NAME>-STATE is an auto-generated, read-only softchan (see
// controlnode/softchan/store.go RegisterStateMachineChannels and
// docs/websocket-protocol.md) carrying the machine's current state as a
// numeric INDEX on the ordinary `data` path — nothing new on the wire. It is
// chartable exactly like any other channel; what's different is how it's
// drawn and read back:
//   - stepped, not interpolated (a straight segment between index 3 and 4
//     would depict a state that never existed) — see addDatasetToChart.
//   - its own y-axis by default — the index shares units with nothing else
//     on the plot (see graphDefaultYAxisId).
//   - its tooltip resolves the index through state_config (machineStateConfig,
//     ws.js applyStateConfig) back to a name — see the tooltip label
//     callback in createCellChart.
//
// The match is done on refDes alone (no state_config lookup) so stepping and
// axis placement are correct even before state_config has arrived, or for a
// machine that no longer exists — only the tooltip's name lookup needs the
// config, and it degrades to the raw index when that lookup fails.
//
// NOTE for item 04 (server-side aggregation, not yet built): when history
// gets bucketed, this channel wants **last-per-bucket**, not the min/max a
// bucketed analogue reading wants. Averaging or min/maxing a state index
// produces a fractional value with no corresponding state and must not
// happen here — bucketing needs to be per-channel policy, not one global
// rule, once item 04 lands.
function graphStateChannelMachine(refDes) {
    const m = /^SM-(.+)-STATE$/.exec(refDes);
    return m ? m[1] : null;
}

// graphStateName resolves a state INDEX to its NAME via state_config for the
// machine named in refDes. Returns null (never "undefined") when refDes
// isn't a state channel, state_config hasn't arrived yet, or the index has
// no matching entry (stale config, machine removed) — callers fall back to
// the raw number in all of those cases.
function graphStateName(refDes, index) {
    const machine = graphStateChannelMachine(refDes);
    if (!machine) return null;
    const cfg = machineStateConfig[machine];
    const state = cfg?.states?.find(s => s.index === index);
    return state ? state.name : null;
}

// graphChannelIsDiscrete decides whether refDes carries a small, fixed set of
// values rather than something that genuinely moves through the readings
// between two samples — item 14's SM-<NAME>-STATE channels, a boolean
// command (role cmd-bool), or the boolean side of an IO-CMD_IO-FB valve pair
// (item 15). A continuous channel — cmd-pct/cmd-float commands, POS-FB
// feedback, an ordinary sensor reading — must NOT be stepped: it really does
// take the values in between, and forcing a step there draws the "state that
// never existed" bug item 14's own comment describes, from the other side.
// The valve case needs the parent CONTROL, not just the channel — a
// feedback channel's role is empty/'sensor' either way; only ctrl.subType
// says whether that feedback is a pair of limit switches (IO-FB) or a
// continuous position (POS-FB) — the same distinction _valveSubtypeInfo
// (pidRender.js) draws for the glyph itself (limit ticks vs. an arc).
function graphChannelIsDiscrete(refDes) {
    if (graphStateChannelMachine(refDes)) return true;
    const info = lookupChannel(refDes);
    if (!info) return false;
    const { ctrl, ch } = info;
    if (ch.role === 'cmd-bool') return true;
    if ((ch.role === '' || ch.role === 'sensor') && ctrl?.type === 'valve') {
        return (ctrl.subType || '').toUpperCase().includes('IO-FB');
    }
    return false;
}

// graphDefaultYAxisId picks the y-axis a newly-added channel starts on.
// State-index channels default to the first axis (1-6) not already used in
// this cell rather than the usual shared axis 1, so they don't land on the
// same scale as an analogue reading and make both unreadable. The operator
// can still move it with the axis badge (updateCellPanel) same as any
// channel; this only sets the starting point.
function graphDefaultYAxisId(cell, refDes) {
    if (!graphStateChannelMachine(refDes)) return 1;
    const used = new Set(cell.channels.map(c => c.yAxisId));
    for (let i = 1; i <= 6; i++) if (!used.has(i)) return i;
    return 1; // all six axes already taken; badge click still moves it
}

// =============================================================================
// Promote a channel from the object side panel to the Graph tab (item 11)
// =============================================================================

// promoteChannelToGraph is the one-click side of the side panel: "the channel
// you are looking at" lands in a real, pannable/zoomable/saveable graph cell,
// not the panel's own stripped-down chart. Deliberately does not offer a
// picker for which tab/cell to target — that is exactly the kind of chrome
// item 13 (time presets) got cut for being more control than the workflow
// needs. The rule instead:
//   - reuse the currently active tab if it is already a Graph tab, so
//     repeated promotes from the sidebar land where the operator is already
//     looking rather than jumping them around,
//   - else reuse the first existing Graph tab, so one click doesn't multiply
//     tabs,
//   - else create one.
// Always targets cell 0 of that tab. A multi-cell grid has no "the cell the
// operator meant" without asking them to pick one, and addChannelToCell is
// already idempotent per refDes, so promoting the same channel twice is a
// no-op rather than a duplicate dataset.
function promoteChannelToGraph(refDes) {
    let tab = tabs.find(t => t.id === activeTabId && t.type === 'graph')
           || tabs.find(t => t.type === 'graph');
    if (!tab) tab = addTab('graph');
    addChannelToCell(tab.id, 0, refDes);
    activateTab(tab.id);
}

// =============================================================================
// Graph layout YAML save / load
// =============================================================================

function graphLayoutToYaml(tabId) {
    const state = graphState[tabId];
    if (!state) return '';
    const q = s => /[:#{}[\],&*?|<>=!%@`'" ]/.test(String(s)) ? "'" + String(s).replace(/'/g, "''") + "'" : String(s);
    let y = 'version: 1\n';
    y += 'grid:\n';
    y += `  rows: ${state.rows}\n`;
    y += `  cols: ${state.cols}\n`;
    y += 'cells:\n';
    for (const cell of state.cells) {
        y += `  - viewWindowSec: ${cell.viewWindowSec ?? 60}\n`;
        y += `    channels:\n`;
        if (!cell.channels.length) { y += `      []\n`; continue; }
        for (const ch of cell.channels) {
            y += `      - refDes: ${q(ch.refDes)}\n`;
            y += `        color: ${q(ch.color)}\n`;
            y += `        yAxisId: ${ch.yAxisId ?? 1}\n`;
            y += `        hidden: ${ch.hidden ? 'true' : 'false'}\n`;
        }
    }
    return y;
}

function _parseGraphYamlKV(content) {
    const ci = content.indexOf(':');
    if (ci === -1) return null;
    return { key: content.slice(0, ci).trim(), val: content.slice(ci + 1).trim() };
}

function _unquoteYaml(s) {
    s = s.trim();
    if (s.startsWith("'") && s.endsWith("'")) return s.slice(1, -1).replace(/''/g, "'");
    if (s.startsWith('"') && s.endsWith('"'))  return s.slice(1, -1).replace(/\\"/g, '"');
    return s;
}

function graphLayoutFromYaml(text) {
    let rows = 1, cols = 1;
    const cells = [];
    let curCell = null, curCh = null;
    for (const raw of text.split('\n')) {
        if (!raw.trim() || raw.trim().startsWith('#')) continue;
        const indent  = raw.search(/\S/);
        const content = raw.trim();
        if (indent === 2 && content.startsWith('- ')) {
            curCh = null;
            curCell = { viewWindowSec: 60, channels: [] };
            cells.push(curCell);
            const kv = _parseGraphYamlKV(content.slice(2));
            if (kv?.key === 'viewWindowSec') curCell.viewWindowSec = parseFloat(kv.val);
        } else if (indent === 2) {
            const kv = _parseGraphYamlKV(content);
            if (kv?.key === 'rows') rows = parseInt(kv.val);
            else if (kv?.key === 'cols') cols = parseInt(kv.val);
        } else if (indent === 4 && curCell) {
            const kv = _parseGraphYamlKV(content);
            if (kv?.key === 'viewWindowSec') curCell.viewWindowSec = parseFloat(kv.val);
        } else if (indent === 6 && curCell && content.startsWith('- ')) {
            curCh = { refDes: '', color: CHART_COLORS[0], yAxisId: 1, hidden: false };
            curCell.channels.push(curCh);
            const kv = _parseGraphYamlKV(content.slice(2));
            if (kv) _applyChKV(curCh, kv.key, _unquoteYaml(kv.val));
        } else if (indent === 8 && curCh) {
            const kv = _parseGraphYamlKV(content);
            if (kv) _applyChKV(curCh, kv.key, _unquoteYaml(kv.val));
        }
    }
    return { rows, cols, cells };
}

function _applyChKV(ch, key, val) {
    if      (key === 'refDes')   ch.refDes   = val;
    else if (key === 'color')    ch.color    = val;
    else if (key === 'yAxisId')  ch.yAxisId  = parseInt(val);
    else if (key === 'hidden')   ch.hidden   = val === 'true';
}

function applyGraphLayout(layout, tabId) {
    resizeGraphGrid(tabId, layout.rows, layout.cols);
    const state = graphState[tabId];
    for (let i = 0; i < layout.cells.length && i < state.cells.length; i++) {
        const lc   = layout.cells[i];
        const cell = state.cells[i];
        cell.viewWindowSec = lc.viewWindowSec;
        for (const lch of lc.channels) {
            if (!lch.refDes) continue;
            addChannelToCell(tabId, i, lch.refDes);
            const ch = cell.channels.find(c => c.refDes === lch.refDes);
            const ds = cell.chart?.data.datasets.find(d => d.label === lch.refDes);
            if (ch) { ch.color = lch.color; ch.hidden = lch.hidden; ch.yAxisId = lch.yAxisId; }
            if (ds) { ds.borderColor = lch.color; ds.backgroundColor = lch.color + '22'; ds.hidden = lch.hidden; ds.yAxisID = 'y' + lch.yAxisId; }
        }
        syncYAxisVisibility(cell);
        updateCellPanel(tabId, i);
    }
}

function buildGraphContent(tab) {
    graphState[tab.id] = { rows: 1, cols: 1, gridEl: null, cells: [], sizeBtn: null, showDesc: true, _dismissHandler: null };

    const wrapper = mkEl('div', 'graph-tab');
    const toolbar = mkEl('div', 'graph-toolbar');

    // Grid size dropdown button
    const sizeWrap = mkEl('div', 'graph-size-wrap');
    const sizeBtn  = mkEl('button', 'graph-size-btn', '1 × 1');
    graphState[tab.id].sizeBtn = sizeBtn;

    const popover = mkEl('div', 'graph-size-popover');
    popover.style.display = 'none';
    
    const gridContainer = mkEl('div', 'graph-size-grid');
    const cells = [];
    for (let r = 1; r <= 5; r++) {
        for (let c = 1; c <= 5; c++) {
            const cell = mkEl('div', 'graph-size-cell');
            cell.dataset.row = r;
            cell.dataset.col = c;
            cells.push(cell);
            
            cell.addEventListener('mouseenter', () => {
                cells.forEach(cel => cel.classList.remove('graph-size-cell--filled'));
                cells.forEach(cel => {
                    const cr = parseInt(cel.dataset.row);
                    const cc = parseInt(cel.dataset.col);
                    if (cr <= r && cc <= c) cel.classList.add('graph-size-cell--filled');
                });
            });
            
            cell.addEventListener('click', () => {
                popover.style.display = 'none';
                resizeGraphGrid(tab.id, r, c);
            });
            
            gridContainer.appendChild(cell);
        }
    }
    popover.appendChild(gridContainer);
    
    sizeBtn.addEventListener('click', (e) => {
        e.stopPropagation();
        popover.style.display = popover.style.display === 'none' ? '' : 'none';
    });
    sizeWrap.appendChild(sizeBtn);
    sizeWrap.appendChild(popover);
    toolbar.appendChild(sizeWrap);

    // Save layout button
    const saveBtn = mkEl('button', 'graph-desc-btn', 'Save');
    saveBtn.title = 'Save graph layout as YAML';
    saveBtn.addEventListener('click', () => {
        const yaml = graphLayoutToYaml(tab.id);
        const blob = new Blob([yaml], { type: 'text/yaml' });
        const url  = URL.createObjectURL(blob);
        const a    = document.createElement('a');
        a.href = url; a.download = 'graph_layout.yaml';
        a.click();
        URL.revokeObjectURL(url);
    });
    toolbar.appendChild(saveBtn);

    // Load layout button (backed by hidden file input)
    const loadBtn   = mkEl('button', 'graph-desc-btn', 'Load');
    const fileInput = document.createElement('input');
    fileInput.type   = 'file';
    fileInput.accept = '.yaml,.yml';
    fileInput.style.display = 'none';
    fileInput.addEventListener('change', () => {
        const file = fileInput.files[0];
        if (!file) return;
        const reader = new FileReader();
        reader.onload = (e) => {
            try {
                const layout = graphLayoutFromYaml(e.target.result);
                applyGraphLayout(layout, tab.id);
            } catch (err) {
                console.error('Failed to load graph layout:', err);
            }
        };
        reader.readAsText(file);
        fileInput.value = '';
    });
    loadBtn.title = 'Load graph layout from YAML';
    loadBtn.addEventListener('click', () => fileInput.click());
    toolbar.appendChild(loadBtn);
    toolbar.appendChild(fileInput);

    wrapper.appendChild(toolbar);

    const gridEl = mkEl('div', 'graph-grid');
    wrapper.appendChild(gridEl);
    tab.contentEl.appendChild(wrapper);
    tab.contentEl.classList.add('tab-content--fixed');
    graphState[tab.id].gridEl = gridEl;

    const dismiss = (e) => { if (!sizeWrap.contains(e.target)) popover.style.display = 'none'; };
    const handleEsc = (e) => { if (e.key === 'Escape') popover.style.display = 'none'; };
    popover.addEventListener('mouseleave', () => { popover.style.display = 'none'; });
    document.addEventListener('mousedown', dismiss);
    document.addEventListener('keydown', handleEsc);
    graphState[tab.id]._dismissHandler = dismiss;
    graphState[tab.id]._escHandler = handleEsc;

    resizeGraphGrid(tab.id, 1, 1);
}

function resizeGraphGrid(tabId, rows, cols) {
    const state  = graphState[tabId];
    const gridEl = state.gridEl;
    const total  = rows * cols;

    // Destroy excess chart instances and clean up body-appended dropdowns
    for (let i = total; i < state.cells.length; i++) {
        state.cells[i].chart?.destroy();
        state.cells[i].cellEl?._dropdown?.remove();
    }

    const preserved = state.cells.slice(0, total);
    while (preserved.length < total) preserved.push({ cellEl: null, chart: null, channels: [], viewWindowSec: 60, viewEnd: null });

    state.rows  = rows;
    state.cols  = cols;
    if (state.sizeBtn) state.sizeBtn.textContent = `${rows} × ${cols}`;
    state.cells = preserved;

    // Clean up body-appended dropdowns for cells being rebuilt
    for (const cell of preserved) cell.cellEl?._dropdown?.remove();
    gridEl.innerHTML = '';
    gridEl.style.gridTemplateColumns = `repeat(${cols}, 1fr)`;
    gridEl.style.gridTemplateRows    = `repeat(${rows}, 1fr)`;

    for (let i = 0; i < total; i++) {
        const cell = state.cells[i];
        if (!('viewWindowSec' in cell)) cell.viewWindowSec = 60;
        if (!('viewEnd'       in cell)) cell.viewEnd       = null;
        if (!('axisLocks'     in cell)) cell.axisLocks     = {};   // { [axisId]: { min, max } }, values are numbers or absent (=auto)
        if (!('frozen'        in cell)) cell.frozen        = false;
        // Item 04: debounced per-cell history fetch. Idempotent-init like the
        // lines above — resizeGraphGrid PRESERVES the cell object across a
        // grid resize, so this must not clobber an existing wrapper (and the
        // in-flight/last-args state debounce() closes over) on every resize.
        if (!cell._debouncedEnsureHistory) cell._debouncedEnsureHistory = debounce(() => ensureCellHistory(cell), 200);

        const cellEl = buildGraphCell(tabId, i);
        gridEl.appendChild(cellEl);
        cell.cellEl = cellEl;

        const canvas = cellEl.querySelector('canvas');
        cell.chart   = createCellChart(canvas);
        applyAllAxisLocks(cell);
        cell.nowBtn  = cellEl._nowBtn;
        cell.nowBtn.addEventListener('click', () => returnCellToLive(cell));
        cell.freezeBtn = cellEl._freezeBtn;
        cell.freezeBtn.addEventListener('click', () => toggleCellFreeze(cell));
        cell.freezeBtn.classList.toggle('graph-freeze-btn--active', cell.frozen);

        attachDragPan(canvas, cell);
        attachScrollZoom(canvas, cell);
        attachProximityTooltip(canvas, cell);

        for (const ch of cell.channels) addDatasetToChart(cell.chart, ch.refDes, ch.color, ch.hidden, ch);
        syncYAxisVisibility(cell);
        updateCellPanel(tabId, i);
    }

    updateActiveGraphChannels();
}

function buildGraphCell(tabId, cellIdx) {
    const cellEl = mkEl('div', 'graph-cell');

    // ── Left panel ──────────────────────────────────────────────────────────
    const panel       = mkEl('div', 'graph-cell-panel');
    const channelList = mkEl('div', 'graph-channel-list');
    panel.appendChild(channelList);

    const searchWrap  = mkEl('div', 'graph-search-wrap');
    const searchInput = document.createElement('input');
    searchInput.type        = 'text';
    searchInput.placeholder = 'Add channel (regex)...';
    searchInput.className   = 'graph-search';
    searchWrap.appendChild(searchInput);
    panel.appendChild(searchWrap);

    // ── Chart area ──────────────────────────────────────────────────────────
    const chartArea = mkEl('div', 'graph-chart-area');
    const canvas    = document.createElement('canvas');
    chartArea.appendChild(canvas);

    const nowBtn = mkEl('button', 'graph-now-btn', 'Now');
    nowBtn.title = 'Jump to live view';
    nowBtn.style.display = 'none';
    chartArea.appendChild(nowBtn);
    cellEl._nowBtn = nowBtn;

    // Freeze (item 16) — makes explicit a state pan already produces
    // implicitly (drag away from live and the window stops following); this
    // is the same idea with a button, so it doesn't take a mouse gesture to
    // discover. Built here, wired in resizeGraphGrid (where `cell` exists) —
    // same two-step build/wire split buildGraphCell already uses for nowBtn.
    const freezeBtn = mkEl('button', 'graph-freeze-btn', 'Freeze');
    freezeBtn.title = "Stop the window advancing — click again, or Now, to resume";
    chartArea.appendChild(freezeBtn);
    cellEl._freezeBtn = freezeBtn;

    // Y-axis lock overlay labels: one min/max pair per possible axis (1-6),
    // shown/positioned/hidden by updateAxisLockLabels() based on which axes
    // are currently active. Click a label to lock that end of the axis to a
    // custom value; clearing the popup input restores auto-scaling.
    cellEl._axisLabels = {};
    for (let i = 1; i <= 6; i++) {
        const minEl = mkEl('div', 'axis-lock-label axis-lock-label--min');
        const maxEl = mkEl('div', 'axis-lock-label axis-lock-label--max');
        minEl.style.display = 'none';
        maxEl.style.display = 'none';
        minEl.title = 'Click to lock this axis minimum';
        maxEl.title = 'Click to lock this axis maximum';
        chartArea.appendChild(minEl);
        chartArea.appendChild(maxEl);
        cellEl._axisLabels[i] = { minEl, maxEl };
    }

    cellEl.appendChild(panel);
    cellEl.appendChild(chartArea);

    const searchDropdown = createChannelSearchDropdown(searchInput, {
        getExcluded:     () => new Set((graphState[tabId]?.cells[cellIdx]?.channels ?? []).map(c => c.refDes)),
        onPick:          (refDes) => addChannelToCell(tabId, cellIdx, refDes),
        position:        'above',
        styleInputError: true,
    });

    cellEl._dropdown = searchDropdown.dropdownEl;  // track for cleanup on grid rebuild
    return cellEl;
}

function getChartColors() {
    const style = getComputedStyle(document.documentElement);
    return {
        grid:           style.getPropertyValue('--border').trim()  || '#30363d',
        tick:           style.getPropertyValue('--muted').trim()   || '#8d969e',
        tooltipBg:      style.getPropertyValue('--surface').trim() || '#101010',
        tooltipBorder:  style.getPropertyValue('--border').trim()  || '#242424',
        tooltipTitle:   style.getPropertyValue('--text').trim()    || '#d0d8d8',
        tooltipBody:    style.getPropertyValue('--muted').trim()   || '#909898',
    };
}

function applyChartColors(chart) {
    const { grid, tick, tooltipBg, tooltipBorder, tooltipTitle, tooltipBody } = getChartColors();
    chart.options.scales.x.ticks.color = tick;
    chart.options.scales.x.grid.color  = grid;
    for (let i = 1; i <= 6; i++) {
        const ax = chart.options.scales['y' + i];
        if (!ax) continue;
        ax.ticks.color = tick;
        if (ax.grid?.color !== undefined) ax.grid.color = grid;
    }
    const tt = chart.options.plugins.tooltip;
    tt.backgroundColor = tooltipBg;
    tt.borderColor     = tooltipBorder;
    tt.titleColor      = tooltipTitle;
    tt.bodyColor       = tooltipBody;
    chart.update('none');
}

function updateAllChartColors() {
    for (const state of Object.values(graphState)) {
        for (const cell of state.cells) {
            if (cell.chart) applyChartColors(cell.chart);
        }
    }
}

function createCellChart(canvas) {
    const { grid, tick } = getChartColors();
    const yAxes = {};
    for (let i = 1; i <= 6; i++) {
        const isLeft = i % 2 === 1;
        yAxes['y' + i] = {
            position: isLeft ? 'left' : 'right',
            display:  false,
            ticks:    { color: tick },
            grid:     isLeft ? { color: grid } : { drawOnChartArea: false }
        };
    }
    return new Chart(canvas, {
        type: 'line',
        data: { datasets: [] },
        options: {
            animation:           false,
            responsive:          true,
            maintainAspectRatio: false,
            parsing:             false,
            events:              [],
            scales: {
                x: {
                    type: 'linear',
                    ticks: {
                        color:         tick,
                        maxTicksLimit: 12,
                        maxRotation:   0,
                        callback: function(v) {
                            const offset = this.chart.options._timeOffset ?? 0;
                            const ago = Math.round(-(v + offset));
                            if (ago <= 0) return 'now';
                            if (ago < 60) return ago + 's';
                            const m = Math.floor(ago / 60);
                            const s = ago % 60;
                            return s > 0 ? `${m}m ${s}s` : `${m}m`;
                        }
                    },
                    grid: { color: grid }
                },
                ...yAxes
            },
            plugins: {
                legend:  { display: false },
                tooltip: {
                    mode:            'index',
                    intersect:       false,
                    position:        'datapoint',
                    backgroundColor: getChartColors().tooltipBg,
                    borderColor:     getChartColors().tooltipBorder,
                    borderWidth:     1,
                    titleColor:      getChartColors().tooltipTitle,
                    bodyColor:       getChartColors().tooltipBody,
                    callbacks: {
                        labelColor: (item) => {
                            const color = item.dataset.borderColor;
                            return { borderColor: color, backgroundColor: color };
                        },
                        title: (items) => {
                            if (!items.length) return '';
                            const chart = items[0].chart;
                            const offset = chart.options._timeOffset ?? 0;
                            const ago = Math.round(-(items[0].parsed.x + offset));
                            if (ago < 60) return ago + 's ago';
                            const m = Math.floor(ago / 60);
                            const s = ago % 60;
                            return s > 0 ? `${m}m ${s}s ago` : `${m}m ago`;
                        },
                        label: (item) => {
                            const refDes = item.dataset.label;
                            const units  = graphGetUnits(refDes);
                            let val;
                            if (typeof item.parsed.y === 'number') {
                                // SM-<NAME>-STATE: render the state NAME through
                                // state_config, not the raw index — falls back to
                                // the number itself when there's no config yet, no
                                // matching index (stale config, machine removed),
                                // or this isn't a state channel at all.
                                const stateName = graphStateName(refDes, Math.round(item.parsed.y));
                                val = stateName ?? item.parsed.y.toFixed(2);
                            } else {
                                val = item.parsed.y;
                            }
                            const showDesc = item.chart.options._showDesc;
                            const desc   = showDesc ? graphGetDesc(refDes) : '';
                            const name   = desc ? `${refDes} (${desc})` : refDes;
                            return ` ${name}: ${val}${units ? ' ' + units : ''}`;
                        }
                    }
                }
            },
            elements: {
                point: { radius: 0, hoverRadius: 5, hoverBorderWidth: 2 },
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
    const ch = { refDes, color, hidden: false, yAxisId: graphDefaultYAxisId(cell, refDes) };
    cell.channels.push(ch);

    if (!channelBuffers[refDes]) channelBuffers[refDes] = { ts: [], vals: [] };
    activeGraphChannels.add(refDes);

    addDatasetToChart(cell.chart, refDes, color, false, ch);
    syncYAxisVisibility(cell);
    updateCellPanel(tabId, cellIdx);
    // Item 04: a channel just added has an empty/short local buffer by
    // definition — check whether the visible window now reaches further back
    // than what's buffered, and fetch the gap if so. Optional-chaining is
    // defensive only: every cell-construction site sets
    // _debouncedEnsureHistory before any addChannelToCell call it will ever
    // receive (see the three call-site comments), so this should never
    // actually be a no-op in practice.
    cell._debouncedEnsureHistory?.();
}

function removeChannelFromCell(tabId, cellIdx, refDes) {
    const cell = graphState[tabId]?.cells[cellIdx];
    if (!cell) return;
    cell.channels = cell.channels.filter(c => c.refDes !== refDes);
    const dsIdx = cell.chart.data.datasets.findIndex(d => d.label === refDes);
    if (dsIdx !== -1) cell.chart.data.datasets.splice(dsIdx, 1);
    syncYAxisVisibility(cell);
    updateCellPanel(tabId, cellIdx);
    updateActiveGraphChannels();
}

// =============================================================================
// Server-side history fetch (item 04)
// =============================================================================

// ensureCellHistory (item 04) is the client half of server-side chart
// aggregation: for every channel in `cell` whose buffered local history
// (channelBuffers) doesn't already reach back to the visible window's
// start, fetch bucketed history from GET /api/history and prepend it ahead
// of whatever the live stream has already accumulated. Never touches or
// duplicates the live tail — see mergeHistoryBuckets. Safe to call directly
// (not just via a debounce wrapper): a call while one is already in flight
// for this cell is a no-op, and the next debounced call naturally retries.
async function ensureCellHistory(cell) {
    if (!cell.channels.length || cell._historyFetchInFlight) return;

    const now         = Date.now() / 1000;
    const windowEnd   = cell.viewEnd ?? now;
    const windowStart = windowEnd - cell.viewWindowSec;

    // Only channels whose buffered history doesn't already reach back to
    // windowStart need anything. 1s of slack absorbs float/tick noise so a
    // channel that's already essentially covered doesn't trigger a fetch
    // for one sample's worth of gap.
    const need = [];
    for (const ch of cell.channels) {
        const buf = channelBuffers[ch.refDes];
        const earliestBuffered = buf?.ts.length ? buf.ts[0] : now;
        if (earliestBuffered > windowStart + 1) need.push(ch.refDes);
    }
    if (!need.length) return;

    // One request for the whole cell, not one per channel — the server
    // endpoint already supports repeating refDes for exactly this reason.
    // Different channels can have different actual gap edges (one might
    // already have a minute of live data, another was just added with an
    // empty buffer); rather than issuing a different `to` per channel,
    // request the single widest span that covers every gap
    // [windowStart, now). mergeHistoryBuckets below only ever prepends
    // points strictly older than what's already buffered, so a channel
    // that gets back some buckets it didn't strictly need is harmless
    // (deduplicated on merge), not wrong.
    const from = windowStart;
    const to   = now;

    // Bucket count from the chart's actual pixel width — a 300px-wide side
    // panel chart doesn't want the same resolution as a full Graph tab
    // cell. Clamped to the endpoint's own [1,2000] range with a sane floor
    // so a not-yet-laid-out canvas (clientWidth 0) still asks for something
    // useful.
    const width   = cell.chart?.canvas?.clientWidth || 300;
    const buckets = Math.max(50, Math.min(800, Math.round(width)));

    const params = new URLSearchParams();
    for (const refDes of need) params.append('refDes', refDes);
    params.set('from', String(from));
    params.set('to', String(to));
    params.set('buckets', String(buckets));

    cell._historyFetchInFlight = true;
    try {
        const resp = await fetch('/api/history?' + params.toString());
        if (!resp.ok) return; // stale/older control node, or a transient error — the chart
                               // already works without this; the next pan/zoom/add retries.
        const data = await resp.json();
        for (const refDes of need) {
            const result = data.channels?.[refDes];
            if (result) mergeHistoryBuckets(refDes, result.buckets);
        }
        // Paint immediately rather than wait out the periodic redraw's own
        // interval — the whole point of this item is a chart that doesn't
        // sit visibly empty for a moment after opening/panning.
        cell.chart?.update('none');
    } catch (e) {
        console.warn('history fetch failed for', need, e);
    } finally {
        cell._historyFetchInFlight = false;
    }
}

// mergeHistoryBuckets prepends server-fetched bucket points that are OLDER
// than anything already buffered for `refDes` — this only ever fills the
// gap further back in time than the live buffer has accumulated on its own,
// never touches or duplicates the live tail. Every bucket's single y-value
// is its `last` sample, uniformly for discrete and continuous channels
// alike: min/max travel over the wire (docs/websocket-protocol.md Part 3)
// but nothing renders them as an envelope yet — that's future work, not
// this item. Buckets arrive in ascending time order and the filtered subset
// stays in that order, so prepending keeps channelBuffers monotonic, which
// buildChartData's binary search already assumes.
function mergeHistoryBuckets(refDes, buckets) {
    if (!buckets || !buckets.length) return;
    if (!channelBuffers[refDes]) channelBuffers[refDes] = { ts: [], vals: [] };
    const buf = channelBuffers[refDes];
    const cutoff = buf.ts.length ? buf.ts[0] : Infinity;

    const newTs = [], newVals = [];
    for (const b of buckets) {
        if (b.t >= cutoff) continue; // already covered by the live buffer or an earlier fetch
        newTs.push(b.t);
        newVals.push(b.last);
    }
    if (!newTs.length) return;
    buf.ts   = newTs.concat(buf.ts);
    buf.vals = newVals.concat(buf.vals);
}

function addDatasetToChart(chart, refDes, color, hidden, ch) {
    const buf  = channelBuffers[refDes];
    const data = buf ? buf.ts.map((t, i) => ({ x: t, y: buf.vals[i] })) : [];
    chart.data.datasets.push({
        label:           refDes,
        data,
        borderColor:     color,
        backgroundColor: color + '22',
        hidden,
        fill:            false,
        yAxisID:         'y' + (ch?.yAxisId ?? 1),
        // A discrete channel — a state INDEX, a boolean command, a boolean
        // valve feedback — must step from one value to the next, not
        // interpolate: a straight line between index 3 and 4 draws a state
        // that never existed, and the same is true of a diagonal ramp
        // between CMD=0 and CMD=1. Chart.js `stepped: true` holds the
        // previous value until the next sample instead, which is also what
        // makes command/feedback lag read as a clean horizontal GAP between
        // two vertical edges (item 15) rather than two overlapping ramps.
        // See graphChannelIsDiscrete above.
        stepped:         graphChannelIsDiscrete(refDes)
    });
    chart.update('none');
}

function updateCellPanel(tabId, cellIdx) {
    const cell = graphState[tabId]?.cells[cellIdx];
    if (!cell?.cellEl) return;
    const list = cell.cellEl.querySelector('.graph-channel-list');
    if (!list) return;
    list.innerHTML = '';

    for (const ch of cell.channels) {
        const item = mkEl('div', 'panel-channel-item');

        // Y-axis badge
        const badge = mkEl('span', 'y-axis-badge', String(ch.yAxisId));
        badge.title = 'Left-click / right-click to change Y axis';
        badge.addEventListener('click', () => {
            ch.yAxisId = (ch.yAxisId % 6) + 1;
            badge.textContent = String(ch.yAxisId);
            const ds = cell.chart.data.datasets.find(d => d.label === ch.refDes);
            if (ds) ds.yAxisID = 'y' + ch.yAxisId;
            syncYAxisVisibility(cell);
        });
        badge.addEventListener('contextmenu', (e) => {
            e.preventDefault();
            ch.yAxisId = ((ch.yAxisId - 2 + 6) % 6) + 1;
            badge.textContent = String(ch.yAxisId);
            const ds = cell.chart.data.datasets.find(d => d.label === ch.refDes);
            if (ds) ds.yAxisID = 'y' + ch.yAxisId;
            syncYAxisVisibility(cell);
        });

        // Color swatch
        const swatch = mkEl('div', 'color-swatch');
        swatch.style.background = ch.color;
        swatch.title = 'Click to change color';
        swatch.addEventListener('click', () => {
            openColorPalette(swatch, ch.color, CHART_COLORS, (newColor) => {
                ch.color = newColor;
                swatch.style.background = newColor;
                const ds = cell.chart.data.datasets.find(d => d.label === ch.refDes);
                if (ds) { ds.borderColor = newColor; ds.backgroundColor = newColor + '22'; }
                cell.chart.update('none');
            });
        });

        // Channel name (+ optional description)
        const nameWrap = mkEl('div', `channel-name${ch.hidden ? ' channel-hidden' : ''}`);
        nameWrap.title = 'Click to toggle visibility';
        nameWrap.appendChild(mkEl('span', 'channel-refdes', ch.refDes));
        if (graphState[tabId]?.showDesc) {
            const desc = graphGetDesc(ch.refDes);
            if (desc) nameWrap.appendChild(mkEl('span', 'channel-desc', desc));
        }
        const lbl = nameWrap;
        lbl.addEventListener('click', () => {
            ch.hidden = !ch.hidden;
            lbl.classList.toggle('channel-hidden', ch.hidden);
            const ds = cell.chart.data.datasets.find(d => d.label === ch.refDes);
            if (ds) ds.hidden = ch.hidden;
            syncYAxisVisibility(cell);
        });

        // Remove button
        const rmBtn = mkEl('button', 'channel-remove', '×');
        rmBtn.title = 'Remove';
        rmBtn.addEventListener('click', () => removeChannelFromCell(tabId, cellIdx, ch.refDes));

        item.appendChild(badge);
        item.appendChild(swatch);
        item.appendChild(lbl);
        item.appendChild(rmBtn);
        list.appendChild(item);
    }
}

function updateActiveGraphChannels() {
    activeGraphChannels.clear();
    for (const state of Object.values(graphState)) {
        for (const cell of state.cells) {
            for (const ch of cell.channels) activeGraphChannels.add(ch.refDes);
        }
    }
    // Preserve buffers for front-panel channels even when not in any graph cell
    for (const refDes of Object.keys(channelBuffers)) {
        if (!activeGraphChannels.has(refDes) && !activePidChannels.has(refDes)) {
            delete channelBuffers[refDes];
        }
    }
}

function rebuildActivePidChannels() {
    activePidChannels.clear();
    for (const t of tabs) {
        if (t.type !== 'frontPanel' || !t.pid) continue;
        for (const obj of t.pid.objects) {
            // Sensors: buffer the resolved channel's refDes (controlRefDes
            // binding, or legacy refDes — see resolveSensorBinding)
            if (obj.type === 'sensor') {
                const binding = resolveSensorBinding(obj, configControls);
                if (binding) {
                    activePidChannels.add(binding.ch.refDes);
                    if (!channelBuffers[binding.ch.refDes]) channelBuffers[binding.ch.refDes] = { ts: [], vals: [] };
                }
            }
            // Valves: buffer the actual sub-channel refDes values (cmd + feedback),
            // not the control-level refDes which is never a data key.
            else if (obj.type === 'valve' && obj.controlRefDes) {
                const ctrl = configControls.find(c => c.refDes === obj.controlRefDes);
                for (const ch of (ctrl?.channels ?? [])) {
                    if (!ch.refDes) continue;
                    activePidChannels.add(ch.refDes);
                    if (!channelBuffers[ch.refDes]) channelBuffers[ch.refDes] = { ts: [], vals: [] };
                }
            }
            // Graphs: buffer all pre-configured channel refDes values
            else if (obj.type === 'graph' && obj.lines) {
                for (const line of obj.lines) {
                    if (line.refDes) {
                        activePidChannels.add(line.refDes);
                        if (!channelBuffers[line.refDes]) channelBuffers[line.refDes] = { ts: [], vals: [] };
                    }
                }
            }
        }
    }
}

// bufferGraphData appends one sample per active channel into channelBuffers.
// IMPORTANT: this is called synchronously from ws.js's onDataMessage/applyData
// on every incoming 'data' frame — i.e. driven by WebSocket message arrival,
// NOT by a render loop (setInterval/requestAnimationFrame). That decoupling is
// deliberate: rAF is fully suspended and setInterval is heavily throttled in a
// backgrounded tab/window, but WebSocket message delivery is not, so the
// rolling buffer keeps filling while the tab/window is unfocused or another
// in-app tab is active. Only the chart REDRAW (updateAllGraphs, on a
// setInterval) is allowed to lag/skip when not visible — see the
// visibilitychange handler in app.js that forces a catch-up redraw on refocus.
function bufferGraphData(data) {
    const now         = Date.now() / 1000;
    const graphCutoff = now - CONFIG.graphBufferMinutes * 60;
    const pidCutoff   = now - 60;

    for (const refDes of activeGraphChannels) {
        if (!(refDes in data)) continue;
        const buf = channelBuffers[refDes];
        if (!buf) continue;
        buf.ts.push(now);
        buf.vals.push(data[refDes]);
        // Use a single splice() instead of repeated shift() — one O(n) move vs many
        let trimCount = 0;
        while (trimCount < buf.ts.length && buf.ts[trimCount] < graphCutoff) trimCount++;
        if (trimCount > 0) { buf.ts.splice(0, trimCount); buf.vals.splice(0, trimCount); }
    }

    // Buffer PID-only channels (skip any already handled above by the graph)
    for (const refDes of activePidChannels) {
        if (activeGraphChannels.has(refDes)) continue;
        if (!(refDes in data)) continue;
        const buf = channelBuffers[refDes];
        if (!buf) continue;
        buf.ts.push(now);
        buf.vals.push(data[refDes]);
        let trimCount = 0;
        while (trimCount < buf.ts.length && buf.ts[trimCount] < pidCutoff) trimCount++;
        if (trimCount > 0) { buf.ts.splice(0, trimCount); buf.vals.splice(0, trimCount); }
    }
}

// buildChartData slices the rolling buffer down to the currently-viewed
// [displayEnd - viewWindowSec, displayEnd] window, translated to axis-relative
// seconds (x in [-viewWindowSec, 0]).
//
// One extra sample is deliberately kept on each side of the window (an
// off-screen anchor point beyond the axis's fixed x.min/x.max) rather than
// hard-clipping exactly at the edge. Chart.js clips the LINE DRAWING to the
// chart area by default, but it only draws a segment between two points it
// actually has — if we hand it only in-window points, the segment connecting
// to whatever is just outside the window is never drawn at all, so the line
// visibly snaps/pops in and out right at the edge instead of entering or
// leaving continuously. Handing it one real off-screen point on each side
// gives Chart.js the endpoint it needs to draw (and clip) that boundary
// segment smoothly. This only changes what's rendered — the underlying
// rolling-buffer window (channelBuffers / bufferGraphData) is untouched.
function buildChartData(buf, displayEnd, viewWindowSec) {
    const { ts, vals } = buf;
    if (!ts.length) return [];
    const absMin = displayEnd - viewWindowSec;
    const absMax = displayEnd;

    // First index within the window; step back one for the left-edge anchor.
    let startIdx = ts.findIndex(t => t >= absMin);
    if (startIdx === -1) startIdx = ts.length - 1;      // everything is before the window
    else if (startIdx > 0) startIdx--;

    // Last index within the window; step forward one for the right-edge anchor.
    let endIdx = -1;
    for (let i = ts.length - 1; i >= 0; i--) {
        if (ts[i] <= absMax) { endIdx = i; break; }
    }
    if (endIdx === -1) endIdx = 0;                       // everything is after the window (shouldn't happen)
    else if (endIdx < ts.length - 1) endIdx++;

    if (endIdx < startIdx) return [];

    const out = new Array(endIdx - startIdx + 1);
    for (let i = startIdx; i <= endIdx; i++) out[i - startIdx] = { x: ts[i] - displayEnd, y: vals[i] };
    return out;
}

// _cellSyncLiveControls keeps the Now button and the Freeze button (item 16)
// honest about the cell's actual state after ANYTHING changes cell.viewEnd —
// the periodic redraw below, a drag, or a scroll-zoom. Returning to live
// (viewEnd === null) always clears `frozen` too: freezing and live-following
// are mutually exclusive by definition, and without this a drag or
// scroll-zoom that happened to reach the live edge while frozen was on would
// leave the Freeze button lit for a chart that is, in fact, advancing again.
function _cellSyncLiveControls(cell) {
    if (cell.viewEnd === null && cell.frozen) {
        cell.frozen = false;
        cell.freezeBtn?.classList.remove('graph-freeze-btn--active');
    }
    if (cell.nowBtn) cell.nowBtn.style.display = cell.viewEnd !== null ? '' : 'none';
}

// toggleCellFreeze flips Freeze (item 16) for one cell. Turning it on while
// already live-following pins the view to the current live edge first —
// the same "latest sample across this cell's channels" computation
// updateAllGraphs uses below — so the button reads as "hold right here",
// not "jump somewhere else first". Turning it off does NOT force a return
// to live: cell.viewEnd is left exactly where Freeze pinned it, which
// behaves precisely like a manual pan (a pinned, non-null viewEnd never
// advances on its own, frozen or not) — only dragging/zooming further, or
// the Now button (already visible, since viewEnd is non-null once frozen),
// moves it again. Un-freezing just removes the guard that stopped the
// live-edge snap-back below from misfiring at the moment of freezing.
function toggleCellFreeze(cell) {
    cell.frozen = !cell.frozen;
    if (cell.frozen && cell.viewEnd === null) {
        let latestTs = Date.now() / 1000;
        for (const ds of cell.chart.data.datasets) {
            const buf = channelBuffers[ds.label];
            if (buf?.ts.length) latestTs = Math.max(latestTs, buf.ts[buf.ts.length - 1]);
        }
        cell.viewEnd = latestTs;
    }
    cell.freezeBtn?.classList.toggle('graph-freeze-btn--active', cell.frozen);
    if (cell.nowBtn) cell.nowBtn.style.display = cell.viewEnd !== null ? '' : 'none';
}

// returnCellToLive is the Now button's action, pulled out so Freeze's own
// state (and the sidebar cell, which builds its Now button inline rather
// than through buildGraphCell/resizeGraphGrid) share the exact same
// "back to live" transition instead of duplicating the reset.
function returnCellToLive(cell) {
    cell.viewEnd = null;
    cell.frozen  = false;
    cell.freezeBtn?.classList.remove('graph-freeze-btn--active');
    if (cell.nowBtn) cell.nowBtn.style.display = 'none';
}

function updateAllGraphs() {
    for (const [tabId, state] of Object.entries(graphState)) {
        if (tabId === '__sidebar__') {
            const sidebarEl = document.getElementById('object-sidebar');
            if (!sidebarEl || sidebarEl.style.display === 'none') continue;
        } else if (tabId.startsWith('__pid_graph_')) {
            // Embedded graph object in a front panel — always update
        } else {
            const tab = tabs.find(t => t.id === tabId);
            if (!tab || tab.contentEl.style.display === 'none') continue;
        }
        for (const cell of state.cells) {
            if (!cell.chart) continue;
            // Determine latest timestamp across all channels in this cell
            let latestTs = Date.now() / 1000;
            for (const ds of cell.chart.data.datasets) {
                const buf = channelBuffers[ds.label];
                if (buf?.ts.length) latestTs = Math.max(latestTs, buf.ts[buf.ts.length - 1]);
            }
            // Snap back to live-follow when pinned view is within 10% of the live edge —
            // but not while frozen: freezing right at the live edge would otherwise put
            // the gap under that threshold on the very next tick and instantly undo itself.
            if (!cell.frozen && cell.viewEnd !== null && latestTs - cell.viewEnd < cell.viewWindowSec * 0.1) cell.viewEnd = null;
            const displayEnd = cell.viewEnd ?? latestTs;
            // Build relative-coord data for each dataset
            for (const ds of cell.chart.data.datasets) {
                const buf = channelBuffers[ds.label];
                ds.data = buf ? buildChartData(buf, displayEnd, cell.viewWindowSec) : [];
            }
            // Offset used by tick/tooltip callbacks: how far displayEnd is behind real now
            cell.chart.options._timeOffset = displayEnd - Date.now() / 1000;
            // Axis range is always fixed; only changes when viewWindowSec changes
            cell.chart.options.scales.x.min = -cell.viewWindowSec;
            cell.chart.options.scales.x.max = 0;
            cell.chart.update('none');
            _cellSyncLiveControls(cell);
            updateAxisLockLabels(cell);
        }
    }
}

function resizeGraphCharts(tabId) {
    const state = graphState[tabId];
    if (!state) return;
    for (const cell of state.cells) cell.chart?.resize();
}

function syncYAxisVisibility(cell) {
    for (let i = 1; i <= 6; i++) {
        const active = cell.channels.some(c => !c.hidden && c.yAxisId === i);
        cell.chart.options.scales['y' + i].display = active;
    }
    cell.chart.update('none');
    updateAxisLockLabels(cell);
}

// =============================================================================
// Y-axis lock (per graph-cell, per-axis min/max)
// =============================================================================

// applyAxisLock pushes cell.axisLocks[axisId] into the chart's scale options.
// Chart.js re-reads options.scales[...].min/max on every update, so once set
// these persist across live data updates, drag-pan and scroll-zoom without
// needing to be re-applied each tick — only when the chart instance itself is
// recreated (grid resize) does this need to be called again.
function applyAxisLock(cell, axisId) {
    const scale = cell.chart?.options.scales['y' + axisId];
    if (!scale) return;
    const lock = cell.axisLocks?.[axisId] || {};
    if (lock.min != null) scale.min = lock.min; else delete scale.min;
    if (lock.max != null) scale.max = lock.max; else delete scale.max;
}

function applyAllAxisLocks(cell) {
    for (let i = 1; i <= 6; i++) applyAxisLock(cell, i);
}

// setAxisLock sets (value is a finite number) or clears (value is null) one
// end ('min'|'max') of one axis's lock, then re-renders.
function setAxisLock(cell, axisId, which, value) {
    if (!cell.axisLocks) cell.axisLocks = {};
    if (!cell.axisLocks[axisId]) cell.axisLocks[axisId] = {};
    if (value == null) delete cell.axisLocks[axisId][which];
    else cell.axisLocks[axisId][which] = value;
    applyAxisLock(cell, axisId);
    cell.chart.update('none');
    updateAxisLockLabels(cell);
}

function fmtAxisVal(v) {
    if (typeof v !== 'number' || !isFinite(v)) return '';
    const abs = Math.abs(v);
    if (abs !== 0 && (abs < 0.01 || abs >= 100000)) return v.toExponential(2);
    return (Math.round(v * 100) / 100).toString();
}

// updateAxisLockLabels positions/shows/hides the clickable min/max overlay for
// every active axis on this cell, reading pixel geometry straight from the
// live Chart.js scale objects (so it tracks whatever the chart just drew,
// whether that came from live data, a lock, a pan or a zoom).
function updateAxisLockLabels(cell) {
    const labels = cell.cellEl?._axisLabels;
    const chart  = cell.chart;
    if (!labels || !chart || !chart.canvas) return;
    const canvas = chart.canvas;
    for (let i = 1; i <= 6; i++) {
        const { minEl, maxEl } = labels[i];
        const optAxis = chart.options.scales['y' + i];
        const axis    = chart.scales['y' + i];
        if (!optAxis?.display || !axis) {
            minEl.style.display = 'none';
            maxEl.style.display = 'none';
            continue;
        }
        const lock   = cell.axisLocks?.[i] || {};
        const isLeft = i % 2 === 1;
        const xPos   = isLeft ? axis.right : axis.left;

        minEl.style.display = '';
        maxEl.style.display = '';
        maxEl.style.top  = (canvas.offsetTop + axis.top) + 'px';
        minEl.style.top  = (canvas.offsetTop + axis.bottom - 12) + 'px';
        const leftPx = canvas.offsetLeft + xPos;
        maxEl.style.left = leftPx + 'px';
        minEl.style.left = leftPx + 'px';
        maxEl.style.transform = isLeft ? 'translateX(-100%)' : 'translateX(0)';
        minEl.style.transform = maxEl.style.transform;

        const maxVal = lock.max != null ? lock.max : axis.max;
        const minVal = lock.min != null ? lock.min : axis.min;
        maxEl.textContent = fmtAxisVal(maxVal);
        minEl.textContent = fmtAxisVal(minVal);
        maxEl.classList.toggle('axis-lock-label--locked', lock.max != null);
        minEl.classList.toggle('axis-lock-label--locked', lock.min != null);

        maxEl.onclick = () => openAxisLockPopup(maxEl, lock.max ?? null, (v) => setAxisLock(cell, i, 'max', v));
        minEl.onclick = () => openAxisLockPopup(minEl, lock.min ?? null, (v) => setAxisLock(cell, i, 'min', v));
    }
}

// openAxisLockPopup — themed popup matching openColorPalette's look/behaviour
// (fixed-position card anchored under the clicked label, dismissed on outside
// click or Escape) but with a numeric input instead of color swatches.
// onSet(value) is called with a finite number to lock, or null to clear
// (restore auto-scaling) — clearing the input and hitting "Auto" is the
// unlock path.
function openAxisLockPopup(anchorEl, currentValue, onSet) {
    document.querySelector('.axis-lock-popup')?.remove();

    const popup = mkEl('div', 'axis-lock-popup');
    const input = document.createElement('input');
    input.type  = 'number';
    input.step  = 'any';
    input.placeholder = 'auto';
    if (currentValue != null) input.value = currentValue;
    popup.appendChild(input);

    const actions  = mkEl('div', 'axis-lock-popup-actions');
    const applyBtn = mkEl('button', '', 'Lock');
    const clearBtn = mkEl('button', '', 'Auto');
    actions.appendChild(applyBtn);
    actions.appendChild(clearBtn);
    popup.appendChild(actions);

    const commit = () => {
        const v = parseFloat(input.value);
        if (input.value.trim() === '' || isNaN(v)) return;
        popup.remove();
        onSet(v);
    };
    applyBtn.addEventListener('mousedown', (e) => { e.preventDefault(); commit(); });
    clearBtn.addEventListener('mousedown', (e) => { e.preventDefault(); popup.remove(); onSet(null); });
    input.addEventListener('keydown', (e) => {
        if (e.key === 'Enter') commit();
        if (e.key === 'Escape') { popup.remove(); }
    });

    document.body.appendChild(popup);
    const rect = anchorEl.getBoundingClientRect();
    popup.style.top  = (rect.bottom + 4) + 'px';
    popup.style.left = rect.left + 'px';
    input.focus();
    input.select();

    const dismiss = (e) => {
        if (!popup.contains(e.target) && e.target !== anchorEl) {
            popup.remove();
            document.removeEventListener('mousedown', dismiss);
        }
    };
    setTimeout(() => document.addEventListener('mousedown', dismiss), 0);
}

function attachDragPan(canvas, cell) {
    let dragging = false;
    let startX = 0;
    let startViewEnd = null;

    canvas.style.cursor = 'grab';

    canvas.addEventListener('pointerdown', (e) => {
        if (e.button !== 0) return;
        dragging = true;
        startX = e.clientX;
        startViewEnd = cell.viewEnd ?? Date.now() / 1000;
        canvas.style.cursor = 'grabbing';
        canvas.setPointerCapture(e.pointerId);
        // Hide tooltip immediately so it doesn't flicker during drag
        cell.chart.tooltip.setActiveElements([], {});
        cell.chart.update('none');
        e.preventDefault();
    });

    canvas.addEventListener('pointermove', (e) => {
        if (!dragging) return;
        const rect = canvas.getBoundingClientRect();
        const dx = e.clientX - startX;             // px moved (positive = drag right = older data)
        const secPerPx = cell.viewWindowSec / rect.width;
        const dtSec = dx * secPerPx;               // seconds to shift (positive dx → pan backward in time)
        const rawEnd = startViewEnd - dtSec;
        const now = Date.now() / 1000;
        cell.viewEnd = rawEnd >= now ? null : rawEnd;

        const displayEnd = cell.viewEnd ?? now;
        for (const ds of cell.chart.data.datasets) {
            const buf = channelBuffers[ds.label];
            ds.data = buf ? buildChartData(buf, displayEnd, cell.viewWindowSec) : [];
        }
        cell.chart.options._timeOffset = displayEnd - now;
        cell.chart.options.scales.x.min = -cell.viewWindowSec;
        cell.chart.options.scales.x.max = 0;
        cell.chart.update('none');
        _cellSyncLiveControls(cell);
        updateAxisLockLabels(cell);
    });

    const endDrag = () => {
        if (!dragging) return;
        dragging = false;
        canvas.style.cursor = 'grab';
        cell._debouncedEnsureHistory?.();
    };

    canvas.addEventListener('pointerup',           endDrag);
    canvas.addEventListener('pointercancel',        endDrag);
    canvas.addEventListener('lostpointercapture',   endDrag);
}

function attachScrollZoom(canvas, cell) {
    let rafPending = false;
    canvas.addEventListener('wheel', (e) => {
        e.preventDefault();
        const rect    = canvas.getBoundingClientRect();
        const ratio   = (e.clientX - rect.left) / rect.width;
        // Normalize delta across scroll modes (pixel / line / page)
        const dy      = e.deltaMode === 1 ? e.deltaY * 40 : e.deltaMode === 2 ? e.deltaY * 800 : e.deltaY;
        const scale   = Math.exp(dy * 0.002);          // smooth continuous zoom
        const newWin  = Math.min(1200, Math.max(30, cell.viewWindowSec * scale));
        const edge    = cell.viewEnd ?? Date.now() / 1000;
        const mouseTs = (edge - cell.viewWindowSec) + ratio * cell.viewWindowSec;
        const rawEnd = mouseTs + (1 - ratio) * newWin;
        const now    = Date.now() / 1000;
        // null = live-follow mode; snap back when the unclamped right edge reaches now
        cell.viewEnd = rawEnd >= now ? null : rawEnd;
        cell.viewWindowSec = newWin;
        // Redraw immediately rather than waiting for the 500ms interval
        if (!rafPending) {
            rafPending = true;
            requestAnimationFrame(() => {
                rafPending = false;
                const now2 = Date.now() / 1000;
                const displayEnd = cell.viewEnd ?? now2;
                for (const ds of cell.chart.data.datasets) {
                    const buf = channelBuffers[ds.label];
                    ds.data = buf ? buildChartData(buf, displayEnd, cell.viewWindowSec) : [];
                }
                cell.chart.options._timeOffset = displayEnd - now2;
                cell.chart.options.scales.x.min = -cell.viewWindowSec;
                cell.chart.options.scales.x.max = 0;
                cell.chart.update('none');
                _cellSyncLiveControls(cell);
                updateAxisLockLabels(cell);
                cell._debouncedEnsureHistory?.();
            });
        }
    }, { passive: false });
}

function attachProximityTooltip(canvas, cell) {
    const HOVER_PX = 14;
    let rafId  = null;
    let lastCx = 0, lastCy = 0;

    canvas.addEventListener('mousemove', (e) => {
        const rect = canvas.getBoundingClientRect();
        lastCx = e.clientX - rect.left;
        lastCy = e.clientY - rect.top;
        if (rafId !== null) return;           // already a frame queued — just update coords
        rafId = requestAnimationFrame(() => {
            rafId = null;
            const chart = cell.chart;
            const cx = lastCx, cy = lastCy;
            const activeEls = [];
            for (let di = 0; di < chart.data.datasets.length; di++) {
                const meta = chart.getDatasetMeta(di);
                if (meta.hidden || !meta.data.length) continue;
                let closestDist = Infinity;
                let closestIdx  = -1;
                for (let pi = 0; pi < meta.data.length; pi++) {
                    const pt   = meta.data[pi];
                    const dx   = cx - pt.x;
                    const dy   = cy - pt.y;
                    const dist = dx * dx + dy * dy;   // skip sqrt — comparing squared is enough
                    if (dist < closestDist) { closestDist = dist; closestIdx = pi; }
                }
                if (closestDist <= HOVER_PX * HOVER_PX) activeEls.push({ datasetIndex: di, index: closestIdx });
            }
            chart.tooltip.setActiveElements(activeEls, { x: cx, y: cy });
            chart.update('none');
        });
    });

    canvas.addEventListener('mouseleave', () => {
        if (rafId !== null) { cancelAnimationFrame(rafId); rafId = null; }
        cell.chart.tooltip.setActiveElements([], {});
        cell.chart.update('none');
    });
}

// openColorPalette moved to pidRender.js (shared with editor.js so the P&ID
// editor's pipe-color picker can reuse the same themed popup).

function cleanupGraphTab(tabId) {
    const state = graphState[tabId];
    if (!state) return;
    for (const cell of state.cells) {
        cell.chart?.destroy();
        cell.cellEl?._dropdown?.remove();
    }
    if (state._dismissHandler) document.removeEventListener('mousedown', state._dismissHandler);
    delete graphState[tabId];
    updateActiveGraphChannels();
}
