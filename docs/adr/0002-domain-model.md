# ADR 0002: Dynamic provider → accounts → windows domain

- Status: accepted
- Date: 2026-08-30

## Context

AI plans expose heterogeneous and evolving limits: rolling five-hour windows, weekly limits, model-scoped limits, tool/MCP allowances, credits, team scopes, and future fields. A fixed `session`/`weekly` schema loses data and makes each new provider a frontend migration.

## Decision

The durable contract is:

```text
provider
  └─ account[]
       └─ snapshot
            └─ windows[]
```

An account has a stable opaque ID, a human label, a source description, and non-secret source metadata. A snapshot records status, staleness, acquisition age, a redacted error, and any number of windows. Each window keeps its source-derived ID and label and can independently carry percentages, absolute quantities, units, reset dates, pace, and projections.

Percentages are normalized to **consumed** at ingestion. Remaining percentage is derived when possible. Unknown window types are stored and rendered; they are never silently renamed to weekly or dropped.

Providers implement discovery and fetch separately. Discovery returns secret-free candidates. Secret values remain inside the provider adapter's memory and are never included in domain objects, logs, SQLite, HTTP responses, fixtures, or diagnostics.

## Failure semantics

- `fresh`: current actionable source data.
- `stale`: source-supplied last-good data.
- `auth_error`: reauthentication is required.
- `unavailable`: temporary command, network, or provider failure.
- `unsupported`: the source/account state cannot currently be acted on.

When a fetch fails after a valid snapshot exists, QuotaDeck persists a new stale status snapshot containing the last valid windows. This preserves dashboard continuity while making the failure explicit.

## Persistence

SQLite stores accounts and snapshots separately. Windows are child rows with a generic JSON payload, allowing additive fields without a destructive schema migration. Writes use one transaction per snapshot, foreign keys, WAL, a busy timeout, and versioned migrations.

## Consequences

- The HTTP API and UI can render newly introduced windows without application changes.
- Provider adapters remain small normalization boundaries.
- Historical snapshots retain the exact dynamic window set observed at that time.
- Provider-specific controls can be added later without contaminating the read-only core.
