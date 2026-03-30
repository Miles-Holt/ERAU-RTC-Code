// =============================================================================
// WebSocket management
// =============================================================================

function connect() {
    if (simActive) return;
    setStatus('connecting', 'Connecting...');
    try {
        ws           = new WebSocket(CONFIG.wsUrl);
        ws.onopen    = onOpen;
        ws.onmessage = onMessage;
        ws.onclose   = onClose;
        ws.onerror   = (e) => console.warn('WebSocket error:', e);
    } catch (e) {
        scheduleReconnect();
    }
}

function onOpen() {
    reconnectDelay       = CONFIG.reconnect.baseMs;
    devStats.connectedAt = Date.now();
    setStatus('connected', 'Connected — waiting for config...');
}

function onMessage(event) {
    devStats.msgCount++;
    let msg;
    try { msg = JSON.parse(event.data); }
    catch { console.warn('Non-JSON message received:', event.data); return; }

    logConsole('in', msg);

    switch (msg.type) {
        case 'config': applyConfig(msg); break;
        case 'data':   applyData(msg);   break;
        default:       console.warn('Unknown message type:', msg.type);
    }
}

function onClose() {
    clearTimeout(stalenessTimer);
    markStale();
    devStats.connectedAt = null;
    setStatus('disconnected', 'Disconnected');
    scheduleReconnect();
}

function scheduleReconnect() {
    if (reconnectTimer) return;
    setStatus('reconnecting', `Reconnecting in ${reconnectDelay / 1000}s...`);
    reconnectTimer = setTimeout(() => {
        reconnectTimer = null;
        connect();
    }, reconnectDelay);
    reconnectDelay = Math.min(reconnectDelay * CONFIG.reconnect.factor, CONFIG.reconnect.maxMs);
}

function sendCommand(refDes, value) {
    const msg = { type: 'cmd', refDes, value, user: operatorName };
    if (simActive) {
        logConsole('out', msg);
        if (typeof simReceiveCommand === 'function') simReceiveCommand(refDes, value);
        return;
    }
    if (!ws || ws.readyState !== WebSocket.OPEN) {
        console.warn('Cannot send command: not connected');
        return;
    }
    ws.send(JSON.stringify(msg));
    logConsole('out', msg);
}


// =============================================================================
// Config & data handling
// =============================================================================

function applyConfig(msg) {
    configControls = msg.controls ?? [];
    configApplied  = true;
    for (const tab of tabs) {
        if (tab.type === 'dataView') rebuildDataView(tab);
    }
    setStatus('connected', 'Connected');
    updateCommandWidgets();
}

function applyData(msg) {
    if (!configApplied) return;
    resetStalenessTimer();
    updateTimestamp(msg.t);
    setStatus('connected', 'Connected');
    trackDataTiming(msg.t);

    // Normalize array format [{ r, v }, ...] to flat object { refDes: value }
    const d = Array.isArray(msg.d)
        ? Object.fromEntries(msg.d.map(e => [e.r, e.v]))
        : msg.d;

    bufferGraphData(d);

    for (const tab of tabs) {
        if (!tab.channelUpdaters) continue;
        for (const [refDes, value] of Object.entries(d)) {
            tab.channelUpdaters[refDes]?.(value);
        }
    }
}

function resetStalenessTimer() {
    clearTimeout(stalenessTimer);
    stalenessTimer = setTimeout(markStale, CONFIG.staleThresholdMs);
}

function markStale() {
    document.querySelectorAll('.value, .fb-label').forEach(el => el.classList.add('stale'));
    setStatus('stale', 'Data stale');
}

function trackDataTiming(t) {
    if (devStats.lastDataT !== null) {
        const gap = t - devStats.lastDataT;
        if (devStats.avgInterval === null) devStats.avgInterval = gap;
        else devStats.avgInterval = devStats.avgInterval * 0.9 + gap * 0.1;
        if (gap > devStats.avgInterval * 2.5) {
            devStats.missedCycles += Math.round(gap / devStats.avgInterval) - 1;
        }
    }
    devStats.lastDataT = t;
}
