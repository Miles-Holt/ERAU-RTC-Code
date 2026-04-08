// =============================================================================
// Sim Mode
// =============================================================================
//
// Injects a fake config + 20 Hz data stream directly into applyConfig/applyData,
// bypassing WebSocket entirely. Valve FB channels follow their CMD channel.
//
// Public API (called from index.html sim button):
//   startSim()
//   stopSim()
//
// app.js integration points:
//   simActive          — flag checked in connect() and sendCommand()
//   simReceiveCommand  — called by sendCommand() when simActive is true
//
// =============================================================================

// --- Sim state ---
let simInterval = null;
const simCmdState = {};  // refDes -> last commanded value (0 or 1)
const simFbDelay  = {};  // refDes -> { value, pendingValue, changeAt }

// --- Config ---
// Mirrors the TC3 controlList from nodeConfigs_0.0.2.xml.
// CPT-02 and the ASI/blank channels (enabled:false) are excluded.

const SIM_CONFIG = {
    type: 'config',
    controls: [

        // ── Thrust ────────────────────────────────────────────────────────────
        {
            refDes: 'LCC-01', description: 'TC3 Load cell cluster', type: 'thrust', subType: '',
            details: {}, channels: [
                { refDes: 'LCC-01-01', role: 'sensor', units: 'lbf' },
                { refDes: 'LCC-01-02', role: 'sensor', units: 'lbf' },
                { refDes: 'LCC-01-03', role: 'sensor', units: 'lbf' },
                { refDes: 'LCC-01-04', role: 'sensor', units: 'lbf' },
            ]
        },

        // ── Digital output ────────────────────────────────────────────────────
        {
            refDes: 'TRIGGER-01', description: 'Trigger signal', type: 'digitalOut', subType: '',
            details: {}, channels: [
                { refDes: 'TRIGGER-01', role: 'cmd-bool', units: '' },
            ]
        },

        // ── Ignition ──────────────────────────────────────────────────────────
        {
            refDes: 'IG-01', description: 'Ignition circuit', type: 'ignition', subType: 'IO-CMD_VAR-FB-CMD_IO-FB',
            details: {}, channels: [
                { refDes: 'IG-01-CMD',    role: 'cmd-bool', units: '' },
                { refDes: 'IG-01-FB',     role: 'sensor',   units: '' },
                { refDes: 'IG-01-FB-CMD', role: 'sensor',   units: '' },
            ]
        },

        // ── Temperatures ──────────────────────────────────────────────────────
        { refDes: 'FT-02', description: 'Fuel Injector Temperature', type: 'temperature', subType: '', details: {}, channels: [{ refDes: 'FT-02', role: 'sensor', units: 'Deg F' }] },
        { refDes: 'FT-01', description: 'Fuel Tank Temperature',     type: 'temperature', subType: '', details: {}, channels: [{ refDes: 'FT-01', role: 'sensor', units: 'Deg F' }] },
        { refDes: 'OT-01', description: 'LOX Tank Temperature 0%',   type: 'temperature', subType: '', details: {}, channels: [{ refDes: 'OT-01', role: 'sensor', units: 'Deg F' }] },
        { refDes: 'OT-02', description: 'LOX Tank Temperature 25%',  type: 'temperature', subType: '', details: {}, channels: [{ refDes: 'OT-02', role: 'sensor', units: 'Deg F' }] },
        { refDes: 'OT-03', description: 'LOX Tank Temperature 50%',  type: 'temperature', subType: '', details: {}, channels: [{ refDes: 'OT-03', role: 'sensor', units: 'Deg F' }] },
        { refDes: 'OT-04', description: 'LOX Tank Temperature 75%',  type: 'temperature', subType: '', details: {}, channels: [{ refDes: 'OT-04', role: 'sensor', units: 'Deg F' }] },
        { refDes: 'OT-05', description: 'LOX Tank Temperature 100%', type: 'temperature', subType: '', details: {}, channels: [{ refDes: 'OT-05', role: 'sensor', units: 'Deg F' }] },
        { refDes: 'OT-06', description: 'LOX Fill Temperature',      type: 'temperature', subType: '', details: {}, channels: [{ refDes: 'OT-06', role: 'sensor', units: 'Deg F' }] },
        { refDes: 'OT-07', description: 'LOX Upstream Temperature',  type: 'temperature', subType: '', details: {}, channels: [{ refDes: 'OT-07', role: 'sensor', units: 'Deg F' }] },
        { refDes: 'OT-08', description: 'LOX Injector Temperature',  type: 'temperature', subType: '', details: {}, channels: [{ refDes: 'OT-08', role: 'sensor', units: 'Deg F' }] },

        // ── Pressures — Nitrogen ──────────────────────────────────────────────
        { refDes: 'NPT-01', description: 'Bottle Pressure',           type: 'pressure', subType: '', details: { absolute: true }, channels: [{ refDes: 'NPT-01', role: 'sensor', units: 'psi' }] },
        { refDes: 'NPT-02', description: 'Muscle Pressure',           type: 'pressure', subType: '', details: { absolute: true }, channels: [{ refDes: 'NPT-02', role: 'sensor', units: 'psi' }] },
        { refDes: 'NPT-03', description: 'High-Pressure Purge Pressure', type: 'pressure', subType: '', details: { absolute: true }, channels: [{ refDes: 'NPT-03', role: 'sensor', units: 'psi' }] },

        // ── Pressures — Fuel ─────────────────────────────────────────────────
        { refDes: 'FPT-01', description: 'Fuel Panel Pressure',            type: 'pressure', subType: '', details: { absolute: false }, channels: [{ refDes: 'FPT-01', role: 'sensor', units: 'psi' }] },
        { refDes: 'FPT-02', description: 'Fuel Tank Pressure',             type: 'pressure', subType: '', details: { absolute: false }, channels: [{ refDes: 'FPT-02', role: 'sensor', units: 'psi' }] },
        { refDes: 'FPT-03', description: 'Fuel Venturi Upstream Pressure', type: 'pressure', subType: '', details: { absolute: false }, channels: [{ refDes: 'FPT-03', role: 'sensor', units: 'psi' }] },
        { refDes: 'FPT-04', description: 'Fuel Injector Pressure',         type: 'pressure', subType: '', details: { absolute: false }, channels: [{ refDes: 'FPT-04', role: 'sensor', units: 'psi' }] },

        // ── Pressures — LOX ──────────────────────────────────────────────────
        { refDes: 'OPT-01', description: 'LOX Panel Pressure',            type: 'pressure', subType: '', details: { absolute: false }, channels: [{ refDes: 'OPT-01', role: 'sensor', units: 'psi' }] },
        { refDes: 'OPT-02', description: 'LOX Tank Pressure',             type: 'pressure', subType: '', details: { absolute: false }, channels: [{ refDes: 'OPT-02', role: 'sensor', units: 'psi' }] },
        { refDes: 'OPT-03', description: 'LOX Venturi Upstream Pressure', type: 'pressure', subType: '', details: { absolute: false }, channels: [{ refDes: 'OPT-03', role: 'sensor', units: 'psi' }] },
        { refDes: 'OPT-04', description: 'LOX Injector Pressure',         type: 'pressure', subType: '', details: { absolute: false }, channels: [{ refDes: 'OPT-04', role: 'sensor', units: 'psi' }] },

        // ── Pressures — Chamber ───────────────────────────────────────────────
        { refDes: 'CPT-01', description: 'Chamber Pressure', type: 'pressure', subType: '', details: { absolute: false }, channels: [{ refDes: 'CPT-01', role: 'sensor', units: 'psi' }] },

        // ── Flow meters ───────────────────────────────────────────────────────
        { refDes: 'FFM-01', description: 'Fuel Turbine Flow Meter', type: 'flowMeter', subType: '', details: {}, channels: [{ refDes: 'FFM-01', role: 'sensor', units: 'GPM' }] },
        { refDes: 'OFM-01', description: 'LOX Turbine Flow Meter',  type: 'flowMeter', subType: '', details: {}, channels: [{ refDes: 'OFM-01', role: 'sensor', units: 'GPM' }] },

        // ── Bang-bang controllers ─────────────────────────────────────────────
        {
            refDes: 'NV-01', description: 'LOX Bang Bang', type: 'bangBang', subType: 'press2',
            details: { senseRefDes: 'OPT-02' }, channels: [
                { refDes: 'NV-01-POS', role: 'cmd-bool', units: '' },
                { refDes: 'NV-01-NEG', role: 'cmd-bool', units: '' },
            ]
        },
        {
            refDes: 'NV-02', description: 'Fuel Bang Bang', type: 'bangBang', subType: 'press2',
            details: { senseRefDes: 'FPT-02' }, channels: [
                { refDes: 'NV-02-POS', role: 'cmd-bool', units: '' },
                { refDes: 'NV-02-NEG', role: 'cmd-bool', units: '' },
            ]
        },

        // ── Nitrogen valves ───────────────────────────────────────────────────
        { refDes: 'NV-03', description: 'LOX Press',  type: 'valve', subType: 'IO-CMD_IO-FB', details: {}, channels: [{ refDes: 'NV-03-CMD', role: 'cmd-bool', units: '' }, { refDes: 'NV-03-FB', role: 'sensor', units: '' }] },
        { refDes: 'NV-04', description: 'Fuel Press',  type: 'valve', subType: 'IO-CMD_IO-FB', details: {}, channels: [{ refDes: 'NV-04-CMD', role: 'cmd-bool', units: '' }, { refDes: 'NV-04-FB', role: 'sensor', units: '' }] },
        { refDes: 'NV-05', description: 'LOX Purge',  type: 'valve', subType: 'IO-CMD_IO-FB', details: {}, channels: [{ refDes: 'NV-05-CMD', role: 'cmd-bool', units: '' }, { refDes: 'NV-05-FB', role: 'sensor', units: '' }] },
        { refDes: 'NV-06', description: 'Fuel Purge',  type: 'valve', subType: 'IO-CMD_IO-FB', details: {}, channels: [{ refDes: 'NV-06-CMD', role: 'cmd-bool', units: '' }, { refDes: 'NV-06-FB', role: 'sensor', units: '' }] },

        // ── LOX valves ────────────────────────────────────────────────────────
        { refDes: 'OV-01', description: 'LOX Vent',  type: 'valve', subType: 'IO-CMD_IO-FB', details: {}, channels: [{ refDes: 'OV-01-CMD', role: 'cmd-bool', units: '' }, { refDes: 'OV-01-FB', role: 'sensor', units: '' }] },
        { refDes: 'OV-02', description: 'LOX Fill',  type: 'valve', subType: 'IO-CMD_IO-FB', details: {}, channels: [{ refDes: 'OV-02-CMD', role: 'cmd-bool', units: '' }, { refDes: 'OV-02-FB', role: 'sensor', units: '' }] },
        { refDes: 'OV-03', description: 'LOX Iso',   type: 'valve', subType: 'IO-CMD_IO-FB', details: {}, channels: [{ refDes: 'OV-03-CMD', role: 'cmd-bool', units: '' }, { refDes: 'OV-03-FB', role: 'sensor', units: '' }] },
        { refDes: 'OV-04', description: 'LOX Bleed', type: 'valve', subType: 'IO-CMD_IO-FB', details: {}, channels: [{ refDes: 'OV-04-CMD', role: 'cmd-bool', units: '' }, { refDes: 'OV-04-FB', role: 'sensor', units: '' }] },
        { refDes: 'OV-05', description: 'LOX Main',  type: 'valve', subType: 'IO-CMD_IO-FB', details: {}, channels: [{ refDes: 'OV-05-CMD', role: 'cmd-bool', units: '' }, { refDes: 'OV-05-FB', role: 'sensor', units: '' }] },

        // ── Fuel valves ───────────────────────────────────────────────────────
        { refDes: 'FV-01', description: 'Fuel Vent',  type: 'valve', subType: 'IO-CMD_IO-FB', details: {}, channels: [{ refDes: 'FV-01-CMD', role: 'cmd-bool', units: '' }, { refDes: 'FV-01-FB', role: 'sensor', units: '' }] },
        { refDes: 'FV-02', description: 'Fuel Iso',   type: 'valve', subType: 'IO-CMD_IO-FB', details: {}, channels: [{ refDes: 'FV-02-CMD', role: 'cmd-bool', units: '' }, { refDes: 'FV-02-FB', role: 'sensor', units: '' }] },
        { refDes: 'FV-03', description: 'Fuel Main',  type: 'valve', subType: 'IO-CMD_IO-FB', details: {}, channels: [{ refDes: 'FV-03-CMD', role: 'cmd-bool', units: '' }, { refDes: 'FV-03-FB', role: 'sensor', units: '' }] },

        // ── Fuel blanks (IO-CMD only, no FB) ──────────────────────────────────
        { refDes: 'FV-BLANK-01', description: 'Fuel Blank 1', type: 'valve', subType: 'IO-CMD', details: {}, channels: [{ refDes: 'FV-BLANK-01', role: 'cmd-bool', units: '' }] },
        { refDes: 'FV-BLANK-02', description: 'Fuel Blank 2', type: 'valve', subType: 'IO-CMD', details: {}, channels: [{ refDes: 'FV-BLANK-02', role: 'cmd-bool', units: '' }] },
    ]
};

