// Structured JSON-lines logger. See log.h for the contract.
//
// Implementation notes:
//   * We write to a stack buffer first, then a single write(2) per record
//     into each sink. That makes per-record output atomic at the kernel
//     level even without the mutex on POSIX pipes; the mutex is still
//     required for ordering on file fds, since write(2) can be split.
//   * JSON escaping is restricted to the seven characters the spec
//     requires (and "/" left unescaped). We do NOT depend on cJSON for
//     emission — the logger needs to be available before cJSON is, and
//     calling into a third-party allocator from a SIGTERM handler would
//     be unsound.
//   * We tolerate sinks dropping out (file unlink, syslog daemon gone)
//     without crashing — failures decay to stderr-only.

#include "sftpadmin/log.h"

#include <errno.h>
#include <fcntl.h>
#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/time.h>
#include <syslog.h>
#include <time.h>
#include <unistd.h>

static struct {
    pthread_mutex_t mu;
    sa_log_level_t  level;
    int             file_fd;       // -1 when no file sink
    bool            syslog_open;
    bool            init_done;
} G = {
    .mu          = PTHREAD_MUTEX_INITIALIZER,
    .level       = SA_LOG_INFO,
    .file_fd     = -1,
    .syslog_open = false,
    .init_done   = false,
};

static void log_atexit(void) { sa_log_close(); }

sa_err_t sa_log_init(void) {
    pthread_mutex_lock(&G.mu);
    if (!G.init_done) {
        G.init_done = true;
        atexit(log_atexit);
    }
    pthread_mutex_unlock(&G.mu);
    return SA_OK;
}

void sa_log_set_level(sa_log_level_t lvl) {
    pthread_mutex_lock(&G.mu);
    G.level = lvl;
    pthread_mutex_unlock(&G.mu);
}

sa_log_level_t sa_log_level(void) {
    pthread_mutex_lock(&G.mu);
    sa_log_level_t l = G.level;
    pthread_mutex_unlock(&G.mu);
    return l;
}

sa_err_t sa_log_open_file(const char *path) {
    if (!path || !*path) return SA_ERR_INVAL;
    // O_CLOEXEC so a fork doesn't bleed the log fd into worker processes
    // that should write through their own sa_log_reset(). 0640 because the
    // log can contain audit data that's useful to a security group but
    // shouldn't be world-readable.
    int fd = open(path, O_WRONLY | O_APPEND | O_CREAT | O_CLOEXEC, 0640);
    if (fd < 0) return sa_err_from_errno(errno);

    pthread_mutex_lock(&G.mu);
    int prev = G.file_fd;
    G.file_fd = fd;
    pthread_mutex_unlock(&G.mu);
    if (prev >= 0) (void)close(prev);
    return SA_OK;
}

sa_err_t sa_log_open_syslog(const char *ident) {
    if (!ident) return SA_ERR_INVAL;
    pthread_mutex_lock(&G.mu);
    if (G.syslog_open) closelog();
    openlog(ident, LOG_PID | LOG_NDELAY | LOG_CONS, LOG_DAEMON);
    G.syslog_open = true;
    pthread_mutex_unlock(&G.mu);
    return SA_OK;
}

void sa_log_reset(void) {
    pthread_mutex_init(&G.mu, NULL);
    if (G.file_fd >= 0) {
        (void)close(G.file_fd);
        G.file_fd = -1;
    }
    if (G.syslog_open) {
        closelog();
        G.syslog_open = false;
    }
}

void sa_log_close(void) {
    pthread_mutex_lock(&G.mu);
    if (G.file_fd >= 0) {
        (void)close(G.file_fd);
        G.file_fd = -1;
    }
    if (G.syslog_open) {
        closelog();
        G.syslog_open = false;
    }
    pthread_mutex_unlock(&G.mu);
}

// ---------------------------------------------------------------------------
// JSON line construction.
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

static int syslog_priority(sa_log_level_t l) {
    switch (l) {
        case SA_LOG_DEBUG: return LOG_DEBUG;
        case SA_LOG_INFO:  return LOG_INFO;
        case SA_LOG_WARN:  return LOG_WARNING;
        case SA_LOG_ERROR: return LOG_ERR;
    }
    return LOG_INFO;
}

// Append a JSON-escaped string into buf[]. Returns bytes written. Will not
// overflow `cap`; truncates with terminator. We escape: " \ \b \f \n \r \t
// and any byte < 0x20 as \u00XX. UTF-8 bytes >= 0x80 pass through unchanged
// — JSON-RFC requires that, and validating UTF-8 byte-by-byte here would
// be a lot of cycles for log output.
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

