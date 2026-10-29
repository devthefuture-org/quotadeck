// Invoked by cinnamon-transport.test.cjs with a local HTTP fixture. Only the UI
// is stubbed: _request uses the installed CJS, Gio.Subprocess and curl.
const Gio = imports.gi.Gio;
const GLib = imports.gi.GLib;
const Mainloop = imports.mainloop;
const ByteArray = imports.byteArray;
const System = imports.system;
const source = ByteArray.toString(GLib.file_get_contents(ARGV[0])[1])
    .replace('http://127.0.0.1:9211', ARGV[1]);
const AppletClass = new Function('imports', source + '\nreturn QuotaDeckApplet;')({
    gi: { Gio, GLib, Clutter: {}, St: {} }, mainloop: Mainloop,
    ui: { applet: { Applet: class {} }, popupMenu: {}, settings: {} },
});

function assert(condition, message) {
    if (!condition) throw new Error(message);
}

function client() {
    return Object.assign(Object.create(AppletClass.prototype), {
        _requests: new Set(), _cancellable: new Gio.Cancellable(), _destroyed: false,
        _timeoutId: 0, _retryId: 0, _renderSourceId: 0, _menuStateChangedId: 0,
        menu: { disconnect() {} }, _settings: { finalize() {} },
        _quotaSection: { destroy() {} }, _panelActors: [], _indicatorBox: { destroy() {} },
    });
}

function request(applet, path, method = 'GET', headers = null) {
    return new Promise(resolve => applet._request(method, path, headers, (error, data) => resolve({ error, data })));
}

async function run() {
    const a = client();
    for (let i = 0; i < 30; i++) {
        const response = await request(a, '/ok');
        assert(!response.error && response.data.ok, 'real HTTP GET failed');
        assert(a._requests.size === 0, 'completed child retained');
    }
    const post = await request(a, '/refresh', 'POST', { 'X-QuotaDeck-Request': 'refresh' });
    assert(!post.error && post.data.method === 'POST' && post.data.header === 'refresh', 'POST/header lost');
    for (const path of ['/error', '/redirect', '/invalid', '/reset']) {
        assert((await request(a, path)).error, 'expected failure: ' + path);
        assert(a._requests.size === 0, 'failed child retained');
    }
    const empty = await request(a, '/empty');
    assert(!empty.error && Object.keys(empty.data).length === 0, 'empty 204 response failed');

    const started = GLib.get_monotonic_time();
    assert((await request(a, '/hang')).error, 'hung server did not time out');
    const seconds = (GLib.get_monotonic_time() - started) / 1000000;
    assert(seconds >= 10 && seconds < 20, 'unexpected timeout: ' + seconds);

    const interrupted = request(a, '/hang');
    [...a._requests][0].force_exit();
    assert((await interrupted).error, 'terminated child did not become a request error');
    assert((await request(a, '/ok')).data.ok, 'transport failed to recover');

    let lateCallbacks = 0;
    a._request('GET', '/hang', null, () => lateCallbacks++);
    const child = [...a._requests][0];
    a.on_applet_removed_from_panel();
    await new Promise(resolve => child.wait_async(null, (process, result) => {
        process.wait_finish(result);
        resolve();
    }));
    assert(child.get_if_signaled(), 'removed applet left its child running');
    assert(a._requests.size === 0 && lateCallbacks === 0, 'removed applet delivered a late callback');
    print('Native transport verified: 30 polls, POST, HTTP/JSON/socket errors, timeout, child termination and teardown.');
}

const loop = new GLib.MainLoop(null, false);
let failed = false;
run().then(() => loop.quit(), error => {
    printerr(error.stack || error.message);
    failed = true;
    loop.quit();
});
loop.run();
if (failed) System.exit(1);
