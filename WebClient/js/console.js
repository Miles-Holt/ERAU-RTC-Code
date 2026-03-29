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
