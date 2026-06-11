// Config loader. JSON on disk (cJSON is in the allowed library list);
// schema-validated into a strongly-typed C struct so the rest of the
// codebase touches typed fields instead of cJSON nodes.
//
// Anything related to listeners, users, profiles is in SQLite — see the
// db.h shape coming in Phase 2. This file only handles the bootstrap
// configuration: where the database lives, where logs go, what
// hostname/port the admin UI binds to, security knobs that need to be
// known BEFORE the database can be opened.
#ifndef SFTPADMIN_CONFIG_H
#define SFTPADMIN_CONFIG_H

#include "sftpadmin/err.h"
#include "sftpadmin/log.h"

#include <stdbool.h>
#include <stdint.h>

// Strings inside sa_config_t are owned by the loader. Caller owns the
// outer struct and must call sa_config_free() when done.
typedef struct {
    // [paths]
    char *db_path;            // SQLite file path (default /var/lib/sftpadmin/sftpadmin.db)
    char *hostkey_dir;        // dir containing encrypted host-key blobs
    char *master_key_file;    // 0600 file containing libsodium master key
    char *run_dir;            // socket + pid file dir (default /run/sftpadmin)
    char *log_file;           // optional; if set, JSON logger also writes here

    // [admin]
    char *admin_bind_addr;    // IP for the admin HTTPS server (default 127.0.0.1)
    uint16_t admin_port;      // default 9443
    char *admin_tls_cert;     // PEM path; auto-generate on first run if missing
    char *admin_tls_key;
    bool  admin_generate_self_signed;

    // [logging]
    sa_log_level_t log_level; // default INFO
    bool log_to_syslog;       // open syslog ident "sftpadmind" on init

    // [security]
    // Argon2id parameters. We default to libsodium MODERATE (about 0.7s on
    // a 2020 server). Per-deployment tuning is reasonable.
    uint64_t argon2_ops;      // default 3
    uint64_t argon2_mem_kb;   // default 65536 (64 MiB)

    // [sftp defaults]
    uint32_t default_max_sessions;       // per-listener cap
    uint32_t default_max_sessions_per_user;
    uint32_t default_idle_timeout_s;     // 0 = no idle timeout
    uint32_t default_auth_grace_s;       // how long client gets to auth before connection is dropped
} sa_config_t;

// Load + parse the config file. On success, *out is populated and the
// caller owns it. On failure *out is NOT modified and a structured error
// is logged at error level.
//
// Unknown top-level keys produce a warning log but do NOT fail the load
// — forward-compat for newer config files used by older daemons.
sa_err_t sa_config_load(const char *path, sa_config_t *out);

// Load from an in-memory JSON string. Used by the unit test suite; also
// useful for embedding config in environment vars during development.
sa_err_t sa_config_load_buf(const char *json, size_t len, sa_config_t *out);

// Populate with built-in defaults (no file I/O). Useful for tests and as
// the starting point that sa_config_load() overlays parsed values on top
// of.
void sa_config_defaults(sa_config_t *out);

void sa_config_free(sa_config_t *cfg);

// Returns SA_OK iff the config is internally consistent (ports in range,
// paths absolute, argon2 params sane). Logs the first violation found and
// returns the matching sa_err_t. Called automatically at the end of
// sa_config_load(); exposed so tests can poke values then re-validate.
sa_err_t sa_config_validate(const sa_config_t *cfg);

#endif // SFTPADMIN_CONFIG_H