// --- Channel wave parameters ─────────────────────────────────────────────────
//
// type 'sine'    : value = base + amp*sin(2π·freq·t + phase) + noise·rand
// type 'bool_cmd': value tracks simCmdState[refDes], starting at 0
// type 'bool_fb' : value mirrors simCmdState[cmdRefDes] after ~150 ms delay

const SIM_CHANNELS = {
    // Load cells — each reads ~quarter of total thrust, slight phase offset
    'LCC-01-01': { type: 'sine', base: 0.5,  amp: 2.0,  freq: 0.08, phase: 0.0,  noise: 0.3 },
    'LCC-01-02': { type: 'sine', base: 0.5,  amp: 2.0,  freq: 0.08, phase: 0.5,  noise: 0.3 },
    'LCC-01-03': { type: 'sine', base: 0.5,  amp: 2.0,  freq: 0.08, phase: 1.0,  noise: 0.3 },
    'LCC-01-04': { type: 'sine', base: 0.5,  amp: 2.0,  freq: 0.08, phase: 1.5,  noise: 0.3 },

    // Trigger / ignition
    'TRIGGER-01':   { type: 'bool_cmd', cmdRefDes: 'TRIGGER-01' },
    'IG-01-CMD':    { type: 'bool_cmd', cmdRefDes: 'IG-01-CMD' },
    'IG-01-FB':     { type: 'bool_fb',  cmdRefDes: 'IG-01-CMD' },
    'IG-01-FB-CMD': { type: 'bool_fb',  cmdRefDes: 'IG-01-CMD' },

    // Temperatures — fuel side (ambient ~70 °F, slight drift)
    'FT-01': { type: 'sine', base: 72,  amp: 1.5, freq: 0.02, phase: 0.0, noise: 0.4 },
    'FT-02': { type: 'sine', base: 68,  amp: 1.2, freq: 0.03, phase: 1.2, noise: 0.4 },

    // Temperatures — LOX side (cryogenic, ~-280 °F)
    'OT-01': { type: 'sine', base: -280, amp: 3.0, freq: 0.015, phase: 0.0, noise: 1.0 },
    'OT-02': { type: 'sine', base: -278, amp: 2.5, freq: 0.015, phase: 0.4, noise: 1.0 },
    'OT-03': { type: 'sine', base: -275, amp: 2.5, freq: 0.015, phase: 0.8, noise: 1.0 },
    'OT-04': { type: 'sine', base: -270, amp: 3.0, freq: 0.015, phase: 1.2, noise: 1.0 },
    'OT-05': { type: 'sine', base: -265, amp: 3.5, freq: 0.015, phase: 1.6, noise: 1.0 },
    'OT-06': { type: 'sine', base: -260, amp: 4.0, freq: 0.015, phase: 2.0, noise: 1.2 },
    'OT-07': { type: 'sine', base: -255, amp: 4.0, freq: 0.015, phase: 2.4, noise: 1.2 },
    'OT-08': { type: 'sine', base: -250, amp: 5.0, freq: 0.015, phase: 2.8, noise: 1.5 },

    // Pressures — Nitrogen (high pressure: 3000–4500 psi)
    'NPT-01': { type: 'sine', base: 4200, amp: 50,  freq: 0.04, phase: 0.0, noise: 5.0 },
    'NPT-02': { type: 'sine', base: 3800, amp: 40,  freq: 0.04, phase: 0.6, noise: 5.0 },
    'NPT-03': { type: 'sine', base: 3500, amp: 35,  freq: 0.04, phase: 1.2, noise: 4.0 },

    // Pressures — Fuel (200–350 psi)
    'FPT-01': { type: 'sine', base: 300, amp: 20, freq: 0.05, phase: 0.0, noise: 2.0 },
    'FPT-02': { type: 'sine', base: 285, amp: 18, freq: 0.05, phase: 0.8, noise: 2.0 },
    'FPT-03': { type: 'sine', base: 260, amp: 15, freq: 0.06, phase: 1.6, noise: 2.0 },
    'FPT-04': { type: 'sine', base: 240, amp: 12, freq: 0.06, phase: 2.4, noise: 1.5 },

    // Pressures — LOX (200–350 psi)
    'OPT-01': { type: 'sine', base: 310, amp: 20, freq: 0.05, phase: 0.2, noise: 2.0 },
    'OPT-02': { type: 'sine', base: 295, amp: 18, freq: 0.05, phase: 1.0, noise: 2.0 },
    'OPT-03': { type: 'sine', base: 270, amp: 15, freq: 0.06, phase: 1.8, noise: 2.0 },
    'OPT-04': { type: 'sine', base: 250, amp: 12, freq: 0.06, phase: 2.6, noise: 1.5 },

    // Chamber pressure
    'CPT-01': { type: 'sine', base: 150, amp: 30, freq: 0.07, phase: 0.5, noise: 3.0 },

    // Flow meters (GPM)
    'FFM-01': { type: 'sine', base: 2.5, amp: 0.8, freq: 0.06, phase: 0.3, noise: 0.1 },
    'OFM-01': { type: 'sine', base: 3.0, amp: 0.9, freq: 0.06, phase: 1.1, noise: 0.1 },

    // Bang-bang — CMD channels only (no FB in config)
    'NV-01-POS': { type: 'bool_cmd', cmdRefDes: 'NV-01-POS' },
    'NV-01-NEG': { type: 'bool_cmd', cmdRefDes: 'NV-01-NEG' },
    'NV-02-POS': { type: 'bool_cmd', cmdRefDes: 'NV-02-POS' },
    'NV-02-NEG': { type: 'bool_cmd', cmdRefDes: 'NV-02-NEG' },

    // Nitrogen valves
    'NV-03-CMD': { type: 'bool_cmd', cmdRefDes: 'NV-03-CMD' }, 'NV-03-FB': { type: 'bool_fb', cmdRefDes: 'NV-03-CMD' },
    'NV-04-CMD': { type: 'bool_cmd', cmdRefDes: 'NV-04-CMD' }, 'NV-04-FB': { type: 'bool_fb', cmdRefDes: 'NV-04-CMD' },
    'NV-05-CMD': { type: 'bool_cmd', cmdRefDes: 'NV-05-CMD' }, 'NV-05-FB': { type: 'bool_fb', cmdRefDes: 'NV-05-CMD' },
    'NV-06-CMD': { type: 'bool_cmd', cmdRefDes: 'NV-06-CMD' }, 'NV-06-FB': { type: 'bool_fb', cmdRefDes: 'NV-06-CMD' },

    // LOX valves
    'OV-01-CMD': { type: 'bool_cmd', cmdRefDes: 'OV-01-CMD' }, 'OV-01-FB': { type: 'bool_fb', cmdRefDes: 'OV-01-CMD' },
    'OV-02-CMD': { type: 'bool_cmd', cmdRefDes: 'OV-02-CMD' }, 'OV-02-FB': { type: 'bool_fb', cmdRefDes: 'OV-02-CMD' },
    'OV-03-CMD': { type: 'bool_cmd', cmdRefDes: 'OV-03-CMD' }, 'OV-03-FB': { type: 'bool_fb', cmdRefDes: 'OV-03-CMD' },
    'OV-04-CMD': { type: 'bool_cmd', cmdRefDes: 'OV-04-CMD' }, 'OV-04-FB': { type: 'bool_fb', cmdRefDes: 'OV-04-CMD' },
    'OV-05-CMD': { type: 'bool_cmd', cmdRefDes: 'OV-05-CMD' }, 'OV-05-FB': { type: 'bool_fb', cmdRefDes: 'OV-05-CMD' },

    // Fuel valves
    'FV-01-CMD': { type: 'bool_cmd', cmdRefDes: 'FV-01-CMD' }, 'FV-01-FB': { type: 'bool_fb', cmdRefDes: 'FV-01-CMD' },
    'FV-02-CMD': { type: 'bool_cmd', cmdRefDes: 'FV-02-CMD' }, 'FV-02-FB': { type: 'bool_fb', cmdRefDes: 'FV-02-CMD' },
    'FV-03-CMD': { type: 'bool_cmd', cmdRefDes: 'FV-03-CMD' }, 'FV-03-FB': { type: 'bool_fb', cmdRefDes: 'FV-03-CMD' },

    // Fuel blanks (cmd only, no FB)
    'FV-BLANK-01': { type: 'bool_cmd', cmdRefDes: 'FV-BLANK-01' },
    'FV-BLANK-02': { type: 'bool_cmd', cmdRefDes: 'FV-BLANK-02' },

    // Health channels
    'CTR001-uptime':       { type: 'sine', base: 3600, amp: 0, freq: 0, phase: 0, noise: 0 },
    'CTR001-daqConnected': { type: 'sine', base: 1,    amp: 0, freq: 0, phase: 0, noise: 0 },
    'CTR001-wcConnected':  { type: 'sine', base: 1,    amp: 0, freq: 0, phase: 0, noise: 0 },

    // State machine channels
    'SYS-STATE':        { type: 'state', stateIdx: 0 },
    'SYS-TARGET-STATE': { type: 'bool_cmd', cmdRefDes: 'SYS-TARGET-STATE' },
};

