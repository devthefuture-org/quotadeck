# Security and privacy

QuotaDeck is local-first and sends no telemetry.

## Guarantees

- HTTP binds only to `127.0.0.1`, `::1`, or `localhost`.
- Provider secrets are excluded from domain objects, SQLite, API responses, logs, fixtures, and diagnostics.
- Claude credentials remain managed by `cswap`; setup invokes only the official package installer plus public `list`/`add` commands, and plan selection invokes only `switch`. Codex credentials remain managed by Codex CLI.
- Z.ai bearer credentials are attached only to validated HTTPS quota URLs.
- Plan-control mutations are loopback-only, require an explicit anti-CSRF header, and never return stored keys.
- Z.ai UI setup writes only the private QuotaDeck environment file and the documented Claude Code environment keys in `~/.claude/settings.json`; unrelated settings are preserved.
- Persisted errors pass through redaction before storage.
- Browser responses include a restrictive Content Security Policy and frame denial.

## Local threat model

QuotaDeck does not attempt to isolate data from another process already running as your operating-system user. Protect your account by keeping XDG configuration and data directories private, and never expose the loopback port through a reverse proxy.

## Report a vulnerability

Do not open a public issue for a suspected vulnerability. Use [GitHub's private security advisory form](https://github.com/devthefuture-org/quotadeck/security/advisories/new) with reproduction steps, impact, and the affected version.

See the repository [security policy](https://github.com/devthefuture-org/quotadeck/blob/main/SECURITY.md) for supported versions and response expectations.
