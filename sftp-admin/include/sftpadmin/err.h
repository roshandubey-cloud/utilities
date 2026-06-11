// Central error code for sftpadmind. The build spec requires a single enum
// with a to_string mapping; we use that consistently across every subsystem
// rather than passing raw errno or libc -1s up the stack.
//
// Rules:
//   * Every public function returns sa_err_t (or a typed result via output
//     parameter + sa_err_t status). Callers branch on SA_OK.
//   * SA_OK MUST be zero so `if (err) {...}` works.
//   * No errno leakage to log lines or API responses — convert through
//     sa_err_from_errno() at the boundary and lose the raw value.
//   * sa_err_str() returns a stable, allocation-free C string suitable for
//     audit log entries and JSON API responses.
//
// Adding a new code: append to the enum AND to the table in err.c. The
// test suite verifies the enum and table stay in lockstep.
#ifndef SFTPADMIN_ERR_H
#define SFTPADMIN_ERR_H

#include <stdbool.h>

typedef enum {
    SA_OK = 0,

    // Generic / boundary
    SA_ERR_INVAL,           // invalid argument from caller
    SA_ERR_NOMEM,           // allocation failed
    SA_ERR_NOSYS,           // platform / kernel does not support this op
    SA_ERR_BUG,             // internal invariant violated (should be impossible)

    // I/O & filesystem
    SA_ERR_IO,              // generic syscall failure
    SA_ERR_NOENT,           // file/dir does not exist
    SA_ERR_EXISTS,          // unique-constraint / path already exists
    SA_ERR_PERM,            // permission denied at OS level
    SA_ERR_TOOBIG,          // value exceeds a hard cap (e.g. config size)
    SA_ERR_PATH_ESCAPE,     // path attempted to leave jail (security)

    // Parse / validation
    SA_ERR_PARSE,           // could not parse input (JSON / config / pubkey)
    SA_ERR_SCHEMA,          // input parsed but failed schema validation
    SA_ERR_DUPLICATE,       // duplicate name / id where unique required

    // Database
    SA_ERR_DB_OPEN,         // could not open SQLite file
    SA_ERR_DB_SCHEMA,       // schema migration failed / version mismatch
    SA_ERR_DB_BUSY,         // SQLITE_BUSY beyond busy_timeout
    SA_ERR_DB_QUERY,        // generic query failure

    // Auth / SFTP
    SA_ERR_AUTH,            // password / pubkey rejected
    SA_ERR_BANNED,          // IP/user temporarily banned for brute-force
    SA_ERR_NOT_ASSIGNED,    // user is not assigned to this listener
    SA_ERR_USER_DISABLED,   // user exists but is disabled / expired
    SA_ERR_OP_DENIED,       // SFTP op denied by user permission flag

    // Crypto
    SA_ERR_CRYPTO,          // libsodium / libssh crypto failure
    SA_ERR_KEY_FORMAT,      // unrecognised SSH public key format
    SA_ERR_HOSTKEY_LOCKED,  // host key file encrypted, no master key loaded

    // Control / IPC
    SA_ERR_CTRL_FRAME,      // malformed control-socket frame
    SA_ERR_CTRL_UNKNOWN_CMD,// unknown control command

    // Lifecycle
    SA_ERR_SHUTDOWN,        // operation aborted because daemon is shutting down
    SA_ERR_TIMEOUT,         // operation deadline exceeded

    SA_ERR__COUNT           // sentinel; keep last
} sa_err_t;

// Allocation-free; stable for the program lifetime. Safe to embed directly
// in log/audit entries.
const char *sa_err_str(sa_err_t e);

// Convert errno (positive) into the closest sa_err_t. Returns SA_ERR_IO for
// values that have no specific mapping. Callers MUST NOT log the raw errno
// after this — the conversion is intentional.
sa_err_t sa_err_from_errno(int e);

// Returns true iff the code is one we consider "expected operationally"
// (e.g. SA_ERR_AUTH, SA_ERR_BANNED) vs a real bug we want loud. Used by
// the logger to choose severity automatically when a caller doesn't pick.
bool sa_err_is_expected(sa_err_t e);

#endif // SFTPADMIN_ERR_H
