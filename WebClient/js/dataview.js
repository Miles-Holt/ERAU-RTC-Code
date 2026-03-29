// =============================================================================
// Data View tab
// =============================================================================

const CMD_TYPES    = new Set(['valve', 'bangBang', 'ignition', 'digitalOut', 'VFD']);
const SENSOR_TYPES = new Set(['pressure', 'temperature', 'flowMeter', 'thrust', 'tank']);

const TYPE_LABELS = {
    pressure:    'Pressures',
    temperature: 'Temperatures',
    flowMeter:   'Flow Meters',
    thrust:      'Thrust',
    tank:        'Tanks',
    valve:       'Valves',
    bangBang:    'Bang-Bang Controllers',
    ignition:    'Ignition',
    digitalOut:  'Digital Outputs',
    VFD:         'VFDs'
};

const TYPE_ORDER = [
    'pressure', 'temperature', 'flowMeter', 'thrust', 'tank',
    'valve', 'bangBang', 'ignition', 'digitalOut', 'VFD'
];

function rebuildDataView(tab) {
    tab.channelUpdaters = {};
    tab.contentEl.innerHTML = '';

    if (!configControls.length) {
        tab.contentEl.appendChild(mkEl('div', 'loading', 'Waiting for configuration from LabVIEW...'));
        return;
    }

    const cmdControls    = configControls.filter(c => CMD_TYPES.has(c.type));
    const sensorControls = configControls.filter(c => SENSOR_TYPES.has(c.type));

    // --- Command cards ---
    if (cmdControls.length) {
        const sec  = mkEl('section', 'dv-section');
        const grid = mkEl('div', 'grid');
        const groups = {};
        for (const c of cmdControls) (groups[c.type] ??= []).push(c);
        for (const type of TYPE_ORDER) {
            if (!groups[type]) continue;
            sec.appendChild(mkEl('h2', 'group-heading', TYPE_LABELS[type] ?? type));
            for (const ctrl of groups[type]) grid.appendChild(buildCard(ctrl, tab));
        }
        sec.appendChild(grid);
        tab.contentEl.appendChild(sec);
    }

    // --- Sensor table ---
    if (sensorControls.length) {
        const sec = mkEl('section', 'dv-section');
        sec.appendChild(mkEl('h2', 'group-heading', 'Sensors'));
        const table = document.createElement('table');
        table.className = 'sensor-table';
        const thead = document.createElement('thead');
        thead.innerHTML = '<tr><th>RefDes</th><th>Description</th><th>Value</th><th>Units</th></tr>';
        table.appendChild(thead);
        const tbody = document.createElement('tbody');

        for (const ctrl of sensorControls) {
            for (const ch of ctrl.channels) {
                const tr     = document.createElement('tr');
                const tdRef  = mkEl('td', 'mono', ch.refDes);
                const tdDesc = mkEl('td', null,   ctrl.description || '');
                const tdVal  = mkEl('td', 'tbl-val stale', '--');
                const tdUnit = mkEl('td', 'muted', ch.units || '');
                tr.append(tdRef, tdDesc, tdVal, tdUnit);
                tbody.appendChild(tr);
                tab.channelUpdaters[ch.refDes] = (v) => {
                    tdVal.textContent = typeof v === 'number' ? v.toFixed(2) : String(v);
                    tdVal.classList.remove('stale');
                };
            }
        }
        table.appendChild(tbody);
        sec.appendChild(table);
        tab.contentEl.appendChild(sec);
    }
}
