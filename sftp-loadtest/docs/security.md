# Security baseline

This document is the production-deployment companion for `sftp-loadtest`. It
covers the threat model, the security-relevant flags introduced by the
`security/baseline-hardening` work, and the recommended deployment shape.

## Threat model

`sftp-loadtest` is a load-testing tool. It is **not** a multi-tenant service.
The default deployment shape is:

- One operator running the binary on a controlled machine.
- The web UI bound to **127.0.0.1** and reached via SSH local-port-forward.
- A single set of SFTP credentials in scope at a time.

Anything outside that shape (multi-user access, public URL, automation that
hits the API) needs the hardening flags below — none are on by default
because production tools should fail closed only when an operator opts in.

## Security-relevant flags

| Flag                        | What it does                                                                                  |
| --------------------------- | --------------------------------------------------------------------------------------------- |
| `-known-hosts <path>`       | Verify SFTP server keys against an OpenSSH-format `known_hosts` file (recommended).           |
| `-insecure-host-key`        | Skip SFTP host-key verification entirely. Lab use only — emits a startup warning every time.  |
| `-tls-cert` / `-tls-key`    | Pair to enable HTTPS on the web UI. Set both, or neither (HTTP).                              |
| `-auth-user` / `-auth-pass` | Pair to require HTTP Basic auth on every request except `/healthz`.                           |
| `-debug`                    | Mounts `/debug/pprof/*`. **Refuses to start** if `-addr` is non-loopback (see Pprof below).   |

The tool **refuses to start** without one of `-known-hosts` or
`-insecure-host-key` so the SSH MITM exposure can never be silent.

## Dependencies

The `security/baseline-hardening` branch updates:

- `golang.org/x/crypto` → `v0.32.0` (fixes CVE-2024-45337)
- `golang.org/x/sys` → `v0.28.0`
- `github.com/pkg/sftp` → `v1.13.7`

On a host with Go-module-proxy access:

```
go get -u ./...
go mod tidy
go build ./...
govulncheck ./...
```

`govulncheck` should report no advisories against the upgraded versions.

If the host **cannot** reach `proxy.golang.org` (corporate firewall),
this branch ships a workspace fallback: place the upstream source archives
under `third_party/` and rely on the gitignored `go.work` file. The
workspace file is **not committed**; CI uses the proxy.

## Server hardening summary

Implemented in this branch:

- **HTTP timeouts**: 5 s ReadHeader, 60 s Read, 60 s Idle. Slowloris-resistant.
- **Body-size limits**: per-endpoint `http.MaxBytesReader`. `/api/start` and
  `/api/schedule` get 2 MiB; `/api/probe` gets 8 KiB; everything else 1 MiB.
- **CSRF guard**: every POST must carry `X-Requested-With: sftp-loadtest`.
  The UI's `apiFetch` helper sends it automatically; cross-origin pages can't.
- **Security headers**: `X-Content-Type-Options: nosniff`, `X-Frame-Options:
  DENY`, `Referrer-Policy: no-referrer`, a self-only `Content-Security-Policy`,
  and `Strict-Transport-Security` when TLS is active.
- **Rate limiting**: token bucket per (client-IP, path) on the expensive
  endpoints (`/api/start`, `/api/probe`, `/api/schedule`,
  `/api/schedule/cancel`, `/api/stop`). Default 10 burst / 1 rps refill.
  Idle buckets are evicted after 10 min.
- **Basic auth**: `subtle.ConstantTimeCompare` against `-auth-user`/`-auth-pass`.
  `/healthz` is intentionally exempt so liveness probes still work.
- **File permissions**: report and schedule directories are created `0o700`;
  files written `0o600`. Avoids leaks on shared hosts when CSV error fields
  carry SSH error text.
- **Pprof gating**: `-debug` refuses to start if `-addr` resolves to anything
  other than loopback. The heap dump contains `RunConfig` (with passwords),
  so a public pprof would be a credential disclosure.

## Client hardening summary

The web UI now keeps credentials out of long-term storage by default:

- **Save passwords in this browser** checkbox (default **off**). When off,
  every periodic auto-save scrubs the password column from each user CSV
  before writing to `localStorage`.
- **Clear stored credentials** button wipes the saved config and blanks the
  password column in every CSV textarea on screen.
- **Export config** by default produces a JSON file with passwords blanked
  in every user CSV. A separate "Include passwords" checkbox opts in for
  the rare case where the operator wants creds in the file (and a `confirm()`
  guards that path).

## Deployment recipes

### Solo operator on their own machine

No flags needed beyond host-key verification:

```
./sftp-loadtest -known-hosts ~/.ssh/known_hosts
```

Open `http://127.0.0.1:8080`. Done.

### Shared dev/staging server

Bind localhost-only, require auth, use TLS:

```
./sftp-loadtest \
  -addr 127.0.0.1:8443 \
  -known-hosts /etc/sftp-loadtest/known_hosts \
  -auth-user oncall \
  -auth-pass "$(pwgen -s 32 1)" \
  -tls-cert /etc/sftp-loadtest/server.crt \
  -tls-key  /etc/sftp-loadtest/server.key
```

Front it with nginx or Caddy if you need network-level access; have them
terminate TLS and forward to the loopback bind. Rate-limit at the proxy
layer too.

### CI / scheduled runs

Run with `-auth-token`-style credentials in environment variables (still
TODO — tracked separately) and `-known-hosts` pointing at a file checked
into the CI workspace. The scheduler subsystem already persists planned
runs to disk so a CI restart resumes cleanly.

## What's still on the backlog

- **SSH public-key auth**: the codebase only handles password auth today.
  Adding `-key-file` per-user is a follow-up.
- **Token-based API auth**: a single `-auth-token` HTTP header is more
  ergonomic than `-auth-user`/`-auth-pass` for automation. Easy add.
- **Per-user audit log**: `started_by` is currently free-text; once auth
  is in place, log the authenticated principal to a separate audit file.
- **Refactor inline JS** out of `index.html` so we can drop
  `script-src 'unsafe-inline'` from the CSP.

## How to verify

After building:

```
gosec -severity high -confidence high ./...    # one acknowledged G402 only
govulncheck ./...                               # clean against bumped deps
nikto -h http://127.0.0.1:8080                 # banner-grab + headers OK
```

Hand the binary, this doc, and the gosec/govulncheck output to whoever's
running the scan. The G402 `InsecureIgnoreHostKey` reference is intentional
(behind `-insecure-host-key`); annotate it with `// nosec G402` if your
team's policy requires explicit suppressions.
