imports.gi.versions.Soup = '3.0';

const Applet = imports.ui.applet;
const ByteArray = imports.byteArray;
const Clutter = imports.gi.Clutter;
const Gio = imports.gi.Gio;
const GLib = imports.gi.GLib;
const Mainloop = imports.mainloop;
const PopupMenu = imports.ui.popupMenu;
const Settings = imports.ui.settings;
const Soup = imports.gi.Soup;
const St = imports.gi.St;

const API_ROOT = 'http://127.0.0.1:9211';
const POLL_SECONDS = 60;
const RETRY_SECONDS = 5;
const OFFLINE_FAILURE_THRESHOLD = 3;
const SERVICE_START_COOLDOWN_SECONDS = 30;
const PROVIDER_LABELS = { claude: 'Claude', codex: 'Codex', zai: 'Z.ai' };

class QuotaDeckApplet extends Applet.Applet {
    constructor(metadata, orientation, panelHeight, instanceId) {
        super(orientation, panelHeight, instanceId);

        this._metadata = metadata;
        this._session = new Soup.Session({ timeout: 12 });
        this._timeoutId = 0;
        this._retryId = 0;
        this._requestInFlight = false;
        this._consecutiveFailures = 0;
        this._serviceStartAttemptedAt = 0;
        this._state = null;
        this._control = null;
        this._panelIconSize = this.getPanelIconSize(St.IconType.SYMBOLIC);

        this.setAllowedLayout(Applet.AllowedLayout.BOTH);
        this._indicatorBox = new St.BoxLayout({
            vertical: [St.Side.LEFT, St.Side.RIGHT].includes(orientation),
            style_class: 'quotadeck-indicator-box',
        });
        this.actor.add_actor(this._indicatorBox);
        this.set_applet_tooltip(_('QuotaDeck is connecting to the local service'));
        this.actor.add_style_class_name('quotadeck-applet');

        this.menu = this._applet_context_menu;
        this._quotaSection = new PopupMenu.PopupMenuSection();
        this.menu.addMenuItem(this._quotaSection);

        this._migrateLegacySettings(metadata.uuid, instanceId);
        this._settings = new Settings.AppletSettings(this, metadata.uuid, instanceId);
        this._settings.bind('display-indicators', 'displayIndicators', () => this._onDisplaySelectionChanged());

        this._renderLoading();
        this._loadState();
        this._timeoutId = Mainloop.timeout_add_seconds(POLL_SECONDS, () => {
            this._loadState();
            return true;
        });
    }

    _migrateLegacySettings(uuid, instanceId) {
        const id = String(instanceId);
        if (!/^\d+$/.test(id)) {
            return;
        }
        const settingsDirectory = GLib.build_filenamev([
            GLib.get_user_config_dir(),
            'cinnamon',
            'spices',
            uuid,
        ]);
        const legacyPath = GLib.build_filenamev([settingsDirectory, uuid + '.json']);
        const instancePath = GLib.build_filenamev([settingsDirectory, id + '.json']);
        if (!GLib.file_test(legacyPath, GLib.FileTest.EXISTS)
            || GLib.file_test(instancePath, GLib.FileTest.EXISTS)) {
            return;
        }
        try {
            Gio.File.new_for_path(legacyPath).move(
                Gio.File.new_for_path(instancePath),
                Gio.FileCopyFlags.NONE,
                null,
                null
            );
            global.log('QuotaDeck: migrated Cinnamon settings to instance ' + id);
        } catch (error) {
            global.logError('QuotaDeck: could not migrate Cinnamon settings: ' + error.message);
        }
    }

    on_applet_clicked() {
        this._openDashboard();
    }

    on_orientation_changed(orientation) {
        this._indicatorBox.set_vertical([St.Side.LEFT, St.Side.RIGHT].includes(orientation));
    }

    on_panel_icon_size_changed(size) {
        this._panelIconSize = size;
        if (this._state) {
            this._renderState(this._state);
        }
    }

    on_applet_removed_from_panel() {
        if (this._timeoutId) {
            Mainloop.source_remove(this._timeoutId);
            this._timeoutId = 0;
        }
        if (this._retryId) {
            Mainloop.source_remove(this._retryId);
            this._retryId = 0;
        }
        this._session.abort();
        this._settings.finalize();
    }

