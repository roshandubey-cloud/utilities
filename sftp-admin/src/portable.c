// Portable shim implementations. See portable.h for the contract.
//
// Implementation notes per platform:
//
//   POSIX (Linux, macOS, BSD)
//     Mutex     = pthread_mutex_t with default attrs.
//     Time      = clock_gettime(CLOCK_REALTIME) (POSIX) -> gmtime_r.
//     PID       = getpid().
//     Default
//      data dir = XDG_DATA_HOME/sftpadmin
//                  -> HOME/.local/share/sftpadmin (Linux/BSD)
//                  -> HOME/Library/Application Support/sftpadmin (macOS).
//     Signals   = sigaction(SIGTERM)+sigaction(SIGINT), SIGPIPE -> SIG_IGN.
//     Wait      = pause() under a loop, woken by handler.
//
//   Windows
//     Mutex     = CRITICAL_SECTION (process-local, no kernel call).
//     Time      = GetSystemTimePreciseAsFileTime -> gmtime_s.
//     PID       = GetCurrentProcessId().
//     Default
//      data dir = %APPDATA%\sftpadmin (typically
//                 C:\Users\<user>\AppData\Roaming\sftpadmin).
//     Signals   = signal(SIGTERM)+signal(SIGINT) +
//                 SetConsoleCtrlHandler for service-style Ctrl+C/CLOSE.
//     Wait      = Sleep(100) loop polling stop flag.

#include "sftpadmin/portable.h"

#include <stdio.h>
#include <string.h>
#include <time.h>

#ifdef _WIN32
  #include <signal.h>
  #include <process.h>     // _getpid
  // <windows.h> already pulled in by portable.h
#else
  #include <errno.h>
  #include <pthread.h>
  #include <signal.h>
  #include <stdlib.h>
  #include <sys/stat.h>
  #include <sys/utsname.h>
  #include <unistd.h>
#endif

// ---------------------------------------------------------------------------
// Mutex
// ---------------------------------------------------------------------------
void sa_mutex_init(sa_mutex_t *m) {
#ifdef _WIN32
    InitializeCriticalSection(m);
#else
    pthread_mutex_init(m, NULL);
#endif
}

void sa_mutex_lock(sa_mutex_t *m) {
#ifdef _WIN32
    EnterCriticalSection(m);
#else
    pthread_mutex_lock(m);
#endif
}

void sa_mutex_unlock(sa_mutex_t *m) {
#ifdef _WIN32
    LeaveCriticalSection(m);
#else
    pthread_mutex_unlock(m);
#endif
}

void sa_mutex_destroy(sa_mutex_t *m) {
#ifdef _WIN32
    DeleteCriticalSection(m);
#else
    pthread_mutex_destroy(m);
#endif
}

// ---------------------------------------------------------------------------
// PID
// ---------------------------------------------------------------------------
long long sa_getpid(void) {
#ifdef _WIN32
    return (long long)GetCurrentProcessId();
#else
    return (long long)getpid();
#endif
}

// ---------------------------------------------------------------------------
// Time
// ---------------------------------------------------------------------------
size_t sa_time_iso8601_utc(char *buf, size_t cap) {
    if (!buf || cap < 25) {
        if (buf && cap > 0) buf[0] = '\0';
        return 0;
    }
#ifdef _WIN32
    FILETIME ft;
    GetSystemTimePreciseAsFileTime(&ft);
    // FILETIME is 100-ns intervals since 1601-01-01. Convert to Unix
    // epoch seconds + millis.
    unsigned long long total = ((unsigned long long)ft.dwHighDateTime << 32) | ft.dwLowDateTime;
    // 11644473600 seconds between 1601 and 1970.
    unsigned long long unix_ns = (total - 116444736000000000ULL) * 100ULL;
    time_t secs = (time_t)(unix_ns / 1000000000ULL);
    long    ms  = (long)((unix_ns / 1000000ULL) % 1000ULL);
    struct tm t;
    gmtime_s(&t, &secs);
    int w = snprintf(buf, cap,
        "%04d-%02d-%02dT%02d:%02d:%02d.%03ldZ",
        t.tm_year + 1900, t.tm_mon + 1, t.tm_mday,
        t.tm_hour, t.tm_min, t.tm_sec, ms);
    return (w > 0 && (size_t)w < cap) ? (size_t)w : 0;
#else
    struct timespec ts;
    if (clock_gettime(CLOCK_REALTIME, &ts) != 0) {
        buf[0] = '\0';
        return 0;
    }
    struct tm t;
    gmtime_r(&ts.tv_sec, &t);
    long ms = ts.tv_nsec / 1000000L;
    int w = snprintf(buf, cap,
        "%04d-%02d-%02dT%02d:%02d:%02d.%03ldZ",
        t.tm_year + 1900, t.tm_mon + 1, t.tm_mday,
        t.tm_hour, t.tm_min, t.tm_sec, ms);
    return (w > 0 && (size_t)w < cap) ? (size_t)w : 0;
#endif
}

