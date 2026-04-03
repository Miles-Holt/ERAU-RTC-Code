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

const GRID_PRESETS = [
    { rows: 1, cols: 1 }, { rows: 1, cols: 2 }, { rows: 1, cols: 3 },
    { rows: 2, cols: 1 }, { rows: 2, cols: 2 }, { rows: 2, cols: 3 },
    { rows: 2, cols: 4 }, { rows: 3, cols: 2 }, { rows: 3, cols: 3 },
    { rows: 3, cols: 4 }, { rows: 4, cols: 3 }, { rows: 4, cols: 4 },
];

function graphGetDesc(refDes) {
    for (const ctrl of configControls) {
        for (const ch of (ctrl.channels ?? [])) {
            if (ch.refDes === refDes) return ctrl.description || '';
        }
    }
    return '';
}

function graphGetUnits(refDes) {
    for (const ctrl of configControls) {
        for (const ch of (ctrl.channels ?? [])) {
            if (ch.refDes === refDes) return ch.units || '';
        }
    }
    return '';
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
    graphState[tab.id] = { rows: 1, cols: 1, gridEl: null, cells: [], sizeBtn: null, showDesc: false, _dismissHandler: null };

    const wrapper = mkEl('div', 'graph-tab');
    const toolbar = mkEl('div', 'graph-toolbar');

    // Grid size dropdown button
    const sizeWrap = mkEl('div', 'graph-size-wrap');
    const sizeBtn  = mkEl('button', 'graph-size-btn', '1 × 1');
    graphState[tab.id].sizeBtn = sizeBtn;

    const popover = mkEl('div', 'graph-size-popover');
    popover.style.display = 'none';
    for (const p of GRID_PRESETS) {
        const item = mkEl('button', 'graph-size-item', `${p.rows} × ${p.cols}`);
        item.addEventListener('click', () => {
            popover.style.display = 'none';
            resizeGraphGrid(tab.id, p.rows, p.cols);
        });
        popover.appendChild(item);
    }
    sizeBtn.addEventListener('click', (e) => {
        e.stopPropagation();
        popover.style.display = popover.style.display === 'none' ? '' : 'none';
    });
    sizeWrap.appendChild(sizeBtn);
    sizeWrap.appendChild(popover);
    toolbar.appendChild(sizeWrap);

    // Description toggle button
    const descBtn = mkEl('button', 'graph-desc-btn', 'Desc');
    descBtn.title = 'Toggle description labels';
    descBtn.addEventListener('click', () => {
        graphState[tab.id].showDesc = !graphState[tab.id].showDesc;
        descBtn.classList.toggle('graph-desc-btn--active', graphState[tab.id].showDesc);
        const state = graphState[tab.id];
        for (let i = 0; i < state.cells.length; i++) {
            if (state.cells[i].chart) state.cells[i].chart.options._showDesc = state.showDesc;
            updateCellPanel(tab.id, i);
        }
    });
    toolbar.appendChild(descBtn);

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
    document.addEventListener('mousedown', dismiss);
    graphState[tab.id]._dismissHandler = dismiss;

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

        const cellEl = buildGraphCell(tabId, i);
        gridEl.appendChild(cellEl);
        cell.cellEl = cellEl;

        const canvas = cellEl.querySelector('canvas');
        cell.chart   = createCellChart(canvas);

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
    const dropdown = mkEl('div', 'graph-dropdown');
    dropdown.style.display = 'none';
    document.body.appendChild(dropdown);   // appended to body so fixed positioning is unambiguous
    searchWrap.appendChild(searchInput);
    panel.appendChild(searchWrap);

    // ── Chart area ──────────────────────────────────────────────────────────
    const chartArea = mkEl('div', 'graph-chart-area');
    const canvas    = document.createElement('canvas');
    chartArea.appendChild(canvas);

    cellEl.appendChild(panel);
    cellEl.appendChild(chartArea);

    const handleSearch = debounce(() => {
        const q = searchInput.value.trim();
        if (!q) { dropdown.style.display = 'none'; return; }
        let re;
        try { re = new RegExp(q, 'i'); } catch { dropdown.style.display = 'none'; return; }
        const selected = new Set((graphState[tabId]?.cells[cellIdx]?.channels ?? []).map(c => c.refDes));
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
            const item = mkEl('div', 'graph-dropdown-item');
            item.appendChild(mkEl('span', 'graph-dropdown-refdes', refDes));
            if (desc) item.appendChild(mkEl('span', 'graph-dropdown-desc', desc));
            item.addEventListener('mousedown', (e) => {
                e.preventDefault();
                addChannelToCell(tabId, cellIdx, refDes);
                searchInput.focus();
                handleSearch();
            });
            dropdown.appendChild(item);
        }
        // Position dropdown above the search bar
        const r = searchInput.getBoundingClientRect();
        dropdown.style.left       = r.left + 'px';
        dropdown.style.width      = r.width + 'px';
        dropdown.style.top        = '-9999px';   // off-screen while measuring
        dropdown.style.bottom     = '';
        dropdown.style.display    = '';
        const h = dropdown.offsetHeight;
        dropdown.style.top        = Math.max(4, r.top - h) + 'px';
    }, 150);

    searchInput.addEventListener('input', handleSearch);
    searchInput.addEventListener('blur', () => setTimeout(() => { dropdown.style.display = 'none' }, 150));

    cellEl._dropdown = dropdown;  // track for cleanup on grid rebuild
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
                            const val    = typeof item.parsed.y === 'number' ? item.parsed.y.toFixed(2) : item.parsed.y;
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
    const ch = { refDes, color, hidden: false, yAxisId: 1 };
    cell.channels.push(ch);

    if (!channelBuffers[refDes]) channelBuffers[refDes] = { ts: [], vals: [] };
    activeGraphChannels.add(refDes);

    addDatasetToChart(cell.chart, refDes, color, false, ch);
    syncYAxisVisibility(cell);
    updateCellPanel(tabId, cellIdx);
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
        yAxisID:         'y' + (ch?.yAxisId ?? 1)
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
            openColorPalette(swatch, ch.color, (newColor) => {
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
            if (obj.type === 'sensor' && obj.refDes) {
                activePidChannels.add(obj.refDes);
                if (!channelBuffers[obj.refDes]) channelBuffers[obj.refDes] = { ts: [], vals: [] };
            }
        }
    }
}

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
        while (buf.ts.length && buf.ts[0] < graphCutoff) { buf.ts.shift(); buf.vals.shift(); }
    }

    // Buffer PID-only channels (skip any already handled above by the graph)
    for (const refDes of activePidChannels) {
        if (activeGraphChannels.has(refDes)) continue;
        if (!(refDes in data)) continue;
        const buf = channelBuffers[refDes];
        if (!buf) continue;
        buf.ts.push(now);
        buf.vals.push(data[refDes]);
        while (buf.ts.length && buf.ts[0] < pidCutoff) { buf.ts.shift(); buf.vals.shift(); }
    }
}

