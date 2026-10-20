const { test } = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');

// Exercise the real constructor, polling and UI lifecycle. The mocks retain
// native children just like Clutter, and reject writes after explicit destroy.
function harness() {
    const metrics = { created: 0, destroyed: 0, finalized: 0 };
    const sources = new Map();
    const requests = [];
    let nextId = 1;
    class Signals {
        constructor() { this.signals = new Map(); }
        connect(name, callback) { const id = nextId++; this.signals.set(id, { name, callback }); return id; }
        disconnect(id) { this.signals.delete(id); }
        emit(name, ...args) {
            for (const signal of [...this.signals.values()]) {
                if (signal.name === name) signal.callback(this, ...args);
            }
        }
    }
    class Actor extends Signals {
        constructor(props = {}) { super(); Object.assign(this, props); this.children = []; metrics.created++; }
        add_actor(actor) { assert.ok(!this.destroyed); actor.parent = this; this.children.push(actor); }
        set_text(text) { assert.ok(!this.destroyed); this.text = text; }
        add_style_class_name(name) { assert.ok(!this.destroyed); this.classes ??= new Set(); this.classes.add(name); }
        remove_style_class_name(name) { assert.ok(!this.destroyed); this.classes?.delete(name); }
        destroy() {
            assert.ok(!this.destroyed, 'native actor destroyed twice');
            [...this.children].forEach(child => child.destroy());
            if (this.parent) this.parent.children.splice(this.parent.children.indexOf(this), 1);
            this.destroyed = true;
            metrics.destroyed++;
            this.emit('destroy');
        }
    }
    class MenuItem extends Signals {
        constructor(text = '') {
            super(); this.actor = new Actor(); this.label = new Actor({ text }); this.actor.add_actor(this.label);
        }
        destroy() { this.actor.destroy(); this.emit('destroy'); }
    }
    class Menu extends Signals {
        constructor() { super(); this.items = []; this.actor = new Actor(); this.isOpen = false; }
        addMenuItem(item, position = this.items.length) {
            this.items.splice(position, 0, item);
            this.actor.add_actor(item.actor);
            item.connect('destroy', () => this.items.splice(this.items.indexOf(item), 1));
        }
        setOpen(open) { this.isOpen = open; this.emit('open-state-changed', open); }
        destroy() { [...this.items].forEach(item => item.destroy()); this.actor.destroy(); this.emit('destroy'); }
    }
    class Applet {
        constructor() { this.actor = new Actor(); this._applet_context_menu = new Menu(); }
        getPanelIconSize() { return 16; }
        setAllowedLayout() {}
        set_applet_tooltip(text) { this.tooltip = text; }
        configureApplet() { this.configured = true; }
    }
    class Settings {
        constructor(applet) {
            this.applet = applet;
            this.settingsData = JSON.parse(fs.readFileSync('packaging/cinnamon/quotadeck@local/settings-schema.json'));
            for (const setting of Object.values(this.settingsData)) setting.value = setting.default;
            this.settingsData['display-indicators'].value = [{ indicator: 'auto' }];
        }
        bind(key, property, callback) { this.applet[property] = this.settingsData[key].value; }
        getValue(key) { return this.settingsData[key].value; }
        _saveToFile() {}
        finalize() { metrics.finalized++; }
    }
    class Session {
        send_and_read_async(message, priority, cancellable, callback) { requests.push({ message, cancellable, callback, session: this }); }
        send_and_read_finish(result) {
            if (result instanceof Error) throw result;
            return { get_data: () => result };
        }
        abort() { this.aborted = true; }
    }
    const mainloop = {
        idle_add(callback) { const id = nextId++; sources.set(id, { callback, idle: true }); return id; },
        timeout_add_seconds(seconds, callback) { const id = nextId++; sources.set(id, { callback, seconds }); return id; },
        source_remove(id) { assert.ok(sources.delete(id), 'removing a missing source'); },
    };
    const context = vm.createContext({
        imports: {
            gi: {
                versions: {}, Clutter: { ActorAlign: { CENTER: 0 } },
                Gio: {
                    Cancellable: class { cancel() { this.cancelled = true; } },
                    FileIcon: class { constructor(props) { Object.assign(this, props); } },
                    File: { new_for_path: p => p }, Subprocess: { new() {} }, SubprocessFlags: { NONE: 0 },
                },
                GLib: {
                    build_filenamev: parts => parts.join('/'), get_user_config_dir: () => '/config',
                    FileTest: { EXISTS: 0 }, file_test: () => false, PRIORITY_DEFAULT: 0,
                    get_monotonic_time: () => 60_000_000,
                },
                St: { BoxLayout: Actor, Label: Actor, Icon: Actor, IconType: { SYMBOLIC: 0, FULLCOLOR: 1 }, Side: { LEFT: 1, RIGHT: 2 } },
                Soup: { Session, Message: { new: (method, url) => ({ method, url, status_code: 200, get_request_headers: () => ({ append() {} }) }) } },
            },
            byteArray: { toString: bytes => bytes.toString() }, mainloop,
            ui: { applet: { Applet, AllowedLayout: { BOTH: 0 } }, settings: { AppletSettings: Settings }, popupMenu: {
                PopupMenuSection: Menu, PopupMenuItem: MenuItem, PopupIconMenuItem: MenuItem, PopupSeparatorMenuItem: MenuItem,
            } },
        }, _: text => text, global: { log() {}, logError() {} },
    });
    const source = fs.readFileSync('packaging/cinnamon/quotadeck@local/applet.js', 'utf8');
    vm.runInContext(source + '\nthis.AppletClass = QuotaDeckApplet;', context);
    const applet = new context.AppletClass({ uuid: 'quotadeck@local', path: '/applet' }, 0, 30, 37);
    function respond(path, data, status = 200) {
        const index = requests.findIndex(request => request.message.url.endsWith(path));
        assert.notEqual(index, -1, 'request exists: ' + path);
        const request = requests.splice(index, 1)[0];
        request.message.status_code = status;
        request.callback(request.session, data instanceof Error ? data : JSON.stringify(data));
    }
    function flush() {
        for (let iterations = 0; iterations < 100; iterations++) {
            const pending = [...sources].filter(([, source]) => source.idle);
            if (!pending.length) return;
            for (const [id, source] of pending) {
                if (sources.has(id) && !source.callback()) sources.delete(id);
            }
        }
        assert.fail('render loop did not settle');
    }
    const state = { accounts: [{ account: { id: 'a', providerId: 'claude', label: 'Personal' }, snapshot: {
        status: 'ok', windows: [{ id: 'seven-day', label: 'Weekly', usedPercent: 40 }],
    } }] };
    const control = { mode: 'claude', claude: { activeAccountId: 'a' } };
    function ready() { respond('/state', state); respond('/control', control); flush(); }
    function poll() { applet._loadState(); ready(); }
    return { applet, state, control, metrics, requests, sources, respond, flush, ready, poll };
}

