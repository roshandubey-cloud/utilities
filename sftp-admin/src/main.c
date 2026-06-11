// sftpadmind — supervisor entrypoint.
//
// Phase 1: parses args, loads config, initialises the logger, prints a
// startup banner, then sleeps on SIGTERM / SIGINT and exits cleanly. The
// real supervisor (fork-listeners, control socket, drain) lands in
// Phase 7; this file is the eventual home for that loop so I'd rather
// land the skeleton now.

#include "sftpadmin/config.h"
#include "sftpadmin/err.h"
#include "sftpadmin/log.h"
#include "sftpadmin/version.h"

#include <errno.h>
#include <getopt.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

// Volatile sig_atomic_t so the handler can flip it without a memory model
// argument; the main loop polls it inside pause()/sigwait().
static volatile sig_atomic_t g_should_stop = 0;

static void on_term(int signo) {
    (void)signo;
    g_should_stop = 1;
}

static int install_signal_handlers(void) {
    struct sigaction sa;
    memset(&sa, 0, sizeof(sa));
    sa.sa_handler = on_term;
    sigemptyset(&sa.sa_mask);
    sa.sa_flags = SA_RESTART;
    if (sigaction(SIGTERM, &sa, NULL) != 0) return -1;
    if (sigaction(SIGINT,  &sa, NULL) != 0) return -1;

    // SIGPIPE is a footgun on every networked daemon. Ignore it; the
    // syscall-level EPIPE return is what we actually want.
    struct sigaction ign;
    memset(&ign, 0, sizeof(ign));
    ign.sa_handler = SIG_IGN;
    if (sigaction(SIGPIPE, &ign, NULL) != 0) return -1;
    return 0;
}

static void usage(FILE *out, const char *argv0) {
    fprintf(out,
        "sftpadmind %s\n"
        "Usage: %s [options]\n"
        "  -c, --config PATH   Path to JSON config (required for normal run)\n"
        "  -V, --version       Print version and exit\n"
        "  -h, --help          Show this help and exit\n"
        "\n"
        "Phase 1 of the daemon: configuration + logging come online. Listener\n"
        "supervision and the admin API land in subsequent phases.\n",
        SFTPADMIN_VERSION, argv0);
}

int main(int argc, char **argv) {
    const char *config_path = NULL;

    static const struct option longopts[] = {
        {"config",  required_argument, NULL, 'c'},
        {"version", no_argument,       NULL, 'V'},
        {"help",    no_argument,       NULL, 'h'},
        {NULL, 0, NULL, 0},
    };
    int opt;
    while ((opt = getopt_long(argc, argv, "c:Vh", longopts, NULL)) != -1) {
        switch (opt) {
            case 'c': config_path = optarg; break;
            case 'V':
                fprintf(stdout, "sftpadmind %s\n", SFTPADMIN_VERSION);
                return 0;
            case 'h':
                usage(stdout, argv[0]);
                return 0;
            default:
                usage(stderr, argv[0]);
                return 2;
        }
    }

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

    // Apply logging settings from config.
    sa_log_set_level(cfg.log_level);
    if (cfg.log_file) {
        (void)sa_log_open_file(cfg.log_file);
    }
    if (cfg.log_to_syslog) {
        (void)sa_log_open_syslog("sftpadmind");
    }

    if (install_signal_handlers() != 0) {
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

    // ---- Phase 1 main loop ----
    // Future phases will fork listeners here, open the control socket,
    // start the admin API, and call the supervisor loop. For now we
    // simply wait for a termination signal so a systemd unit test can
    // verify clean start + clean stop.
    while (!g_should_stop) {
        pause();
    }

    sa_log_info("shutdown", "received termination signal; cleaning up",
        SA_LOG_END);

    sa_config_free(&cfg);
    sa_log_close();
    return 0;
}
