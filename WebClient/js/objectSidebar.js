// =============================================================================
// Object Detail Sidebar
// =============================================================================
// Shown when the user right-clicks a sensor object in a Front Panel tab.
// Displays a graph pre-populated with ALL channels of the parent control,
// using the same chart infrastructure as the Graph tab.
// =============================================================================

const SIDEBAR_TAB_ID  = '__sidebar__';
const SIDEBAR_CELL_IDX = 0;

// Build the sidebar DOM once at load time and register it in graphState.
(function initObjectSidebar() {
    // ── Outer container ──────────────────────────────────────────────────────
    const el = document.createElement('div');
    el.id = 'object-sidebar';
    el.className = 'object-sidebar';
    el.style.display = 'none';

    // ── Header ───────────────────────────────────────────────────────────────
    const header    = mkEl('div', 'object-sidebar-header');
    const titleWrap = mkEl('div', 'object-sidebar-title');
    const refdesEl  = mkEl('span', 'object-sidebar-refdes');
    const descEl    = mkEl('span', 'object-sidebar-desc');
    titleWrap.append(refdesEl, descEl);
    const closeBtn = mkEl('button', 'object-sidebar-close', '×');
    closeBtn.title = 'Close';
    closeBtn.addEventListener('click', closeObjectSidebar);
    header.append(titleWrap, closeBtn);
    el.appendChild(header);

    // ── Graph cell (column layout: chart on top, panel below) ────────────────
    const cellEl = document.createElement('div');
    cellEl.className = 'graph-cell graph-cell--column';

    // Chart area (top)
    const chartArea = mkEl('div', 'graph-chart-area');
    const canvas    = document.createElement('canvas');
    chartArea.appendChild(canvas);
    cellEl.appendChild(chartArea);

    // Channel panel (bottom): list + search bar
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
    cellEl.appendChild(panel);

    el.appendChild(cellEl);
    document.getElementById('tab-viewport').appendChild(el);

    // ── Chart (reuses same factory as Graph tab) ─────────────────────────────
    const chart = createCellChart(canvas);

    // ── Cell state object (matches graphState[tabId].cells[i] shape) ─────────
    const cell = {
        cellEl,
        chart,
        channels:      [],
        viewWindowSec: 60,
        viewEnd:       null,
    };

    // Attach drag-pan, scroll-zoom and proximity tooltip — same as graph tab cells
    attachDragPan(canvas, cell);
    attachScrollZoom(canvas, cell);
    attachProximityTooltip(canvas, cell);

    // ── Search dropdown (appended to body for unambiguous fixed positioning) ──
    const dropdown = mkEl('div', 'graph-dropdown');
    dropdown.style.display = 'none';
    document.body.appendChild(dropdown);
    cellEl._dropdown = dropdown;   // tracked for potential future cleanup

    const handleSearch = debounce(() => {
        const q = searchInput.value.trim();
        if (!q) { dropdown.style.display = 'none'; return; }
        let re;
        try { re = new RegExp(q, 'i'); } catch { dropdown.style.display = 'none'; return; }
        const selected = new Set(cell.channels.map(c => c.refDes));
        const matches  = [];
        for (const ctrl of configControls) {
            for (const ch of (ctrl.channels ?? [])) {
                if (!selected.has(ch.refDes) &&
                    (re.test(ch.refDes) || re.test(ctrl.description || ''))) {
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
            item.addEventListener('mousedown', (ev) => {
                ev.preventDefault();
                addChannelToCell(SIDEBAR_TAB_ID, SIDEBAR_CELL_IDX, refDes);
                searchInput.focus();
                handleSearch();
            });
            dropdown.appendChild(item);
        }
        // Position dropdown above the search input
        const r = searchInput.getBoundingClientRect();
        dropdown.style.left    = r.left + 'px';
        dropdown.style.width   = r.width + 'px';
        dropdown.style.top     = '-9999px';
        dropdown.style.bottom  = '';
        dropdown.style.display = '';
        const h = dropdown.offsetHeight;
        dropdown.style.top     = Math.max(4, r.top - h) + 'px';
    }, 150);

    searchInput.addEventListener('input', handleSearch);
    searchInput.addEventListener('blur', () => {
        setTimeout(() => { dropdown.style.display = 'none'; }, 150);
    });

    // ── Store header element refs on the container for openObjectSidebar ──────
    el._refdesEl = refdesEl;
    el._descEl   = descEl;

    // ── Register in graphState ────────────────────────────────────────────────
    // This lets updateActiveGraphChannels() and updateAllGraphs() handle the
    // sidebar automatically. The '__sidebar__' key is recognised by updateAllGraphs.
    graphState[SIDEBAR_TAB_ID] = {
        rows: 1, cols: 1, gridEl: null,
        cells: [cell],
        sizeBtn: null, showDesc: false, _dismissHandler: null,
    };
})();

// =============================================================================
// Open
// =============================================================================

function openObjectSidebar(refDes) {
    const sidebarEl = document.getElementById('object-sidebar');
    if (!sidebarEl) return;

    // Sync chart colors to the current theme (chart may have been created before
    // the theme was applied from localStorage on first page load).
    const _state = graphState[SIDEBAR_TAB_ID];
    if (_state?.cells[0]?.chart) applyChartColors(_state.cells[0].chart);

    // Find the parent control that owns this channel refDes
    const ctrl = configControls.find(c => c.channels?.some(ch => ch.refDes === refDes));
    if (!ctrl) return;

    const state = graphState[SIDEBAR_TAB_ID];
    if (!state) return;
    const cell = state.cells[0];

    // Clear any existing channels
    for (const rd of [...cell.channels.map(c => c.refDes)]) {
        removeChannelFromCell(SIDEBAR_TAB_ID, SIDEBAR_CELL_IDX, rd);
    }

    // Update header
    sidebarEl._refdesEl.textContent = ctrl.refDes;
    sidebarEl._descEl.textContent   = ctrl.description ?? '';

    // Add ALL channels from this control
    for (const ch of (ctrl.channels ?? [])) {
        addChannelToCell(SIDEBAR_TAB_ID, SIDEBAR_CELL_IDX, ch.refDes);
    }

    sidebarEl.style.display = '';

    // Trigger an immediate resize so the chart fills its container correctly
    setTimeout(() => cell.chart?.resize(), 0);
}

// =============================================================================
// Close
// =============================================================================

// Close the sidebar on any right-click outside it
document.addEventListener('contextmenu', (e) => {
    const sidebarEl = document.getElementById('object-sidebar');
    if (!sidebarEl?.contains(e.target)) closeObjectSidebar();
});

function closeObjectSidebar() {
    const sidebarEl = document.getElementById('object-sidebar');
    if (!sidebarEl) return;

    sidebarEl.style.display = 'none';

    const state = graphState[SIDEBAR_TAB_ID];
    if (!state) return;
    const cell = state.cells[0];

    // Remove all channels so their buffers are freed
    for (const rd of [...cell.channels.map(c => c.refDes)]) {
        removeChannelFromCell(SIDEBAR_TAB_ID, SIDEBAR_CELL_IDX, rd);
    }
}
