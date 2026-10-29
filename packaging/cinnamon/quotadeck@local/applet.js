const Applet = imports.ui.applet;
const Clutter = imports.gi.Clutter;
const Gio = imports.gi.Gio;
const GLib = imports.gi.GLib;
const Mainloop = imports.mainloop;
const PopupMenu = imports.ui.popupMenu;
const Settings = imports.ui.settings;
const St = imports.gi.St;

const API_ROOT = 'http://127.0.0.1:9211';
const POLL_SECONDS = 60;
const RETRY_SECONDS = 5;
const OFFLINE_FAILURE_THRESHOLD = 3;
const SERVICE_START_COOLDOWN_SECONDS = 30;
const PROVIDER_LABELS = { claude: 'Claude', codex: 'Codex', zai: 'Z.ai', kimi: 'Kimi' };

class QuotaDeckApplet extends Applet.Applet {
    constructor(metadata, orientation, panelHeight, instanceId) {
        super(orientation, panelHeight, instanceId);

        this._metadata = metadata;
        this._requests = new Set();
        this._cancellable = new Gio.Cancellable();
        this._destroyed = false;
        this._renderSourceId = 0;
        this._displayStatus = 'loading';
        this._panelActors = [];
        this._menuRows = [];
        this._timeoutId = 0;
        this._retryId = 0;
        this._requestInFlight = false;
        this._requestToken = 0;
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
        this._menuStateChangedId = this.menu.connect('open-state-changed', (menu, isOpen) => {
            if (isOpen && !this._destroyed) {
                this._renderVisibleMenu();
            }
        });

        this._migrateLegacySettings(metadata.uuid, instanceId);
        this._settings = new Settings.AppletSettings(this, metadata.uuid, instanceId);
        this._settings.bind('display-indicators', 'displayIndicators', () => this._onDisplaySelectionChanged());
        this._settings.bind('threshold-alert-enabled', 'thresholdAlertEnabled', () => this._onDisplaySelectionChanged());
        this._settings.bind('threshold-alert-percent', 'thresholdAlertPercent', () => this._onDisplaySelectionChanged());

        this._renderLoading();
        this._loadState();
        this._timeoutId = Mainloop.timeout_add_seconds(POLL_SECONDS, () => {
            // Recover the slot if a completion was lost by the shell. HTTP
            // requests have a shorter, independent process timeout.
            this._requestInFlight = false;
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
        this._scheduleRender();
    }

    on_applet_removed_from_panel() {
        if (this._destroyed) {
            return;
        }
        // Cancellation may synchronously complete requests: mark disposed first.
        this._destroyed = true;
        for (const key of ['_timeoutId', '_retryId', '_renderSourceId']) {
            if (this[key]) {
                Mainloop.source_remove(this[key]);
                this[key] = 0;
            }
        }
        this.menu.disconnect(this._menuStateChangedId);
        this._cancellable.cancel();
        this._requests.forEach(process => process.force_exit());
        this._requests.clear();
        this._settings.finalize();
        // Keep JS wrappers alive until their native actors have been destroyed.
        // Otherwise their destroy callbacks can run later, during CJS GC.
        this._quotaSection.destroy();
        this._menuRows = [];
        this._panelActors.forEach(actor => actor.destroy());
        this._panelActors = [];
        this._indicatorBox.destroy();
    }

    _scheduleRender() {
        if (this._destroyed || !this._displayStatus || this._renderSourceId) {
            return;
        }
        // Coalesce settings/network updates outside their completion callbacks.
        this._renderSourceId = Mainloop.idle_add(() => {
            this._renderSourceId = 0;
            if (this._destroyed) {
                return false;
            }
            if (this._displayStatus === 'offline') {
                this._renderOffline();
            } else if (this._displayStatus === 'reconnecting') {
                this._renderReconnecting();
            } else if (this._state) {
                this._renderState(this._state);
            }
            return false;
        });
    }

    _loadState() {
        if (this._destroyed || this._requestInFlight) {
            return;
        }
        this._requestInFlight = true;
        const token = ++this._requestToken;
        this._request('GET', '/api/v1/state', null, (error, state) => {
            // A completion the poll cycle already gave up on owns nothing.
            if (token !== this._requestToken) {
                return;
            }
            this._requestInFlight = false;
            if (error) {
                this._consecutiveFailures += 1;
                this._displayStatus = !this._state || this._consecutiveFailures >= OFFLINE_FAILURE_THRESHOLD
                    ? 'offline' : 'reconnecting';
                this._scheduleRender();
                this._startServiceOnce();
                this._scheduleRetry();
                return;
            }
            this._consecutiveFailures = 0;
            this._serviceStartAttemptedAt = 0;
            if (this._retryId) {
                Mainloop.source_remove(this._retryId);
                this._retryId = 0;
            }
            this._state = state;
            this._displayStatus = 'ready';
            this._scheduleRender();
            this._loadControl();
        });
    }

    _loadControl() {
        this._request('GET', '/api/v1/control', null, (error, control) => {
            if (error) {
                return;
            }
            this._control = control;
            this._scheduleRender();
        });
    }

    _scheduleRetry() {
        if (this._destroyed || this._retryId) {
            return;
        }
        this._retryId = Mainloop.timeout_add_seconds(RETRY_SECONDS, () => {
            this._retryId = 0;
            this._loadState();
            return false;
        });
    }

    _request(method, path, headers, callback) {
        if (this._destroyed) {
            return;
        }
        // Keep native HTTP outside Cinnamon: libsoup 3.4 has crashed the entire
        // shell while completing local responses. Each curl
        // process owns its connection and deadline; failures become JS errors.
        const argv = [
            'curl', '--disable', '--silent', '--show-error', '--noproxy', '*',
            '--connect-timeout', '3', '--max-time', '12',
            '--write-out', '\n%{http_code}', '--request', method,
        ];
        if (headers) {
            Object.keys(headers).forEach(name => argv.push('--header', name + ': ' + headers[name]));
        }
        argv.push(API_ROOT + path);
        let process;
        try {
            process = Gio.Subprocess.new(argv, Gio.SubprocessFlags.STDOUT_PIPE | Gio.SubprocessFlags.STDERR_PIPE);
        } catch (error) {
            callback(error, null);
            return;
        }
        this._requests.add(process);
        process.communicate_utf8_async(null, this._cancellable, (process, result) => {
            let error = null;
            let data = null;
            try {
                const [, stdout, stderr] = process.communicate_utf8_finish(result);
                if (!process.get_successful()) {
                    throw new Error((stderr || '').trim() || 'QuotaDeck HTTP request failed');
                }
                const boundary = stdout.lastIndexOf('\n');
                const status = Number(stdout.slice(boundary + 1));
                if (boundary < 0 || !Number.isInteger(status) || status < 200 || status >= 300) {
                    throw new Error('HTTP ' + status);
                }
                const raw = stdout.slice(0, boundary);
                data = raw ? JSON.parse(raw) : {};
            } catch (requestError) {
                error = requestError;
            } finally {
                this._requests.delete(process);
            }
            if (!this._destroyed) {
                // A consumer error is not a network error: never call it twice.
                callback(error, data);
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
    }

    _renderOffline() {
        this.actor.remove_style_class_name('quotadeck-reconnecting');
        this._renderPanelMessage('offline', 'offline');
        this.set_applet_tooltip(_('QuotaDeck local service is unavailable'));
        if (this.menu.isOpen) {
            this._renderVisibleMenu();
        }
    }

    _renderReconnecting() {
        this.actor.add_style_class_name('quotadeck-reconnecting');
        this.set_applet_tooltip(_('QuotaDeck refresh delayed · retrying…'));
    }

    _renderState(state) {
        this.actor.remove_style_class_name('quotadeck-reconnecting');
        const accounts = Array.isArray(state.accounts) ? state.accounts : [];
        this._refreshSettingsOptions(accounts);
        const indicators = this._selectIndicators(accounts);
        const selected = accounts.find(item => this._isSelectedClaude(item));
        // Keep a blocked active plan visible even when only other quotas are configured.
        if (selected && this._blockedWindow(selected) && !indicators.some(indicator => indicator.item === selected)) {
            const window = this._blockedWindow(selected);
            indicators.push({ item: selected, window, used: this._usedPercent(window) });
        }
        this._renderPanelIndicators(indicators);
        const summaries = indicators.length === 0
            ? [_('No actionable quota window')]
            : indicators.map(indicator => this._indicatorSummary(indicator));
        this.set_applet_tooltip(['QuotaDeck', ...summaries].join('\n'));

        if (this.menu.isOpen) {
            this._renderMenu(accounts);
        }
    }

    _renderVisibleMenu() {
        if (this._displayStatus === 'offline') {
            this._setMenuRows([
                { text: _('QuotaDeck service is offline') },
                { text: _('The applet will retry automatically.') },
                { kind: 'separator' },
                ...this._actionRows(),
            ]);
        } else if (this._state) {
            this._renderMenu(Array.isArray(this._state.accounts) ? this._state.accounts : []);
        } else {
            this._setMenuRows([{ text: _('Loading quotas…') }]);
        }
    }

    _renderMenu(accounts) {
        const rows = [
            { text: _('QuotaDeck · local quotas'), styleClass: 'quotadeck-menu-title' },
            { text: _('Selected Claude Code plan'), styleClass: 'quotadeck-plan-heading' },
            { text: '  ' + this._selectedPlanLabel(accounts), styleClass: 'quotadeck-selected-plan' },
            { kind: 'separator' },
        ];
        if (accounts.length === 0) {
            rows.push({ text: _('No account detected yet') });
        }
        accounts.forEach(item => rows.push(...this._accountRows(item)));
        rows.push({ kind: 'separator' }, ...this._actionRows());
        this._setMenuRows(rows);
    }

    _setMenuRows(rows) {
        rows.forEach((row, index) => {
            let entry = this._menuRows[index];
            if (!entry || entry.kind !== row.kind || entry.id !== row.id) {
                if (entry) {
                    entry.item.destroy();
                }
                const item = row.kind === 'separator' ? new PopupMenu.PopupSeparatorMenuItem()
                    : row.kind === 'action' ? new PopupMenu.PopupIconMenuItem(row.text, row.icon, St.IconType.SYMBOLIC)
                    : this._labelItem(row.text);
                if (row.activate) {
                    item.connect('activate', row.activate);
                }
                this._quotaSection.addMenuItem(item, index);
                entry = { kind: row.kind, id: row.id, item };
                this._menuRows[index] = entry;
            }
            if (row.text !== undefined && entry.item.label.text !== row.text) {
                entry.item.label.set_text(row.text);
            }
            if (entry.styleClass !== row.styleClass) {
                if (entry.styleClass) {
                    entry.item.actor.remove_style_class_name(entry.styleClass);
                }
                if (row.styleClass) {
                    entry.item.actor.add_style_class_name(row.styleClass);
                }
                entry.styleClass = row.styleClass;
            }
        });
        this._menuRows.splice(rows.length).forEach(entry => entry.item.destroy());
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
        this._renderPanelItems([{ provider: null, text, level }]);
    }

    _renderPanelIndicators(indicators) {
        if (indicators.length === 0) {
            this._renderPanelMessage('—', 'normal');
            return;
        }
        this._renderPanelItems(indicators.map(indicator => ({
            provider: ((indicator.item || {}).account || {}).providerId,
            text: indicator.used === null ? '—' : Math.round(indicator.used) + '%',
            level: this._indicatorLevel(indicator), indicator,
        })));
    }

    _renderPanelItems(items) {
        items.forEach(({ provider, text, level, indicator }, index) => {
            if (this._panelActors[index]) {
                this._updateIndicatorActor(this._panelActors[index], provider, text, level, indicator);
            } else {
                const actor = this._indicatorActor(provider, text, level, indicator);
                this._panelActors.push(actor);
                this._indicatorBox.add_actor(actor);
            }
        });
        this._panelActors.splice(items.length).forEach(actor => actor.destroy());
    }

    _indicatorActor(provider, text, level, indicator = null) {
        const box = new St.BoxLayout({ y_align: Clutter.ActorAlign.CENTER });
        const icon = new St.Icon();
        const label = new St.Label({ y_align: Clutter.ActorAlign.CENTER, style_class: 'applet-label quotadeck-indicator-value' });
        box.add_actor(icon);
        box.add_actor(label);
        box._quotaView = { icon, label, alert: null, spec: {} };
        this._updateIndicatorActor(box, provider, text, level, indicator);
        return box;
    }

    _updateIndicatorActor(box, provider, text, level, indicator = null) {
        const supported = ['claude', 'codex', 'zai', 'kimi'];
        const blocked = indicator && this._blockedWindow(indicator.item);
        const filename = blocked ? 'icon-claude-blocked.svg' : supported.includes(provider)
            ? 'icon-' + provider + '.svg'
            : 'icon-symbolic.svg';
        const selected = indicator && this._isSelectedClaude(indicator.item);
        const spec = {
            style_class: 'quotadeck-indicator quotadeck-' + level + (selected ? ' quotadeck-selected-indicator' : ''),
            style: 'border: 2px solid ' + (selected ? '#7ab7ff' : 'transparent')
                + '; border-radius: 6px; padding: 2px 4px;'
                + (selected ? ' background-color: #253b50;' : ''),
            filename, text, icon_type: supported.includes(provider) ? St.IconType.FULLCOLOR : St.IconType.SYMBOLIC,
            icon_size: this._panelIconSize,
            iconClass: 'quotadeck-provider-icon quotadeck-provider-' + (provider || 'default'),
            alert: Boolean(!blocked && indicator && this._thresholdReached(indicator)),
        };
        const view = box._quotaView;
        for (const key of ['style_class', 'style']) {
            if (view.spec[key] !== spec[key]) {
                box[key] = spec[key];
            }
        }
        if (view.spec.filename !== filename) {
            view.icon.gicon = new Gio.FileIcon({
                file: Gio.File.new_for_path(GLib.build_filenamev([this._metadata.path, filename])),
            });
        }
        for (const key of ['icon_type', 'icon_size']) {
            if (view.spec[key] !== spec[key]) {
                view.icon[key] = spec[key];
            }
        }
        if (view.spec.iconClass !== spec.iconClass) {
            view.icon.style_class = spec.iconClass;
        }
        if (view.spec.text !== text) {
            view.label.set_text(text);
        }
        if (spec.alert && !view.alert) {
            view.alert = new St.Icon({
                icon_name: 'dialog-warning-symbolic',
                icon_type: St.IconType.SYMBOLIC,
                icon_size: this._panelIconSize,
                style_class: 'quotadeck-alert-icon',
            });
            box.add_actor(view.alert);
        } else if (!spec.alert && view.alert) {
            view.alert.destroy();
            view.alert = null;
        }
        if (view.alert && view.spec.icon_size !== spec.icon_size) {
            view.alert.icon_size = spec.icon_size;
        }
        view.spec = spec;
    }

    _isSelectedClaude(item) {
        const account = (item || {}).account || {};
        return Boolean(this._control && this._control.mode === 'claude'
            && account.providerId === 'claude' && account.id
            && account.id === (this._control.claude || {}).activeAccountId);
    }

    _blockedWindow(item) {
        const snapshot = (item || {}).snapshot || {};
        if (((item || {}).account || {}).providerId !== 'claude' || snapshot.stale) {
            return null;
        }
        return (Array.isArray(snapshot.windows) ? snapshot.windows : []).find(window => {
            if (window.id !== 'five-hour' || this._usedPercent(window) !== 100) {
                return false;
            }
            // A past reset must not leave a misleading red ring on the panel.
            return !window.resetsAt || new Date(window.resetsAt).getTime() > Date.now();
        }) || null;
    }

    _thresholdReached(indicator) {
        const threshold = Number.isFinite(this.thresholdAlertPercent)
            ? Math.max(1, Math.min(100, this.thresholdAlertPercent)) : 80;
        return this.thresholdAlertEnabled && !((indicator.item || {}).snapshot || {}).stale
            && indicator.used !== null && indicator.used >= threshold;
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
        this._scheduleRender();
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
        const notes = [details ? used + ' · ' + details : used];
        if (this._isSelectedClaude(indicator.item)) {
            notes.push(_('Selected Claude Code plan'));
        }
        const blocked = this._blockedWindow(indicator.item);
        if (blocked) {
            notes.push(_('Blocked by the 5-hour window') + (blocked.resetsAt ? ' · ' + this._formatReset(blocked.resetsAt) : ''));
        }
        if (this._thresholdReached(indicator)) {
            notes.push(_('Alert threshold reached'));
        }
        if ((indicator.item.snapshot || {}).stale) {
            notes.push(_('Stale quota data'));
        }
        return notes.join(' · ');
    }

    _accountRows(item) {
        const account = item.account || {};
        const snapshot = item.snapshot || {};
        const provider = account.providerId || 'provider';
        const status = snapshot.status || 'unknown';
        const rows = [{ text: (account.label || provider) + '  ·  ' + status, styleClass: 'quotadeck-account-title' }];

        const windows = Array.isArray(snapshot.windows) ? snapshot.windows : [];
        if (windows.length === 0) {
            rows.push({ text: '  ' + _('No quota window'), styleClass: 'quotadeck-window-muted' });
            return rows;
        }
        windows.forEach(window => {
            const used = this._usedPercent(window);
            const percentage = used === null ? '—' : Math.round(used) + '%';
            const reset = window.resetsAt ? this._formatReset(window.resetsAt) : _('no reset');
            rows.push({ text: '  ' + (window.label || _('Window')) + ':  ' + percentage + '  ·  ' + reset, styleClass: 'quotadeck-window' });
        });
        return rows;
    }

    _actionRows() {
        return [
            { kind: 'action', id: 'open', text: _('Open QuotaDeck'), icon: 'quotadeck', activate: () => this._openDashboard() },
            { kind: 'action', id: 'refresh', text: _('Refresh now'), icon: 'view-refresh-symbolic', activate: () => {
                this._request('POST', '/api/v1/refresh', { 'X-QuotaDeck-Request': 'refresh' }, () => this._loadState());
            } },
            { kind: 'action', id: 'configure', text: _('Configure panel indicators'), icon: 'preferences-system-symbolic', activate: () => this.configureApplet() },
        ];
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
        if (reset.getTime() <= Date.now()) {
            return _('reset time passed · awaiting refresh');
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
