# ADR 0001: Reference projects and license boundary

- Status: accepted
- Date: 2026-08-30

## Context

QuotaDeck is a greenfield MIT project. The engineering brief names four projects as behavioral and architectural references. Their source remains outside this repository and no code has been copied into QuotaDeck.

## References inspected

| Project | Commit inspected | License | Use in QuotaDeck |
| --- | --- | --- | --- |
| [claude-swap](https://github.com/realiti4/claude-swap) | `2213700b5a1331f50939ce1f41b531e674d612a6` | MIT | The documented additive `cswap list --json` schema: schema version 1, slots, aliases, current and last-good usage, source age, pace fields, and dynamic scoped windows. |
| [CodexBar](https://github.com/steipete/CodexBar) | `8ee6704efa283f24ed29e2090c258ad8d9627676` | MIT | Provider-neutral quota concepts, Codex app-server methods, and Z.ai endpoint/response variants. The local Codex CLI generated protocol schema was used as the authoritative wire contract. |
| [CrossUsage](https://github.com/barramee27/crossusage) | `6465844be7f5e229fa25e5e26ac9c351b216645d` | MIT | Behavioral comparison for Linux packaging and provider discovery. No UI or credential-management design was reused. |
| [onWatch](https://github.com/onllm-dev/onwatch) | `9ff07bc6de7f61b58f2cb9db2599cb3788e48e96` | GPL-3.0 | Architectural reference only: local daemon, SQLite WAL, SSE, and user service. No source code, assets, or text were copied. |

The commit IDs are recorded rather than floating branches so later reviews can reproduce the reference point. Network instability prevented a complete checkout of some large worktrees during the initial implementation; their public documentation, license metadata, and exact HEADs were still verified. This limitation does not affect attribution because no reference code was imported.

## Decision

QuotaDeck remains MIT. Implementations are original and expressed through QuotaDeck's own domain interfaces. If code is ever copied from an MIT reference, the exact file, commit, modification, and required notice must be added to this ADR before merging. GPL-3.0 code from onWatch must never enter this repository unless the entire project is deliberately relicensed under a compatible GPL license through a separate ADR.

## Consequences

- `cswap` is executed as an external program and remains the credential boundary for Claude.
- Codex is queried through the locally installed app-server protocol; QuotaDeck never imports CodexBar source.
- Z.ai parsing is dynamic and fixture-driven rather than copied from another provider implementation.
- The GPL reference is informative only.
