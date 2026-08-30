# QuotaDeck

QuotaDeck is a local-first dashboard for the quota windows attached to Claude, OpenAI Codex/ChatGPT plans, and Z.ai/GLM Coding Plans. It treats `provider → accounts → windows[]` as the domain, so newly returned windows are persisted, exposed, and rendered without a `session`/`weekly` hard-code.

The daemon binds to `127.0.0.1`, embeds its Preact frontend in one Go binary, stores history in SQLite WAL, streams changes with Server-Sent Events, and sends no telemetry.

## Current providers

- **Claude:** `cswap list --json` is the canonical multi-account source. QuotaDeck never calls `cswap export`, opens its credential store, or refreshes Claude OAuth itself.
- **Codex:** one `codex app-server --stdio` process per configured `CODEX_HOME` fetches `account/read` and `account/rateLimits/read`. QuotaDeck does not read or modify `auth.json`.
- **Z.ai:** explicit environment references, `ZAI_API_KEY`, `GLM_API_KEY`, and recognized Z.ai entries in Claude settings. Tokens stay in memory and are deduplicated with a truncated in-memory SHA-256 fingerprint.

## Build and run

Requirements: Go 1.25+, Node 22+, npm, and `dpkg-deb` for Debian packaging. The Linux desktop build additionally needs Wails v2, GTK 3, WebKitGTK 4.1, and ImageMagick.

```bash
make test
make test-race
make lint
make build
./dist/quotadeck doctor
./dist/quotadeck serve --bind 127.0.0.1 --port 9211
```

Open <http://127.0.0.1:9211>. For frontend/backend development, use `make dev`.

## Configuration

The default file is `${XDG_CONFIG_HOME:-~/.config}/quotadeck/config.yaml`. A missing file is valid and uses safe defaults. Copy [config.example.yaml](config.example.yaml) to configure multiple Z.ai environment references or multiple Codex homes.

Data defaults to `${XDG_DATA_HOME:-~/.local/share}/quotadeck/quotadeck.db`, with 30-day retention. The default poll interval is 60 seconds.

Useful commands:

```bash
quotadeck doctor --json
quotadeck refresh
quotadeck status --json
quotadeck service install --user
quotadeck service status
quotadeck service uninstall --user
```

`doctor` reports paths, versions, source decisions, and secret-presence booleans only. It never returns a token value. `POST /api/v1/refresh` is loopback-only and requires QuotaDeck's explicit request header.

## HTTP API

```text
GET  /api/v1/state
GET  /api/v1/providers
GET  /api/v1/accounts
GET  /api/v1/accounts/{id}/history?from=&to=
GET  /api/v1/health
GET  /api/v1/doctor
GET  /api/v1/events
POST /api/v1/refresh
```

Errors are redacted before persistence. On a temporary source failure, the newest status becomes stale/unavailable while the last valid windows remain visible.

## Debian package

```bash
make desktop-build
make package-cinnamon
make package-deb
```

`desktop-build` produces the native Wails executable at `dist/quotadeck-desktop-linux-amd64`. `package-cinnamon` creates a standalone applet zip. `package-deb` produces `dist/quotadeck_<version>_<arch>.deb` containing the CLI, the desktop launcher, the systemd user unit, and the Cinnamon applet.

After installing the Debian package, launch **QuotaDeck** from the application menu. To add the panel indicator, open Cinnamon's **System Settings → Applets**, select QuotaDeck, and add it to the panel. The applet reads the loopback API and starts the packaged user service when needed.

For a portable Linux bundle:

```bash
make package-appimage
```

The first AppImage build downloads `linuxdeploy` and its GTK plugin into the user cache. The resulting file is written to `dist/QuotaDeck-<version>-x86_64.AppImage`.

Prebuilt packages, portable archives, and SHA-256 checksums are available from the [latest GitHub release](https://github.com/devthefuture-org/quotadeck/releases/latest).

## Security and license

- Loopback binding is enforced by configuration validation.
- Provider secrets are absent from domain objects, logs, SQLite, API responses, fixtures, and diagnostics.
- Z.ai bearer credentials are attached only to validated HTTPS quota URLs.
- Account identities derive from public slots or source references, never raw credentials.
- The project is MIT licensed. See [ADR 0001](docs/adr/0001-reference-projects.md) for reference-project boundaries and [ADR 0002](docs/adr/0002-domain-model.md) for the dynamic data model.