test('1000 background polls update values without rebuilding panel or closed menu actors', () => {
    const h = harness(); h.ready();
    const actor = h.applet._panelActors[0];
    const before = { ...h.metrics };
    for (let i = 0; i < 1000; i++) {
        h.state.accounts[0].snapshot.windows[0].usedPercent = 20 + i % 50;
        h.poll();
    }
    assert.equal(h.applet._panelActors[0], actor);
    assert.equal(actor._quotaView.label.text, '69%');
    assert.equal(h.applet._quotaSection.items.length, 0);
    assert.deepEqual(h.metrics, before);
    assert.equal(h.sources.size, 1, 'only the periodic poll remains');
});

test('open menu reuses rows, shows fresh values on reopening and keeps one action handler', () => {
    const h = harness(); h.ready(); h.applet.menu.setOpen(true);
    const before = { ...h.metrics };
    const items = [...h.applet._quotaSection.items];
    for (let i = 0; i < 100; i++) {
        h.state.accounts[0].snapshot.windows[0].usedPercent = 10 + i % 50;
        h.poll();
    }
    assert.deepEqual([...h.applet._quotaSection.items], items);
    assert.deepEqual(h.metrics, before);
    h.applet.menu.setOpen(false);
    h.state.accounts[0].snapshot.windows[0].usedPercent = 30;
    h.poll();
    assert.ok(h.applet._menuRows.some(row => row.item.label.text.includes('59%')));
    h.applet.menu.setOpen(true);
    assert.ok(h.applet._menuRows.some(row => row.item.label.text.includes('30%')));
    h.applet._menuRows.find(row => row.id === 'refresh').item.emit('activate');
    assert.equal(h.requests.filter(request => request.message.method === 'POST').length, 1);
    h.respond('/refresh', {});
    assert.equal(h.requests.filter(request => request.message.url.endsWith('/state')).length, 1);
});

