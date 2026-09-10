# Security Policy

## Supported versions

This repository publishes **two modules from two branches**, and they have different support
promises. You are reading the copy on `main` — the active `/v2` line. The `1.x` line has its own
copy of this file on `support/go1.13`; both describe the same policy.

| Module | Versions | Branch | Supported | What it receives |
| ------ | -------- | ------ | --------- | ---------------- |
| `github.com/octoverse-id/octonomy-go/v2` | `2.x` | `main` | ✅ | Active development — features, fixes, security |
| `github.com/octoverse-id/octonomy-go` | `1.x` | `support/go1.13` | ✅ until **2027-08-31** | **Security fixes only** |
| — | `0.x` | — | n/a | Never released; no `v0.x` tag exists and the module proxy has never served one |

**The `1.x` line takes security fixes and nothing else.** No features, no ordinary bug fixes, no
`/api/v2`, no namespaces, no webhooks — see the support policy in
[docs/versioning.md](docs/versioning.md). It exists for consumers pinned to **Go 1.13**; if you need
anything beyond a security fix, the upgrade is to Go 1.24+ and the `/v2` module.

**Sunset: 2027-08-31.** After that date the `1.x` line receives nothing at all, including security
fixes. Owner: the SDK maintainer (see [`.github/CODEOWNERS`](.github/CODEOWNERS)); revisable only by
agreement with the consuming team. The rule is 12 months from the `v1.0.0` tag (2026-08-26),
published to the end of the twelfth month so the date is fixed rather than dependent on the hour the
tag was pushed — rounding can only give you more time, never less.

**A fix that applies to both lines lands on `main` first**, then is cherry-picked onto
`support/go1.13` and released as a `1.x` patch — the backport step is part of the runbook in
[docs/release.md](docs/release.md). A fix that only affects `/v2`, namespaces, or anything else
absent from the compat line needs no backport.

**Go 1.13 itself receives no security patches.** Its last release was `go1.13.15` (August 2020), and
the Go team supports only the two most recent major versions, so a consumer on that toolchain carries
unpatched standard-library and toolchain advisories regardless of what this SDK does. Staying on the
`1.x` line is an informed trade, not a supported-forever state.

**A published `1.x` version cannot be recalled for that audience.** `retract` shipped in Go 1.16, so
a Go 1.13 toolchain ignores it, and `GOPROXY` caches tags permanently. An advisory on that line is
something to upgrade past, not something we can withdraw — which is why its releases are kept
deliberately small and its CI runs a real `go1.13` job as a required check.

## Reporting a vulnerability

Please **do not** open a public issue, pull request, or discussion for security vulnerabilities.

Instead, report privately through GitHub:

1. Go to the repository's **Security** tab.
2. Click **Report a vulnerability** to open a private security advisory.

This keeps the report confidential while we investigate.

Please include, where possible:

- A description of the issue and its potential impact.
- Steps to reproduce or a proof of concept.
- Affected version or commit.

## What to expect

- We aim to acknowledge new reports within a few business days.
- We'll keep you updated on our assessment and remediation progress.
- Once a fix is available, we'll coordinate disclosure and credit you if you'd like.

Thank you for helping keep Octonomy and its users safe.
