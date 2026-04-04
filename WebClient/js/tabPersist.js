// =============================================================================
// Tab persistence — save/restore tab state across page reloads
// =============================================================================
//
// saveTabState()  — serializes current tabs to sessionStorage['rtc-tab-state']
// restoreTabState() — reads sessionStorage and recreates tabs; called from
//                    applyConfig() in ws.js after the server config arrives.
// reloadWithTabState() — saves state then calls location.reload(); used by
//                        the "Reload" button in the alerts bar.
// =============================================================================

const TAB_STATE_KEY = 'rtc-tab-state';

function saveTabState() {
    const state = {
        activeIndex: tabs.findIndex(t => t.id === activeTabId),
        tabs: tabs.map(tab => {
            const entry = { type: tab.type };
            if (tab.type === 'frontPanel') {
                entry.layoutFilename = tab.pid?.layoutFilename || '';
            }
            if (tab.type === 'graph') {
                const gs = graphState[tab.id];
                if (gs) {
                    entry.graph = {
                        rows: gs.rows,
                        cols: gs.cols,
                        cells: gs.cells.map(c => ({
                            viewWindowSec: c.viewWindowSec ?? 60,
                            channels: (c.channels || []).map(ch => ({
                                refDes: ch.refDes,
                                color:  ch.color,
                                hidden: ch.hidden,
                                yAxis:  ch.yAxis,
                            })),
                        })),
                    };
                }
            }
            return entry;
        }),
    };
    try {
        sessionStorage.setItem(TAB_STATE_KEY, JSON.stringify(state));
    } catch (e) {
        console.warn('tabPersist: could not save tab state:', e);
    }
}

function restoreTabState() {
    let raw;
    try { raw = sessionStorage.getItem(TAB_STATE_KEY); } catch { return; }
    if (!raw) return;
    try { sessionStorage.removeItem(TAB_STATE_KEY); } catch {}

    let state;
    try { state = JSON.parse(raw); } catch { return; }
    if (!Array.isArray(state.tabs) || state.tabs.length === 0) return;

    // Remove any default tabs that were auto-created on startup
    while (tabs.length > 0) {
        const t = tabs[0];
        if (t.type === 'graph')      cleanupGraphTab(t.id);
        if (t.type === 'frontPanel') cleanupPidGraphStates(t.id);
        t.contentEl.remove();
        tabs.splice(0, 1);
    }
    renderTabBar();

    for (const entry of state.tabs) {
        const tab = addTab(entry.type);

        if (entry.type === 'frontPanel' && entry.layoutFilename) {
            if (pidLayouts[entry.layoutFilename]) {
                loadPidLayout(tab, pidLayouts[entry.layoutFilename]);
            } else {
                // Layout hasn't arrived from the server yet; mark as pending.
                // applyPidLayout in ws.js will load it when the message arrives.
                tab.pid.pendingLayout = entry.layoutFilename;
            }
        }

        if (entry.type === 'graph' && entry.graph) {
            const g = entry.graph;
            resizeGraphGrid(tab.id, g.rows, g.cols);
            const gs = graphState[tab.id];
            if (gs) {
                g.cells.forEach((savedCell, i) => {
                    if (i >= gs.cells.length) return;
                    gs.cells[i].viewWindowSec = savedCell.viewWindowSec ?? 60;
                    for (const ch of (savedCell.channels || [])) {
                        addChannelToCell(tab.id, i, ch.refDes);
                        // Restore hidden state and color if they differ from defaults
                        const live = gs.cells[i].channels.find(c => c.refDes === ch.refDes);
                        if (live) {
                            if (ch.color)  live.color  = ch.color;
                            if (ch.hidden) live.hidden = ch.hidden;
                        }
                    }
                });
            }
        }
    }

    // Activate the previously active tab (by index, since IDs are regenerated)
    const targetIdx = Math.min(state.activeIndex ?? 0, tabs.length - 1);
    if (tabs[targetIdx]) activateTab(tabs[targetIdx].id);
}

function reloadWithTabState() {
    saveTabState();
    location.reload();
}
