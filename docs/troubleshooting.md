# Troubleshooting

Start with:

```bash
quotadeck doctor
quotadeck service status
```

## cswap is missing or Claude indicators stay stale

Run the one-command setup, then refresh:

```bash
quotadeck setup cswap
quotadeck refresh
```

QuotaDeck searches common per-user binary directories even when systemd or a graphical launcher has a reduced `PATH`. Setup uses `uv` first and falls back to `pipx`; the Debian package installs `pipx` as a dependency.

## Codex says authentication is required

Codex authentication belongs to a specific `CODEX_HOME`. Confirm that QuotaDeck and the login command use the same value:

```bash
CODEX_HOME="$HOME/.codex-work" codex login
CODEX_HOME="$HOME/.codex-work" codex account
```

Then either start QuotaDeck with that environment or add the home explicitly under `providers.codex.accounts`.

## No actionable quota window is available

This means the provider returned no usable rate-limit window. Check the account in `quotadeck doctor`, run `quotadeck refresh`, and inspect the service logs. For Codex, an authenticated CLI does not always imply that the current plan exposes a quota window through app-server.

## Z.ai works in a terminal but not from the menu

Desktop launchers and systemd user services do not automatically inherit variables exported in an interactive shell. Follow the [private systemd environment file](./configuration#z-ai-environment-references) setup.

## The local page does not respond

```bash
systemctl --user restart quotadeck.service
journalctl --user -u quotadeck.service -n 100 --no-pager
curl --fail http://127.0.0.1:9211/api/v1/health
```

If another process already owns port `9211`, change `server.port` in the configuration and restart both the service and desktop application.

## Debian install shows an `_apt` warning

When installing a local `.deb` from a private home directory, APT can warn that the download ran unsandboxed because the `_apt` user could not read the file. The package is still installed. Moving the file to `/tmp` before `sudo apt install` avoids the warning.
