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

The packaged user service automatically reads this optional private environment file:

```text
~/.config/quotadeck/environment
```

Create it with restrictive permissions:

```bash
install -d -m 700 ~/.config/quotadeck
install -m 600 /dev/null ~/.config/quotadeck/environment
```

Put `ZAI_TEAM_API_KEY=...` in that file and restart the service:

```bash
systemctl --user daemon-reload
systemctl --user restart quotadeck.service
```

Keep the file outside source control and readable only by your user.
When running `quotadeck serve` directly, expose the same variables in the process environment.

## Dashboard configuration

The **Plans** screen provides the same setup without editing files manually:

- **Save key** updates `ZAI_API_KEY` in the private QuotaDeck environment file;
- **Save & use Z.ai** also configures `~/.claude/settings.json` for the official Z.ai Anthropic endpoint;
- choosing a Claude account removes the active Z.ai routing keys and runs `cswap switch` for the selected slot.

QuotaDeck never includes a stored key in API responses, diagnostics, logs, or SQLite. Leaving the key field blank keeps the already stored value.
