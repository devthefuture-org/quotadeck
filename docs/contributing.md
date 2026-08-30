# Contributing

Issues, documentation improvements, provider adapters, and focused fixes are welcome.

## Development loop

```bash
npm ci --prefix web
npm ci --prefix docs
make test
make lint
make dev
```

Use `make demo` for UI work and documentation screenshots. The demo server listens on `127.0.0.1:9212`, creates a temporary database, and never discovers real provider accounts.

Before opening a pull request:

```bash
make test test-race lint docs-build
git diff --check
```

Please keep the dynamic `provider → accounts → windows[]` model intact and add fixtures for provider parsing changes. Read the full [contribution guide](https://github.com/devthefuture-org/quotadeck/blob/main/CONTRIBUTING.md) before submitting code.