function buildChartData(buf, displayEnd, viewWindowSec) {
    const { ts, vals } = buf;
    if (!ts.length) return [];
    const absMin = displayEnd - viewWindowSec;
    const absMax = displayEnd;
    const out = [];
    for (let i = 0; i < ts.length; i++) {
        const t = ts[i], v = vals[i];
        // Interpolate entry at left boundary
        if (i > 0 && ts[i - 1] < absMin && t >= absMin) {
            const frac = (absMin - ts[i - 1]) / (t - ts[i - 1]);
            out.push({ x: -viewWindowSec, y: vals[i - 1] + frac * (v - vals[i - 1]) });
        }
        if (t >= absMin && t <= absMax) out.push({ x: t - displayEnd, y: v });
        // Interpolate exit at right boundary
        if (i + 1 < ts.length && t <= absMax && ts[i + 1] > absMax) {
            const frac = (absMax - t) / (ts[i + 1] - t);
            out.push({ x: 0, y: v + frac * (vals[i + 1] - v) });
            break;
        }
    }
    return out;
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
            // Snap back to live-follow when pinned view is within 10% of the live edge
            if (cell.viewEnd !== null && latestTs - cell.viewEnd < cell.viewWindowSec * 0.1) cell.viewEnd = null;
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
            });
        }
    }, { passive: false });
}

function attachProximityTooltip(canvas, cell) {
    const HOVER_PX = 14;
    canvas.addEventListener('mousemove', (e) => {
        const chart = cell.chart;
        const rect  = canvas.getBoundingClientRect();
        const cx    = e.clientX - rect.left;
        const cy    = e.clientY - rect.top;

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
                const dist = Math.sqrt(dx * dx + dy * dy);
                if (dist < closestDist) { closestDist = dist; closestIdx = pi; }
            }
            if (closestDist <= HOVER_PX) activeEls.push({ datasetIndex: di, index: closestIdx });
        }

        chart.tooltip.setActiveElements(activeEls, { x: cx, y: cy });
        chart.update('none');
    });

    canvas.addEventListener('mouseleave', () => {
        cell.chart.tooltip.setActiveElements([], {});
        cell.chart.update('none');
    });
}

function openColorPalette(anchorEl, currentColor, onSelect) {
    const existing = document.querySelector('.color-palette-popup');
    if (existing) existing.remove();

    const popup = mkEl('div', 'color-palette-popup');

    for (const c of CHART_COLORS) {
        const opt = mkEl('div', `color-palette-option${c === currentColor ? ' active' : ''}`);
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
    const customBtn  = mkEl('div', 'color-palette-custom');
    customBtn.title  = 'Custom color';
    customBtn.textContent = '✎';
    const hiddenInput = document.createElement('input');
    hiddenInput.type  = 'color';
    hiddenInput.value = currentColor;
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
