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
    // Decoded payload size (post-decompression, so this reflects logical bytes on
    // the data socket — useful for gauging state-broadcast volume, not wire bytes).
    if (typeof event.data === 'string') devStats.byteCount += event.data.length;
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
        case 'state_config':    applyStateConfig(msg);          break;
        case 'state_change':    applyStateChange(msg);          break;
        case 'softchan_config': applySoftchanConfig(msg);       break;
        case 'bad_data':          handleBadData(msg);                      break;
        case 'bad_data_snapshot': msg.channels.forEach(handleBadData);     break;
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
        // Client-side-only alert: this is LOCAL link state (our control socket
        // is down, or nobody is logged in on this browser). The server cannot
        // see it, so this is the one alert the browser still constructs.
        if (typeof ingestAlert === 'function') {
            ingestAlert({
                id:        'cmd-not-sent',   // stable id → replaces, not stacks
                category:  'warning',
                message:   operatorName
                    ? 'Control link down — command not sent'
                    : 'Not logged in — command not sent',
                timestamp: Date.now(),
                acked:     false,
            });
        }
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
    rebuildConfigIndex();
    if (msg.broadcastRateHz) setLiveUpdateRate(msg.broadcastRateHz);
    if (msg.channelStaleMs)  CONFIG.channelStaleMs = msg.channelStaleMs;
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
    // Front-panel objects carry state as a class pair rather than a `.stale`
    // flag on one text node, so they are marked through the renderer. Without
    // this a dropped link leaves every reading looking live.
    if (typeof pidMarkAllObjectsStale === 'function') pidMarkAllObjectsStale();
    setStatus('stale', 'Data stale');
}

// handleDaqError logs a DAQ error message. Render-only: the control node
// raises the operator alert for node faults itself (config/alerts/*.alert), so
// building one here would duplicate it and could not be acked consistently
// across browsers.
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

function applyStateConfig(msg) {
    // msg.machines is an array of { name, targetRefDes, states: [{name, index, operator}] }
    if (!msg.machines) return;
    // machineStateConfig is a const object (state.js) — clear in place, don't reassign.
    for (const k of Object.keys(machineStateConfig)) delete machineStateConfig[k];
    for (const machine of msg.machines) {
        machineStateConfig[machine.name] = machine;
    }
    // Drop remembered current states for machines that no longer exist.
    for (const k of Object.keys(machineCurrentState)) {
        if (!machineStateConfig[k]) delete machineCurrentState[k];
    }
    // Re-render any front panel tabs that have daqControl widgets.  renderPidAll
    // → rebindPidLiveData repaints each widget with its last known state, so a
    // late state_config does not leave the widget stuck on "State: ---".
    for (const tab of tabs) {
        if (tab.type === 'frontPanel' && tab.pid && tab.pid.objects.length) {
            renderPidAll(tab);
        }
    }
}

// applyStateChange handles the authoritative state_change message
// (controlnode/webclient StateChangeJSON: { type, machine, state }).  msg.state
// is the state NAME.  The same machine also streams SM-<MACHINE>-STATE as a
// numeric index on the data path; both funnel into _updateDaqControlState.
function applyStateChange(msg) {
    if (!msg.machine || typeof msg.state !== 'string') {
        console.warn('state_change: missing machine/state:', msg);
        return;
    }
    machineCurrentState[msg.machine] = msg.state;
    if (!machineStateConfig[msg.machine]) {
        // state_config has not arrived (or the machine is unknown): remember the
        // state so the widget picks it up when the config lands.
        console.warn('state_change: no state_config for machine', msg.machine);
        return;
    }
    // _updateDaqControlState (pid.js) resolves the state and repaints every
    // rendered object bound to this machine across every front-panel tab —
    // see _repaintMachineWidgets.
    _updateDaqControlState(msg.machine, msg.state);
}

function applySoftchanConfig(msg) {
    // Store soft channel definitions for future use
    if (msg.channels) {
        for (const ch of msg.channels) {
            softchanConfigMap[ch.refDes] = ch;
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
