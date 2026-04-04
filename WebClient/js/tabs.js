// =============================================================================
// Tab management
// =============================================================================

const TAB_TYPE_LABELS = {
    frontPanel: 'Front Panel',
    dataView:   'Channel List',
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
    tab.contentEl.classList.remove('tab-content--fixed');
    tab.channelUpdaters = {};
    switch (tab.type) {
        case 'frontPanel': buildFrontPanelContent(tab); break;
        case 'dataView':   rebuildDataView(tab);        break;
        case 'graph':      buildGraphContent(tab);      break;
        case 'dev':        buildDevContent(tab);        break;
        case 'console':    buildConsoleContent(tab);    break;
    }
}

function cleanupPidGraphStates(tabId) {
    const pfx = '__pid_graph_' + tabId + '_';
    for (const key of Object.keys(graphState)) {
        if (!key.startsWith(pfx)) continue;
        const st = graphState[key];
        for (const cell of st.cells) {
            for (const ch of [...cell.channels]) removeChannelFromCell(key, 0, ch.refDes);
            cell.chart?.destroy();
        }
        delete graphState[key];
    }
}

function removeTab(id) {
    const idx = tabs.findIndex(t => t.id === id);
    if (idx === -1) return;
    const tab = tabs[idx];

    if (tab.type === 'graph')      cleanupGraphTab(id);
    if (tab.type === 'frontPanel') cleanupPidGraphStates(id);
    if (tab.type === 'dev')        devTabs     = devTabs.filter(t => t.id !== id);
    if (tab.type === 'console')    consoleTabs = consoleTabs.filter(t => t.id !== id);

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

    if (tab.type === 'graph')      cleanupGraphTab(id);
    if (tab.type === 'frontPanel') cleanupPidGraphStates(id);
    if (tab.type === 'dev')        devTabs     = devTabs.filter(t => t.id !== id);
    if (tab.type === 'console')    consoleTabs = consoleTabs.filter(t => t.id !== id);

    tab.type = newType;
    tab.name = nextTabName(newType);
    buildTabContent(tab);
    renderTabBar();
}

function activateTab(id) {
    const prevTab = tabs.find(t => t.id === activeTabId);
    activeTabId = id;
    for (const tab of tabs) {
        const active = tab.id === id;
        tab.contentEl.style.display = active ? '' : 'none';
        if (active && tab.type === 'graph') {
            setTimeout(() => resizeGraphCharts(id), 0);
        }
    }
    // Close the object sidebar when leaving a front panel tab
    if (prevTab && prevTab.type === 'frontPanel' && prevTab.id !== id) {
        closeObjectSidebar();
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
        el.addEventListener('click', (e) => {
            if (tab.id === activeTabId) { e.stopPropagation(); startRename(tab, nameEl); }
            else activateTab(tab.id);
        });
        el.addEventListener('contextmenu', (e) => {
            e.preventDefault();
            showContextMenu(e.clientX, e.clientY, [
                { label: 'Front Panel', action: () => changeTabType(tab.id, 'frontPanel') },
                { label: 'Graph',       action: () => changeTabType(tab.id, 'graph')      },
                { label: 'Channel List', action: () => changeTabType(tab.id, 'dataView')   },
                { label: 'Console',     action: () => changeTabType(tab.id, 'console')    },
                { label: 'Dev',         action: () => changeTabType(tab.id, 'dev')        },
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


// Front Panel tab content is built by pid.js → buildFrontPanelContent(tab)
