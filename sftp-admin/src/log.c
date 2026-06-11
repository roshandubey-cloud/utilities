// Portable structured JSON-lines logger. See log.h for the contract.
//
// Sinks (all optional, additive):
//   * stderr   — always; the universally-available daemon log sink.
//   * file     — via fopen("ab"); per-line atomicity comes from our
//                mutex, not from O_APPEND alone (which doesn't guarantee
//                atomicity across all platforms for our record sizes).
//   * syslog   — POSIX only. The SA_HAVE_SYSLOG macro from portable.h
//                gates inclusion; on Windows the call is a no-op.
//
// JSON escaping is handwritten (no allocator dependency) so the logger
// is callable from signal-adjacent code on POSIX without invoking
// async-unsafe APIs through cJSON.

#include "sftpadmin/log.h"
#include "sftpadmin/portable.h"

#include <errno.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#if SA_HAVE_SYSLOG
  #include <syslog.h>
#endif

static struct {
    sa_mutex_t    mu;
    bool          mu_init;
    sa_log_level_t level;
    FILE         *file;
    bool          syslog_open;
    bool          init_done;
} G = {
    // .mu intentionally left uninitialised — sa_mutex_init() lazily
    // initialises on first sa_log_init() call. (POSIX accepts the
    // static initializer PTHREAD_MUTEX_INITIALIZER, but Windows
    // CRITICAL_SECTION cannot be statically initialised and pthread
    // is itself an aggregate that trips -Wmissing-braces on some
    // toolchains.)
    .mu_init     = false,
    .level       = SA_LOG_INFO,
    .file        = NULL,
    .syslog_open = false,
    .init_done   = false,
};

// One-time init. Safe to call repeatedly; the inner guard prevents
// double-init of the mutex on Windows (where InitializeCriticalSection
// is NOT idempotent — it leaks if called twice).
static void log_atexit(void) { sa_log_close(); }

sa_err_t sa_log_init(void) {
    if (!G.init_done) {
        if (!G.mu_init) {
            sa_mutex_init(&G.mu);
            G.mu_init = true;
        }
        G.init_done = true;
        atexit(log_atexit);
    }
    return SA_OK;
}

void sa_log_set_level(sa_log_level_t lvl) {
    sa_log_init();
    sa_mutex_lock(&G.mu);
    G.level = lvl;
    sa_mutex_unlock(&G.mu);
}

sa_log_level_t sa_log_level(void) {
    sa_log_init();
    sa_mutex_lock(&G.mu);
    sa_log_level_t l = G.level;
    sa_mutex_unlock(&G.mu);
    return l;
}

sa_err_t sa_log_open_file(const char *path) {
    if (!path || !*path) return SA_ERR_INVAL;
    sa_log_init();
    // "ab" is portable: append + binary. Binary mode matters on Windows
    // so the runtime doesn't translate '\n' to '\r\n' and double our
    // record terminator.
    FILE *f = fopen(path, "ab");
    if (!f) return sa_err_from_errno(errno);
    // Line-buffered isn't enforceable across all libc's; flush per
    // record from the emit path instead.
    setvbuf(f, NULL, _IONBF, 0);

    sa_mutex_lock(&G.mu);
    FILE *prev = G.file;
    G.file = f;
    sa_mutex_unlock(&G.mu);
    if (prev) (void)fclose(prev);
    return SA_OK;
}

sa_err_t sa_log_open_syslog(const char *ident) {
    if (!ident) return SA_ERR_INVAL;
    sa_log_init();
#if SA_HAVE_SYSLOG
    sa_mutex_lock(&G.mu);
    if (G.syslog_open) closelog();
    openlog(ident, LOG_PID | LOG_NDELAY | LOG_CONS, LOG_DAEMON);
    G.syslog_open = true;
    sa_mutex_unlock(&G.mu);
    return SA_OK;
#else
    (void)ident;
    return SA_ERR_NOSYS;
#endif
}

void sa_log_reset(void) {
    // Post-fork reinit. Windows doesn't fork in Phase 1, but the call
    // is still meaningful — it lets a subprocess clear inherited
    // handles before logging on its own.
    if (G.mu_init) sa_mutex_destroy(&G.mu);
    sa_mutex_init(&G.mu);
    G.mu_init = true;
    if (G.file) {
        (void)fclose(G.file);
        G.file = NULL;
    }
#if SA_HAVE_SYSLOG
    if (G.syslog_open) {
        closelog();
        G.syslog_open = false;
    }
#endif
}

void sa_log_close(void) {
    if (!G.mu_init) return;
    sa_mutex_lock(&G.mu);
    if (G.file) {
        (void)fclose(G.file);
        G.file = NULL;
    }
#if SA_HAVE_SYSLOG
    if (G.syslog_open) {
        closelog();
        G.syslog_open = false;
    }
#endif
    sa_mutex_unlock(&G.mu);
}

// ---------------------------------------------------------------------------
// JSON line construction. Hand-rolled; no allocator usage.
// ---------------------------------------------------------------------------
static const char *level_name(sa_log_level_t l) {
    switch (l) {
        case SA_LOG_DEBUG: return "debug";
        case SA_LOG_INFO:  return "info";
        case SA_LOG_WARN:  return "warn";
        case SA_LOG_ERROR: return "error";
    }
    return "info";
}

#if SA_HAVE_SYSLOG
static int syslog_priority(sa_log_level_t l) {
    switch (l) {
        case SA_LOG_DEBUG: return LOG_DEBUG;
        case SA_LOG_INFO:  return LOG_INFO;
        case SA_LOG_WARN:  return LOG_WARNING;
        case SA_LOG_ERROR: return LOG_ERR;
    }
    return LOG_INFO;
}
#endif

