# Security policy

QuotaDeck handles quota metadata around locally authenticated provider tools. Credential isolation and redaction are core project constraints.

## Supported versions

Security fixes are applied to the latest published release and the `main` branch.

## Reporting a vulnerability

Do not open a public issue. Submit a report through [GitHub private vulnerability reporting](https://github.com/devthefuture-org/quotadeck/security/advisories/new).

Include the affected version, impact, reproduction steps, and any suggested mitigation. Remove access tokens, account identifiers, home paths, database contents, and other personal data from the report.

Maintainers will acknowledge a complete report as soon as practical, investigate privately, and coordinate disclosure after a fix is available. Please avoid public disclosure while the issue is being assessed.

## Scope

Reports about secret exposure, loopback-boundary bypasses, unsafe provider endpoints, redaction failures, dependency compromise, and release artifact integrity are in scope. General support questions belong in the issue tracker.
