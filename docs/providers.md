# Providers

QuotaDeck discovers quota metadata through provider-supported local tools and endpoints. Credentials never enter the domain model, database, API, fixtures, or logs.

## Claude

Claude multi-account discovery uses [`cswap`](https://github.com/realiti4/claude-swap) as its canonical source. QuotaDeck can install and initialize it with the supported `uv`/`pipx` flow:

```bash
quotadeck setup cswap
cswap list --json
quotadeck doctor
```

The same action is available as **Install & set up** under **Manage plans** on the dashboard. If no cswap account exists, setup invokes `cswap add` so cswap—not QuotaDeck—imports the current Claude Code login. QuotaDeck then reads public account slots and usage data. The **Use in Claude Code** button on each quota card activates that account by calling `cswap switch <slot> --json`. Claude quota cards also carry **Disable account** (`cswap disable <slot>`, holds it out of cswap rotation, reversible) and **Remove** (`cswap remove <slot>`, deletes the account and its stored credentials from cswap after a confirmation; removing the account Claude Code currently serves is refused). QuotaDeck never calls `cswap export`, opens the credential store, or refreshes Claude OAuth itself.

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

## Kimi Code

QuotaDeck reads the Kimi for Coding plan from the Kimi Code usage API:

- `KIMI_API_KEY` or `KIMI_CODING_API_KEY` from its process environment;
- explicit environment references in `config.yaml`;
- a recognized Kimi base URL in Claude settings (any `*.kimi.com` host, e.g. `api.kimi.com`);
- Kimi Code CLI credential files under `~/.kimi-code/credentials`.

The same privacy rules as Z.ai apply: credentials stay in memory, are deduplicated by fingerprint, and never reach storage, logs, or API responses. All windows the API returns are shown — the 5-hour window, the monthly total, and the monthly coding quota.

## Selecting the active Claude Code plan

The dashboard groups quota usage and plan selection in the same view. Compare consumption and reset times, then select:

- **Use in Claude Code** on any enabled Claude subscription card managed by `cswap`;
- **Use Z.ai in Claude Code** beside the Z.ai quotas to activate the configured GLM Coding Plan key;
- **Use Kimi K3 in Claude Code** beside the Kimi quotas to activate the configured Kimi Code key and select `k3` for the main model, all model tiers, and sub-agents.

The selected Claude account is highlighted, and the current plan remains visible above the quota filters. **Manage plans** opens the setup panel on the same page.

Selecting a Claude subscription removes the active Z.ai or Kimi routing from Claude Code settings, restores model preferences overridden by Kimi, preserves unrelated settings, and delegates credential activation to `cswap`. If `cswap` fails, QuotaDeck restores the original settings file.

For Z.ai, open **Manage plans**, paste the API key once, then choose **Save & use Z.ai**. Later, use the button beside its quotas to select the stored key. QuotaDeck writes the private service environment and configures Claude Code's official Anthropic-compatible endpoint in `~/.claude/settings.json`. Both files are created with user-only permissions, and the key is never returned by the HTTP API. The endpoint follows the [official Z.ai Claude Code setup](https://docs.z.ai/devpack/quick-start). Like every gateway plan, activating Z.ai sets `ENABLE_CLAUDEAI_MCP_SERVERS=false` so replayed session history stays free of Anthropic-native MCP blocks.

For Kimi, a local Kimi Code login is detected automatically from its `config.toml` and file-backed OAuth credentials. Choose **Use Kimi K3 in Claude Code** beside the quotas, or **Use Kimi K3** in **Manage plans**, without entering a key. QuotaDeck follows the configured mainland/global region and installs a Claude Code `apiKeyHelper` that reads and renews the local credential. The helper checks the store on each call; refreshes use the same directory lock as Kimi Code and atomically update its private credential file. OAuth tokens are never copied into Claude settings or the QuotaDeck environment file. If the login has been revoked, run `kimi login` again.

API keys remain supported: paste one in **Manage plans**, or supply `KIMI_API_KEY` / `KIMI_CODING_API_KEY`. A configured API key takes precedence over the local OAuth login. The integration follows the [official Kimi Claude Code setup](https://www.kimi.com/code/docs/en/third-party-tools/claude-code.html), using the requested `k3` model with a 256K context cap for compatibility across K3 subscription tiers. Switching between Kimi and Z.ai preserves their keys for later selection. Like every gateway plan, activating Kimi sets `ENABLE_CLAUDEAI_MCP_SERVERS=false` so replayed session history stays free of Anthropic-native MCP blocks.

The selection applies to new Claude Code processes. Existing sessions retain the environment with which they started.

See [configuration](./configuration#z-ai-environment-references) when running QuotaDeck through systemd or a desktop launcher.

## OpenCode Go / Go Plus

QuotaDeck discovers OpenCode Console connections stored by OpenCode v2 in
`$XDG_DATA_HOME/opencode/opencode.db` (default `~/.local/share/opencode/opencode.db`).
Connect Console in OpenCode v2 first. OAuth and stored Console API keys are supported;
custom Console servers and legacy v1 auth.json files are not used.

QuotaDeck reads the rolling five-hour, weekly and monthly Go allowance from
`https://opencode.ai/inference/go/v1/usage`, including reset times. Go and Go Plus
share this API, which does not identify the subscription tier; cards say **Go**.
Missing or rejected quota responses are reported as errors, not zero usage.

**Use OpenCode Go in Claude Code** selects the available `deepseek-v4.1-flash`
alias for all model tiers and subagents. Go does not currently expose the dated
`deepseek-v4.1-flash-1017` model. The official base URL is
`https://opencode.ai/inference/go/anthropic`. DeepSeek requires the **Global**
region in the workspace's Console privacy settings; QuotaDeck never changes that
setting automatically.

The Go gateway is a translated target, not a native Anthropic Messages endpoint:
it rejects Anthropic-native MCP content (`mcp_tool_use`, `mcp_tool_result`) that
claude.ai connectors produce while a session runs against a native Anthropic
endpoint. Continuing such a session behind Go fails with `400 … requires a native
Anthropic Messages target`; restarting the session (new tab) clears it. So that
sessions stay portable, QuotaDeck sets `ENABLE_CLAUDEAI_MCP_SERVERS=false`
behind every gateway plan (OpenCode, Z.ai, Kimi): claude.ai connectors no longer
auto-load there and history keeps plain `tool_use` blocks. If a gateway also
refuses other beta-only block types, Claude Code already retries without
`tool_reference` (2.1.305+) and the extra switches `ENABLE_TOOL_SEARCH=false`
and `CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS=1` remove them at the source.

Claude Code uses `quotadeck credential opencode` as its credential helper. The
selected connection is pinned by ID; switching or removing it in OpenCode does
not silently redirect existing Claude sessions to another account. Tokens are
reread and renewed when necessary. Model preferences, custom headers and any
previous credential helper are restored when selecting another plan.

References: [Go](https://opencode.ai/v2/docs/console/go/),
[Claude Code setup](https://opencode.ai/v2/docs/console/harnesses/claude-code/).
