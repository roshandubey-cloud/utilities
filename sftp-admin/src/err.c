#include "sftpadmin/err.h"

#include <errno.h>

// String table. Index matches sa_err_t enum value 1:1; the unit test
// "test_err.c" walks every index from 0 to SA_ERR__COUNT-1 and asserts a
// non-NULL, non-empty string, so a missing or out-of-order entry fails
// loudly at test time rather than mysteriously at runtime.
static const char *const SA_ERR_NAMES[] = {
    [SA_OK]                   = "ok",

    [SA_ERR_INVAL]            = "invalid argument",
    [SA_ERR_NOMEM]            = "out of memory",
    [SA_ERR_NOSYS]            = "operation not supported on this platform",
    [SA_ERR_BUG]              = "internal invariant violated",

    [SA_ERR_IO]               = "I/O error",
    [SA_ERR_NOENT]            = "no such file or directory",
    [SA_ERR_EXISTS]           = "already exists",
    [SA_ERR_PERM]             = "permission denied",
    [SA_ERR_TOOBIG]           = "value too large",
    [SA_ERR_PATH_ESCAPE]      = "path attempted to escape jail",

    [SA_ERR_PARSE]            = "parse error",
    [SA_ERR_SCHEMA]           = "schema validation failed",
    [SA_ERR_DUPLICATE]        = "duplicate value where unique required",

    [SA_ERR_DB_OPEN]          = "database open failed",
    [SA_ERR_DB_SCHEMA]        = "database schema migration failed",
    [SA_ERR_DB_BUSY]          = "database busy",
    [SA_ERR_DB_QUERY]         = "database query failed",

    [SA_ERR_AUTH]             = "authentication failed",
    [SA_ERR_BANNED]           = "temporarily banned (brute-force defense)",
    [SA_ERR_NOT_ASSIGNED]     = "user not assigned to this listener",
    [SA_ERR_USER_DISABLED]    = "user disabled or expired",
    [SA_ERR_OP_DENIED]        = "operation denied by permission flag",

    [SA_ERR_CRYPTO]           = "cryptographic operation failed",
    [SA_ERR_KEY_FORMAT]       = "unrecognised public key format",
    [SA_ERR_HOSTKEY_LOCKED]   = "host key encrypted; master key not loaded",

    [SA_ERR_CTRL_FRAME]       = "malformed control-socket frame",
    [SA_ERR_CTRL_UNKNOWN_CMD] = "unknown control command",

    [SA_ERR_SHUTDOWN]         = "daemon is shutting down",
    [SA_ERR_TIMEOUT]          = "operation timed out",
};

const char *sa_err_str(sa_err_t e) {
    // Defence in depth: an out-of-range value (corrupted memory, future
    // code path returning an integer cast through sa_err_t) returns the
    // BUG string rather than reading off the end of the array.
    if ((int)e < 0 || (int)e >= (int)SA_ERR__COUNT) {
        return SA_ERR_NAMES[SA_ERR_BUG];
    }
    const char *s = SA_ERR_NAMES[e];
    return s ? s : SA_ERR_NAMES[SA_ERR_BUG];
}

sa_err_t sa_err_from_errno(int e) {
    switch (e) {
        case 0:           return SA_OK;
        case ENOMEM:      return SA_ERR_NOMEM;
        case EINVAL:      return SA_ERR_INVAL;
        case ENOENT:      return SA_ERR_NOENT;
        case EEXIST:      return SA_ERR_EXISTS;
        case EACCES:
        case EPERM:       return SA_ERR_PERM;
        case ENOTSUP:     return SA_ERR_NOSYS;
        case ETIMEDOUT:   return SA_ERR_TIMEOUT;
        case EFBIG:
        case EOVERFLOW:   return SA_ERR_TOOBIG;
        default:          return SA_ERR_IO;
    }
}

bool sa_err_is_expected(sa_err_t e) {
    switch (e) {
        case SA_OK:
        case SA_ERR_AUTH:
        case SA_ERR_BANNED:
        case SA_ERR_NOT_ASSIGNED:
        case SA_ERR_USER_DISABLED:
        case SA_ERR_OP_DENIED:
        case SA_ERR_NOENT:
        case SA_ERR_TIMEOUT:
            return true;
        default:
            return false;
    }
}
