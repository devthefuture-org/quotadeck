# Desktop and Cinnamon

The Debian package is the complete Linux experience: native Wails window, CLI/server, systemd user service, application launcher, and Cinnamon panel applet.

## Desktop application

Launch **QuotaDeck** from the application menu. The window starts or reuses the local service and loads the dashboard from `127.0.0.1`.

![QuotaDeck compact mobile layout using synthetic data](/screenshots/dashboard-mobile.png)

## Cinnamon applet

After installing the `.deb`:

1. Open **System Settings → Applets**.
2. Find **QuotaDeck**.
3. Add it to the panel.

The applet reads the loopback API and starts the packaged user service when necessary. Its menu summarizes every quota window and opens the full dashboard for details. In the applet settings, **Plan / account** selects the subscription to represent in the panel and **Indicator** selects one of its quota windows. Both default to automatic selection of the tightest available window.

Use **Configure panel indicators** and press **+** to display as many indicators as needed inside one applet. Each row selects an account and quota window, can be reordered independently, and renders a Claude, Codex, or Z.ai provider glyph beside its percentage. Multiple applet instances remain available when separate panel groups are useful. Left-click the applet to open QuotaDeck; right-click to open its quota menu, which also identifies the Claude Code plan currently selected. A transient local API error keeps the last values visible while the applet retries; `offline` appears only after repeated failures.

To install only the applet from a source checkout:

```bash
make package-cinnamon
```

Install the generated zip through Cinnamon's applet settings.

When switching from a manually installed zip to the Debian package, remove or rename the user copy in `~/.local/share/cinnamon/applets/quotadeck@local`; Cinnamon gives it priority over `/usr/share/cinnamon/applets/quotadeck@local`. QuotaDeck migrates legacy single-instance settings to the active numeric instance automatically when the packaged applet first loads.

## User service

```bash
quotadeck service install --user
quotadeck service status
quotadeck service uninstall --user
```

The package installs a systemd user unit. Logs are available with:

```bash
journalctl --user -u quotadeck.service -f
```
