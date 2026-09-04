# Installation

QuotaDeck runs on Linux as a native desktop application, a background service, a Cinnamon applet, or a single CLI/server binary.

## Debian or Linux Mint

Download the latest `quotadeck_<version>_amd64.deb` from [GitHub Releases](https://github.com/devthefuture-org/quotadeck/releases/latest), then install it:

```bash
sudo apt install ./quotadeck_0.1.0_amd64.deb
```

Open **QuotaDeck** from the application menu. The package includes the daemon, desktop application, systemd user unit, and Cinnamon applet.

Open **Plans** and choose **Install & set up** to prepare `cswap`, or run the equivalent CLI command:

```bash
quotadeck setup cswap
```

QuotaDeck uses the package's `pipx` dependency (or an existing `uv`) to install `claude-swap` for the current user. If no account exists yet, it asks `cswap` to add the current Claude Code login; sign in to Claude Code first.

## Portable AppImage

Download `QuotaDeck-<version>-x86_64.AppImage`, make it executable, and launch it:

```bash
chmod +x QuotaDeck-0.1.0-x86_64.AppImage
./QuotaDeck-0.1.0-x86_64.AppImage
```

## Server and CLI

The release archive contains the standalone `quotadeck` binary. Start the local dashboard with:

```bash
quotadeck doctor
quotadeck serve
```

Open [http://127.0.0.1:9211](http://127.0.0.1:9211). The server refuses non-loopback bind addresses.

## First check

Use diagnostics before changing configuration:

```bash
quotadeck doctor
quotadeck status --json
```

`doctor` reports only paths, versions, source decisions, and secret-presence booleans. It never prints a provider token. Continue with [provider setup](./providers) if a source is missing.

## Build from source with Devbox

The repository's Devbox environment pins the complete build toolchain:

```bash
git clone https://github.com/devthefuture-org/quotadeck.git
cd quotadeck
devbox run bootstrap
devbox run test
devbox run build
```

Use `devbox run setup-cswap` to install and initialize `cswap` in the same portable environment.

## Build from source without Devbox

Building requires Go 1.25+, Node.js 22+, and npm:

```bash
git clone https://github.com/devthefuture-org/quotadeck.git
cd quotadeck
npm ci --prefix web
make test build
./dist/quotadeck serve
```

Native desktop and package builds additionally require Wails v2, GTK 3, WebKitGTK 4.1, ImageMagick, and `dpkg-deb`.
