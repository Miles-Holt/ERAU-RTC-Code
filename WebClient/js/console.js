// =============================================================================
// Console tab
// =============================================================================

function buildConsoleContent(tab) {
    consoleTabs.push(tab);

    const wrapper = mkEl('div', 'console-tab');

    // ── Toolbar row 1: direction + type toggles + clear ──
    const toolbar1 = mkEl('div', 'console-toolbar');

    // Direction filter
    const dirLabel = mkEl('span', 'console-lbl', 'Dir:');
    function makeDirBtn(label, value) {
        const btn = mkEl('button', 'console-filter-btn console-filter-active', label);
        btn.dataset.value = value;
        btn.addEventListener('click', () => {
            btn.classList.toggle('console-filter-active');
            reRenderConsole(tab);
        });
        return btn;
    }
    const btnIn  = makeDirBtn('← in',  'in');
    const btnOut = makeDirBtn('→ out', 'out');

    // Type filter
    const typeLabel = mkEl('span', 'console-lbl', 'Type:');
    function makeTypeBtn(label) {
        const btn = mkEl('button', 'console-filter-btn console-filter-active', label);
        btn.dataset.value = label;
        btn.addEventListener('click', () => {
            btn.classList.toggle('console-filter-active');
            reRenderConsole(tab);
        });
        return btn;
    }
    const btnData   = makeTypeBtn('data');
    const btnConfig = makeTypeBtn('config');
    const btnCmd    = makeTypeBtn('cmd');
    const btnOther  = makeTypeBtn('other');

    const clearBtn = mkEl('button', 'btn console-clear', 'Clear');
    clearBtn.addEventListener('click', () => { consoleLog.length = 0; tab._consoleLogEl.innerHTML = ''; });

    toolbar1.append(dirLabel, btnIn, btnOut, typeLabel, btnData, btnConfig, btnCmd, btnOther, clearBtn);

    // ── Toolbar row 2: free-text / regex filter + buffer size ──
    const toolbar2 = mkEl('div', 'console-toolbar console-toolbar-row2');

    const searchInput = document.createElement('input');
    searchInput.type = 'text';
    searchInput.placeholder = 'Filter (text or /regex/)';
    searchInput.className = 'console-search';

    const limLbl   = mkEl('label', 'console-lbl', 'Buffer:');
    const limInput = document.createElement('input');
    limInput.type = 'number'; limInput.min = 50; limInput.max = 5000;
    limInput.value = CONFIG.consoleBufferLimit;
    limInput.className = 'console-buf-input';

    toolbar2.append(searchInput, limLbl, limInput);

    const logEl = mkEl('div', 'console-log');
    wrapper.append(toolbar1, toolbar2, logEl);
    tab.contentEl.appendChild(wrapper);

    tab._consoleLogEl = logEl;
    tab._consoleLimit = () => parseInt(limInput.value) || CONFIG.consoleBufferLimit;

    // Filter state accessors stored on tab
    tab._consoleFilter = () => {
        const dirs    = new Set([btnIn, btnOut].filter(b => b.classList.contains('console-filter-active')).map(b => b.dataset.value));
        const types   = new Set([btnData, btnConfig, btnCmd, btnOther].filter(b => b.classList.contains('console-filter-active')).map(b => b.dataset.value));
        const raw     = searchInput.value.trim();
        let regex     = null;
        if (raw) {
            const m = raw.match(/^\/(.+)\/([gimy]*)$/);
            try { regex = m ? new RegExp(m[1], m[2]) : new RegExp(raw.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'), 'i'); }
            catch { regex = null; }
        }
        return { dirs, types, regex };
    };

    searchInput.addEventListener('input',  () => reRenderConsole(tab));
    limInput.addEventListener('change',    () => reRenderConsole(tab));

    for (const entry of consoleLog) appendConsoleEntry(tab, entry);
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
    if (!tab._consoleLogEl) return;

    const f = tab._consoleFilter?.();
    if (f) {
        // Direction filter
        if (!f.dirs.has(entry.dir)) return;
        // Type filter
        const msgType = entry.msg?.type ?? 'other';
        const bucket  = ['data', 'config', 'cmd'].includes(msgType) ? msgType : 'other';
        if (!f.types.has(bucket)) return;
        // Text/regex filter
        if (f.regex) {
            const serialised = JSON.stringify(entry.msg);
            if (!f.regex.test(serialised)) return;
        }
    }

    const logEl = tab._consoleLogEl;
    const el    = mkEl('div', `console-entry console-${entry.dir}`);
    const time  = new Date(entry.time).toLocaleTimeString('en-US', {
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
