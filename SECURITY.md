# Security Policy

## Supported versions

This repository publishes **two modules from two branches**, and they have different support
promises. You are reading the copy on `support/go1.13` — the Go 1.13 line, whose entire support
policy is this page plus [docs/versioning.md](docs/versioning.md).

| Module | Versions | Branch | Supported | What it receives |
| ------ | -------- | ------ | --------- | ---------------- |
| `github.com/octoverse-id/octonomy-go` | `1.x` | `support/go1.13` | ✅ until **2027-08-31** | Security fixes, bug fixes, and capability parity with `main` ([#88](https://github.com/octoverse-id/octonomy-go/issues/88)) — never a major, never a webhook receiver |
| `github.com/octoverse-id/octonomy-go/v2` | `2.x` | `main` | ✅ | Active development |
| — | `0.x` | — | n/a | Never released; no `v0.x` tag exists and the module proxy has never served one |

**The `1.x` line takes security fixes, bug fixes, and ports of what `main` already has** — the
resource groups, `/api/v2` and the namespace axis. The security-fixes-only freeze published with
`v1.0.0` was withdrawn in #89; see the support policy in [docs/versioning.md](docs/versioning.md).
Two things never come here: a **webhook receiver**, and a **breaking change** — the line's
unsuffixed module path can never publish a major, so every `1.x` keeps `v1.0.0` code compiling
(unkeyed struct literals aside — see the MINOR rule in [docs/versioning.md](docs/versioning.md)). If
you need either, the upgrade is to the `/v2` module on a current Go toolchain —
[`main`'s README](https://github.com/octoverse-id/octonomy-go/blob/main/README.md) states the floor
it requires today.

**Sunset: 2027-08-31.** After that date the `1.x` line receives nothing at all, including security
fixes. Owner: the SDK maintainer (see [`.github/CODEOWNERS`](.github/CODEOWNERS)); revisable only by
agreement with the consuming team. The rule is 12 months from the `v1.0.0` tag, published to the end
of the twelfth month so the date is fixed rather than dependent on the hour the tag was pushed —
rounding can only give you more time, never less. Plan the toolchain upgrade against this date; it is
the only real fix.

**A published `1.x` version cannot be recalled for this audience.** `retract` shipped in Go 1.16, so
a Go 1.13 toolchain ignores it, and `GOPROXY` caches tags permanently. If you are pinned here, treat
an advisory on this line as something to upgrade past, not something we can withdraw.

### Reporting against the compat line

Report as below and say explicitly that you are on the `1.x` / Go 1.13 line. A fix lands on `main`
first and is then ported here, so the two lines need separate verification.

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
