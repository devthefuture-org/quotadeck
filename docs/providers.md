# Providers

QuotaDeck discovers quota metadata through provider-supported local tools and endpoints. Credentials never enter the domain model, database, API, fixtures, or logs.

## Claude

Claude multi-account discovery uses [`cswap`](https://github.com/realiti4/claude-swap) as its canonical source. QuotaDeck can install and initialize it with the supported `uv`/`pipx` flow:

```bash
quotadeck setup cswap
cswap list --json
quotadeck doctor
```

The same action is available as **Install & set up** on the **Plans** screen. If no cswap account exists, setup invokes `cswap add` so cswap—not QuotaDeck—imports the current Claude Code login. QuotaDeck then reads public account slots and usage data. The **Plans** screen can activate one of them by calling `cswap switch <slot> --json`. It never calls `cswap export`, opens the credential store, or refreshes Claude OAuth itself.

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

Bearer credentials are read only from their configured private sources and attached only to validated HTTPS quota endpoints. They never enter domain objects, SQLite, diagnostics, logs, or API responses. Multiple sources for the same secret are deduplicated through a truncated in-memory SHA-256 fingerprint.

## Selecting the active Claude Code plan

Open **Plans** in the dashboard to choose from one unified list containing:

- any enabled Claude subscription managed by `cswap`;
- the Z.ai GLM Coding Plan.

Selecting a Claude subscription removes only an active Z.ai endpoint and token from Claude Code settings, preserves unrelated settings, and delegates credential activation to `cswap`. If `cswap` fails, QuotaDeck restores the original settings file.

For Z.ai, choose **Set up**, paste the API key once, then select it like any Claude subscription. QuotaDeck writes the private service environment and configures Claude Code's official Anthropic-compatible endpoint in `~/.claude/settings.json`. Both files are created with user-only permissions, and the key is never returned by the HTTP API. The endpoint follows the [official Z.ai Claude Code setup](https://docs.z.ai/devpack/quick-start).

The selection applies to new Claude Code processes. Existing sessions retain the environment with which they started.

See [configuration](./configuration#z-ai-environment-references) when running QuotaDeck through systemd or a desktop launcher.