    _loadState() {
        if (this._requestInFlight) {
            return;
        }
        this._requestInFlight = true;
        this._request('GET', '/api/v1/state', null, (error, state) => {
            this._requestInFlight = false;
            if (error) {
                this._consecutiveFailures += 1;
                if (!this._state || this._consecutiveFailures >= OFFLINE_FAILURE_THRESHOLD) {
                    this._renderOffline();
                } else {
                    this._renderReconnecting();
                }
                this._startServiceOnce();
                this._scheduleRetry();
                return;
            }
            this._consecutiveFailures = 0;
            this._serviceStartAttemptedAt = 0;
            this.actor.remove_style_class_name('quotadeck-reconnecting');
            if (this._retryId) {
                Mainloop.source_remove(this._retryId);
                this._retryId = 0;
            }
            this._state = state;
            this._renderState(state);
            this._loadControl();
        });
    }

    _loadControl() {
        this._request('GET', '/api/v1/control', null, (error, control) => {
            if (error) {
                return;
            }
            this._control = control;
            if (this._state) {
                const accounts = Array.isArray(this._state.accounts) ? this._state.accounts : [];
                this._renderMenu(accounts);
            }
        });
    }

    _scheduleRetry() {
        if (this._retryId) {
            return;
        }
        this._retryId = Mainloop.timeout_add_seconds(RETRY_SECONDS, () => {
            this._retryId = 0;
            this._loadState();
            return false;
        });
    }

    _request(method, path, headers, callback) {
        const message = Soup.Message.new(method, API_ROOT + path);
        if (headers) {
            Object.keys(headers).forEach(name => message.get_request_headers().append(name, headers[name]));
        }
        this._session.send_and_read_async(message, GLib.PRIORITY_DEFAULT, null, (session, result) => {
            try {
                const bytes = session.send_and_read_finish(result);
                if (message.status_code < 200 || message.status_code >= 300) {
                    callback(new Error('HTTP ' + message.status_code), null);
                    return;
                }
                const raw = ByteArray.toString(bytes.get_data());
                callback(null, raw ? JSON.parse(raw) : {});
            } catch (error) {
                callback(error, null);
            }
        });
    }

    _startServiceOnce() {
        const now = GLib.get_monotonic_time() / 1000000;
        if (this._serviceStartAttemptedAt
            && now - this._serviceStartAttemptedAt < SERVICE_START_COOLDOWN_SECONDS) {
            return;
        }
        this._serviceStartAttemptedAt = now;
        try {
            Gio.Subprocess.new(
                ['systemctl', '--user', 'start', 'quotadeck.service'],
                Gio.SubprocessFlags.NONE
            );
        } catch (error) {
            global.logError('QuotaDeck: could not start user service: ' + error.message);
        }
    }

    _renderLoading() {
        this._renderPanelMessage('—', 'normal');
        this._quotaSection.removeAll();
        this._quotaSection.addMenuItem(this._labelItem(_('Loading quotas…')));
    }

    _renderOffline() {
        this.actor.remove_style_class_name('quotadeck-reconnecting');
        this._renderPanelMessage('offline', 'offline');
        this.set_applet_tooltip(_('QuotaDeck local service is unavailable'));
        this._quotaSection.removeAll();
        this._quotaSection.addMenuItem(this._labelItem(_('QuotaDeck service is offline')));
        this._quotaSection.addMenuItem(this._labelItem(_('The applet will retry automatically.')));
        this._quotaSection.addMenuItem(new PopupMenu.PopupSeparatorMenuItem());
        this._addActions();
    }

    _renderReconnecting() {
        this.actor.add_style_class_name('quotadeck-reconnecting');
        this.set_applet_tooltip(_('QuotaDeck refresh delayed · retrying…'));
    }

    _renderState(state) {
        const accounts = Array.isArray(state.accounts) ? state.accounts : [];
        this._refreshSettingsOptions(accounts);
        const indicators = this._selectIndicators(accounts);
        this._renderPanelIndicators(indicators);
        const summaries = indicators.length === 0
            ? [_('No actionable quota window')]
            : indicators.map(indicator => this._indicatorSummary(indicator));
        this.set_applet_tooltip(['QuotaDeck', ...summaries].join('\n'));

        this._renderMenu(accounts);
    }

