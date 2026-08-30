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

The applet reads the loopback API and starts the packaged user service when necessary. Its menu summarizes the tightest quota window and opens the full dashboard for details.

To install only the applet from a source checkout:

```bash
make package-cinnamon
```

Install the generated zip through Cinnamon's applet settings.

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
