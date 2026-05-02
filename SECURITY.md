# Security Policy

## Supported versions

`sftp-loadtest` follows semver. Security fixes are backported only to the
latest minor release line. If you're on an older release, plan an upgrade
before reporting.

| Version       | Supported          |
|---------------|--------------------|
| `0.13.x`      | Yes (current)      |
| `0.12.x`      | No                 |
| `< 0.12`      | No                 |

## Reporting a vulnerability

**Do not open a public GitHub issue for security findings.** Instead use
GitHub's private vulnerability reporting:

  https://github.com/roshandubey-cloud/utilities/security/advisories/new

If that channel is not available, email **rdship@gmail.com** with subject
line `[security] sftp-loadtest <component>` and a brief proof-of-concept.
PGP keys are not required; encrypted attachments are welcome.

We aim to:
- Acknowledge receipt within **3 business days**.
- Confirm reproduction or close as not-a-vuln within **7 business days**.
- Ship a patch release within **30 days** for confirmed High/Critical
  findings (CVSS ≥ 7.0). Lower-severity findings ride normal release
  cadence.

## Scope

In scope:
- The `sftp-loadtest` webui binary, its HTTP API, and the embedded UI.
- The `sftp-loadtest-desktop` Wails bundle.
- The cluster coordinator + SSH-bootstrapped worker spawn paths.
- The mock servers (`cmd/mockserver`, `cmd/mockftpserver`) — only when
  used as a test harness; their security posture is intentionally
  permissive for unit tests.

Out of scope:
- Third-party SFTP / FTP / FTPS servers we connect to.
- DoS via high-volume load runs against your own infrastructure (that's
  the tool working as designed — please rate-limit your test targets).
- Issues already fixed in the latest tagged release.

## Supply chain

- Every release has a CycloneDX SBOM
  (`sftp-loadtest-sbom-<tag>.cdx.json`) attached as a release asset.
- `govulncheck` runs in CI on every push and gates merges on
  vulnerability-free dependency closure.
- `dependabot` opens weekly PRs for Go module updates and monthly PRs
  for GitHub Actions updates.

## Known security posture

The included `docs/security.md` (sftp-loadtest module) documents the
threat model, OWASP-grade headers, CSRF guard, rate-limiting, host-key
TOFU, body-size limits, and the trust stores used for SSH host keys
and FTPS server certs.