    _renderMenu(accounts) {
        this._quotaSection.removeAll();
        this._quotaSection.addMenuItem(this._labelItem(_('QuotaDeck · local quotas'), 'quotadeck-menu-title'));
        this._quotaSection.addMenuItem(this._labelItem(_('Selected Claude Code plan'), 'quotadeck-plan-heading'));
        this._quotaSection.addMenuItem(this._labelItem('  ' + this._selectedPlanLabel(accounts), 'quotadeck-selected-plan'));
        this._quotaSection.addMenuItem(new PopupMenu.PopupSeparatorMenuItem());
        if (accounts.length === 0) {
            this._quotaSection.addMenuItem(this._labelItem(_('No account detected yet')));
        }
        accounts.forEach(item => this._addAccount(item));
        this._quotaSection.addMenuItem(new PopupMenu.PopupSeparatorMenuItem());
        this._addActions();
    }

    _selectedPlanLabel(accounts) {
        if (!this._control) {
            return _('Detecting…');
        }
        if (this._control.mode === 'zai') {
            return 'Z.ai · GLM Coding Plan';
        }
        if (this._control.mode === 'claude') {
            const activeId = ((this._control.claude || {}).activeAccountId);
            const selected = accounts.find(item => ((item || {}).account || {}).id === activeId);
            if (selected) {
                const account = selected.account || {};
                return [account.label || activeId, account.plan].filter(Boolean).join(' · ');
            }
            return activeId || _('Claude subscription');
        }
        return _('No plan detected');
    }

    _renderPanelMessage(text, level) {
        this._indicatorBox.destroy_all_children();
        this._indicatorBox.add_actor(this._indicatorActor(null, text, level));
    }

    _renderPanelIndicators(indicators) {
        this._indicatorBox.destroy_all_children();
        if (indicators.length === 0) {
            this._indicatorBox.add_actor(this._indicatorActor(null, '—', 'normal'));
            return;
        }
        indicators.forEach(indicator => {
            const provider = ((indicator.item || {}).account || {}).providerId;
            const text = indicator.used === null ? '—' : Math.round(indicator.used) + '%';
            this._indicatorBox.add_actor(this._indicatorActor(provider, text, this._indicatorLevel(indicator)));
        });
    }

    _indicatorActor(provider, text, level) {
        const supported = ['claude', 'codex', 'zai'];
        const filename = supported.includes(provider)
            ? 'icon-' + provider + '-symbolic.svg'
            : 'icon-symbolic.svg';
        const box = new St.BoxLayout({ style_class: 'quotadeck-indicator quotadeck-' + level });
        const icon = new St.Icon({
            gicon: new Gio.FileIcon({
                file: Gio.File.new_for_path(GLib.build_filenamev([this._metadata.path, filename])),
            }),
            icon_type: St.IconType.SYMBOLIC,
            icon_size: this._panelIconSize,
            style_class: 'quotadeck-provider-icon',
        });
        const label = new St.Label({
            text,
            y_align: Clutter.ActorAlign.CENTER,
            style_class: 'applet-label quotadeck-indicator-value',
        });
        box.add_actor(icon);
        box.add_actor(label);
        return box;
    }

    _indicatorLevel(indicator) {
        if (Boolean(((indicator.item || {}).snapshot || {}).stale)) {
            return 'offline';
        }
        if (indicator.used === null) {
            return 'normal';
        }
        return indicator.used >= 90 ? 'critical' : indicator.used >= 75 ? 'warning' : 'normal';
    }

    _onDisplaySelectionChanged() {
        if (this._state) {
            this._renderState(this._state);
        }
    }