// ---------------------------------------------------------------------------
// Default data directory
// ---------------------------------------------------------------------------
static int join_path(char *buf, size_t cap, const char *a, const char *b) {
    int w = snprintf(buf, cap, "%s%s%s", a, SA_PATHSEP, b);
    return (w > 0 && (size_t)w < cap) ? 1 : 0;
}

int sa_default_data_dir(char *buf, size_t cap) {
    if (!buf || cap < 16) { if (buf && cap) buf[0] = '\0'; return 0; }
#ifdef _WIN32
    char *appdata = NULL;
    size_t len = 0;
    if (_dupenv_s(&appdata, &len, "APPDATA") == 0 && appdata && *appdata) {
        int ok = join_path(buf, cap, appdata, "sftpadmin");
        free(appdata);
        return ok;
    }
    // Fallback to the working dir.
    int w = snprintf(buf, cap, ".%ssftpadmin-data", SA_PATHSEP);
    (void)w;
    return 0;
#else
    const char *xdg = getenv("XDG_DATA_HOME");
    const char *home = getenv("HOME");
  #ifdef __APPLE__
    if (home && *home) {
        // ~/Library/Application Support/sftpadmin
        char tmp[1024];
        int w = snprintf(tmp, sizeof(tmp), "%s/Library/Application Support", home);
        (void)w;
        return join_path(buf, cap, tmp, "sftpadmin");
    }
  #else
    if (xdg && *xdg)         return join_path(buf, cap, xdg,  "sftpadmin");
    if (home && *home) {
        char tmp[1024];
        int w = snprintf(tmp, sizeof(tmp), "%s/.local/share", home);
        (void)w;
        return join_path(buf, cap, tmp, "sftpadmin");
    }
  #endif
    int w = snprintf(buf, cap, "./sftpadmin-data");
    (void)w;
    return 0;
#endif
}

// ---------------------------------------------------------------------------
// Signal handling + wait loop.
//
// We store the caller's stop-flag pointer in a file-scope variable so the
// minimal C signal handler (which can't take user data) can find it.
// Multiple installations stomp each other, but in this daemon only one
// flag exists.
// ---------------------------------------------------------------------------
static volatile int *g_stop_flag = NULL;

#ifdef _WIN32
static BOOL WINAPI on_console_ctrl(DWORD ctrl) {
    if (ctrl == CTRL_C_EVENT     || ctrl == CTRL_BREAK_EVENT ||
        ctrl == CTRL_CLOSE_EVENT || ctrl == CTRL_LOGOFF_EVENT ||
        ctrl == CTRL_SHUTDOWN_EVENT) {
        if (g_stop_flag) *g_stop_flag = 1;
        return TRUE;
    }
    return FALSE;
}
#endif

static void on_term(int signo) {
    (void)signo;
    if (g_stop_flag) *g_stop_flag = 1;
}

int sa_install_termination_handlers(volatile int *stop_flag) {
    g_stop_flag = stop_flag;

#ifdef _WIN32
    // signal() exists on Windows but lacks a couple of UNIX-style
    // signals; SIGTERM and SIGINT both work and are the typical
    // shutdown vectors when a console hosts the process.
    if (signal(SIGTERM, on_term) == SIG_ERR) return -1;
    if (signal(SIGINT,  on_term) == SIG_ERR) return -1;
    if (!SetConsoleCtrlHandler(on_console_ctrl, TRUE))  return -1;
    return 0;
#else
    struct sigaction sa;
    memset(&sa, 0, sizeof(sa));
    sa.sa_handler = on_term;
    sigemptyset(&sa.sa_mask);
    sa.sa_flags = SA_RESTART;
    if (sigaction(SIGTERM, &sa, NULL) != 0) return -1;
    if (sigaction(SIGINT,  &sa, NULL) != 0) return -1;

    // SIGPIPE: ignore. Network code wants EPIPE returns instead of
    // dying when the peer slams the connection.
    struct sigaction ign;
    memset(&ign, 0, sizeof(ign));
    ign.sa_handler = SIG_IGN;
    if (sigaction(SIGPIPE, &ign, NULL) != 0) return -1;
    return 0;
#endif
}

void sa_wait_for_termination(volatile int *stop_flag) {
    if (!stop_flag) return;
    while (!*stop_flag) {
#ifdef _WIN32
        Sleep(100);
#else
        // pause() blocks until any signal arrives. EINTR returns mean
        // a signal was handled — re-check the flag.
        (void)pause();
        if (errno != EINTR && errno != 0) {
            // Defensive: avoid a tight loop if pause is broken in some
            // exotic libc. 100 ms sleep is fine for shutdown semantics.
            struct timespec ts = { .tv_sec = 0, .tv_nsec = 100 * 1000 * 1000 };
            nanosleep(&ts, NULL);
            errno = 0;
        }
#endif
    }
}