test('panel updates selection, block, threshold and icon size without replacing the row', () => {
    const h = harness(); h.ready();
    const actor = h.applet._panelActors[0];
    h.state.accounts[0].snapshot.windows[0].usedPercent = 90;
    h.poll();
    assert.ok(actor._quotaView.alert);
    h.state.accounts[0].snapshot.windows.push({ id: 'five-hour', usedPercent: 100 });
    h.poll();
    assert.equal(actor._quotaView.alert, null);
    assert.equal(actor._quotaView.icon.gicon.file, '/applet/icon-claude-blocked.svg');
    h.control.claude.activeAccountId = 'other'; h.poll();
    assert.doesNotMatch(actor.style_class, /selected-indicator/);
    h.applet.on_panel_icon_size_changed(24); h.flush();
    assert.equal(actor._quotaView.icon.icon_size, 24);
    assert.equal(h.applet._panelActors[0], actor);
});

test('removal cancels timers and ignores late network completions without retrying or drawing', () => {
    const h = harness(); h.ready(); h.applet.menu.setOpen(true);
    h.applet._loadState(); h.applet._scheduleRender(); h.applet._scheduleRetry();
    const request = h.requests[0];
    h.applet.on_applet_removed_from_panel();
    const before = { ...h.metrics };
    assert.equal(request.cancellable.cancelled, true);
    h.respond('/state', new Error('cancelled'));
    h.applet._scheduleRender(); h.applet._scheduleRetry(); h.applet._loadState();
    h.applet.on_applet_removed_from_panel(); h.flush();
    assert.equal(h.sources.size, 0);
    assert.equal(h.requests.length, 0);
    assert.equal(h.applet._quotaSection.items.length, 0);
    assert.equal(h.applet._panelActors.length, 0);
    assert.equal(h.applet.menu.signals.size, 0);
    assert.deepEqual(h.metrics, before);
    assert.equal(h.metrics.finalized, 1);
});

test('consumer exceptions are not mistaken for HTTP failures and delivered twice', () => {
    const h = harness(); h.ready();
    let calls = 0;
    h.applet._request('GET', '/test', null, () => { calls++; throw new Error('UI failure'); });
    assert.throws(() => h.respond('/test', {}), /UI failure/);
    assert.equal(calls, 1);
});

test('temporary and sustained outages retain cached state and recover using the existing panel', () => {
    const h = harness(); h.ready();
    const actor = h.applet._panelActors[0];
    for (let i = 0; i < 3; i++) {
        h.applet._loadState(); h.respond('/state', {}, 503); h.flush();
        if (i < 2) assert.equal(actor._quotaView.label.text, '40%');
    }
    assert.equal(actor._quotaView.label.text, 'offline');
    assert.equal(h.sources.size, 2, 'periodic poll plus one retry');
    h.applet.menu.setOpen(true);
    assert.ok(h.applet._menuRows.some(row => row.item.label.text.includes('offline')));
    h.poll();
    assert.equal(actor._quotaView.label.text, '40%');
    assert.equal(h.applet._panelActors[0], actor);
    assert.equal(h.sources.size, 1);
    assert.ok(h.applet._menuRows.some(row => row.item.label.text.includes('40%')));
});

test('a lost libsoup completion cannot stop the periodic polls', () => {
    const h = harness(); h.ready();
    // The service restarts mid-request: this completion is never delivered,
    // and no error reaches the applet — the poll cycle is the only recovery.
    h.applet._loadState();
    assert.equal(h.requests.length, 1);
    const tick = [...h.sources.values()].find(source => source.seconds === 60);
    assert.ok(tick, 'the periodic poll source exists');
    tick.callback();
    assert.equal(h.requests.length, 2, 'the next cycle must issue a fresh request');
    // A late delivery of the superseded request must not free the live slot.
    const lost = h.requests.shift();
    lost.callback(lost.session, JSON.stringify(h.state));
    tick.callback();
    assert.equal(h.requests.length, 2, 'the reclaimed slot stays owned by the live request');
    h.respond('/state', h.state);
    h.flush();
});
