# Providers

QuotaDeck discovers quota metadata through provider-supported local tools and endpoints. Credentials never enter the domain model, database, API, fixtures, or logs.

## Claude

Claude multi-account discovery uses [`cswap`](https://github.com/dean0x/cswap) as its canonical source:

```bash
cswap list --json
quotadeck doctor
```

QuotaDeck reads public account slots and usage data. It never calls `cswap export`, opens the credential store, or refreshes Claude OAuth itself.

## Codex

QuotaDeck starts one `codex app-server --stdio` process for each configured home and reads `account/read` plus `account/rateLimits/read`.

```bash
CODEX_HOME="$HOME/.codex" codex login
CODEX_HOME="$HOME/.codex" quotadeck doctor
```

QuotaDeck does not parse or modify `auth.json`. Each configured home remains an isolated Codex profile. When no account list is configured, the running process's `CODEX_HOME` takes precedence over the default `~/.codex`.

## Z.ai / GLM Coding Plan

QuotaDeck can use:

- explicit environment references in `config.yaml`;
- `ZAI_API_KEY` or `GLM_API_KEY` from its process environment;
- recognized Z.ai entries in Claude settings.

Bearer credentials stay in memory and are attached only to validated HTTPS quota endpoints. Multiple sources for the same secret are deduplicated through a truncated in-memory SHA-256 fingerprint.

See [configuration](./configuration#z-ai-environment-references) when running QuotaDeck through systemd or a desktop launcher.
