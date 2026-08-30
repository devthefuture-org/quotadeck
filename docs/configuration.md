# Configuration

QuotaDeck works without a configuration file. The default path is:

```text
${XDG_CONFIG_HOME:-~/.config}/quotadeck/config.yaml
```

Copy [`config.example.yaml`](https://github.com/devthefuture-org/quotadeck/blob/main/config.example.yaml) when you need multiple Codex homes or explicit Z.ai accounts.

## Defaults

| Setting | Default | Purpose |
| --- | --- | --- |
| `server.bind` | `127.0.0.1` | Loopback-only HTTP API |
| `server.port` | `9211` | Dashboard and API port |
| `storage.database` | XDG data directory | SQLite history database |
| `storage.retentionDays` | `30` | Snapshot retention |
| `polling.interval` | `60s` | Provider refresh interval |
| `polling.timeout` | `20s` | Per-provider timeout |

QuotaDeck rejects public bind addresses during validation.

## Multiple Codex profiles

```yaml
providers:
  codex:
    enabled: true
    binary: codex
    accounts:
      - label: Personal
        home: ~/.codex
      - label: Work
        home: ~/.codex-work
```

If `accounts` is empty, QuotaDeck honors the process `CODEX_HOME` and then falls back to `~/.codex`.

## Z.ai environment references

Configuration stores the **name** of an environment variable, never its token:

```yaml
providers:
  zai:
    enabled: true
    accounts:
      - label: GLM Team
        keyEnv: ZAI_TEAM_API_KEY
```

For a persistent user service, create a private environment file and reference it from a systemd override:

```bash
install -m 600 /dev/null ~/.config/quotadeck/secrets.env
systemctl --user edit quotadeck.service
```

Add:

```ini
[Service]
EnvironmentFile=%h/.config/quotadeck/secrets.env
```

Then put `ZAI_TEAM_API_KEY=...` in that file and restart the service:

```bash
systemctl --user daemon-reload
systemctl --user restart quotadeck.service
```

Keep the file outside source control and readable only by your user.
