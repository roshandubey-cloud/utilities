# SECURITY

Decisions and posture statements. Growing document — every phase adds
its specific decisions here. Phase 1 contains the foundational ones.

---

## Foundational (Phase 1)

### `errno` never reaches a log line or API response

Every syscall failure is converted through `sa_err_from_errno()` at the
call site. The raw `errno` value is dropped. This guarantees an attacker
cannot probe internal state (file existence, race conditions) by reading
back distinguishing error codes from the admin API.

### All logger output is JSON-escaped at emission

Caller-supplied values cannot break out of a log line. The escaper
handles all required control chars; UTF-8 high bytes pass through (RFC
8259 requires that). A user-controlled string containing `"` or `\n`
shows up correctly in the log without injecting new key/values.

### Logger record cap is 4 KiB

An attacker who controls a value flowing into a log line cannot
cause unbounded memory use or fd-flooding. Records that would exceed
4 KiB are truncated and re-closed with `}\n` so they remain valid JSON.

### Config file cap is 1 MiB

A bogus config (truncation attack, accidental binary paste) is refused
rather than parsed. Real production configs are kilobytes.

### Strict compiler flags + hardening

Build flags include:
`-Wall -Wextra -Werror -Wconversion -Wshadow -Wpointer-arith`
`-Wstrict-prototypes -Wmissing-prototypes -Wformat=2 -Wformat-security`
`-fstack-protector-strong -fno-common -fPIE`
`-D_FORTIFY_SOURCE=2 -D_FILE_OFFSET_BITS=64`
Release links add `-Wl,-z,relro -Wl,-z,now -pie`.

These are non-negotiable in the spec; the CMake toggle to disable them
(`-DSFTPADMIN_STRICT=OFF`) exists only for short-term refactor windows
and CI must keep them ON.

---

## Deliberate decisions deferred until their phase

These will be filled in as phases land — listed now so the security
posture is visible up front:

* **No symlinks inside the jail.** SFTP `SYMLINK` / `READLINK` will
  return `OP_UNSUPPORTED`. Documented constraint, not a bug. (Phase 4)
* **`O_NOFOLLOW` on every path operation inside the jail.** (Phase 4)
* **Path resolution order:** canonicalize → `realpath` parent →
  containment prefix check → reject any `..` leaf. Fuzz target on
  this function specifically. (Phase 4)
* **Privilege drop:** session worker drops to unprivileged uid/gid
  after auth and BEFORE chroot/path-jail enters the user's tree. (Phase 7)
* **Host keys encrypted at rest** with libsodium secretbox; master key
  from a 0600 keyfile; decrypted host key only ever lands on tmpfs at
  0600 and is unlinked after `ssh_bind` loads it. (Phase 5)
* **Two-phase pubkey auth:** correct libssh flow — probe phase
  (PUBKEY_AUTH_PARTIAL) before signature verification. (Phase 6)
* **Constant-time dummy verify** for unknown usernames so timing
  cannot distinguish "user exists, password wrong" from "user does not
  exist". (Phase 6)
* **CSRF guard** on every mutating admin route + `SameSite=Strict`
  cookie attribute. (Phase 8)
* **Rate-limited login** on the admin API; per-IP failure ban with
  decay matches the SFTP-side brute-force defense. (Phase 8)
* **TLS for the admin port**; self-signed certificate generated on
  first boot with a LOUD startup warning instructing the operator to
  replace it. (Phase 8)

---

## Reporting

Security issues should not be filed publicly on GitHub. Contact path
will be added in Phase 10 once the project has a published release.
