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
    let spawnError = null;
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
    class Subprocess {
        static new(argv) {
            if (spawnError) { const error = spawnError; spawnError = null; throw error; }
            return new Subprocess(argv);
        }
        constructor(argv) { this.argv = argv; this.successful = true; }
        communicate_utf8_async(input, cancellable, callback) {
            assert.equal(input, null);
            requests.push({
                message: { method: this.argv[this.argv.indexOf('--request') + 1], url: this.argv.at(-1) },
                argv: this.argv, cancellable, callback, process: this,
            });
        }
        communicate_utf8_finish(result) {
            if (result instanceof Error) throw result;
            return result;
        }
        get_successful() { return this.successful; }
        force_exit() { this.killed = true; }
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
                    File: { new_for_path: p => p }, Subprocess, SubprocessFlags: { NONE: 0, STDOUT_PIPE: 1, STDERR_PIPE: 2 },
                },
                GLib: {
                    build_filenamev: parts => parts.join('/'), get_user_config_dir: () => '/config',
                    FileTest: { EXISTS: 0 }, file_test: () => false, PRIORITY_DEFAULT: 0,
                    get_monotonic_time: () => 60_000_000,
                },
                St: { BoxLayout: Actor, Label: Actor, Icon: Actor, IconType: { SYMBOLIC: 0, FULLCOLOR: 1 }, Side: { LEFT: 1, RIGHT: 2 } },
            },
            mainloop,
            ui: { applet: { Applet, AllowedLayout: { BOTH: 0 } }, settings: { AppletSettings: Settings }, popupMenu: {
                PopupMenuSection: Menu, PopupMenuItem: MenuItem, PopupIconMenuItem: MenuItem, PopupSeparatorMenuItem: MenuItem,
            } },
        }, _: text => text, global: { log() {}, logError() {} },
    });
    const source = fs.readFileSync('packaging/cinnamon/quotadeck@local/applet.js', 'utf8');
    vm.runInContext(source + '\nthis.AppletClass = QuotaDeckApplet;', context);
    const applet = new context.AppletClass({ uuid: 'quotadeck@local', path: '/applet' }, 0, 30, 37);
    function respond(path, data, status = 200, options = {}) {
        const index = requests.findIndex(request => request.message.url.endsWith(path));
        assert.notEqual(index, -1, 'request exists: ' + path);
        const request = requests.splice(index, 1)[0];
        request.process.successful = options.successful !== false;
        request.callback(request.process, data instanceof Error ? data
            : [true, (options.raw ?? JSON.stringify(data)) + '\n' + status, options.stderr || '']);
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
    return { applet, state, control, metrics, requests, sources, respond, flush, ready, poll,
        failNextSpawn: error => { spawnError = error; } };
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
    assert.equal(h.applet._requests.size, 0, 'completed subprocesses are released');
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
    assert.equal(request.process.killed, true);
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
    assert.equal(h.applet._requests.size, 0);
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

test('a lost completion cannot stop the periodic polls', () => {
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
    lost.callback(lost.process, [true, JSON.stringify(h.state) + '\n200', '']);
    tick.callback();
    assert.equal(h.requests.length, 2, 'the reclaimed slot stays owned by the live request');
    h.respond('/state', h.state);
    h.flush();
});

test('HTTP uses bounded isolated subprocesses and passes headers as literal arguments', () => {
    const h = harness(); h.ready();
    const header = 'refresh; $(touch /not-executed)';
    h.applet._request('POST', '/api/v1/refresh', { 'X-QuotaDeck-Request': header }, () => {});
    const argv = [...h.requests[0].argv];
    assert.equal(argv[0], 'curl');
    assert.equal(argv[1], '--disable', 'ignore user curlrc settings');
    assert.equal(argv[argv.indexOf('--noproxy') + 1], '*');
    assert.equal(argv[argv.indexOf('--max-time') + 1], '12');
    assert.equal(argv[argv.indexOf('--connect-timeout') + 1], '3');
    assert.equal(argv[argv.indexOf('--header') + 1], 'X-QuotaDeck-Request: ' + header);
    assert.equal(argv.at(-1), 'http://127.0.0.1:9211/api/v1/refresh');
});

test('child failure, HTTP errors and malformed JSON report once and release each request', () => {
    const h = harness(); h.ready();
    for (const response of [
        { status: 503 }, { status: 302 }, { status: 200, raw: '{invalid' },
        { status: 200, successful: false, stderr: 'curl: timeout' },
    ]) {
        let calls = 0;
        h.applet._request('GET', '/test', null, (error, data) => {
            calls++; assert.ok(error); assert.equal(data, null);
        });
        h.respond('/test', {}, response.status, response);
        assert.equal(calls, 1);
        assert.equal(h.applet._requests.size, 0);
    }
});

test('a missing HTTP executable is reported and empty successful responses remain valid', () => {
    const h = harness(); h.ready();
    h.failNextSpawn(new Error('curl not found'));
    let calls = 0;
    h.applet._request('GET', '/test', null, error => { calls++; assert.match(error.message, /not found/); });
    assert.equal(calls, 1);
    assert.equal(h.applet._requests.size, 0);
    h.applet._request('POST', '/test', null, (error, data) => {
        assert.equal(error, null); assert.equal(Object.keys(data).length, 0);
    });
    h.respond('/test', null, 204, { raw: '' });
});
