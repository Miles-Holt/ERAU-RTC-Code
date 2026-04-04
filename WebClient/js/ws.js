// =============================================================================
// WebSocket management — two connections
//   /ws/data  (anonymous, server→client): config, layouts, live data, alerts
//   /ws/ctrl  (auth required, bidirectional): auth, commands, ack_alert
// =============================================================================

// =============================================================================
// Data WebSocket
// =============================================================================

function connect() {
    if (simActive) return;
    setStatus('connecting', 'Connecting...');
    try {
        ws           = new WebSocket(CONFIG.wsBase + '/ws/data');
        ws.onopen    = onDataOpen;
        ws.onmessage = onDataMessage;
        ws.onclose   = onDataClose;
        ws.onerror   = (e) => console.warn('Data WS error:', e);
    } catch (e) {
        scheduleReconnect();
    }
}

function onDataOpen() {
    reconnectDelay       = CONFIG.reconnect.baseMs;
    devStats.connectedAt = Date.now();
    setStatus('connected', 'Connected — waiting for config...');
}

function onDataMessage(event) {
    devStats.msgCount++;
    let msg;
    try { msg = JSON.parse(event.data); }
    catch { console.warn('Non-JSON message received:', event.data); return; }

    logConsole('in', msg);

    switch (msg.type) {
        case 'config':          applyConfig(msg);             break;
        case 'data':            applyData(msg);               break;
        case 'pid_layout':      applyPidLayout(msg);          break;
        case 'err':             handleDaqError(msg);          break;
        case 'alert':           ingestAlert(msg);             break;
        case 'alert_acked':     ackAlertLocally(msg.id);      break;
        case 'alert_snapshot':  msg.alerts.forEach(ingestAlert); break;
        default: console.warn('Unknown data message type:', msg.type);
    }
}

function onDataClose() {
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

// =============================================================================
// Control WebSocket
// =============================================================================

function connectCtrl() {
    if (simActive) return;
    try {
        wsCtrl           = new WebSocket(CONFIG.wsBase + '/ws/ctrl');
        wsCtrl.onopen    = onCtrlOpen;
        wsCtrl.onmessage = onCtrlMessage;
        wsCtrl.onclose   = onCtrlClose;
        wsCtrl.onerror   = (e) => console.warn('Ctrl WS error:', e);
    } catch (e) {
        scheduleReconnectCtrl();
    }
}

function onCtrlOpen() {
    reconnectDelayCtrl = CONFIG.reconnect.baseMs;
}

function onCtrlMessage(event) {
    let msg;
    try { msg = JSON.parse(event.data); }
    catch { return; }

    if (msg.type === 'auth_response') handleAuthResponse(msg);
}

function onCtrlClose() {
    wsCtrl = null;
    operatorName = '';
    updateOperatorButton();
    updateCommandWidgets();
    scheduleReconnectCtrl();
}

function scheduleReconnectCtrl() {
    if (reconnectTimerCtrl) return;
    reconnectTimerCtrl = setTimeout(() => {
        reconnectTimerCtrl = null;
        connectCtrl();
    }, reconnectDelayCtrl);
    reconnectDelayCtrl = Math.min(reconnectDelayCtrl * CONFIG.reconnect.factor, CONFIG.reconnect.maxMs);
}

// sendWsCtrl sends a message on the control WebSocket.
function sendWsCtrl(msg) {
    if (!wsCtrl || wsCtrl.readyState !== WebSocket.OPEN) {
        console.warn('Cannot send: ctrl WS not connected');
        return;
    }
    wsCtrl.send(JSON.stringify(msg));
    logConsole('out', msg);
}

function sendCommand(refDes, value) {
    const msg = { type: 'cmd', refDes, value, user: operatorName };
    if (simActive) {
        logConsole('out', msg);
        if (typeof simReceiveCommand === 'function') simReceiveCommand(refDes, value);
        return;
    }
    sendWsCtrl(msg);
}

// =============================================================================
// Config & data handling
// =============================================================================

function applyConfig(msg) {
    configControls = msg.controls ?? [];
    configApplied  = true;
    if (msg.broadcastRateHz) setLiveUpdateRate(msg.broadcastRateHz);
    restoreTabState();
    for (const tab of tabs) {
        if (tab.type === 'dataView') rebuildDataView(tab);
        if (tab.type === 'frontPanel' && tab.pid && tab.pid.objects.length) renderPidAll(tab);
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
    stalenessTimer = setTimeout(() => setStatus('stale', 'Data stale'), CONFIG.staleThresholdMs);
}

function markStale() {
    document.querySelectorAll('.value, .fb-label, .pid-sensor-value').forEach(el => el.classList.add('stale'));
    setStatus('stale', 'Data stale');
}

function handleDaqError(msg) {
    const ts = msg.t ? new Date(msg.t * 1000).toISOString() : '?';
    console.error(`[${ts}] DAQ error from ${msg.daqNode}: ${msg.err}`);
}

function applyPidLayout(msg) {
    if (!msg.filename || !msg.content) return;
    pidLayouts[msg.filename] = { name: msg.name || msg.filename, filename: msg.filename, content: msg.content };
    for (const tab of tabs) {
        if (tab.type === 'frontPanel' && tab.pid) {
            refreshPidLayoutPicker(tab);
            if (tab.pid.layoutFilename === msg.filename) {
                loadPidLayout(tab, pidLayouts[msg.filename]);
            }
            // Restore pending layout from tab persistence
            if (tab.pid.pendingLayout === msg.filename) {
                tab.pid.pendingLayout = null;
                loadPidLayout(tab, pidLayouts[msg.filename]);
            }
        }
    }
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
