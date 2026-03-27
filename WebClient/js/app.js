// =============================================================================
// RTC WebSocket Client
// =============================================================================
//
// PROTOCOL (all messages are JSON strings):
//
//   LabVIEW -> Browser:
//     Config  { "type": "config", "controls": [ <control>, ... ] }
//     Data    { "type": "data",   "t": <unix_epoch_s>, "d": { "<refDes>": <value>, ... } }
//
//   Browser -> LabVIEW:
//     Command { "type": "cmd", "refDes": "<refDes>", "value": <number|bool> }
//
//   Config control object:
//     {
//       "refDes":      "NV-03",
//       "description": "LOX Press",
//       "enabled":     true,
//       "type":        "valve",          // see TYPE_ORDER for all types
//       "subType":     "IO-CMD_IO-FB",
//       "details":     { "senseRefDes": "OPT-02" },  // optional
//       "channels": [
//         { "refDes": "NV-03-CMD", "role": "cmd-bool", "units": "" },
//         { "refDes": "NV-03-FB",  "role": "sensor",   "units": "" }
//       ]
//     }
//
//   Channel roles: "sensor" | "cmd-bool" | "cmd-pct" | "cmd-float"
//
//   TIMESTAMP NOTE:
//     LabVIEW epoch starts 1904-01-01. Unix epoch starts 1970-01-01.
//     Convert in LabVIEW before sending: unix_t = lv_t - 2082844800
//
// =============================================================================

const CONFIG = {
    wsUrl:            'ws://localhost:8000',
    staleThresholdMs: 500,    // mark data stale after 500ms (~10 missed frames at 20Hz)
    reconnect: {
        baseMs:  1000,
        maxMs:  10000,
        factor: 2
    }
};

// --- Connection state ---
let ws               = null;
let reconnectDelay   = CONFIG.reconnect.baseMs;
let reconnectTimer   = null;
let stalenessTimer   = null;

// --- UI state ---
let channelUpdaters = {};   // { refDes: (value) => void }  — populated by buildUI()
let configApplied   = false;


// =============================================================================
// WebSocket management
// =============================================================================

function connect() {
    setStatus('connecting', 'Connecting...');
    try {
        ws            = new WebSocket(CONFIG.wsUrl);
        ws.onopen     = onOpen;
        ws.onmessage  = onMessage;
        ws.onclose    = onClose;
        ws.onerror    = () => {};   // onclose fires immediately after onerror
    } catch (e) {
        scheduleReconnect();
    }
}

function onOpen() {
    reconnectDelay = CONFIG.reconnect.baseMs;
    setStatus('connected', 'Connected — waiting for config...');
}

function onMessage(event) {
    let msg;
    try {
        msg = JSON.parse(event.data);
    } catch {
        console.warn('Non-JSON message received:', event.data);
        return;
    }

    switch (msg.type) {
        case 'config': applyConfig(msg); break;
        case 'data':   applyData(msg);   break;
        default:       console.warn('Unknown message type:', msg.type);
    }
}

function onClose() {
    clearTimeout(stalenessTimer);
    markStale();
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
    if (!ws || ws.readyState !== WebSocket.OPEN) {
        console.warn('Cannot send command: not connected');
        return;
    }
    ws.send(JSON.stringify({ type: 'cmd', refDes, value }));
}


// =============================================================================
// Config handling & UI construction
// =============================================================================

function applyConfig(msg) {
    channelUpdaters = {};
    buildUI(msg.controls.filter(c => c.enabled));
    configApplied = true;
    setStatus('connected', 'Connected');
}

const TYPE_LABELS = {
    pressure:   'Pressures',
    temperature:'Temperatures',
    flowMeter:  'Flow Meters',
    tank:       'Tanks',
    valve:      'Valves',
    bangBang:   'Bang-Bang Controllers',
    ignition:   'Ignition',
    digitalOut: 'Digital Outputs',
    VFD:        'VFDs'
};

