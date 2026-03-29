// =============================================================================
// Graph tab
// =============================================================================

const GRID_PRESETS = [
    { rows: 1, cols: 1 }, { rows: 1, cols: 2 }, { rows: 1, cols: 3 },
    { rows: 2, cols: 1 }, { rows: 2, cols: 2 }, { rows: 2, cols: 3 },
    { rows: 2, cols: 4 }, { rows: 3, cols: 2 }, { rows: 3, cols: 3 },
    { rows: 3, cols: 4 }, { rows: 4, cols: 3 }, { rows: 4, cols: 4 },
];

function buildGraphContent(tab) {
    graphState[tab.id] = { rows: 1, cols: 1, gridEl: null, cells: [], sizeBtn: null, _dismissHandler: null };

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
        const all     = configControls.flatMap(c => (c.channels ?? []).map(ch => ch.refDes));
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

function createCellChart(canvas) {
    const yAxes = {};
    for (let i = 1; i <= 6; i++) {
        const isLeft = i % 2 === 1;
        yAxes['y' + i] = {
            position: isLeft ? 'left' : 'right',
            display:  false,
            ticks:    { color: '#8d969e' },
            grid:     isLeft ? { color: '#30363d' } : { drawOnChartArea: false }
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
                        color:         '#8d969e',
                        maxTicksLimit: 12,
                        maxRotation:   0,
                        callback: (v) => {
                            const ago = Math.round(Date.now() / 1000 - v);
                            if (ago < 60) return ago + 's';
                            return Math.round(ago / 60) + 'm';
                        }
                    },
                    grid: { color: '#30363d' }
                },
                ...yAxes
            },
            plugins: {
                legend:  { display: false },
                tooltip: {
                    mode:      'index',
                    intersect: false,
                    callbacks: {
                        title: (items) => {
                            if (!items.length) return '';
                            const ago  = Math.round(Date.now() / 1000 - items[0].parsed.x);
                            if (ago < 60) return ago + 's ago';
                            const m = Math.floor(ago / 60);
                            const s = ago % 60;
                            return s > 0 ? `${m}m ${s}s ago` : `${m}m ago`;
                        }
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

        // Channel name
        const lbl = mkEl('span', `channel-name${ch.hidden ? ' channel-hidden' : ''}`, ch.refDes);
        lbl.title = 'Click to toggle visibility';
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
            let latestTs = Date.now() / 1000;
            for (const ds of cell.chart.data.datasets) {
                const buf = channelBuffers[ds.label];
                if (!buf) continue;
                ds.data = buf.ts.map((t, i) => ({ x: t, y: buf.vals[i] }));
                if (buf.ts.length) latestTs = Math.max(latestTs, buf.ts[buf.ts.length - 1]);
            }
            // Snap back to live-follow if pinned view is within 5s of the live edge
            if (cell.viewEnd !== null && latestTs - cell.viewEnd < 5) cell.viewEnd = null;
            // viewEnd === null means live-follow; never write latestTs back so it keeps advancing
            const displayEnd = cell.viewEnd ?? latestTs;
            cell.chart.options.scales.x.min = displayEnd - cell.viewWindowSec;
            cell.chart.options.scales.x.max = displayEnd;
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
                const end = cell.viewEnd ?? Date.now() / 1000;
                cell.chart.options.scales.x.min = end - cell.viewWindowSec;
                cell.chart.options.scales.x.max = end;
                cell.chart.update('none');
            });
        }
    }, { passive: false });
}

function attachProximityTooltip(canvas, cell) {
    const HOVER_PX = 28;
    canvas.addEventListener('mousemove', (e) => {
        const chart = cell.chart;
        const rect  = canvas.getBoundingClientRect();
        const cx    = e.clientX - rect.left;
        const cy    = e.clientY - rect.top;

        let closestDist = Infinity;
        let closestIdx  = -1;

        for (let di = 0; di < chart.data.datasets.length; di++) {
            const meta = chart.getDatasetMeta(di);
            if (meta.hidden) continue;
            for (let pi = 0; pi < meta.data.length; pi++) {
                const pt   = meta.data[pi];
                const dx   = cx - pt.x;
                const dy   = cy - pt.y;
                const dist = Math.sqrt(dx * dx + dy * dy);
                if (dist < closestDist) { closestDist = dist; closestIdx = pi; }
            }
        }

        if (closestDist <= HOVER_PX && closestIdx !== -1) {
            const activeEls = [];
            for (let di = 0; di < chart.data.datasets.length; di++) {
                const meta = chart.getDatasetMeta(di);
                if (meta.hidden || !meta.data.length) continue;
                activeEls.push({ datasetIndex: di, index: Math.min(closestIdx, meta.data.length - 1) });
            }
            chart.tooltip.setActiveElements(activeEls, { x: cx, y: cy });
        } else {
            chart.tooltip.setActiveElements([], {});
        }
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