// --- Command intercept ────────────────────────────────────────────────────────

function simReceiveCommand(refDes, value) {
    // Handle state transition commands
    if (refDes === 'SYS-TARGET-STATE') {
        const sysState = SIM_CHANNELS['SYS-STATE'];
        if (sysState) {
            const idx = typeof value === 'number' ? value : parseInt(value, 10);
            if (!isNaN(idx)) sysState.stateIdx = idx;
        }
        return;
    }

    simCmdState[refDes] = value ? 1 : 0;
    // Schedule FB to follow after ~150 ms
    const fbRefDes = refDes.replace(/-CMD$/, '-FB');
    if (SIM_CHANNELS[fbRefDes]?.type === 'bool_fb') {
        simFbDelay[fbRefDes] = { pendingValue: value ? 1 : 0, changeAt: Date.now() + 150 };
    }
}

// --- Data generation ─────────────────────────────────────────────────────────

function generateData(tSec) {
    const now = Date.now();
    const d = {};
    for (const [refDes, ch] of Object.entries(SIM_CHANNELS)) {
        switch (ch.type) {
            case 'sine':
                d[refDes] = ch.base
                    + ch.amp * Math.sin(2 * Math.PI * ch.freq * tSec + ch.phase)
                    + (Math.random() - 0.5) * ch.noise;
                break;
            case 'bool_cmd':
                d[refDes] = simCmdState[ch.cmdRefDes] ?? 0;
                break;
            case 'bool_fb': {
                const delay = simFbDelay[refDes];
                if (delay && now >= delay.changeAt) {
                    simCmdState[refDes] = delay.pendingValue;
                    delete simFbDelay[refDes];
                }
                d[refDes] = simCmdState[refDes] ?? 0;
                break;
            }
            case 'state':
                d[refDes] = ch.stateIdx;
                break;
        }
    }
    return d;
}