// Controls what order sections appear on the page
const TYPE_ORDER = [
    'pressure', 'temperature', 'flowMeter', 'tank',
    'valve', 'bangBang', 'ignition', 'digitalOut', 'VFD'
];

// Returns true for any commandable channel role
const isCmd = ch => ch.role === 'cmd-bool' || ch.role === 'cmd-pct' || ch.role === 'cmd-float';

function buildUI(controls) {
    const panel = document.getElementById('panel');
    panel.innerHTML = '';

    // Group controls by type
    const groups = {};
    for (const ctrl of controls) {
        (groups[ctrl.type] ??= []).push(ctrl);
    }

    for (const type of TYPE_ORDER) {
        if (!groups[type]) continue;

        const section = mkEl('section', `group group-${type}`);
        section.appendChild(mkEl('h2', 'group-heading', TYPE_LABELS[type] ?? type));

        const grid = mkEl('div', 'grid');
        for (const ctrl of groups[type]) {
            grid.appendChild(buildCard(ctrl));
        }
        section.appendChild(grid);
        panel.appendChild(section);
    }
}

function buildCard(ctrl) {
    switch (ctrl.type) {
        case 'pressure':
        case 'temperature':
        case 'flowMeter':
        case 'tank':
            return buildSensorCard(ctrl);
        case 'valve':
            return buildValveCard(ctrl);
        case 'bangBang':
            return buildBangBangCard(ctrl);
        case 'ignition':
            return buildIgnitionCard(ctrl);
        case 'digitalOut':
            return buildDigitalOutCard(ctrl);
        case 'VFD':
            return buildVFDCard(ctrl);
        default:
            return buildSensorCard(ctrl);
    }
}

// --- Sensor card (pressure, temperature, flowMeter, tank) ---
function buildSensorCard(ctrl) {
    const card = mkEl('div', `card card-sensor card-${ctrl.type}`);
    card.appendChild(mkEl('div', 'card-desc', ctrl.description || ctrl.refDes));
    card.appendChild(mkEl('div', 'card-refdes', ctrl.refDes));

    for (const ch of ctrl.channels) {
        const row   = mkEl('div', 'sensor-row');
        const valEl = mkEl('span', 'value stale', '--');
        const unitEl = mkEl('span', 'units', ch.units ?? '');
        row.appendChild(valEl);
        row.appendChild(unitEl);
        card.appendChild(row);

        channelUpdaters[ch.refDes] = (v) => {
            valEl.textContent = typeof v === 'number' ? v.toFixed(2) : String(v);
            valEl.classList.remove('stale');
        };
    }
    return card;
}

