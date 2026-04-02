// =============================================================================
// Card builders
// =============================================================================

const isCmd = ch => ch.role === 'cmd-bool' || ch.role === 'cmd-pct' || ch.role === 'cmd-float';

function buildCard(ctrl, tab) {
    switch (ctrl.type) {
        case 'pressure':
        case 'temperature':
        case 'flowMeter':
        case 'thrust':
        case 'tank':       return buildSensorCard(ctrl, tab);
        case 'valve':      return buildValveCard(ctrl, tab);
        case 'bangBang':   return buildBangBangCard(ctrl, tab);
        case 'ignition':   return buildIgnitionCard(ctrl, tab);
        case 'digitalOut': return buildDigitalOutCard(ctrl, tab);
        case 'VFD':        return buildVFDCard(ctrl, tab);
        default:           return buildSensorCard(ctrl, tab);
    }
}

function buildSensorCard(ctrl, tab) {
    const card = mkEl('div', `card card-sensor card-${ctrl.type}`);
    card.appendChild(mkEl('div', 'card-desc',   ctrl.description || ctrl.refDes));
    card.appendChild(mkEl('div', 'card-refdes', ctrl.refDes));

    for (const ch of ctrl.channels) {
        const row   = mkEl('div', 'sensor-row');
        const valEl = mkEl('span', 'value stale', '--');
        row.appendChild(valEl);
        row.appendChild(mkEl('span', 'units', ch.units ?? ''));
        card.appendChild(row);
        let staleTimer = null;
        tab.channelUpdaters[ch.refDes] = (v) => {
            valEl.textContent = typeof v === 'number' ? v.toFixed(2) : String(v);
            valEl.classList.remove('stale');
            clearTimeout(staleTimer);
            staleTimer = setTimeout(() => valEl.classList.add('stale'), CONFIG.channelStaleMs);
        };
    }
    return card;
}

function buildValveCard(ctrl, tab) {
    const card = mkEl('div', `card card-valve subtype-${ctrl.subType}`);
    card.appendChild(mkEl('div', 'card-desc',   ctrl.description || ctrl.refDes));
    card.appendChild(mkEl('div', 'card-refdes', ctrl.refDes));

    const cmdCh = ctrl.channels.find(c => isCmd(c));
    const fbCh  = ctrl.channels.find(c => !isCmd(c) && c.refDes.endsWith('-FB'));
    const posCh = ctrl.channels.find(c => !isCmd(c) && c.refDes.endsWith('-POS'));

    if (fbCh) {
        const fbRow = mkEl('div', 'fb-row');
        const led   = mkEl('span', 'led led-unknown');
        const lbl   = mkEl('span', 'fb-label stale', 'FB: --');
        fbRow.appendChild(led); fbRow.appendChild(lbl);
        card.appendChild(fbRow);
        let fbStaleTimer = null;
        tab.channelUpdaters[fbCh.refDes] = (v) => {
            const open = Boolean(v);
            led.className   = `led ${open ? 'led-open' : 'led-closed'}`;
            lbl.textContent = `FB: ${open ? 'OPEN' : 'CLOSED'}`;
            lbl.classList.remove('stale');
            clearTimeout(fbStaleTimer);
            fbStaleTimer = setTimeout(() => lbl.classList.add('stale'), CONFIG.channelStaleMs);
        };
    }

    if (posCh) {
        const row   = mkEl('div', 'sensor-row');
        const valEl = mkEl('span', 'value stale', '--');
        row.appendChild(valEl); row.appendChild(mkEl('span', 'units', '%'));
        card.appendChild(row);
        let posStaleTimer = null;
        tab.channelUpdaters[posCh.refDes] = (v) => {
            valEl.textContent = typeof v === 'number' ? v.toFixed(1) : String(v);
            valEl.classList.remove('stale');
            clearTimeout(posStaleTimer);
            posStaleTimer = setTimeout(() => valEl.classList.add('stale'), CONFIG.channelStaleMs);
        };
    }

    if (cmdCh) {
        const btnRow = mkEl('div', 'btn-row');
        if (cmdCh.role === 'cmd-pct') {
            const slider = document.createElement('input');
            slider.type = 'range'; slider.min = 0; slider.max = 100; slider.value = 0;
            slider.className = 'pos-slider';
            const posOut = mkEl('span', 'pos-out', '0%');
            slider.addEventListener('input',  () => posOut.textContent = `${slider.value}%`);
            slider.addEventListener('change', () => sendCommand(cmdCh.refDes, parseFloat(slider.value)));
            btnRow.appendChild(slider); btnRow.appendChild(posOut);
        } else {
            const openBtn  = mkEl('button', 'btn btn-open',  'OPEN');
            const closeBtn = mkEl('button', 'btn btn-close', 'CLOSE');
            openBtn.addEventListener('click',  () => sendCommand(cmdCh.refDes, 1));
            closeBtn.addEventListener('click', () => sendCommand(cmdCh.refDes, 0));
            btnRow.appendChild(openBtn); btnRow.appendChild(closeBtn);
        }
        card.appendChild(btnRow);
    }
    return card;
}

