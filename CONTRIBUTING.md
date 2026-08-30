# Contributing to QuotaDeck

Thanks for helping improve QuotaDeck. Focused bug fixes, provider fixtures, accessibility work, documentation, and packaging improvements are welcome.

## Before you start

- Search existing issues and discussions.
- Open an issue before a large behavioral or architectural change.
- Never include provider tokens, exported credentials, real account labels, or private filesystem paths in fixtures and screenshots.
- Preserve the dynamic `provider → accounts → windows[]` model described in [ADR 0002](docs/adr/0002-domain-model.md).

## Local setup

Requirements are Go 1.25+, Node.js 22+, and npm.

```bash
npm ci --prefix web
npm ci --prefix docs
make test
make dev
```

For UI work without provider credentials:

```bash
make demo
```

Open <http://127.0.0.1:9212>. This creates an ephemeral SQLite database containing synthetic data and never starts provider discovery.

## Quality checks

Run before submitting a pull request:

```bash
make test
make test-race
make lint
make docs-build
git diff --check
```

Provider changes should include sanitized fixtures and parser tests. User-visible behavior should include a test at the narrowest useful layer.

## Pull requests

Keep each pull request reviewable and explain:

- the problem and intended behavior;
- the privacy or credential-handling impact;
- how the change was tested;
- screenshots for visible changes, generated with synthetic data.

By contributing, you agree that your contribution is licensed under the project's MIT License.
