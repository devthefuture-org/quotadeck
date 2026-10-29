const { test } = require('node:test');
const assert = require('node:assert/strict');
const http = require('node:http');
const path = require('node:path');
const { spawn, spawnSync } = require('node:child_process');

const available = ['cjs', 'curl'].every(command => spawnSync(command, ['--version']).status === 0);

test('native CJS transport contains network failures and terminates pending children', {
    skip: available ? false : 'requires cjs and curl', timeout: 30000,
}, async t => {
    const server = http.createServer((request, response) => {
        if (request.url === '/hang') return;
        if (request.url === '/reset') { request.socket.destroy(); return; }
        if (request.url === '/error') response.statusCode = 503;
        if (request.url === '/redirect') response.statusCode = 302;
        if (request.url === '/empty') response.statusCode = 204;
        response.end(request.url === '/invalid' ? '{invalid' : JSON.stringify({
            ok: true, method: request.method, header: request.headers['x-quotadeck-request'],
        }));
    });
    t.after(() => { server.closeAllConnections(); server.close(); });
    await new Promise((resolve, reject) => {
        server.once('error', reject);
        server.listen(0, '127.0.0.1', resolve);
    });
    const child = spawn('cjs', [
        path.join(__dirname, 'cinnamon-transport-gjs.js'),
        path.join(__dirname, '../packaging/cinnamon/quotadeck@local/applet.js'),
        'http://127.0.0.1:' + server.address().port,
    ], { env: { ...process.env, http_proxy: 'http://127.0.0.1:1', no_proxy: '' } });
    t.after(() => { if (child.exitCode === null) child.kill(); });
    let output = '';
    child.stdout.on('data', data => { output += data; });
    child.stderr.on('data', data => { output += data; });
    const code = await new Promise((resolve, reject) => { child.once('error', reject); child.once('close', resolve); });
    assert.equal(code, 0, output);
    assert.doesNotMatch(output, /CRITICAL|WARNING/, 'native API misuse: ' + output);
    assert.match(output, /Native transport verified/);
});