function buildBangBangCard(ctrl, tab) {
    const card = mkEl('div', 'card card-bangbang');
    card.appendChild(mkEl('div', 'card-desc',   ctrl.description || ctrl.refDes));
    card.appendChild(mkEl('div', 'card-refdes', ctrl.refDes));
    if (ctrl.details?.senseRefDes) {
        card.appendChild(mkEl('div', 'sense-label', `Sense: ${ctrl.details.senseRefDes}`));
    }
    for (const ch of ctrl.channels) {
        const row = mkEl('div', 'fb-row');
        const led = mkEl('span', 'led led-unknown');
        const lbl = mkEl('span', 'fb-label stale', `${ch.refDes}: --`);
        row.appendChild(led); row.appendChild(lbl);
        card.appendChild(row);
        let staleTimer = null;
        tab.channelUpdaters[ch.refDes] = (v) => {
            const on = Boolean(v);
            led.className   = `led ${on ? 'led-open' : 'led-closed'}`;
            lbl.textContent = `${ch.refDes}: ${on ? 'ON' : 'OFF'}`;
            lbl.classList.remove('stale');
            clearTimeout(staleTimer);
            staleTimer = setTimeout(() => lbl.classList.add('stale'), CONFIG.channelStaleMs);
        };
    }
    return card;
}

function buildIgnitionCard(ctrl, tab) {
    const card = mkEl('div', 'card card-ignition');
    card.appendChild(mkEl('div', 'card-desc',   ctrl.description || ctrl.refDes));
    card.appendChild(mkEl('div', 'card-refdes', ctrl.refDes));

    for (const ch of ctrl.channels.filter(c => !isCmd(c))) {
        const row = mkEl('div', 'fb-row');
        const led = mkEl('span', 'led led-unknown');
        const lbl = mkEl('span', 'fb-label stale', `${ch.refDes}: --`);
        row.appendChild(led); row.appendChild(lbl);
        card.appendChild(row);
        let staleTimer = null;
        tab.channelUpdaters[ch.refDes] = (v) => {
            const active = Boolean(v);
            led.className   = `led ${active ? 'led-active' : 'led-inactive'}`;
            lbl.textContent = `${ch.refDes}: ${active ? 'ACTIVE' : 'INACTIVE'}`;
            lbl.classList.remove('stale');
            clearTimeout(staleTimer);
            staleTimer = setTimeout(() => lbl.classList.add('stale'), CONFIG.channelStaleMs);
        };
    }

    const cmdCh = ctrl.channels.find(c => isCmd(c));
    if (cmdCh) {
        const btnRow = mkEl('div', 'btn-row ignition-row');
        const armId  = `arm-${ctrl.refDes}-${tab.id}`;
        const armBox = document.createElement('input');
        armBox.type = 'checkbox'; armBox.id = armId; armBox.className = 'arm-checkbox';
        const armLbl = document.createElement('label');
        armLbl.htmlFor = armId; armLbl.textContent = 'ARM'; armLbl.className = 'arm-label';
        const fireBtn = mkEl('button', 'btn btn-fire', 'FIRE');
        fireBtn.disabled = true;
        armBox.addEventListener('change', () => { fireBtn.disabled = !armBox.checked; });
        fireBtn.addEventListener('click', () => {
            if (armBox.checked) {
                sendCommand(cmdCh.refDes, 1);
                armBox.checked = false; fireBtn.disabled = true;
            }
        });
        btnRow.appendChild(armBox); btnRow.appendChild(armLbl); btnRow.appendChild(fireBtn);
        card.appendChild(btnRow);
    }
    return card;
}

function buildDigitalOutCard(ctrl, _tab) {
    const card = mkEl('div', 'card card-digital');
    card.appendChild(mkEl('div', 'card-desc',   ctrl.description || ctrl.refDes));
    card.appendChild(mkEl('div', 'card-refdes', ctrl.refDes));
    const cmdCh = ctrl.channels.find(c => isCmd(c)) ?? ctrl.channels[0];
    if (cmdCh) {
        const btnRow = mkEl('div', 'btn-row');
        const onBtn  = mkEl('button', 'btn btn-open',  'ON');
        const offBtn = mkEl('button', 'btn btn-close', 'OFF');
        onBtn.addEventListener('click',  () => sendCommand(cmdCh.refDes, 1));
        offBtn.addEventListener('click', () => sendCommand(cmdCh.refDes, 0));
        btnRow.appendChild(onBtn); btnRow.appendChild(offBtn);
        card.appendChild(btnRow);
    }
    return card;
}

function buildVFDCard(ctrl, _tab) {
    const card = mkEl('div', 'card card-vfd');
    card.appendChild(mkEl('div', 'card-desc',   ctrl.description || ctrl.refDes));
    card.appendChild(mkEl('div', 'card-refdes', ctrl.refDes));
    const cmdCh = ctrl.channels.find(c => isCmd(c));
    if (cmdCh) {
        const row   = mkEl('div', 'btn-row');
        const input = document.createElement('input');
        input.type = 'number'; input.min = 0; input.max = 60; input.value = 0;
        input.className = 'vfd-input';
        const sendBtn = mkEl('button', 'btn', 'Set Hz');
        sendBtn.addEventListener('click', () => sendCommand(cmdCh.refDes, parseFloat(input.value)));
        row.appendChild(input); row.appendChild(sendBtn);
        card.appendChild(row);
    }
    return card;
}