static size_t json_str(char *buf, size_t cap, const char *in) {
    if (cap == 0) return 0;
    size_t n = 0;
    if (n < cap) buf[n++] = '"';
    for (const unsigned char *p = (const unsigned char *)in; *p; p++) {
        unsigned char c = *p;
        const char *esc = NULL;
        char escbuf[8];
        switch (c) {
            case '"':  esc = "\\\""; break;
            case '\\': esc = "\\\\"; break;
            case '\b': esc = "\\b";  break;
            case '\f': esc = "\\f";  break;
            case '\n': esc = "\\n";  break;
            case '\r': esc = "\\r";  break;
            case '\t': esc = "\\t";  break;
            default:
                if (c < 0x20) {
                    int w = snprintf(escbuf, sizeof(escbuf), "\\u%04x", c);
                    if (w > 0) esc = escbuf;
                }
        }
        if (esc) {
            size_t l = strlen(esc);
            if (n + l >= cap) goto truncate;
            memcpy(&buf[n], esc, l);
            n += l;
        } else {
            if (n + 1 >= cap) goto truncate;
            buf[n++] = (char)c;
        }
    }
    if (n < cap) buf[n++] = '"';
    if (n < cap) buf[n] = '\0';
    return n;
truncate:
    if (n < cap) buf[n] = '\0';
    return n;
}

static size_t json_kv_str(char *buf, size_t cap, const char *k, const char *v, bool comma) {
    if (cap == 0) return 0;
    size_t n = 0;
    if (comma && n < cap) buf[n++] = ',';
    n += json_str(&buf[n], cap - n, k);
    if (n < cap) buf[n++] = ':';
    n += json_str(&buf[n], cap - n, v ? v : "");
    if (n < cap) buf[n] = '\0';
    return n;
}

static size_t json_kv_int(char *buf, size_t cap, const char *k, long long v, bool comma) {
    if (cap == 0) return 0;
    size_t n = 0;
    if (comma && n < cap) buf[n++] = ',';
    n += json_str(&buf[n], cap - n, k);
    if (n < cap) buf[n++] = ':';
    char num[32];
    int w = snprintf(num, sizeof(num), "%lld", v);
    if (w > 0) {
        size_t l = (size_t)w;
        if (n + l < cap) { memcpy(&buf[n], num, l); n += l; }
    }
    if (n < cap) buf[n] = '\0';
    return n;
}

static size_t json_kv_bool(char *buf, size_t cap, const char *k, bool v, bool comma) {
    if (cap == 0) return 0;
    size_t n = 0;
    if (comma && n < cap) buf[n++] = ',';
    n += json_str(&buf[n], cap - n, k);
    if (n < cap) buf[n++] = ':';
    const char *t = v ? "true" : "false";
    size_t l = strlen(t);
    if (n + l < cap) { memcpy(&buf[n], t, l); n += l; }
    if (n < cap) buf[n] = '\0';
    return n;
}

void sa_log_emit(sa_log_level_t lvl, const char *subsys, const char *msg, ...) {
    sa_log_init();
    // Fast-path level filter; nothing else allocated if we drop.
    sa_mutex_lock(&G.mu);
    sa_log_level_t cur = G.level;
    FILE *file_sink = G.file;
    bool syslog_open = G.syslog_open;
    sa_mutex_unlock(&G.mu);
    if (lvl < cur) return;

    char line[4096];
    size_t n = 0;
    if (n < sizeof(line)) line[n++] = '{';

    char ts[40];
    sa_time_iso8601_utc(ts, sizeof(ts));

    n += json_kv_str(&line[n], sizeof(line) - n, "ts",     ts,                       false);
    n += json_kv_str(&line[n], sizeof(line) - n, "level",  level_name(lvl),          true);
    n += json_kv_str(&line[n], sizeof(line) - n, "subsys", subsys ? subsys : "core", true);
    n += json_kv_str(&line[n], sizeof(line) - n, "msg",    msg    ? msg    : "",     true);
    n += json_kv_int(&line[n], sizeof(line) - n, "pid",    sa_getpid(),              true);

    va_list ap;
    va_start(ap, msg);
    for (;;) {
        sa_log_kv_t kv = va_arg(ap, sa_log_kv_t);
        if (kv.kind == SA_LOG_VAL_END || !kv.key) break;
        switch (kv.kind) {
            case SA_LOG_VAL_STR:
                n += json_kv_str(&line[n], sizeof(line) - n, kv.key, kv.v.s, true);
                break;
            case SA_LOG_VAL_INT:
                n += json_kv_int(&line[n], sizeof(line) - n, kv.key, kv.v.i, true);
                break;
            case SA_LOG_VAL_BOOL:
                n += json_kv_bool(&line[n], sizeof(line) - n, kv.key, kv.v.b, true);
                break;
            case SA_LOG_VAL_END:
                break;
        }
    }
    va_end(ap);

    if (n + 2 >= sizeof(line)) n = sizeof(line) - 3;
    line[n++] = '}';
    line[n++] = '\n';
    line[n]   = '\0';

    sa_mutex_lock(&G.mu);
    fwrite(line, 1, n, stderr);
    fflush(stderr);
    if (file_sink) {
        fwrite(line, 1, n, file_sink);
        fflush(file_sink);
    }
    sa_mutex_unlock(&G.mu);

#if SA_HAVE_SYSLOG
    if (syslog_open) {
        // strip the trailing newline; syslog frames its own records.
        if (n > 0 && line[n - 1] == '\n') line[n - 1] = '\0';
        syslog(syslog_priority(lvl), "%s", line);
    }
#else
    (void)syslog_open;
#endif
}
