// =============================================================================
// Dev tab
// =============================================================================

function forceReconnect() {
    if (reconnectTimer) { clearTimeout(reconnectTimer); reconnectTimer = null; }
    reconnectDelay = CONFIG.reconnect.baseMs;
    if (ws) { ws.onclose = null; ws.close(); }
    connect();
}

function setDevMode(enabled) {
    devMode = enabled;
    document.getElementById('sim-btn').style.display = enabled ? '' : 'none';
    document.querySelectorAll('.dev-mode-section').forEach(el => {
        el.style.display = enabled ? '' : 'none';
    });
    document.querySelectorAll('.dev-mode-toggle').forEach(cb => { cb.checked = enabled; });
}

function buildDevContent(tab) {
    devTabs.push(tab);
    const hasMem = !!performance.memory;

    tab.contentEl.innerHTML = `
        <div class="dev-tab">
            <div class="dev-section">
                <h3 class="dev-heading">WebSocket</h3>
                <table class="dev-table">
                    <tr><td>Endpoint</td>          <td class="mono">${CONFIG.wsUrl}</td></tr>
                    <tr><td>State</td>              <td class="dev-state">--</td></tr>
                    <tr><td>Uptime</td>             <td class="dev-uptime">--</td></tr>
                    <tr><td>Messages received</td>  <td class="dev-msg-count">0</td></tr>
                    <tr><td>Message rate</td>       <td class="dev-msg-rate">--</td></tr>
                    <tr><td>Missed data cycles</td> <td class="dev-missed">0</td></tr>
                </table>
            </div>
            ${hasMem ? `
            <div class="dev-section">
                <h3 class="dev-heading">Browser Memory</h3>
                <table class="dev-table">
                    <tr><td>JS Heap Used</td>  <td class="dev-heap-used">--</td></tr>
                    <tr><td>JS Heap Total</td> <td class="dev-heap-total">--</td></tr>
                </table>
            </div>` : ''}
            <div class="dev-section">
                <h3 class="dev-heading">Developer Options</h3>
                <table class="dev-table">
                    <tr>
                        <td>Dev mode</td>
                        <td><label class="dev-toggle-label">
                            <input type="checkbox" class="dev-mode-toggle" ${devMode ? 'checked' : ''}>
                            <span class="dev-toggle-hint">enables sim + force reconnect</span>
                        </label></td>
                    </tr>
                </table>
            </div>
            <div class="dev-mode-section" style="display:${devMode ? '' : 'none'}">
                <div class="dev-section">
                    <h3 class="dev-heading">Actions</h3>
                    <table class="dev-table">
                        <tr>
                            <td>WebSocket</td>
                            <td><button class="dev-reconnect-btn">Force reconnect</button></td>
                        </tr>
                        <tr>
                            <td>Simulation</td>
                            <td><button class="dev-sim-toggle-btn">${simActive ? 'Stop Sim' : 'Start Sim'}</button></td>
                        </tr>
                    </table>
                </div>
            </div>
        </div>`;

    tab._devEls = {
        state:     tab.contentEl.querySelector('.dev-state'),
        uptime:    tab.contentEl.querySelector('.dev-uptime'),
        msgCount:  tab.contentEl.querySelector('.dev-msg-count'),
        msgRate:   tab.contentEl.querySelector('.dev-msg-rate'),
        missed:    tab.contentEl.querySelector('.dev-missed'),
        heapUsed:  tab.contentEl.querySelector('.dev-heap-used'),
        heapTotal: tab.contentEl.querySelector('.dev-heap-total'),
    };

    tab.contentEl.querySelector('.dev-mode-toggle')
        .addEventListener('change', e => setDevMode(e.target.checked));
    tab.contentEl.querySelector('.dev-reconnect-btn')
        .addEventListener('click', forceReconnect);
    tab.contentEl.querySelector('.dev-sim-toggle-btn')
        .addEventListener('click', () => simActive ? stopSim() : startSim());
}

function refreshDevTabs() {
    if (!devTabs.length) return;
    const stateStr  = ws ? (['CONNECTING','OPEN','CLOSING','CLOSED'][ws.readyState] ?? '--') : 'CLOSED';
    const uptime    = devStats.connectedAt ? Math.floor((Date.now() - devStats.connectedAt) / 1000) : null;
    const uptimeStr = uptime !== null ? fmtUptime(uptime) : '--';
    const rate      = ((devStats.msgCount - devStats.lastMsgCount) / 2).toFixed(1);
    devStats.lastMsgCount = devStats.msgCount;

    for (const tab of devTabs) {
        const e = tab._devEls;
        if (!e) continue;
        if (e.state)    e.state.textContent    = stateStr;
        if (e.uptime)   e.uptime.textContent   = uptimeStr;
        if (e.msgCount) e.msgCount.textContent = devStats.msgCount;
        if (e.msgRate)  e.msgRate.textContent  = `${rate} msg/s`;
        if (e.missed)   e.missed.textContent   = devStats.missedCycles;
        if (e.heapUsed && performance.memory) {
            e.heapUsed.textContent  = fmtBytes(performance.memory.usedJSHeapSize);
            e.heapTotal.textContent = fmtBytes(performance.memory.totalJSHeapSize);
        }
    }

    document.querySelectorAll('.dev-sim-toggle-btn')
        .forEach(btn => btn.textContent = simActive ? 'Stop Sim' : 'Start Sim');
}

function fmtUptime(s) {
    const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), sec = s % 60;
    return `${String(h).padStart(2,'0')}:${String(m).padStart(2,'0')}:${String(sec).padStart(2,'0')}`;
}

function fmtBytes(b) {
    return b > 1e6 ? (b / 1e6).toFixed(1) + ' MB' : (b / 1e3).toFixed(1) + ' KB';
}
