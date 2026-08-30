imports.gi.versions.Soup = '3.0';

const Applet = imports.ui.applet;
const ByteArray = imports.byteArray;
const Gio = imports.gi.Gio;
const GLib = imports.gi.GLib;
const Mainloop = imports.mainloop;
const PopupMenu = imports.ui.popupMenu;
const Soup = imports.gi.Soup;
const St = imports.gi.St;

const API_ROOT = 'http://127.0.0.1:9211';
const POLL_SECONDS = 60;

class QuotaDeckApplet extends Applet.TextIconApplet {
    constructor(metadata, orientation, panelHeight, instanceId) {
        super(orientation, panelHeight, instanceId);

        this._metadata = metadata;
        this._session = new Soup.Session({ timeout: 12 });
        this._timeoutId = 0;
        this._serviceStartAttempted = false;

        this.set_applet_icon_path(GLib.build_filenamev([metadata.path, 'icon-symbolic.svg']));
        this.set_applet_label('—');
        this.set_applet_tooltip(_('QuotaDeck is connecting to the local service'));
        this.actor.add_style_class_name('quotadeck-applet');

        this.menuManager = new PopupMenu.PopupMenuManager(this);
        this.menu = new Applet.AppletPopupMenu(this, orientation);
        this.menuManager.addMenu(this.menu);

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
        this._session.abort();
    }

    _loadState() {
        this._request('GET', '/api/v1/state', null, (error, state) => {
            if (error) {
                this._renderOffline();
                this._startServiceOnce();
                return;
            }
            this._serviceStartAttempted = false;
            this._renderState(state);
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
        if (this._serviceStartAttempted) {
            return;
        }
        this._serviceStartAttempted = true;
        try {
            Gio.Subprocess.new(
                ['systemctl', '--user', 'start', 'quotadeck.service'],
                Gio.SubprocessFlags.NONE
            );
            Mainloop.timeout_add_seconds(3, () => {
                this._loadState();
                return false;
            });
        } catch (error) {
            global.logError('QuotaDeck: could not start user service: ' + error.message);
        }
    }

    _renderLoading() {
        this.menu.removeAll();
        this.menu.addMenuItem(this._labelItem(_('Loading quotas…')));
    }

    _renderOffline() {
        this.set_applet_label('offline');
        this.set_applet_tooltip(_('QuotaDeck local service is unavailable'));
        this._setLevel('offline');
        this.menu.removeAll();
        this.menu.addMenuItem(this._labelItem(_('QuotaDeck service is offline')));
        this.menu.addMenuItem(this._labelItem(_('The applet will retry automatically.')));
        this.menu.addMenuItem(new PopupMenu.PopupSeparatorMenuItem());
        this._addActions();
    }

    _renderState(state) {
        const accounts = Array.isArray(state.accounts) ? state.accounts : [];
        let tightest = null;
        let stale = false;

        accounts.forEach(item => {
            const snapshot = item.snapshot || {};
            stale = stale || Boolean(snapshot.stale);
            const windows = Array.isArray(snapshot.windows) ? snapshot.windows : [];
            windows.forEach(window => {
                const used = this._usedPercent(window);
                if (used !== null && (tightest === null || used > tightest)) {
                    tightest = used;
                }
            });
        });

        if (tightest === null) {
            this.set_applet_label('—');
            this._setLevel(stale ? 'offline' : 'normal');
        } else {
            this.set_applet_label(Math.round(tightest) + '%');
            this._setLevel(tightest >= 90 ? 'critical' : tightest >= 75 ? 'warning' : 'normal');
        }
        const summary = tightest === null
            ? _('No actionable quota window')
            : Math.round(tightest) + _('% used in the tightest window');
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