// --- Valve card ---
function buildValveCard(ctrl) {
    const card = mkEl('div', `card card-valve subtype-${ctrl.subType}`);
    card.appendChild(mkEl('div', 'card-desc', ctrl.description || ctrl.refDes));
    card.appendChild(mkEl('div', 'card-refdes', ctrl.refDes));

    const cmdCh = ctrl.channels.find(c => isCmd(c));
    const fbCh  = ctrl.channels.find(c => !isCmd(c) && c.refDes.endsWith('-FB'));
    const posCh = ctrl.channels.find(c => !isCmd(c) && c.refDes.endsWith('-POS'));

    // Feedback indicator
    if (fbCh) {
        const fbRow = mkEl('div', 'fb-row');
        const led   = mkEl('span', 'led led-unknown');
        const lbl   = mkEl('span', 'fb-label stale', 'FB: --');
        fbRow.appendChild(led);
        fbRow.appendChild(lbl);
        card.appendChild(fbRow);

        channelUpdaters[fbCh.refDes] = (v) => {
            const open = Boolean(v);
            led.className  = `led ${open ? 'led-open' : 'led-closed'}`;
            lbl.textContent = `FB: ${open ? 'OPEN' : 'CLOSED'}`;
            lbl.classList.remove('stale');
        };
    }

    // Position readout for POS-FB subtypes
    if (posCh) {
        const row   = mkEl('div', 'sensor-row');
        const valEl = mkEl('span', 'value stale', '--');
        row.appendChild(valEl);
        row.appendChild(mkEl('span', 'units', '%'));
        card.appendChild(row);

        channelUpdaters[posCh.refDes] = (v) => {
            valEl.textContent = typeof v === 'number' ? v.toFixed(1) : String(v);
            valEl.classList.remove('stale');
        };
    }

    // Command controls
    if (cmdCh) {
        const btnRow = mkEl('div', 'btn-row');

        if (cmdCh.role === 'cmd-pct') {
            const slider = document.createElement('input');
            slider.type = 'range'; slider.min = 0; slider.max = 100; slider.value = 0;
            slider.className = 'pos-slider';
            const posOut = mkEl('span', 'pos-out', '0%');
            slider.addEventListener('input',  () => posOut.textContent = `${slider.value}%`);
            slider.addEventListener('change', () => sendCommand(cmdCh.refDes, parseFloat(slider.value)));
            btnRow.appendChild(slider);
            btnRow.appendChild(posOut);
        } else {
            const openBtn  = mkEl('button', 'btn btn-open',  'OPEN');
            const closeBtn = mkEl('button', 'btn btn-close', 'CLOSE');
            openBtn.addEventListener('click',  () => sendCommand(cmdCh.refDes, 1));
            closeBtn.addEventListener('click', () => sendCommand(cmdCh.refDes, 0));
            btnRow.appendChild(openBtn);
            btnRow.appendChild(closeBtn);
        }
        card.appendChild(btnRow);
    }
    return card;
}

// --- Bang-Bang card ---
function buildBangBangCard(ctrl) {
    const card = mkEl('div', 'card card-bangbang');
    card.appendChild(mkEl('div', 'card-desc', ctrl.description || ctrl.refDes));
    card.appendChild(mkEl('div', 'card-refdes', ctrl.refDes));

    if (ctrl.details?.senseRefDes) {
        card.appendChild(mkEl('div', 'sense-label', `Sense: ${ctrl.details.senseRefDes}`));
    }

    for (const ch of ctrl.channels) {
        const row = mkEl('div', 'fb-row');
        const led = mkEl('span', 'led led-unknown');
        const lbl = mkEl('span', 'fb-label stale', `${ch.refDes}: --`);
        row.appendChild(led);
        row.appendChild(lbl);
        card.appendChild(row);

        channelUpdaters[ch.refDes] = (v) => {
            const on = Boolean(v);
            led.className  = `led ${on ? 'led-open' : 'led-closed'}`;
            lbl.textContent = `${ch.refDes}: ${on ? 'ON' : 'OFF'}`;
            lbl.classList.remove('stale');
        };
    }
    return card;
}

// --- Ignition card ---
// Requires ARM checkbox to be checked before FIRE button is enabled.
function buildIgnitionCard(ctrl) {
    const card = mkEl('div', 'card card-ignition');
    card.appendChild(mkEl('div', 'card-desc', ctrl.description || ctrl.refDes));
    card.appendChild(mkEl('div', 'card-refdes', ctrl.refDes));

    // Status rows for all feedback channels
    for (const ch of ctrl.channels.filter(c => !isCmd(c))) {
        const row = mkEl('div', 'fb-row');
        const led = mkEl('span', 'led led-unknown');
        const lbl = mkEl('span', 'fb-label stale', `${ch.refDes}: --`);
        row.appendChild(led);
        row.appendChild(lbl);
        card.appendChild(row);

        channelUpdaters[ch.refDes] = (v) => {
            const active = Boolean(v);
            led.className  = `led ${active ? 'led-active' : 'led-inactive'}`;
            lbl.textContent = `${ch.refDes}: ${active ? 'ACTIVE' : 'INACTIVE'}`;
            lbl.classList.remove('stale');
        };
    }

    const cmdCh = ctrl.channels.find(c => isCmd(c));
    if (cmdCh) {
        const btnRow  = mkEl('div', 'btn-row ignition-row');
        const armId   = `arm-${ctrl.refDes}`;
        const armBox  = document.createElement('input');
        armBox.type = 'checkbox'; armBox.id = armId; armBox.className = 'arm-checkbox';
        const armLbl  = document.createElement('label');
        armLbl.htmlFor = armId; armLbl.textContent = 'ARM'; armLbl.className = 'arm-label';
        const fireBtn = mkEl('button', 'btn btn-fire', 'FIRE');
        fireBtn.disabled = true;

        armBox.addEventListener('change', () => { fireBtn.disabled = !armBox.checked; });
        fireBtn.addEventListener('click', () => {
            if (armBox.checked) {
                sendCommand(cmdCh.refDes, 1);
                armBox.checked  = false;
                fireBtn.disabled = true;
            }
        });

        btnRow.appendChild(armBox);
        btnRow.appendChild(armLbl);
        btnRow.appendChild(fireBtn);
        card.appendChild(btnRow);
    }
    return card;
}

