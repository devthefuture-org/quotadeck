# Architecture

QuotaDeck is a local application with a deliberately small trust boundary.

```text
provider adapters
      │
      ▼
provider → accounts → windows[]
      │
      ├── SQLite WAL history
      ├── loopback JSON API + SSE
      └── embedded Preact dashboard
               ├── browser
               ├── Wails desktop
               └── Cinnamon applet
```

## Dynamic domain model

Quota windows are data, not columns. Each window carries an ID, label, kind, percentages, optional quantities, and optional reset/projection metadata. Unknown future windows can therefore travel from a provider response to storage and UI without a `session`/`weekly` schema migration.

See [ADR 0002](./adr/0002-domain-model) for the decision record.

## Local API

| Method | Route | Purpose |
| --- | --- | --- |
| `GET` | `/api/v1/state` | Complete current dashboard state |
| `GET` | `/api/v1/providers` | Enabled provider metadata |
| `GET` | `/api/v1/accounts` | Discovered accounts |
| `GET` | `/api/v1/accounts/{id}/history` | Account snapshots |
| `GET` | `/api/v1/health` | Service health |
| `GET` | `/api/v1/doctor` | Redacted diagnostics |
| `GET` | `/api/v1/events` | Server-Sent Events stream |
| `POST` | `/api/v1/refresh` | Loopback-only manual refresh |

Refresh requests require the explicit `X-QuotaDeck-Request: refresh` header in addition to loopback origin checks.
