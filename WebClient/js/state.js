// =============================================================================
// State
// =============================================================================

const CONFIG = {
    wsUrl:              `ws://${window.location.hostname || 'localhost'}:8000`,
    staleThresholdMs:   500,
    reconnect:          { baseMs: 1000, maxMs: 10000, factor: 2 },
    graphBufferMinutes: 15,
    consoleBufferLimit: 500
};

// --- Connection ---
let ws             = null;
let reconnectDelay = CONFIG.reconnect.baseMs;
let reconnectTimer = null;
let stalenessTimer = null;
let simActive      = false;
let devMode        = false;

// --- Auth ---
let operatorName = '';

// --- Config ---
let configControls = [];
let configApplied  = false;

// --- Tabs ---
let tabs        = [];
let activeTabId = null;
const tabCounts = {};   // { frontPanel: 1, ... } running counter for auto-naming

// --- Graph ---
// graphState[tabId] = { rows, cols, gridEl, cells: [{ cellEl, chart, channels: [{refDes,color,hidden}] }] }
const graphState          = {};
const channelBuffers      = {};         // refDes -> { ts: number[], vals: number[] }
const activeGraphChannels = new Set();

const CHART_COLORS = [
    '#58a6ff', '#3fb950', '#f78166', '#e3b341',
    '#bc8cff', '#56d364', '#79c0ff', '#ffa657',
    '#ff7b72', '#d2a8ff'
];

// --- Dev ---
const devStats = {
    connectedAt:     null,
    msgCount:        0,
    lastWindowCount: 0,
    missedCycles:    0,
    lastDataT:       null,
    avgInterval:     null
};
let devTabs = [];

// --- Console ---
const consoleLog = [];   // [{ time, dir: 'in'|'out', msg }]
let consoleTabs  = [];
