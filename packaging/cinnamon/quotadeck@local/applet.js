imports.gi.versions.Soup = '3.0';

const Applet = imports.ui.applet;
const ByteArray = imports.byteArray;
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

class QuotaDeckApplet extends Applet.TextIconApplet {
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

        this.set_applet_icon_symbolic_path(GLib.build_filenamev([metadata.path, 'icon-symbolic.svg']));
        this.set_applet_label('—');
        this.set_applet_tooltip(_('QuotaDeck is connecting to the local service'));
        this.actor.add_style_class_name('quotadeck-applet');

        this.menuManager = new PopupMenu.PopupMenuManager(this);
        this.menu = new Applet.AppletPopupMenu(this, orientation);
        this.menuManager.addMenu(this.menu);

        this._settings = new Settings.AppletSettings(this, metadata.uuid, instanceId);
        this._settings.bind('display-account', 'displayAccountId', () => this._onDisplaySelectionChanged());
        this._settings.bind('display-window', 'displayWindowId', () => this._onDisplaySelectionChanged());
        this._lastDisplayAccountId = this.displayAccountId;

        this._renderLoading();
        this._loadState();
        this._timeoutId = Mainloop.timeout_add_seconds(POLL_SECONDS, () => {
            this._loadState();
            return true;
        });
    }

    on_applet_clicked() {
        this.menu.toggle();
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
        this.menu.removeAll();
        this.menu.addMenuItem(this._labelItem(_('Loading quotas…')));
    }

    _renderOffline() {
        this.actor.remove_style_class_name('quotadeck-reconnecting');
        this.set_applet_label('offline');
        this.set_applet_tooltip(_('QuotaDeck local service is unavailable'));
        this._setLevel('offline');
        this.menu.removeAll();
        this.menu.addMenuItem(this._labelItem(_('QuotaDeck service is offline')));
        this.menu.addMenuItem(this._labelItem(_('The applet will retry automatically.')));
        this.menu.addMenuItem(new PopupMenu.PopupSeparatorMenuItem());
        this._addActions();
    }

    _renderReconnecting() {
        this.actor.add_style_class_name('quotadeck-reconnecting');
        this.set_applet_tooltip(_('QuotaDeck refresh delayed · retrying…'));
    }

    _renderState(state) {
        const accounts = Array.isArray(state.accounts) ? state.accounts : [];
        this._refreshSettingsOptions(accounts);
        const indicator = this._selectIndicator(accounts);
        const selectedItems = this._selectedAccounts(accounts);
        const iconItem = indicator === null && selectedItems.length === 1 ? selectedItems[0] : indicator?.item;
        this._setProviderIcon(iconItem);

        if (indicator === null) {
            this.set_applet_label('—');
            const stale = selectedItems.some(item => Boolean((item.snapshot || {}).stale));
            this._setLevel(stale ? 'offline' : 'normal');
        } else {
            this.set_applet_label(Math.round(indicator.used) + '%');
            this._setLevel(indicator.used >= 90 ? 'critical' : indicator.used >= 75 ? 'warning' : 'normal');
        }
        const summary = indicator === null
            ? _('No actionable quota window')
            : this._indicatorSummary(indicator);
        this.set_applet_tooltip('QuotaDeck · ' + summary);

        this.menu.removeAll();
        this.menu.addMenuItem(this._labelItem(_('QuotaDeck · local quotas'), 'quotadeck-menu-title'));
        if (accounts.length === 0) {
            this.menu.addMenuItem(this._labelItem(_('No account detected yet')));
        }
        accounts.forEach(item => this._addAccount(item));
        this.menu.addMenuItem(new PopupMenu.PopupSeparatorMenuItem());
        this._addActions();
    }

    _setProviderIcon(item) {
        const provider = ((item || {}).account || {}).providerId;
        const supported = ['claude', 'codex', 'zai'];
        const filename = supported.includes(provider)
            ? 'icon-' + provider + '-symbolic.svg'
            : 'icon-symbolic.svg';
        this.set_applet_icon_symbolic_path(GLib.build_filenamev([this._metadata.path, filename]));
    }

    _onDisplaySelectionChanged() {
        if (this.displayAccountId !== this._lastDisplayAccountId) {
            this._lastDisplayAccountId = this.displayAccountId;
            this.displayWindowId = 'auto';
        }
        if (this._state) {
            this._renderState(this._state);
        }
    }

    _refreshSettingsOptions(accounts) {
        const accountOptions = {};
        accountOptions[_('Automatic — tightest plan')] = 'auto';
        accounts.forEach(item => {
            const account = item.account || {};
            if (!account.id) {
                return;
            }
            const details = account.plan || account.providerId || _('unknown plan');
            this._addOption(accountOptions, (account.label || account.id) + ' · ' + details, account.id);
        });
        if (this.displayAccountId !== 'auto' && !this._hasOptionValue(accountOptions, this.displayAccountId)) {
            this._addOption(accountOptions, _('Unavailable account') + ' · ' + this.displayAccountId, this.displayAccountId);
        }
        this._setOptions('display-account', accountOptions);

        const windowOptions = {};
        windowOptions[_('Automatic — tightest indicator')] = 'auto';
        const selected = accounts.find(item => (item.account || {}).id === this.displayAccountId);
        if (selected) {
            const windows = Array.isArray((selected.snapshot || {}).windows)
                ? selected.snapshot.windows
                : [];
            windows.forEach(window => {
                if (window.id) {
                    this._addOption(windowOptions, window.label || window.id, window.id);
                }
            });
        }
        if (this.displayWindowId !== 'auto' && !this._hasOptionValue(windowOptions, this.displayWindowId)) {
            this._addOption(windowOptions, _('Unavailable indicator') + ' · ' + this.displayWindowId, this.displayWindowId);
        }
        this._setOptions('display-window', windowOptions);
    }

    _hasOptionValue(options, value) {
        return Object.keys(options).some(label => options[label] === value);
    }

    _setOptions(key, options) {
        const current = this._settings.getOptions(key);
        if (JSON.stringify(current) !== JSON.stringify(options)) {
            this._settings.setOptions(key, options);
        }
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

    _selectedAccounts(accounts) {
        if (!this.displayAccountId || this.displayAccountId === 'auto') {
            return accounts;
        }
        return accounts.filter(item => (item.account || {}).id === this.displayAccountId);
    }

    _selectIndicator(accounts) {
        const selectedItems = this._selectedAccounts(accounts);
        let best = null;
        selectedItems.forEach(item => {
            const windows = Array.isArray((item.snapshot || {}).windows)
                ? item.snapshot.windows
                : [];
            windows.forEach(window => {
                if (this.displayAccountId !== 'auto'
                    && this.displayWindowId
                    && this.displayWindowId !== 'auto'
                    && window.id !== this.displayWindowId) {
                    return;
                }
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
        const used = Math.round(indicator.used) + _('% used');
        return details ? used + ' · ' + details : used;
    }

    _addAccount(item) {
        const account = item.account || {};
        const snapshot = item.snapshot || {};
        const provider = account.providerId || 'provider';
        const status = snapshot.status || 'unknown';
        this.menu.addMenuItem(this._labelItem(
            (account.label || provider) + '  ·  ' + status,
            'quotadeck-account-title'
        ));

        const windows = Array.isArray(snapshot.windows) ? snapshot.windows : [];
        if (windows.length === 0) {
            this.menu.addMenuItem(this._labelItem('  ' + _('No quota window'), 'quotadeck-window-muted'));
            return;
        }
        windows.forEach(window => {
            const used = this._usedPercent(window);
            const percentage = used === null ? '—' : Math.round(used) + '%';
            const reset = window.resetsAt ? this._formatReset(window.resetsAt) : _('no reset');
            this.menu.addMenuItem(this._labelItem(
                '  ' + (window.label || _('Window')) + ':  ' + percentage + '  ·  ' + reset,
                'quotadeck-window'
            ));
        });
    }

    _addActions() {
        const open = new PopupMenu.PopupIconMenuItem(_('Open QuotaDeck'), 'quotadeck', St.IconType.SYMBOLIC);
        open.connect('activate', () => this._openDashboard());
        this.menu.addMenuItem(open);

        const refresh = new PopupMenu.PopupIconMenuItem(_('Refresh now'), 'view-refresh-symbolic', St.IconType.SYMBOLIC);
        refresh.connect('activate', () => {
            this._request('POST', '/api/v1/refresh', { 'X-QuotaDeck-Request': 'refresh' }, () => this._loadState());
        });
        this.menu.addMenuItem(refresh);

        const configure = new PopupMenu.PopupIconMenuItem(_('Configure panel indicator'), 'preferences-system-symbolic', St.IconType.SYMBOLIC);
        configure.connect('activate', () => this.configureApplet());
        this.menu.addMenuItem(configure);
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

    _setLevel(level) {
        ['normal', 'warning', 'critical', 'offline'].forEach(name => {
            this.actor.remove_style_class_name('quotadeck-' + name);
        });
        this.actor.add_style_class_name('quotadeck-' + level);
    }
}

function main(metadata, orientation, panelHeight, instanceId) {
    return new QuotaDeckApplet(metadata, orientation, panelHeight, instanceId);
}
