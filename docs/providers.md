# Providers

QuotaDeck discovers quota metadata through provider-supported local tools and endpoints. Credentials never enter the domain model, database, API, fixtures, or logs.

## Claude

Claude multi-account discovery uses [`cswap`](https://github.com/realiti4/claude-swap) as its canonical source. QuotaDeck can install and initialize it with the supported `uv`/`pipx` flow:

```bash
quotadeck setup cswap
cswap list --json
quotadeck doctor
```

The same action is available as **Install & set up** under **Manage plans** on the dashboard. If no cswap account exists, setup invokes `cswap add` so cswap—not QuotaDeck—imports the current Claude Code login. QuotaDeck then reads public account slots and usage data. The **Use in Claude Code** button on each quota card activates that account by calling `cswap switch <slot> --json`. It never calls `cswap export`, opens the credential store, or refreshes Claude OAuth itself.

## Codex

QuotaDeck starts one `codex app-server --stdio` process for each configured home and reads `account/read` plus `account/rateLimits/read`.

```bash
CODEX_HOME="$HOME/.codex" codex login
CODEX_HOME="$HOME/.codex" quotadeck doctor
```

QuotaDeck does not parse or modify `auth.json`. Each configured home remains an isolated Codex profile. When no account list is configured, the running process's `CODEX_HOME` takes precedence over the default `~/.codex`.

### Signing in from the dashboard

When a Codex account reports **auth error**, its card offers **Reconnect**. QuotaDeck asks the app-server to start a sign-in and shows you the link to open; **Use a code instead** switches to a device code you type on OpenAI's activation page. Either way Codex writes the new credentials into that account's `CODEX_HOME`, exactly as `codex login` would, and the quotas refresh on their own once it succeeds.

Three constraints are worth knowing:

- **Codex hosts the browser callback on a fixed local port** (1455, falling back to 1457), and it cancels whatever already holds that port. So QuotaDeck runs at most one browser sign-in at a time, and starting one here can interrupt a `codex login` running in a terminal — and the reverse. The device code binds no port and is never affected.
- **Polling pauses for the home being signed in.** A rate-limit read renews and rewrites the same `auth.json` the sign-in is replacing. The account keeps the figures it already had until the sign-in ends.
- **A sign-in does not survive a QuotaDeck restart**, because the callback lives inside the Codex process. The dashboard says so and offers to start again.

QuotaDeck never reads the credentials themselves: it passes `CODEX_HOME` to Codex and reports what Codex answers.

## Z.ai / GLM Coding Plan

QuotaDeck can use:

- explicit environment references in `config.yaml`;
- `ZAI_API_KEY` or `GLM_API_KEY` from its process environment;
- recognized Z.ai entries in Claude settings.

Bearer credentials are read only from their configured private sources and attached only to validated HTTPS quota endpoints. They never enter domain objects, SQLite, diagnostics, logs, or API responses. Multiple sources for the same secret are deduplicated through a truncated in-memory SHA-256 fingerprint.

## Selecting the active Claude Code plan

The dashboard groups quota usage and plan selection in the same view. Compare consumption and reset times, then select:

- **Use in Claude Code** on any enabled Claude subscription card managed by `cswap`;
- **Use Z.ai in Claude Code** beside the Z.ai quotas to activate the configured GLM Coding Plan key.

The selected Claude account is highlighted, and the current plan remains visible above the quota filters. **Manage plans** opens the setup panel on the same page.

Selecting a Claude subscription removes only an active Z.ai endpoint and token from Claude Code settings, preserves unrelated settings, and delegates credential activation to `cswap`. If `cswap` fails, QuotaDeck restores the original settings file.

For Z.ai, open **Manage plans**, paste the API key once, then choose **Save & use Z.ai**. Later, use the button beside its quotas to select the stored key. QuotaDeck writes the private service environment and configures Claude Code's official Anthropic-compatible endpoint in `~/.claude/settings.json`. Both files are created with user-only permissions, and the key is never returned by the HTTP API. The endpoint follows the [official Z.ai Claude Code setup](https://docs.z.ai/devpack/quick-start).

The selection applies to new Claude Code processes. Existing sessions retain the environment with which they started.

See [configuration](./configuration#z-ai-environment-references) when running QuotaDeck through systemd or a desktop launcher.
