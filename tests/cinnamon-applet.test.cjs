const { test } = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');
class Actor {
    constructor(props) { Object.assign(this, props); this.children = []; }
    add_actor(actor) { this.children.push(actor); }
}
const context = vm.createContext({
    imports: { gi: { versions: {}, Clutter: { ActorAlign: { CENTER: 0 } },
        Gio: { FileIcon: Actor, File: { new_for_path: p => p } }, GLib: { build_filenamev: p => p.join('/') },
        St: { BoxLayout: Actor, Label: Actor, Icon: Actor, IconType: { SYMBOLIC: 0, FULLCOLOR: 1 } } },
        ui: { applet: { Applet: class {} } } }, _: s => s,
});
vm.runInContext(fs.readFileSync('packaging/cinnamon/quotadeck@local/applet.js', 'utf8') + '\nthis.AppletClass = QuotaDeckApplet;', context);
function fixture() {
    const applet = Object.create(context.AppletClass.prototype);
    Object.assign(applet, { _control: { mode: 'claude', claude: { activeAccountId: 'a' } },
        _metadata: { path: '/applet' }, _panelIconSize: 16, thresholdAlertEnabled: true, thresholdAlertPercent: 80 });
    const item = { account: { id: 'a', providerId: 'claude' }, snapshot: { windows: [
        { id: 'five-hour', usedPercent: 100, resetsAt: new Date(Date.now() + 3600000).toISOString() },
        { id: 'seven-day', usedPercent: 40 },
    ] } };
    return { applet, item, indicator: { item, window: item.snapshot.windows[1], used: 40 } };
}
test('selected Claude weekly indicator has a colored provider, selection frame and 5h ring', () => {
    const { applet, indicator } = fixture();
    const actor = applet._indicatorActor('claude', '40%', 'normal', indicator);
    assert.match(actor.children[0].style_class, /provider-claude/);
    assert.match(actor.style_class, /quotadeck-selected-indicator/);
    assert.match(actor.style, /border: 2px solid #7ab7ff/);
    assert.match(actor.style, /background-color: #253b50/);
    assert.equal(actor.y_align, 0);
    assert.equal(actor.children[0].gicon.file, '/applet/icon-claude-blocked.svg');
    assert.equal(actor.children[0].icon_type, 1);
    assert.match(applet._indicatorSummary(indicator), /Blocked by the 5-hour window/);
});
test('threshold is inclusive, configurable and can be disabled', () => {
    const { applet, indicator } = fixture();
    for (const [used, expected] of [[79.9, false], [80, true], [100, true], [null, false]]) {
        indicator.used = used;
        assert.equal(applet._thresholdReached(indicator), expected);
    }
    indicator.used = 90; applet.thresholdAlertPercent = 95;
    assert.equal(applet._thresholdReached(indicator), false);
    applet.thresholdAlertPercent = 80; applet.thresholdAlertEnabled = false;
    assert.equal(applet._thresholdReached(indicator), false);
});
test('stale and expired windows do not claim an active block', () => {
    const { applet, item, indicator } = fixture();
    item.snapshot.stale = true; indicator.used = 100;
    assert.equal(applet._blockedWindow(item), null);
    assert.equal(applet._thresholdReached(indicator), false);
    item.snapshot.stale = false;
    item.snapshot.windows[0].resetsAt = new Date(Date.now() - 1000).toISOString();
    assert.equal(applet._blockedWindow(item), null);
    applet._control.mode = 'zai';
    assert.equal(applet._isSelectedClaude(item), false);
});

test('expired reset times are not presented as future countdowns', () => {
    const { applet } = fixture();
    assert.equal(applet._formatReset(new Date(Date.now() - 10 * 3600000).toISOString()),
        'reset time passed · awaiting refresh');
    assert.equal(applet._formatReset(new Date(Date.now() + 2 * 3600000).toISOString()), 'resets in 2h');
    assert.equal(applet._formatReset('invalid'), 'reset unknown');
});
test('hidden selected account is surfaced when blocked', () => {
    const { applet, item } = fixture();
    applet._refreshSettingsOptions = () => {};
    applet._selectIndicators = () => [];
    applet._renderPanelIndicators = indicators => {
        assert.equal(indicators.length, 1);
        assert.equal(indicators[0].window.id, 'five-hour');
    };
    applet.set_applet_tooltip = () => {};
    applet._renderMenu = () => {};
    applet._renderState({ accounts: [item] });
});

test('non-selected Claude account still shows its 5h ring on a weekly indicator', () => {
    const { applet, item, indicator } = fixture();
    applet._control.claude.activeAccountId = 'another-account';
    const actor = applet._indicatorActor('claude', '40%', 'normal', indicator);
    assert.equal(actor.children.length, 2);
    assert.doesNotMatch(actor.style_class, /quotadeck-selected-indicator/);
    assert.equal(actor.children[0].gicon.file, '/applet/icon-claude-blocked.svg');
    assert.match(applet._indicatorSummary(indicator), /Blocked by the 5-hour window/);
    assert.doesNotMatch(applet._indicatorSummary(indicator), /Selected Claude/);
    item.account.providerId = 'codex';
    assert.equal(applet._blockedWindow(item), null);
});