    _refreshSettingsOptions(accounts) {
        const options = {};
        options[_('Automatic — tightest available')] = 'auto';
        accounts.forEach(item => {
            const account = item.account || {};
            if (!account.id) {
                return;
            }
            const provider = PROVIDER_LABELS[account.providerId] || account.providerId || _('Provider');
            const accountLabel = account.label || account.id;
            this._addOption(
                options,
                provider + ' · ' + accountLabel + ' · ' + _('Tightest indicator'),
                this._encodeSelector(account.id, 'auto')
            );
            const windows = Array.isArray((item.snapshot || {}).windows) ? item.snapshot.windows : [];
            windows.forEach(window => {
                if (window.id) {
                    this._addOption(
                        options,
                        provider + ' · ' + accountLabel + ' · ' + (window.label || window.id),
                        this._encodeSelector(account.id, window.id)
                    );
                }
            });
        });
        const rows = Array.isArray(this.displayIndicators) ? this.displayIndicators : [];
        rows.forEach(row => {
            const value = row && row.indicator;
            if (value && !Object.values(options).includes(value)) {
                this._addOption(options, _('Unavailable indicator') + ' · ' + value, value);
            }
        });
        this._setIndicatorOptions(options);
        if (rows.length === 0) {
            const legacyAccount = this._settings.getValue('display-account') || 'auto';
            const legacyWindow = this._settings.getValue('display-window') || 'auto';
            const legacySelector = legacyAccount === 'auto'
                ? 'auto'
                : this._encodeSelector(legacyAccount, legacyWindow);
            this._settings.setValue('display-indicators', [{ indicator: legacySelector }]);
        }
    }

    _setIndicatorOptions(options) {
        const setting = this._settings.settingsData['display-indicators'];
        const column = setting && setting.columns.find(item => item.id === 'indicator');
        if (!column || JSON.stringify(column.options) === JSON.stringify(options)) {
            return;
        }
        column.options = options;
        // Cinnamon has no public API for options nested inside a list column.
        this._settings._saveToFile();
    }

    _addOption(options, label, value) {
        let candidate = label;
        let suffix = 2;
        while (Object.prototype.hasOwnProperty.call(options, candidate)) {
            candidate = label + ' (' + suffix + ')';
            suffix += 1;
        }
        options[candidate] = value;
    }

    _encodeSelector(accountId, windowId) {
        return JSON.stringify([accountId, windowId]);
    }

    _decodeSelector(value) {
        try {
            const parsed = JSON.parse(value);
            return Array.isArray(parsed) && parsed.length === 2 ? parsed : null;
        } catch (error) {
            return null;
        }
    }

    _selectIndicators(accounts) {
        const rows = Array.isArray(this.displayIndicators) ? this.displayIndicators : [];
        const selectors = rows.length > 0
            ? rows.map(row => row && row.indicator).filter(Boolean)
            : ['auto'];
        const seen = new Set();
        const indicators = [];
        selectors.forEach(selector => {
            const indicator = this._resolveIndicator(accounts, selector);
            if (!indicator) {
                return;
            }
            const key = this._encodeSelector((indicator.item.account || {}).id, indicator.window.id);
            if (!seen.has(key)) {
                seen.add(key);
                indicators.push(indicator);
            }
        });
        return indicators;
    }

    _resolveIndicator(accounts, selector) {
        if (selector === 'auto') {
            return this._tightestIndicator(accounts);
        }
        const decoded = this._decodeSelector(selector);
        if (!decoded) {
            return null;
        }
        const [accountId, windowId] = decoded;
        const item = accounts.find(candidate => ((candidate || {}).account || {}).id === accountId);
        if (!item) {
            return null;
        }
        if (windowId === 'auto') {
            return this._tightestIndicator([item]);
        }
        const windows = Array.isArray((item.snapshot || {}).windows) ? item.snapshot.windows : [];
        const window = windows.find(candidate => candidate.id === windowId);
        return window ? { item, window, used: this._usedPercent(window) } : null;
    }

    _tightestIndicator(items) {
        let best = null;
        items.forEach(item => {
            const windows = Array.isArray((item.snapshot || {}).windows)
                ? item.snapshot.windows
                : [];
            windows.forEach(window => {
                const used = this._usedPercent(window);
                if (used !== null && (best === null || used > best.used)) {
                    best = { item, window, used };
                }
            });
        });
        return best;
    }

