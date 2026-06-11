// sftpadmind — supervisor entrypoint. Portable C: builds and runs on
// Linux, macOS, BSD, and Windows (MSVC / MinGW).
//
// Phase 1: parses args, loads config, initialises the logger, prints a
// startup banner, then waits on a portable signal-driven flag for
// SIGTERM / SIGINT (or the equivalent Windows console-control events)
// and exits cleanly.
//
// We avoid getopt/getopt_long — they aren't present in MSVC and a tiny
// hand-rolled parser is clearer than dragging in a polyfill.

#include "sftpadmin/config.h"
#include "sftpadmin/err.h"
#include "sftpadmin/log.h"
#include "sftpadmin/portable.h"
#include "sftpadmin/version.h"

#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static volatile int g_should_stop = 0;

static void usage(FILE *out, const char *argv0) {
    fprintf(out,
        "sftpadmind %s\n"
        "Usage: %s [options]\n"
        "  -c, --config PATH   Path to JSON config (required for normal run)\n"
        "  -V, --version       Print version and exit\n"
        "  -h, --help          Show this help and exit\n"
        "\n"
        "Phase 1 of the daemon: configuration + logging come online.\n"
        "Listener supervision and the admin API land in later phases.\n",
        SFTPADMIN_VERSION, argv0);
}

// Minimal argv parser. Recognises:
//   -h / --help
//   -V / --version
//   -c PATH / --config PATH / --config=PATH
// Returns:
//    0 on success (out_config may be NULL)
//   -1 on parse error (message written to stderr)
//    1 if help printed (caller should exit 0)
//    2 if version printed (caller should exit 0)
static int parse_args(int argc, char **argv, const char **out_config) {
    *out_config = NULL;
    for (int i = 1; i < argc; i++) {
        const char *a = argv[i];
        if (!strcmp(a, "-h") || !strcmp(a, "--help")) {
            usage(stdout, argv[0]);
            return 1;
        }
        if (!strcmp(a, "-V") || !strcmp(a, "--version")) {
            fprintf(stdout, "sftpadmind %s\n", SFTPADMIN_VERSION);
            return 2;
        }
        if (!strcmp(a, "-c") || !strcmp(a, "--config")) {
            if (i + 1 >= argc) {
                fprintf(stderr, "error: %s requires a path argument\n", a);
                return -1;
            }
            *out_config = argv[++i];
            continue;
        }
        if (!strncmp(a, "--config=", 9)) {
            *out_config = a + 9;
            continue;
        }
        fprintf(stderr, "error: unknown argument %s\n", a);
        usage(stderr, argv[0]);
        return -1;
    }
    return 0;
}

int main(int argc, char **argv) {
    const char *config_path = NULL;
    int pr = parse_args(argc, argv, &config_path);
    if (pr < 0) return 2;
    if (pr == 1 || pr == 2) return 0;

    (void)sa_log_init();

    if (!config_path) {
        sa_log_error("startup", "no config path provided (-c REQUIRED)",
            SA_LOG_END);
        usage(stderr, argv[0]);
        return 2;
    }

    sa_config_t cfg;
    sa_err_t e = sa_config_load(config_path, &cfg);
    if (e != SA_OK) {
        sa_log_err("startup", "could not load config", e,
            SA_LOG_KV("path", config_path), SA_LOG_END);
        return 1;
    }

    sa_log_set_level(cfg.log_level);
    if (cfg.log_file)        (void)sa_log_open_file(cfg.log_file);
    if (cfg.log_to_syslog)   (void)sa_log_open_syslog("sftpadmind");

    if (sa_install_termination_handlers(&g_should_stop) != 0) {
        sa_log_err("startup", "could not install signal handlers",
            sa_err_from_errno(errno), SA_LOG_END);
        sa_config_free(&cfg);
        return 1;
    }

    sa_log_info("startup", "sftpadmind starting",
        SA_LOG_KV("version", SFTPADMIN_VERSION),
        SA_LOG_KV("config",  config_path),
        SA_LOG_KV("db",      cfg.db_path),
        SA_LOG_KV_INT("admin_port", (long long)cfg.admin_port),
        SA_LOG_END);

    // Phase 1 main loop: wait on the termination flag. The real
    // supervisor (Phase 7) replaces this with fork/launch + control
    // socket + drain.
    sa_wait_for_termination(&g_should_stop);

    sa_log_info("shutdown", "received termination signal; cleaning up",
        SA_LOG_END);

    sa_config_free(&cfg);
    sa_log_close();
    return 0;
}
