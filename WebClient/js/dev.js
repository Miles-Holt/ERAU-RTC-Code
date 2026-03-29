// =============================================================================
// Dev tab
// =============================================================================

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
}

function refreshDevTabs() {
    if (!devTabs.length) return;
    const stateStr  = ws ? (['CONNECTING','OPEN','CLOSING','CLOSED'][ws.readyState] ?? '--') : 'CLOSED';
    const uptime    = devStats.connectedAt ? Math.floor((Date.now() - devStats.connectedAt) / 1000) : null;
    const uptimeStr = uptime !== null ? fmtUptime(uptime) : '--';
    const rate      = ((devStats.msgCount - devStats.lastWindowCount) / 2).toFixed(1);
    devStats.lastWindowCount = devStats.msgCount;

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
}

function fmtUptime(s) {
    const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), sec = s % 60;
    return `${String(h).padStart(2,'0')}:${String(m).padStart(2,'0')}:${String(sec).padStart(2,'0')}`;
}

function fmtBytes(b) {
    return b > 1e6 ? (b / 1e6).toFixed(1) + ' MB' : (b / 1e3).toFixed(1) + ' KB';
}