    _indicatorSummary(indicator) {
        const account = indicator.item.account || {};
        const plan = account.plan || account.label || account.providerId;
        const windowLabel = indicator.window.label || indicator.window.id;
        const details = [plan, windowLabel].filter(Boolean).join(' · ');
        const used = indicator.used === null ? _('No percentage') : Math.round(indicator.used) + _('% used');
        return details ? used + ' · ' + details : used;
    }

    _addAccount(item) {
        const account = item.account || {};
        const snapshot = item.snapshot || {};
        const provider = account.providerId || 'provider';
        const status = snapshot.status || 'unknown';
        this._quotaSection.addMenuItem(this._labelItem(
            (account.label || provider) + '  ·  ' + status,
            'quotadeck-account-title'
        ));

        const windows = Array.isArray(snapshot.windows) ? snapshot.windows : [];
        if (windows.length === 0) {
            this._quotaSection.addMenuItem(this._labelItem('  ' + _('No quota window'), 'quotadeck-window-muted'));
            return;
        }
        windows.forEach(window => {
            const used = this._usedPercent(window);
            const percentage = used === null ? '—' : Math.round(used) + '%';
            const reset = window.resetsAt ? this._formatReset(window.resetsAt) : _('no reset');
            this._quotaSection.addMenuItem(this._labelItem(
                '  ' + (window.label || _('Window')) + ':  ' + percentage + '  ·  ' + reset,
                'quotadeck-window'
            ));
        });
    }

    _addActions() {
        const open = new PopupMenu.PopupIconMenuItem(_('Open QuotaDeck'), 'quotadeck', St.IconType.SYMBOLIC);
        open.connect('activate', () => this._openDashboard());
        this._quotaSection.addMenuItem(open);

        const refresh = new PopupMenu.PopupIconMenuItem(_('Refresh now'), 'view-refresh-symbolic', St.IconType.SYMBOLIC);
        refresh.connect('activate', () => {
            this._request('POST', '/api/v1/refresh', { 'X-QuotaDeck-Request': 'refresh' }, () => this._loadState());
        });
        this._quotaSection.addMenuItem(refresh);

        const configure = new PopupMenu.PopupIconMenuItem(_('Configure panel indicators'), 'preferences-system-symbolic', St.IconType.SYMBOLIC);
        configure.connect('activate', () => this.configureApplet());
        this._quotaSection.addMenuItem(configure);
    }

    _openDashboard() {
        try {
            const desktop = GLib.find_program_in_path('quotadeck-desktop');
            if (desktop) {
                Gio.Subprocess.new([desktop], Gio.SubprocessFlags.NONE);
                return;
            }
            Gio.AppInfo.launch_default_for_uri(API_ROOT + '/', null);
        } catch (error) {
            global.logError('QuotaDeck: could not open dashboard: ' + error.message);
        }
    }

    _labelItem(text, styleClass) {
        const item = new PopupMenu.PopupMenuItem(text, { reactive: false });
        if (styleClass) {
            item.actor.add_style_class_name(styleClass);
        }
        return item;
    }

    _usedPercent(window) {
        if (typeof window.usedPercent === 'number') {
            return Math.max(0, Math.min(100, window.usedPercent));
        }
        if (typeof window.remainingPercent === 'number') {
            return Math.max(0, Math.min(100, 100 - window.remainingPercent));
        }
        if (typeof window.used === 'number' && typeof window.limit === 'number' && window.limit > 0) {
            return Math.max(0, Math.min(100, window.used / window.limit * 100));
        }
        return null;
    }

    _formatReset(raw) {
        const reset = new Date(raw);
        if (Number.isNaN(reset.getTime())) {
            return _('reset unknown');
        }
        const seconds = Math.max(0, Math.round((reset.getTime() - Date.now()) / 1000));
        if (seconds < 3600) {
            return _('resets in ') + Math.max(1, Math.round(seconds / 60)) + 'm';
        }
        if (seconds < 86400) {
            return _('resets in ') + Math.round(seconds / 3600) + 'h';
        }
        return _('resets in ') + Math.round(seconds / 86400) + 'd';
    }

}

function main(metadata, orientation, panelHeight, instanceId) {
    return new QuotaDeckApplet(metadata, orientation, panelHeight, instanceId);
}