static size_t json_kv_str(char *buf, size_t cap, const char *k, const char *v, bool leading_comma) {
    if (cap == 0) return 0;
    size_t n = 0;
    if (leading_comma && n < cap) buf[n++] = ',';
    n += json_str(&buf[n], cap - n, k);
    if (n < cap) buf[n++] = ':';
    n += json_str(&buf[n], cap - n, v ? v : "");
    if (n < cap) buf[n] = '\0';
    return n;
}

static size_t json_kv_int(char *buf, size_t cap, const char *k, long long v, bool leading_comma) {
    if (cap == 0) return 0;
    size_t n = 0;
    if (leading_comma && n < cap) buf[n++] = ',';
    n += json_str(&buf[n], cap - n, k);
    if (n < cap) buf[n++] = ':';
    char num[32];
    int w = snprintf(num, sizeof(num), "%lld", v);
    if (w > 0) {
        size_t l = (size_t)w;
        if (n + l < cap) {
            memcpy(&buf[n], num, l);
            n += l;
        }
    }
    if (n < cap) buf[n] = '\0';
    return n;
}

static size_t json_kv_bool(char *buf, size_t cap, const char *k, bool v, bool leading_comma) {
    if (cap == 0) return 0;
    size_t n = 0;
    if (leading_comma && n < cap) buf[n++] = ',';
    n += json_str(&buf[n], cap - n, k);
    if (n < cap) buf[n++] = ':';
    const char *t = v ? "true" : "false";
    size_t l = strlen(t);
    if (n + l < cap) {
        memcpy(&buf[n], t, l);
        n += l;
    }
    if (n < cap) buf[n] = '\0';
    return n;
}

static size_t iso8601_now(char *buf, size_t cap) {
    struct timeval tv;
    if (gettimeofday(&tv, NULL) != 0) {
        if (cap > 0) buf[0] = '\0';
        return 0;
    }
    struct tm tm;
    gmtime_r(&tv.tv_sec, &tm);
    int w = snprintf(buf, cap,
        "%04d-%02d-%02dT%02d:%02d:%02d.%03ldZ",
        tm.tm_year + 1900, tm.tm_mon + 1, tm.tm_mday,
        tm.tm_hour, tm.tm_min, tm.tm_sec, (long)(tv.tv_usec / 1000));
    return (w > 0 && (size_t)w < cap) ? (size_t)w : 0;
}

void sa_log_emit(sa_log_level_t lvl, const char *subsys, const char *msg, ...) {
    // Fast path: level filter without any allocation/format work.
    pthread_mutex_lock(&G.mu);
    sa_log_level_t cur = G.level;
    int file_fd = G.file_fd;
    bool syslog_open = G.syslog_open;
    pthread_mutex_unlock(&G.mu);
    if (lvl < cur) return;

    // 4 KiB caps a single log record. The build spec prohibits unbounded
    // log floods; if a caller hands us a 1 MB SQL string it gets truncated
    // here rather than balloon the logs.
    char line[4096];
    size_t n = 0;

    if (n < sizeof(line)) line[n++] = '{';
    char ts[40];
    iso8601_now(ts, sizeof(ts));

    n += json_kv_str(&line[n], sizeof(line) - n, "ts",     ts,                       false);
    n += json_kv_str(&line[n], sizeof(line) - n, "level",  level_name(lvl),          true);
    n += json_kv_str(&line[n], sizeof(line) - n, "subsys", subsys ? subsys : "core", true);
    n += json_kv_str(&line[n], sizeof(line) - n, "msg",    msg    ? msg    : "",     true);
    n += json_kv_int(&line[n], sizeof(line) - n, "pid",    (long long)getpid(),      true);

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

    if (n + 2 >= sizeof(line)) {
        // Out of space for the trailer; truncate-and-close so the line is
        // still valid JSON.
        n = sizeof(line) - 3;
    }
    line[n++] = '}';
    line[n++] = '\n';
    line[n] = '\0';

    // Stable: try every sink, ignore individual failures so one bad sink
    // doesn't take logging down entirely.
    pthread_mutex_lock(&G.mu);
    (void)!write(STDERR_FILENO, line, n);
    if (file_fd >= 0) (void)!write(file_fd, line, n);
    pthread_mutex_unlock(&G.mu);

    if (syslog_open) {
        // syslog handles its own locking + framing; strip our trailing
        // newline so it's not double-counted.
        if (n > 0 && line[n - 1] == '\n') line[n - 1] = '\0';
        syslog(syslog_priority(lvl), "%s", line);
    }
}
