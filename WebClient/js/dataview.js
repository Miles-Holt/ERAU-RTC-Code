// =============================================================================
// Channel List tab
// =============================================================================

const DV_BUFFER_SECS = 15;
const DV_STALE_MS    = 2000;

// ---------------------------------------------------------------------------
// Entry point — called by tabs.js on tab create and config reload
// ---------------------------------------------------------------------------

function rebuildDataView(tab) {
    if (!tab.dvRows)    tab.dvRows    = [];
    if (!tab.dvBuffers) tab.dvBuffers = {};
    if (!tab.dvCharts)  tab.dvCharts  = {};

    tab.contentEl.innerHTML = '';
    tab.contentEl.classList.add('tab-content--fixed');
    tab.channelUpdaters = {};

    if (!configControls.length) {
        tab.contentEl.appendChild(mkEl('div', 'loading', 'Waiting for configuration from LabVIEW...'));
        return;
    }

    // Search bar
    const searchWrap = mkEl('div', 'dv-search-wrap');
    const searchInput = document.createElement('input');
    searchInput.type = 'text';
    searchInput.className = 'dv-search-input';
    searchInput.placeholder = 'Search channels (regex)…';
    searchWrap.appendChild(searchInput);
    tab.contentEl.appendChild(searchWrap);

    // Rows container
    const rowsEl = mkEl('div', 'dv-rows');
    tab.contentEl.appendChild(rowsEl);
    tab._dvRowsEl = rowsEl;

    // Re-render any rows that survived a config reload
    // Filter out refDes that no longer exist in the new config
    tab.dvRows = tab.dvRows.filter(r => _dvFindChannel(r) !== null);
    for (const refDes of [...tab.dvRows]) {
        tab.dvRows.splice(tab.dvRows.indexOf(refDes), 1); // addDvRow will re-push
        _addDvRow(tab, refDes);
    }

    _renderDvSearchDropdown(tab, searchInput);
}

// ---------------------------------------------------------------------------
// Search / dropdown
// ---------------------------------------------------------------------------

function _renderDvSearchDropdown(tab, input) {
    const dropdown = mkEl('div', 'graph-dropdown');
    document.body.appendChild(dropdown);

    let dropdownOpen = false;

    const closeDropdown = () => {
        dropdown.style.display = 'none';
        dropdownOpen = false;
    };

    const openDropdown = () => {
        const rect = input.getBoundingClientRect();
        dropdown.style.top    = `${rect.bottom + window.scrollY + 2}px`;
        dropdown.style.left   = `${rect.left   + window.scrollX}px`;
        dropdown.style.width  = `${rect.width}px`;
        dropdown.style.display = '';
        dropdownOpen = true;
    };

    const populateDropdown = debounce(() => {
        dropdown.innerHTML = '';
        const q = input.value.trim();
        if (!q) { closeDropdown(); return; }

        let re;
        try { re = new RegExp(q, 'i'); } catch { closeDropdown(); return; }

        const matches = [];
        for (const ctrl of configControls) {
            for (const ch of (ctrl.channels ?? [])) {
                if (re.test(ch.refDes) || re.test(ctrl.description || '')) {
                    if (!tab.dvRows.includes(ch.refDes)) {
                        matches.push({ refDes: ch.refDes, desc: ctrl.description || '' });
                    }
                }
            }
            if (matches.length >= 20) break;
        }

        if (!matches.length) { closeDropdown(); return; }

        for (const m of matches) {
            const item = mkEl('div', 'graph-dropdown-item');
            item.textContent = m.refDes + (m.desc ? ` — ${m.desc}` : '');
            item.addEventListener('mousedown', (e) => {
                e.preventDefault();
                _addDvRow(tab, m.refDes);
                input.value = '';
                closeDropdown();
            });
            dropdown.appendChild(item);
        }
        openDropdown();
    }, 150);

    input.addEventListener('input', populateDropdown);
    input.addEventListener('focus', populateDropdown);
    input.addEventListener('blur', () => setTimeout(closeDropdown, 150));

    // Clean up dropdown when tab is destroyed (contentEl replaced)
    const observer = new MutationObserver(() => {
        if (!document.body.contains(input)) {
            dropdown.remove();
            observer.disconnect();
        }
    });
    observer.observe(document.body, { childList: true, subtree: true });
}

// ---------------------------------------------------------------------------
// Row management
// ---------------------------------------------------------------------------

function _dvFindChannel(refDes) {
    for (const ctrl of configControls) {
        for (const ch of (ctrl.channels ?? [])) {
            if (ch.refDes === refDes) return { ctrl, ch };
        }
    }
    return null;
}

function _addDvRow(tab, refDes) {
    const found = _dvFindChannel(refDes);
    if (!found) return;
    const { ctrl, ch } = found;

    tab.dvRows.push(refDes);

    // Init buffer (reset on config reload)
    tab.dvBuffers[refDes] = { ts: [], vals: [] };

    const rowEl = _buildDvRowEl(tab, ctrl, ch);
    tab._dvRowsEl.appendChild(rowEl);
}

function _removeDvRow(tab, refDes, rowEl) {
    const idx = tab.dvRows.indexOf(refDes);
    if (idx !== -1) tab.dvRows.splice(idx, 1);

    if (tab.dvCharts[refDes]) {
        tab.dvCharts[refDes].destroy();
        delete tab.dvCharts[refDes];
    }
    delete tab.dvBuffers[refDes];
    delete tab.channelUpdaters[refDes];
    rowEl.remove();
}

// ---------------------------------------------------------------------------
// Row DOM builder
// ---------------------------------------------------------------------------

