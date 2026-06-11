// Portable shims for the tiny platform-divergent bits Phase 1 needs.
//
// Goal: the rest of the codebase looks identical on Linux, macOS, BSD,
// and Windows; the #ifdef noise is confined here. This file is small on
// purpose — every leaf is one of:
//
//   sa_mutex_t / sa_mutex_init / lock / unlock / destroy
//     POSIX:   pthread_mutex_t (pthreads — universally available on POSIX)
//     Windows: CRITICAL_SECTION (cheap user-mode mutex; no kernel call)
//
//   sa_pid_t / sa_getpid()
//     long long that fits a Windows DWORD or a POSIX pid_t.
//
//   sa_time_iso8601_utc(buf, cap)
//     Writes a fixed-format UTC timestamp ("2026-06-11T17:55:38.134Z")
//     into buf. Implementation uses C11 timespec_get + gmtime_s/gmtime_r,
//     both of which exist on every supported platform.
//
//   sa_default_data_dir(buf, cap)
//     Per-OS XDG / Library / APPDATA convention. Used by the config
//     loader so we don't bake "/var/lib/sftpadmin" into a Windows
//     binary's defaults.
//
// We deliberately do NOT abstract fork/exec or socket types here. Those
// land in later phases where the divergence is large enough to deserve
// their own files.
#ifndef SFTPADMIN_PORTABLE_H
#define SFTPADMIN_PORTABLE_H

#include <stddef.h>
#include <stdbool.h>

#ifdef _WIN32
  #ifndef WIN32_LEAN_AND_MEAN
    #define WIN32_LEAN_AND_MEAN
  #endif
  #include <windows.h>
  typedef CRITICAL_SECTION sa_mutex_t;
  #define SA_HAVE_SYSLOG 0
  #define SA_PATHSEP "\\"
#else
  #include <pthread.h>
  typedef pthread_mutex_t sa_mutex_t;
  // POSIX has syslog; we still gate inclusion at the .c so a stripped-
  // down POSIX (musl static, sandbox) without syslog support builds.
  #define SA_HAVE_SYSLOG 1
  #define SA_PATHSEP "/"
#endif

// Lifecycle. sa_mutex_init must be called before the first lock; there
// is no static initializer that works on Windows. The logger calls it
// from sa_log_init() under a one-time guard.
void sa_mutex_init   (sa_mutex_t *m);
void sa_mutex_lock   (sa_mutex_t *m);
void sa_mutex_unlock (sa_mutex_t *m);
void sa_mutex_destroy(sa_mutex_t *m);

// Process id. Returned as long long so the logger doesn't need a
// per-platform format specifier.
long long sa_getpid(void);

// Fills buf with an ISO-8601 UTC timestamp ending in 'Z', millisecond
// precision: "2026-06-11T17:55:38.134Z". Returns the number of bytes
// written (excluding the NUL). If cap is too small, writes "" and
// returns 0.
size_t sa_time_iso8601_utc(char *buf, size_t cap);

// Writes the platform-appropriate default data directory for the user
// running the process:
//   Linux/BSD:  $XDG_DATA_HOME/sftpadmin  or  $HOME/.local/share/sftpadmin
//   macOS:      $HOME/Library/Application Support/sftpadmin
//   Windows:    %APPDATA%\sftpadmin
//
// On lookup failure (no $HOME, no %APPDATA%) writes a fixed fallback
// inside the current working directory and returns 0 so the caller can
// log a warning.
//
// Returns 1 on success, 0 on fallback. Never NUL-truncates without
// writing SOMETHING.
int sa_default_data_dir(char *buf, size_t cap);

// Portable signal-wait. Blocks until SIGTERM / SIGINT arrives (Windows:
// the corresponding console-control events plus CTRL+C). Returns when
// either fires. Implementation is a sleep loop polling a g_should_stop
// flag set by the signal handler — accurate enough for daemon-shutdown
// semantics and works on every platform.
void sa_wait_for_termination(volatile int *stop_flag);

// Install handlers that set *stop_flag = 1 on SIGTERM/SIGINT (and the
// Windows console-control equivalents). On POSIX also ignores SIGPIPE.
// Returns 0 on success, -1 on failure (logged by caller).
int sa_install_termination_handlers(volatile int *stop_flag);

#endif // SFTPADMIN_PORTABLE_H
