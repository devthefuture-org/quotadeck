# Security and privacy

QuotaDeck is local-first and sends no telemetry.

## Guarantees

- HTTP binds only to `127.0.0.1`, `::1`, or `localhost`.
- Provider secrets are excluded from domain objects, SQLite, API responses, logs, fixtures, and diagnostics.
- Claude credentials remain managed by `cswap`; Codex credentials remain managed by Codex CLI.
- Z.ai bearer credentials are attached only to validated HTTPS quota URLs.
- Persisted errors pass through redaction before storage.
- Browser responses include a restrictive Content Security Policy and frame denial.

## Local threat model

QuotaDeck does not attempt to isolate data from another process already running as your operating-system user. Protect your account by keeping XDG configuration and data directories private, and never expose the loopback port through a reverse proxy.

## Report a vulnerability

Do not open a public issue for a suspected vulnerability. Use [GitHub's private security advisory form](https://github.com/devthefuture-org/quotadeck/security/advisories/new) with reproduction steps, impact, and the affected version.

See the repository [security policy](https://github.com/devthefuture-org/quotadeck/blob/main/SECURITY.md) for supported versions and response expectations.