function _buildDvRowEl(tab, ctrl, ch) {
    const color = getComputedStyle(document.documentElement).getPropertyValue('--muted').trim() || '#6e7681';
    const refDes = ch.refDes;
    const cmd    = isCmd(ch);

    const row = mkEl('div', 'dv-row');
    row.dataset.refdes = refDes;

    // --- LED ---
    const led = mkEl('div', 'dv-led dv-led-stale');
    row.appendChild(led);

    // --- Left: refDes + description ---
    const left = mkEl('div', 'dv-row-left');
    left.appendChild(mkEl('span', 'dv-row-refdes', refDes));
    left.appendChild(mkEl('span', 'dv-row-desc',   ctrl.description || ''));
    row.appendChild(left);

    // --- Middle: sparkline ---
    const chartWrap = mkEl('div', 'dv-row-chart');
    const canvas    = document.createElement('canvas');
    chartWrap.appendChild(canvas);
    row.appendChild(chartWrap);

    // --- Right: value or command widget ---
    const right = mkEl('div', 'dv-row-right');

    let valEl   = null;
    let inputEl = null;

    if (cmd) {
        inputEl = document.createElement('input');
        inputEl.type      = 'number';
        inputEl.className = 'dv-row-input';
        inputEl.placeholder = ch.role === 'cmd-bool' ? '0 / 1' : '—';
        inputEl.step = ch.role === 'cmd-bool' ? '1' : 'any';

        const sendBtn = mkEl('button', 'btn', 'Send');
        const unitsEl = mkEl('span', 'dv-row-units', ch.units || '');

        const doSend = () => {
            const raw = parseFloat(inputEl.value);
            if (isNaN(raw)) {
                inputEl.classList.add('input-error');
                inputEl.title = 'Not a number';
                return;
            }
            if (ch.role === 'cmd-bool' && raw !== 0 && raw !== 1) {
                inputEl.classList.add('input-error');
                inputEl.title = 'Must be 0 or 1';
                return;
            }
            inputEl.classList.remove('input-error');
            inputEl.title = '';
            sendCommand(refDes, raw);
        };

        sendBtn.addEventListener('click', doSend);
        inputEl.addEventListener('keydown', (e) => { if (e.key === 'Enter') doSend(); });
        inputEl.addEventListener('input', () => {
            inputEl.classList.remove('input-error');
            inputEl.title = '';
        });

        right.appendChild(inputEl);
        right.appendChild(sendBtn);
        right.appendChild(unitsEl);
    } else {
        valEl = mkEl('span', 'dv-row-value stale', '--');
        const unitsEl = mkEl('span', 'dv-row-units', ch.units || '');
        right.appendChild(valEl);
        right.appendChild(unitsEl);
    }

    row.appendChild(right);

    // --- Close button ---
    const closeBtn = document.createElement('button');
    closeBtn.className = 'tab-close';
    closeBtn.textContent = '✕';
    closeBtn.title = 'Remove row';
    closeBtn.addEventListener('click', () => _removeDvRow(tab, refDes, row));
    row.appendChild(closeBtn);

    // --- Sparkline chart ---
    // Defer until canvas is in DOM so Chart.js can measure it
    requestAnimationFrame(() => {
        tab.dvCharts[refDes] = _createDvSparkline(canvas, color);
    });

    // --- Channel updater ---
    let staleTimer = null;
    tab.channelUpdaters[refDes] = (v) => {
        const now    = Date.now() / 1000;
        const buf    = tab.dvBuffers[refDes];
        const cutoff = now - DV_BUFFER_SECS;
        buf.ts.push(now);
        buf.vals.push(v);
        while (buf.ts.length && buf.ts[0] < cutoff) { buf.ts.shift(); buf.vals.shift(); }

        if (valEl) {
            valEl.textContent = typeof v === 'number' ? v.toFixed(2) : String(v);
            valEl.classList.remove('stale');
        }

        led.className = 'dv-led dv-led-online';
        clearTimeout(staleTimer);
        staleTimer = setTimeout(() => { led.className = 'dv-led dv-led-stale'; }, DV_STALE_MS);
    };

    return row;
}

// ---------------------------------------------------------------------------
// Sparkline chart
// ---------------------------------------------------------------------------

function _createDvSparkline(canvas, color) { // color resolved from --muted at row build time
    return new Chart(canvas, {
        type: 'line',
        data: {
            datasets: [{
                data:            [],
                borderColor:     color,
                borderWidth:     1.5,
                fill:            false,
                pointRadius:     0,
                tension:         0,
            }]
        },
        options: {
            animation:           false,
            responsive:          true,
            maintainAspectRatio: false,
            parsing:             false,
            events:              [],
            scales: {
                x: { type: 'linear', min: -DV_BUFFER_SECS, max: 0, display: false },
                y: { display: false }
            },
            plugins: {
                legend:  { display: false },
                tooltip: { enabled: false }
            }
        }
    });
}

// ---------------------------------------------------------------------------
// Update loop — called every 500 ms from app.js
// ---------------------------------------------------------------------------

function updateAllDataViews() {
    const now = Date.now() / 1000;
    for (const tab of tabs) {
        if (tab.type !== 'dataView') continue;
        if (!tab.dvRows || !tab.dvCharts) continue;
        for (const refDes of tab.dvRows) {
            const chart = tab.dvCharts[refDes];
            const buf   = tab.dvBuffers[refDes];
            if (!chart || !buf) continue;
            chart.data.datasets[0].data = buf.ts.map((t, i) => ({ x: t - now, y: buf.vals[i] }));
            chart.update('none');
        }
    }
}
