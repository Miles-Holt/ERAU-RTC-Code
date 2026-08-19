// =============================================================================
// Object Detail Sidebar
// =============================================================================
// Shown when the user right-clicks a sensor object in a Front Panel tab.
// Displays a graph pre-populated with ALL channels of the parent control,
// using the same chart infrastructure as the Graph tab.
// =============================================================================

const SIDEBAR_TAB_ID  = '__sidebar__';
const SIDEBAR_CELL_IDX = 0;

// The SVG group currently glowing to mark which object the panel is bound to.
// Exactly one object may glow at a time, so this is a single slot, not a set —
// openObjectSidebar clears it before adopting a new target and closeObjectSidebar
// clears it on the way out. Tracked here (rather than reading '.selected' back
// off the DOM) because a caller with no group at all — e.g. a graph object,
// see openObjectSidebarForGraph in pid.js — legitimately opens the panel
// without anything to glow.
let sidebarGlowEl = null;

// sidebarSetGlow retargets the glow to `el` (or clears it for `null`), leaving
// every other class on the outgoing/incoming groups untouched. It deliberately
// does not go through pidSetObjectState: that function also re-derives the
// st-live/st-stale/alarmed axes from arguments this module doesn't have, and
// calling it with placeholder values would clobber real state. 'selected' is
// layered on top of those axes (see pidSetObjectState's own comment), so
// toggling the class directly is the correct, narrower operation.
function sidebarSetGlow(el) {
    if (sidebarGlowEl && sidebarGlowEl !== el) sidebarGlowEl.classList.remove('selected');
    sidebarGlowEl = el || null;
    if (sidebarGlowEl) sidebarGlowEl.classList.add('selected');
}

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
    const searchDropdown = createChannelSearchDropdown(searchInput, {
        getExcluded: () => new Set(cell.channels.map(c => c.refDes)),
        onPick:      (refDes) => addChannelToCell(SIDEBAR_TAB_ID, SIDEBAR_CELL_IDX, refDes),
        position:    'above',
    });
    cellEl._dropdown = searchDropdown.dropdownEl;   // tracked for potential future cleanup

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

// objEl is the SVG group the caller right-clicked — passed through so the
// panel can glow it (item 01) and clear the glow again on close or retarget.
// Optional: openObjectSidebarForGraph has no such group and passes nothing.
function openObjectSidebar(refDes, objEl) {
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

    // Retarget the glow. Runs after the ctrl lookup above so a right-click on
    // an unbound/misconfigured object (silent failure, item 03) leaves the
    // panel — and whatever it was already glowing — exactly as it was.
    sidebarSetGlow(objEl);

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

// Esc closes the panel, matching the command panels (machinePanel.js,
// valveDropdown.js). Side panels never pin — settled in the design doc — so
// there is no "only if unpinned" guard to carry over from those.
//
// A right-click elsewhere does NOT close it any more. That used to be a
// document-level 'contextmenu' listener that closed on any right-click whose
// target wasn't inside the panel. Every object's own contextmenu handler
// already calls e.stopPropagation(), so a right-click on ANOTHER object never
// reached this listener at all — retargeting worked only because that handler
// calls openObjectSidebar(refDes, g) directly (see pid.js) and overwrites the
// panel in place, not because of anything this listener did. What the listener
// actually did was close the panel on a right-click on empty canvas, blank UI,
// or any other object type with no handler of its own — which is the real
// defect item 02 fixes. Removing it, and keeping the per-object retarget call
// and the new sidebarSetGlow() below, is the whole fix.
document.addEventListener('keydown', (e) => {
    if (e.key !== 'Escape') return;
    const sidebarEl = document.getElementById('object-sidebar');
    if (sidebarEl && sidebarEl.style.display !== 'none') closeObjectSidebar();
});

function closeObjectSidebar() {
    const sidebarEl = document.getElementById('object-sidebar');
    if (!sidebarEl) return;

    sidebarEl.style.display = 'none';
    sidebarSetGlow(null);

    const state = graphState[SIDEBAR_TAB_ID];
    if (!state) return;
    const cell = state.cells[0];

    // Remove all channels so their buffers are freed
    for (const rd of [...cell.channels.map(c => c.refDes)]) {
        removeChannelFromCell(SIDEBAR_TAB_ID, SIDEBAR_CELL_IDX, rd);
    }
}
