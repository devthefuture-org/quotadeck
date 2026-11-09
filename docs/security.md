# Security and privacy

QuotaDeck is local-first and sends no telemetry.

## Guarantees

- HTTP binds only to `127.0.0.1`, `::1`, or `localhost`.
- Provider secrets are excluded from domain objects, SQLite, API responses, logs, fixtures, and diagnostics.
- Claude credentials remain managed by `cswap`; setup invokes only the official package installer plus public `list`/`add` commands, plan selection invokes only `switch`, and account management invokes only `disable`/`enable`/`remove` (remove discards the stored credentials after an explicit UI confirmation and is refused for the account currently served). Codex credentials remain managed by Codex CLI.
- Z.ai bearer credentials are attached only to validated HTTPS quota URLs.
- Plan-control mutations are loopback-only, require an explicit anti-CSRF header, and never return stored keys.
- Z.ai and Kimi UI setup writes the private QuotaDeck environment file and the documented Claude Code environment keys in `~/.claude/settings.json`; unrelated settings are preserved. Kimi also saves the model preferences and credential helper it replaces in the private `settings.json.quotadeck-models.json` sidecar, so switching back can restore them; Z.ai does the same for its fixed-value environment keys in `settings.json.quotadeck-zai.json`. Kimi CLI OAuth credentials remain in the CLI store. The explicit `quotadeck credential kimi` command emits only the access token on stdout for Claude Code’s `apiKeyHelper`; it is not exposed through HTTP. Renewal uses only the configured official Kimi OAuth host, refuses redirects, serializes with Kimi Code’s lock, and saves rotated tokens with mode `0600`.
- Persisted errors pass through redaction before storage.
- Browser responses include a restrictive Content Security Policy and frame denial.

## Local threat model

QuotaDeck does not attempt to isolate data from another process already running as your operating-system user. Protect your account by keeping XDG configuration and data directories private, and never expose the loopback port through a reverse proxy.

## Report a vulnerability

Do not open a public issue for a suspected vulnerability. Use [GitHub's private security advisory form](https://github.com/devthefuture-org/quotadeck/security/advisories/new) with reproduction steps, impact, and the affected version.

See the repository [security policy](https://github.com/devthefuture-org/quotadeck/blob/main/SECURITY.md) for supported versions and response expectations.

## OpenCode v2 credentials

OpenCode Console credentials stay in OpenCode's SQLite `credential` table.
QuotaDeck persists only a source reference and quota metadata in its own database.
The Claude Code settings store the helper command and workspace ID, never the
OAuth access or refresh token. Only the explicit credential helper emits the
access token on stdout for Claude Code to consume.

Discovery and normal reads open SQLite in read-only mode. OAuth renewal contacts
only `https://opencode.ai/console/auth/device/token`, refuses redirects and
updates the original row with a compare-and-swap after acquiring OpenCode's
`credential-refresh:<id>` directory lock in its XDG state directory. Unknown
credential fields are preserved. Logout or replacement during renewal prevents
the update. Remote error bodies are never exposed in diagnostics or HTTP.
