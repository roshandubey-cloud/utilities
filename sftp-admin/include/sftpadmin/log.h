// Structured JSON-lines logger.
//
// Output sinks:
//   * Always: stderr (so systemd journal captures it when the unit runs).
//   * Optional file path (set via sa_log_open_file).
//   * Optional syslog (set via sa_log_open_syslog with an ident).
//
// Format: one JSON object per line, with stable keys:
//   ts        ISO-8601 millisecond UTC, eg 2026-06-11T09:47:01.234Z
//   level     debug|info|warn|error
//   subsys    short string set per-call site (eg "config", "listener.start")
//   msg       free-form message; values that need structure go in fields
//   pid       process id (so per-process forks can be filtered)
//   err       sa_err_str() result, ONLY when level=error and an err was given
//   *         arbitrary key=value extras from sa_log_kv()
//
// Thread-safety: every emission acquires an internal mutex so a multi-fd
// write doesn't interleave bytes. Forked children must call sa_log_reset()
// to re-init the mutex and clear the parent's fd handles.
//
// We deliberately do NOT use printf-style %s formatting in messages. Pass
// the literal as `msg` and structured data via key/value pairs — that's
// how production log search actually works.
#ifndef SFTPADMIN_LOG_H
#define SFTPADMIN_LOG_H

#include "sftpadmin/err.h"

#include <stdarg.h>
#include <stdbool.h>
#include <stddef.h>

typedef enum {
    SA_LOG_DEBUG = 0,
    SA_LOG_INFO  = 1,
    SA_LOG_WARN  = 2,
    SA_LOG_ERROR = 3,
} sa_log_level_t;

// One-time process init. Safe to call multiple times; idempotent.
sa_err_t sa_log_init(void);

// Set the minimum level. Calls below this level are dropped before any
// JSON encoding, so a debug-flood in production has zero cost.
void sa_log_set_level(sa_log_level_t lvl);
sa_log_level_t sa_log_level(void);

// Reopen-to-file. Path is taken by value; we open in append mode O_CLOEXEC.
// Returns SA_ERR_IO on failure; the existing file sink (if any) stays open
// on failure so a bad reopen doesn't lose logging.
sa_err_t sa_log_open_file(const char *path);

// Enable syslog with the given ident. ident must remain valid for the
// process lifetime (typically a static string). Returns SA_OK or SA_ERR_*.
sa_err_t sa_log_open_syslog(const char *ident);

// Re-initialise after fork. Closes any inherited file fds, recreates the
// mutex. MUST be called in every fork child before its first log call.
void sa_log_reset(void);

// Close sinks. Idempotent. Called via atexit() from sa_log_init().
void sa_log_close(void);

// ---------------------------------------------------------------------------
// Emission API. There's exactly one core function (sa_log_emit) and four
// thin wrappers for ergonomics. We use a varargs key=value tail terminated
// by NULL (SA_LOG_END) rather than printf format strings to discourage
// log-injection through user-controlled data.
//
// Example:
//   sa_log_info("listener", "started", SA_LOG_KV("addr", "0.0.0.0:2222"),
//                                       SA_LOG_KV_INT("pid", child_pid),
//                                       SA_LOG_END);
// ---------------------------------------------------------------------------
typedef struct {
    const char *key;
    enum { SA_LOG_VAL_STR, SA_LOG_VAL_INT, SA_LOG_VAL_BOOL, SA_LOG_VAL_END } kind;
    union {
        const char *s;
        long long   i;
        bool        b;
    } v;
} sa_log_kv_t;

#define SA_LOG_KV(K, V)      ((sa_log_kv_t){.key=(K), .kind=SA_LOG_VAL_STR,  .v.s=(V)})
#define SA_LOG_KV_INT(K, V)  ((sa_log_kv_t){.key=(K), .kind=SA_LOG_VAL_INT,  .v.i=(long long)(V)})
#define SA_LOG_KV_BOOL(K, V) ((sa_log_kv_t){.key=(K), .kind=SA_LOG_VAL_BOOL, .v.b=(V)})
#define SA_LOG_END           ((sa_log_kv_t){.key=NULL, .kind=SA_LOG_VAL_END})

void sa_log_emit(sa_log_level_t lvl, const char *subsys, const char *msg, ...);

#define sa_log_debug(subsys, msg, ...) sa_log_emit(SA_LOG_DEBUG, (subsys), (msg), __VA_ARGS__)
#define sa_log_info(subsys,  msg, ...) sa_log_emit(SA_LOG_INFO,  (subsys), (msg), __VA_ARGS__)
#define sa_log_warn(subsys,  msg, ...) sa_log_emit(SA_LOG_WARN,  (subsys), (msg), __VA_ARGS__)
#define sa_log_error(subsys, msg, ...) sa_log_emit(SA_LOG_ERROR, (subsys), (msg), __VA_ARGS__)

// Convenience: error log with an attached sa_err_t. The "err" key is
// added automatically.
#define sa_log_err(subsys, msg, e, ...) \
    sa_log_emit(SA_LOG_ERROR, (subsys), (msg), SA_LOG_KV("err", sa_err_str(e)), __VA_ARGS__)

#endif // SFTPADMIN_LOG_H
