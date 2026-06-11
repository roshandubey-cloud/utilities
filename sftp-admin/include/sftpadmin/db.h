// SQLite data layer. The daemon's single source of truth for users,
// listeners, security profiles, host keys, user-to-listener assignments,
// audit log, and the brute-force ban table.
//
// Design rules:
//   * One sa_db_t per process. SQLite handles are NOT thread-safe across
//     processes (one of the reasons we fork-per-listener on POSIX), but
//     they ARE thread-safe within a process when opened with SQLITE_
//     OPEN_FULLMUTEX. We use FULLMUTEX so the caller doesn't need to
//     serialize their own reads/writes.
//   * Prepared statements only — every parameter is bound. No string
//     concatenation anywhere.
//   * WAL mode + 5000 ms busy_timeout so two listeners writing
//     simultaneously don't deadlock.
//   * Schema migrations run on every open. Each migration is idempotent
//     and the schema_version table tracks the highest applied number.
//   * All CRUD functions return sa_err_t; the typed output is via
//     out-pointer. NULL out-pointer means "don't return the row".
//
// Phase 2 ships the open/migrate/seed plumbing plus CRUD for the three
// load-bearing entities (security profiles, listeners, users). Host
// keys, SSH keys, assignments, bans, and audit get CRUD in their
// respective phases.
#ifndef SFTPADMIN_DB_H
#define SFTPADMIN_DB_H

#include "sftpadmin/err.h"

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

typedef struct sa_db sa_db_t;

// Open the database at `path` (created if missing) and run any
// outstanding migrations. The returned handle is owned by the caller;
// pass it to sa_db_close() to release.
sa_err_t sa_db_open (const char *path, sa_db_t **out);

// Close + free. Safe to pass NULL.
void     sa_db_close(sa_db_t *db);

// Returns the current schema version (0 if the DB was freshly created
// and migrate hasn't run). Useful for tests and the /api/health
// endpoint we'll add in Phase 8.
int      sa_db_schema_version(const sa_db_t *db);

// Runs all migrations whose number is greater than the current schema
// version. Idempotent — calling repeatedly is a no-op. The migration
// list is compiled into the binary; the on-disk DB is the only thing
// that changes.
sa_err_t sa_db_migrate(sa_db_t *db);

// Insert the three built-in immutable security profiles if they don't
// already exist. Idempotent. Called once at startup.
sa_err_t sa_db_seed_profiles(sa_db_t *db);

// ---------------------------------------------------------------------------
// Security profiles
// ---------------------------------------------------------------------------
typedef struct {
    int64_t  id;
    char    *name;
    bool     immutable;
    char    *kex_algorithms;
    char    *ciphers;
    char    *macs;
    char    *hostkey_algorithms;
    char    *pubkey_accepted_algos;
    int64_t  rekey_mb;
    int64_t  rekey_seconds;
} sa_profile_t;

void sa_profile_free(sa_profile_t *p);  // safe on NULL

// Read by id or name. Returns SA_ERR_NOENT if not found.
sa_err_t sa_db_profile_get_by_id  (sa_db_t *db, int64_t id,        sa_profile_t *out);
sa_err_t sa_db_profile_get_by_name(sa_db_t *db, const char *name,  sa_profile_t *out);

// Iterate over every profile. On each row the callback gets a borrowed
// pointer (do NOT free) and the user-supplied ctx. Returning non-zero
// from the callback short-circuits iteration; the return value becomes
// the function result.
typedef int (*sa_profile_cb)(const sa_profile_t *row, void *ctx);
sa_err_t sa_db_profile_iter(sa_db_t *db, sa_profile_cb cb, void *ctx, int *out_cb_rc);

// Create a new profile (returns the assigned id via out_id, may be
// NULL). Fails with SA_ERR_DUPLICATE if a profile by that name exists.
sa_err_t sa_db_profile_create(sa_db_t *db, const sa_profile_t *in, int64_t *out_id);

// Update an existing profile. SA_ERR_NOENT if id doesn't exist;
// SA_ERR_PERM if the row is marked immutable.
sa_err_t sa_db_profile_update(sa_db_t *db, const sa_profile_t *in);

// Delete by id. SA_ERR_NOENT if id doesn't exist; SA_ERR_PERM for
// immutable; SA_ERR_DB_QUERY (with FK message) if a listener references
// it.
sa_err_t sa_db_profile_delete(sa_db_t *db, int64_t id);

// ---------------------------------------------------------------------------
// Listeners
// ---------------------------------------------------------------------------
typedef struct {
    int64_t  id;
    char    *name;
    char    *bind_addr;
    uint16_t port;
    bool     enabled;
    int64_t  max_sessions;
    int64_t  max_sessions_per_user;
    int64_t  idle_timeout_s;
    char    *auth_methods;      // comma-separated: "password,publickey"
    char    *pre_auth_banner;   // may be NULL
    int64_t  security_profile_id;
} sa_listener_t;

void sa_listener_free(sa_listener_t *l);

sa_err_t sa_db_listener_get_by_id  (sa_db_t *db, int64_t id,       sa_listener_t *out);
sa_err_t sa_db_listener_get_by_name(sa_db_t *db, const char *name, sa_listener_t *out);

typedef int (*sa_listener_cb)(const sa_listener_t *row, void *ctx);
sa_err_t sa_db_listener_iter   (sa_db_t *db, sa_listener_cb cb, void *ctx, int *out_cb_rc);
sa_err_t sa_db_listener_iter_enabled(sa_db_t *db, sa_listener_cb cb, void *ctx, int *out_cb_rc);

sa_err_t sa_db_listener_create(sa_db_t *db, const sa_listener_t *in, int64_t *out_id);
sa_err_t sa_db_listener_update(sa_db_t *db, const sa_listener_t *in);
sa_err_t sa_db_listener_delete(sa_db_t *db, int64_t id);
sa_err_t sa_db_listener_set_enabled(sa_db_t *db, int64_t id, bool enabled);

// ---------------------------------------------------------------------------
// Users
// ---------------------------------------------------------------------------
typedef struct {
    int64_t  id;
    char    *username;
    bool     enabled;
    char    *expires_at;        // ISO-8601; NULL = never
    char    *password_hash;     // argon2id encoded; NULL = no password auth
    char    *virtual_root;
    char    *comment;
    bool     perm_read;
    bool     perm_write;
    bool     perm_delete;
    bool     perm_mkdir;
    bool     perm_rename;
    int64_t  bandwidth_kbps;
    int64_t  max_open_handles;
} sa_user_t;

void sa_user_free(sa_user_t *u);

sa_err_t sa_db_user_get_by_id      (sa_db_t *db, int64_t id,           sa_user_t *out);
sa_err_t sa_db_user_get_by_username(sa_db_t *db, const char *username, sa_user_t *out);

typedef int (*sa_user_cb)(const sa_user_t *row, void *ctx);
sa_err_t sa_db_user_iter(sa_db_t *db, sa_user_cb cb, void *ctx, int *out_cb_rc);

sa_err_t sa_db_user_create        (sa_db_t *db, const sa_user_t *in, int64_t *out_id);
sa_err_t sa_db_user_update        (sa_db_t *db, const sa_user_t *in);
sa_err_t sa_db_user_delete        (sa_db_t *db, int64_t id);
sa_err_t sa_db_user_set_password_hash(sa_db_t *db, int64_t id, const char *new_hash);

#endif // SFTPADMIN_DB_H