// --- Digital Out card ---
function buildDigitalOutCard(ctrl) {
    const card   = mkEl('div', 'card card-digital');
    card.appendChild(mkEl('div', 'card-desc', ctrl.description || ctrl.refDes));
    card.appendChild(mkEl('div', 'card-refdes', ctrl.refDes));

    const cmdCh = ctrl.channels.find(c => isCmd(c)) ?? ctrl.channels[0];
    if (cmdCh) {
        const btnRow = mkEl('div', 'btn-row');
        const onBtn  = mkEl('button', 'btn btn-open',  'ON');
        const offBtn = mkEl('button', 'btn btn-close', 'OFF');
        onBtn.addEventListener('click',  () => sendCommand(cmdCh.refDes, 1));
        offBtn.addEventListener('click', () => sendCommand(cmdCh.refDes, 0));
        btnRow.appendChild(onBtn);
        btnRow.appendChild(offBtn);
        card.appendChild(btnRow);
    }
    return card;
}

// --- VFD card ---
function buildVFDCard(ctrl) {
    const card = mkEl('div', 'card card-vfd');
    card.appendChild(mkEl('div', 'card-desc', ctrl.description || ctrl.refDes));
    card.appendChild(mkEl('div', 'card-refdes', ctrl.refDes));

    const cmdCh = ctrl.channels.find(c => isCmd(c));
    if (cmdCh) {
        const row     = mkEl('div', 'btn-row');
        const input   = document.createElement('input');
        input.type = 'number'; input.min = 0; input.max = 60; input.value = 0;
        input.className = 'vfd-input';
        const sendBtn = mkEl('button', 'btn', 'Set Hz');
        sendBtn.addEventListener('click', () => sendCommand(cmdCh.refDes, parseFloat(input.value)));
        row.appendChild(input);
        row.appendChild(sendBtn);
        card.appendChild(row);
    }
    return card;
}


// =============================================================================
// Data updates
// =============================================================================

function applyData(msg) {
    if (!configApplied) return;

    resetStalenessTimer();
    updateTimestamp(msg.t);
    setStatus('connected', 'Connected');

    for (const [refDes, value] of Object.entries(msg.d)) {
        channelUpdaters[refDes]?.(value);
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


// =============================================================================
// Status bar & timestamp
// =============================================================================

function setStatus(state, text) {
    document.getElementById('status-indicator').className = `status-indicator status-${state}`;
    document.getElementById('status-text').textContent = text;
}

function updateTimestamp(unixSeconds) {
    const el = document.getElementById('timestamp');
    if (!el) return;
    const d = new Date(unixSeconds * 1000);
    el.textContent = d.toLocaleTimeString('en-US', {
        hour12: false,
        hour: '2-digit', minute: '2-digit', second: '2-digit',
        fractionalSecondDigits: 3
    });
}


// =============================================================================
// Helpers
// =============================================================================

function mkEl(tag, className, text) {
    const e = document.createElement(tag);
    if (className) e.className = className;
    if (text !== undefined) e.textContent = text;
    return e;
}


// =============================================================================
// Init
// =============================================================================

connect();
