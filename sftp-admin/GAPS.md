# GAPS

Items intentionally not yet implemented, with the reason and the phase
they're scheduled for. The Definition of Done requires every Section-3
leaf to either be implemented or appear in this file with rationale.

---

## Phase 1 (current) — portable foundation

| Leaf | Status | Note |
|---|---|---|
| Logger (JSON + syslog) | ✅ done | thread-safe, fork-safe; syslog on POSIX, stderr+file everywhere |
| Error subsystem | ✅ done | central enum, errno never leaks |
| Config loader | ✅ done | cJSON, schema validation, 1 MiB cap, per-OS default paths |
| Entrypoint (args, signals, banner) | ✅ done | portable wait loop; SIGTERM/SIGINT + Windows console-ctrl events |
| Portable shim (`portable.h`) | ✅ done | mutex/time/pid/data-dir/signal install all behind one tiny header |
| CMake cross-platform | ✅ done | strict flags branch on MSVC vs gcc/clang |
| CI matrix (Linux x64, macOS arm64, Windows x64) | ✅ done | `.github/workflows/sftp-admin-build.yml` |

## Phase 2 — SQLite layer (NOT YET IMPLEMENTED)

| Leaf | Reason / planned approach |
|---|---|
| Schema + migration | `schema_version` table; idempotent SQL migration files numbered 0001_, 0002_, … |
| WAL mode + `busy_timeout` | enabled on every open; per-process handle |
| Typed CRUD for users/listeners/profiles/audit/bans | Hand-rolled wrappers over `sqlite3_prepare_v2`; prepared statements only, NO concatenation |
| Seed: built-in immutable security profiles | "Modern (strict)", "Compatible (legacy)", "FIPS-leaning" written on first-boot |

## Phase 3 — first listener end-to-end

Everything in `listener.h` / `auth.h` / `sftp_engine.h` placeholders. The
**5 GB round-trip** milestone test is required here before moving on.

## Phase 4 — full SFTP handler set

`OPEN/READ/WRITE/CLOSE/LSTAT/FSTAT/STAT/SETSTAT/FSETSTAT/OPENDIR/READDIR/`
`RMDIR/MKDIR/REMOVE/RENAME/REALPATH/EXTENDED`, per-op permission flags,
path-jail fuzz harness.

## Phase 5 — security profiles + multi-hostkey

Validation engine, hard-block list (arcfour, *-cbc with SHA1, hmac-md5,
group1), warn-level UI badges, host-key encryption at rest with
libsodium secretbox.

## Phase 6 — pubkey auth + brute-force defense

Many keys per user, SHA256 fingerprint preview, two-phase libssh pubkey
flow, per-(IP, listener) failure counter with decay.

## Phase 7 — supervisor + control socket + multi-listener

Fork per listener, exponential backoff restart, control protocol
(length-prefixed JSON), `session.kill`, graceful drain on SIGTERM.

## Phase 8 — admin API + sftpadminctl

REST/JSON over libmicrohttpd HTTPS, argon2id admin accounts, session
cookies (HttpOnly+Secure+SameSite=Strict), CSRF, rate-limited login.
`sftpadminctl` CLI speaking the control socket.

## Phase 9 — web UI + SSE

Static HTML/CSS/vanilla JS served by libmicrohttpd, SSE `/api/v1/events`
with polling fallback.

## Phase 10 — observability + systemd packaging

Prometheus `/metrics`, structured audit to SQLite, systemd unit with
hardening directives (NoNewPrivileges, ProtectSystem=strict, PrivateTmp,
CapabilityBoundingSet=CAP_NET_BIND_SERVICE), logrotate config,
install/uninstall scripts.

## Recursive improvement loop

Per section 6, after Phase 10 the audit/rank/fix/verify/report loop
runs ≥3 cycles, exit criterion zero Critical/High/Medium in two
consecutive cycles.