// --- Start / stop ────────────────────────────────────────────────────────────

function startSim() {
    // Tear down any live WebSocket connection and suppress auto-reconnect
    if (ws) { ws.onclose = null; ws.onerror = null; ws.close(); ws = null; }
    clearTimeout(reconnectTimer);
    reconnectTimer = null;

    simActive = true;
    devStats.connectedAt = Date.now();
    setStatus('connected', 'Sim mode active');

    applyConfig(SIM_CONFIG);

    // Inject fake state machine config for the daqControl widget
    applyStateConfig({
        type: 'state_config',
        daqNodes: [{
            daqNode: 'DAQ001',
            states: {
                safe:           { operatorControl: false, transitions: [{ target: 'manualControl', on: 'operator_request' }] },
                manualControl:  { operatorControl: true,  transitions: [{ target: 'autoSequence', on: 'operator_request' }, { target: 'safe', on: 'operator_request' }] },
                autoSequence:   { operatorControl: false, transitions: [{ target: 'abort', on: 'operator_abort' }] },
                abort:          { operatorControl: false, transitions: [{ target: 'safe', on: 'operator_request' }] },
            }
        }]
    });

    simInterval = setInterval(() => {
        const t = Date.now() / 1000;
        applyData({ type: 'data', t, d: generateData(t) });
    }, 50);   // 20 Hz

    document.getElementById('sim-btn').textContent = 'Stop Sim';
    document.getElementById('sim-btn').classList.add('sim-active');
}

function stopSim() {
    simActive = false;
    clearInterval(simInterval);
    simInterval = null;

    // Clear sim state so a fresh start is clean
    for (const k of Object.keys(simCmdState)) delete simCmdState[k];
    for (const k of Object.keys(simFbDelay))  delete simFbDelay[k];

    setStatus('disconnected', 'Disconnected');
    devStats.connectedAt = null;

    document.getElementById('sim-btn').textContent = 'Sim';
    document.getElementById('sim-btn').classList.remove('sim-active');

    scheduleReconnect();
}
