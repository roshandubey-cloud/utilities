// SQLite data layer. See db.h for the contract.
//
// Implementation notes:
//
//   * Every CRUD function uses sqlite3_prepare_v2 + sqlite3_bind_* —
//     no string concatenation, no printf-style SQL. This is the only
//     defence against SQL injection that actually works.
//
//   * Connections are opened with SQLITE_OPEN_FULLMUTEX, which makes the
//     handle thread-safe across the process. The slight serialization
//     cost is fine for the daemon's QPS profile.
//
//   * PRAGMA foreign_keys=ON is set per connection because SQLite
//     defaults it to OFF for backwards-compatibility reasons (a famous
//     foot-gun).
//
//   * Migrations run inside a BEGIN/COMMIT transaction; if any statement
//     fails the whole step rolls back and the schema_version stays put.
//
//   * The owning struct (sa_profile_t / sa_listener_t / sa_user_t) uses
//     strdup'd strings. *_free walks the fields and zeroes the pointers
//     so a caller that re-uses a stack struct doesn't double-free.

#include "sftpadmin/db.h"
#include "sftpadmin/db_schema.h"
#include "sftpadmin/log.h"

#include <sqlite3.h>

#include <errno.h>
#include <inttypes.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

struct sa_db {
    sqlite3 *h;
};

static char *xstrdup_or_null(const char *s) {
    if (!s) return NULL;
    size_t n = strlen(s) + 1;
    char *p = malloc(n);
    if (!p) return NULL;
    memcpy(p, s, n);
    return p;
}

// Map sqlite3 result codes to sa_err_t. We log the human-readable
// error at the call site, since sqlite_errmsg only stays valid until
// the next call on the handle.
static sa_err_t err_from_sqlite(int rc) {
    switch (rc) {
        case SQLITE_OK:        return SA_OK;
        case SQLITE_BUSY:      return SA_ERR_DB_BUSY;
        case SQLITE_CONSTRAINT:return SA_ERR_DUPLICATE;
        case SQLITE_PERM:
        case SQLITE_READONLY:  return SA_ERR_PERM;
        case SQLITE_FULL:      return SA_ERR_TOOBIG;
        case SQLITE_NOMEM:     return SA_ERR_NOMEM;
        case SQLITE_CANTOPEN:  return SA_ERR_DB_OPEN;
        case SQLITE_NOTFOUND:  return SA_ERR_NOENT;
        default:               return SA_ERR_DB_QUERY;
    }
}

// ---------------------------------------------------------------------------
// open / close / migrate
// ---------------------------------------------------------------------------
sa_err_t sa_db_open(const char *path, sa_db_t **out) {
    if (!path || !out) return SA_ERR_INVAL;
    *out = NULL;

    sa_db_t *db = calloc(1, sizeof(*db));
    if (!db) return SA_ERR_NOMEM;

    int flags = SQLITE_OPEN_READWRITE | SQLITE_OPEN_CREATE | SQLITE_OPEN_FULLMUTEX;
    int rc = sqlite3_open_v2(path, &db->h, flags, NULL);
    if (rc != SQLITE_OK) {
        sa_log_error("db", "sqlite3_open_v2 failed",
            SA_LOG_KV("path", path),
            SA_LOG_KV("sqlite_err", db->h ? sqlite3_errmsg(db->h) : sqlite3_errstr(rc)),
            SA_LOG_END);
        if (db->h) sqlite3_close(db->h);
        free(db);
        return SA_ERR_DB_OPEN;
    }

    // Connection pragmas. These are per-connection state, not schema,
    // so they live here rather than in a migration.
    const char *pragmas =
        "PRAGMA journal_mode = WAL;"
        "PRAGMA synchronous  = NORMAL;"
        "PRAGMA foreign_keys = ON;"
        "PRAGMA busy_timeout = 5000;"
        "PRAGMA cache_size   = -8000;"   // 8 MiB
        "PRAGMA temp_store   = MEMORY;";
    char *errmsg = NULL;
    rc = sqlite3_exec(db->h, pragmas, NULL, NULL, &errmsg);
    if (rc != SQLITE_OK) {
        sa_log_error("db", "pragma exec failed",
            SA_LOG_KV("sqlite_err", errmsg ? errmsg : "(none)"),
            SA_LOG_END);
        sqlite3_free(errmsg);
        sqlite3_close(db->h);
        free(db);
        return SA_ERR_DB_OPEN;
    }

    *out = db;
    return SA_OK;
}

void sa_db_close(sa_db_t *db) {
    if (!db) return;
    if (db->h) sqlite3_close(db->h);
    free(db);
}

int sa_db_schema_version(const sa_db_t *db) {
    if (!db || !db->h) return -1;
    sqlite3_stmt *st = NULL;
    int v = 0;
    if (sqlite3_prepare_v2(db->h,
            "SELECT MAX(version) FROM schema_version", -1, &st, NULL) == SQLITE_OK) {
        if (sqlite3_step(st) == SQLITE_ROW) {
            v = sqlite3_column_int(st, 0);
        }
    } else {
        // Table doesn't exist yet -> version 0.
        v = 0;
    }
    sqlite3_finalize(st);
    return v;
}

sa_err_t sa_db_migrate(sa_db_t *db) {
    if (!db || !db->h) return SA_ERR_INVAL;

    int current = sa_db_schema_version(db);
    if (current < 0) current = 0;

    for (size_t v = (size_t)current + 1; v < SA_MIGRATIONS_COUNT; v++) {
        const char *sql = SA_MIGRATIONS[v];
        if (!sql) continue;

        // Each migration is a transaction. Failure rolls back atomically.
        char *errmsg = NULL;
        if (sqlite3_exec(db->h, "BEGIN", NULL, NULL, &errmsg) != SQLITE_OK) {
            sa_log_error("db", "BEGIN failed",
                SA_LOG_KV("sqlite_err", errmsg ? errmsg : "(none)"),
                SA_LOG_END);
            sqlite3_free(errmsg);
            return SA_ERR_DB_SCHEMA;
        }
        if (sqlite3_exec(db->h, sql, NULL, NULL, &errmsg) != SQLITE_OK) {
            sa_log_error("db", "migration failed",
                SA_LOG_KV_INT("version", (long long)v),
                SA_LOG_KV("sqlite_err", errmsg ? errmsg : "(none)"),
                SA_LOG_END);
            sqlite3_free(errmsg);
            (void)sqlite3_exec(db->h, "ROLLBACK", NULL, NULL, NULL);
            return SA_ERR_DB_SCHEMA;
        }
        if (sqlite3_exec(db->h, "COMMIT", NULL, NULL, &errmsg) != SQLITE_OK) {
            sa_log_error("db", "COMMIT failed",
                SA_LOG_KV("sqlite_err", errmsg ? errmsg : "(none)"),
                SA_LOG_END);
            sqlite3_free(errmsg);
            (void)sqlite3_exec(db->h, "ROLLBACK", NULL, NULL, NULL);
            return SA_ERR_DB_SCHEMA;
        }
        sa_log_info("db", "migration applied",
            SA_LOG_KV_INT("version", (long long)v), SA_LOG_END);
    }
    return SA_OK;
}

// ---------------------------------------------------------------------------
// Built-in security profiles
//
// These are the "Modern (strict)", "Compatible (legacy)", and
// "FIPS-leaning" presets the spec calls for. The strings are libssh's
// algorithm-list format (comma-separated). Hard-blocked algorithms
// (arcfour, *-cbc-with-SHA1-MAC, hmac-md5, group1) appear NOWHERE in
// any preset.
// ---------------------------------------------------------------------------
typedef struct {
    const char *name;
    const char *kex;
    const char *ciphers;
    const char *macs;
    const char *hostkey;
    const char *pubkey;
} preset_t;

static const preset_t PRESETS[] = {
    {
        .name    = "Modern (strict)",
        .kex     = "curve25519-sha256,curve25519-sha256@libssh.org,ecdh-sha2-nistp256",
        .ciphers = "chacha20-poly1305@openssh.com,aes256-gcm@openssh.com,aes128-gcm@openssh.com",
        .macs    = "hmac-sha2-512-etm@openssh.com,hmac-sha2-256-etm@openssh.com",
        .hostkey = "ssh-ed25519,rsa-sha2-512,rsa-sha2-256",
        .pubkey  = "ssh-ed25519,rsa-sha2-512,rsa-sha2-256",
    },
    {
        .name    = "Compatible (legacy)",
        .kex     = "curve25519-sha256,curve25519-sha256@libssh.org,"
                   "ecdh-sha2-nistp256,ecdh-sha2-nistp384,ecdh-sha2-nistp521,"
                   "diffie-hellman-group-exchange-sha256",
        .ciphers = "chacha20-poly1305@openssh.com,aes256-gcm@openssh.com,"
                   "aes128-gcm@openssh.com,aes256-ctr,aes192-ctr,aes128-ctr",
        .macs    = "hmac-sha2-512-etm@openssh.com,hmac-sha2-256-etm@openssh.com,"
                   "hmac-sha2-512,hmac-sha2-256",
        .hostkey = "ssh-ed25519,rsa-sha2-512,rsa-sha2-256,ecdsa-sha2-nistp256,ecdsa-sha2-nistp384",
        .pubkey  = "ssh-ed25519,rsa-sha2-512,rsa-sha2-256,ecdsa-sha2-nistp256,ecdsa-sha2-nistp384",
    },
    {
        .name    = "FIPS-leaning",
        // FIPS 140-2/3 accepted algorithms only. No chacha20-poly1305,
        // no ed25519 (not FIPS-approved in many programmes). RSA keys
        // must be >=2048 bits; we don't enforce that here, but the
        // hostkey-create path in Phase 5 will.
        .kex     = "ecdh-sha2-nistp256,ecdh-sha2-nistp384,ecdh-sha2-nistp521,"
                   "diffie-hellman-group-exchange-sha256",
        .ciphers = "aes256-gcm@openssh.com,aes128-gcm@openssh.com,"
                   "aes256-ctr,aes192-ctr,aes128-ctr",
        .macs    = "hmac-sha2-512-etm@openssh.com,hmac-sha2-256-etm@openssh.com,"
                   "hmac-sha2-512,hmac-sha2-256",
        .hostkey = "rsa-sha2-512,rsa-sha2-256,ecdsa-sha2-nistp256,ecdsa-sha2-nistp384",
        .pubkey  = "rsa-sha2-512,rsa-sha2-256,ecdsa-sha2-nistp256,ecdsa-sha2-nistp384",
    },
};

sa_err_t sa_db_seed_profiles(sa_db_t *db) {
    if (!db || !db->h) return SA_ERR_INVAL;
    const char *sql =
        "INSERT INTO security_profiles "
        "(name, is_immutable, kex_algorithms, ciphers, macs, hostkey_algorithms, "
        " pubkey_accepted_algos, rekey_mb, rekey_seconds) "
        "VALUES (?, 1, ?, ?, ?, ?, ?, 1024, 3600) "
        "ON CONFLICT(name) DO NOTHING";

    for (size_t i = 0; i < sizeof(PRESETS) / sizeof(PRESETS[0]); i++) {
        sqlite3_stmt *st = NULL;
        int rc = sqlite3_prepare_v2(db->h, sql, -1, &st, NULL);
        if (rc != SQLITE_OK) {
            sa_log_error("db", "prepare seed profile failed",
                SA_LOG_KV("sqlite_err", sqlite3_errmsg(db->h)),
                SA_LOG_END);
            sqlite3_finalize(st);
            return err_from_sqlite(rc);
        }
        sqlite3_bind_text(st, 1, PRESETS[i].name,    -1, SQLITE_STATIC);
        sqlite3_bind_text(st, 2, PRESETS[i].kex,     -1, SQLITE_STATIC);
        sqlite3_bind_text(st, 3, PRESETS[i].ciphers, -1, SQLITE_STATIC);
        sqlite3_bind_text(st, 4, PRESETS[i].macs,    -1, SQLITE_STATIC);
        sqlite3_bind_text(st, 5, PRESETS[i].hostkey, -1, SQLITE_STATIC);
        sqlite3_bind_text(st, 6, PRESETS[i].pubkey,  -1, SQLITE_STATIC);
        rc = sqlite3_step(st);
        sqlite3_finalize(st);
        if (rc != SQLITE_DONE) {
            sa_log_error("db", "seed profile insert failed",
                SA_LOG_KV("name", PRESETS[i].name),
                SA_LOG_KV("sqlite_err", sqlite3_errmsg(db->h)),
                SA_LOG_END);
            return err_from_sqlite(rc);
        }
    }
    return SA_OK;
}

// ---------------------------------------------------------------------------
// Common row fillers for our three primary entities.
// ---------------------------------------------------------------------------
static const char *col_text(sqlite3_stmt *st, int col) {
    return (const char *)sqlite3_column_text(st, col);
}

static char *col_strdup(sqlite3_stmt *st, int col) {
    return xstrdup_or_null(col_text(st, col));
}

static void fill_profile(sqlite3_stmt *st, sa_profile_t *out) {
    memset(out, 0, sizeof(*out));
    out->id                    = sqlite3_column_int64(st, 0);
    out->name                  = col_strdup(st, 1);
    out->immutable             = sqlite3_column_int(st, 2) != 0;
    out->kex_algorithms        = col_strdup(st, 3);
    out->ciphers               = col_strdup(st, 4);
    out->macs                  = col_strdup(st, 5);
    out->hostkey_algorithms    = col_strdup(st, 6);
    out->pubkey_accepted_algos = col_strdup(st, 7);
    out->rekey_mb              = sqlite3_column_int64(st, 8);
    out->rekey_seconds         = sqlite3_column_int64(st, 9);
}

#define PROFILE_COLS \
    "id, name, is_immutable, kex_algorithms, ciphers, macs, " \
    "hostkey_algorithms, pubkey_accepted_algos, rekey_mb, rekey_seconds"

void sa_profile_free(sa_profile_t *p) {
    if (!p) return;
    free(p->name);                  p->name = NULL;
    free(p->kex_algorithms);        p->kex_algorithms = NULL;
    free(p->ciphers);               p->ciphers = NULL;
    free(p->macs);                  p->macs = NULL;
    free(p->hostkey_algorithms);    p->hostkey_algorithms = NULL;
    free(p->pubkey_accepted_algos); p->pubkey_accepted_algos = NULL;
}

sa_err_t sa_db_profile_get_by_id(sa_db_t *db, int64_t id, sa_profile_t *out) {
    if (!db || !db->h) return SA_ERR_INVAL;
    sqlite3_stmt *st = NULL;
    int rc = sqlite3_prepare_v2(db->h,
        "SELECT " PROFILE_COLS " FROM security_profiles WHERE id = ?", -1, &st, NULL);
    if (rc != SQLITE_OK) { sqlite3_finalize(st); return err_from_sqlite(rc); }
    sqlite3_bind_int64(st, 1, id);
    sa_err_t r;
    if (sqlite3_step(st) == SQLITE_ROW) {
        if (out) fill_profile(st, out);
        r = SA_OK;
    } else {
        r = SA_ERR_NOENT;
    }
    sqlite3_finalize(st);
    return r;
}

sa_err_t sa_db_profile_get_by_name(sa_db_t *db, const char *name, sa_profile_t *out) {
    if (!db || !db->h || !name) return SA_ERR_INVAL;
    sqlite3_stmt *st = NULL;
    int rc = sqlite3_prepare_v2(db->h,
        "SELECT " PROFILE_COLS " FROM security_profiles WHERE name = ?", -1, &st, NULL);
    if (rc != SQLITE_OK) { sqlite3_finalize(st); return err_from_sqlite(rc); }
    sqlite3_bind_text(st, 1, name, -1, SQLITE_STATIC);
    sa_err_t r;
    if (sqlite3_step(st) == SQLITE_ROW) {
        if (out) fill_profile(st, out);
        r = SA_OK;
    } else {
        r = SA_ERR_NOENT;
    }
    sqlite3_finalize(st);
    return r;
}

sa_err_t sa_db_profile_iter(sa_db_t *db, sa_profile_cb cb, void *ctx, int *out_cb_rc) {
    if (!db || !db->h || !cb) return SA_ERR_INVAL;
    sqlite3_stmt *st = NULL;
    int rc = sqlite3_prepare_v2(db->h,
        "SELECT " PROFILE_COLS " FROM security_profiles ORDER BY id", -1, &st, NULL);
    if (rc != SQLITE_OK) { sqlite3_finalize(st); return err_from_sqlite(rc); }
    int cb_rc = 0;
    while (sqlite3_step(st) == SQLITE_ROW) {
        sa_profile_t row;
        fill_profile(st, &row);
        cb_rc = cb(&row, ctx);
        sa_profile_free(&row);
        if (cb_rc != 0) break;
    }
    sqlite3_finalize(st);
    if (out_cb_rc) *out_cb_rc = cb_rc;
    return SA_OK;
}

sa_err_t sa_db_profile_create(sa_db_t *db, const sa_profile_t *in, int64_t *out_id) {
    if (!db || !db->h || !in) return SA_ERR_INVAL;
    sqlite3_stmt *st = NULL;
    int rc = sqlite3_prepare_v2(db->h,
        "INSERT INTO security_profiles "
        "(name, is_immutable, kex_algorithms, ciphers, macs, hostkey_algorithms, "
        " pubkey_accepted_algos, rekey_mb, rekey_seconds) "
        "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)", -1, &st, NULL);
    if (rc != SQLITE_OK) { sqlite3_finalize(st); return err_from_sqlite(rc); }
    sqlite3_bind_text (st, 1, in->name,                  -1, SQLITE_STATIC);
    sqlite3_bind_int  (st, 2, in->immutable ? 1 : 0);
    sqlite3_bind_text (st, 3, in->kex_algorithms,        -1, SQLITE_STATIC);
    sqlite3_bind_text (st, 4, in->ciphers,               -1, SQLITE_STATIC);
    sqlite3_bind_text (st, 5, in->macs,                  -1, SQLITE_STATIC);
    sqlite3_bind_text (st, 6, in->hostkey_algorithms,    -1, SQLITE_STATIC);
    sqlite3_bind_text (st, 7, in->pubkey_accepted_algos, -1, SQLITE_STATIC);
    sqlite3_bind_int64(st, 8, in->rekey_mb ? in->rekey_mb : 1024);
    sqlite3_bind_int64(st, 9, in->rekey_seconds ? in->rekey_seconds : 3600);
    rc = sqlite3_step(st);
    sqlite3_finalize(st);
    if (rc != SQLITE_DONE) return err_from_sqlite(rc);
    if (out_id) *out_id = sqlite3_last_insert_rowid(db->h);
    return SA_OK;
}

sa_err_t sa_db_profile_update(sa_db_t *db, const sa_profile_t *in) {
    if (!db || !db->h || !in) return SA_ERR_INVAL;
    // Reject mutation of immutable rows up-front to give a clean error
    // (and to avoid surprises if a later phase removes the trigger).
    sa_profile_t existing = {0};
    sa_err_t e = sa_db_profile_get_by_id(db, in->id, &existing);
    if (e != SA_OK) return e;
    bool was_immutable = existing.immutable;
    sa_profile_free(&existing);
    if (was_immutable) return SA_ERR_PERM;

    sqlite3_stmt *st = NULL;
    int rc = sqlite3_prepare_v2(db->h,
        "UPDATE security_profiles SET "
        "  name = ?, kex_algorithms = ?, ciphers = ?, macs = ?, "
        "  hostkey_algorithms = ?, pubkey_accepted_algos = ?, "
        "  rekey_mb = ?, rekey_seconds = ?, updated_at = datetime('now') "
        "WHERE id = ?", -1, &st, NULL);
    if (rc != SQLITE_OK) { sqlite3_finalize(st); return err_from_sqlite(rc); }
    sqlite3_bind_text (st, 1, in->name,                  -1, SQLITE_STATIC);
    sqlite3_bind_text (st, 2, in->kex_algorithms,        -1, SQLITE_STATIC);
    sqlite3_bind_text (st, 3, in->ciphers,               -1, SQLITE_STATIC);
    sqlite3_bind_text (st, 4, in->macs,                  -1, SQLITE_STATIC);
    sqlite3_bind_text (st, 5, in->hostkey_algorithms,    -1, SQLITE_STATIC);
    sqlite3_bind_text (st, 6, in->pubkey_accepted_algos, -1, SQLITE_STATIC);
    sqlite3_bind_int64(st, 7, in->rekey_mb);
    sqlite3_bind_int64(st, 8, in->rekey_seconds);
    sqlite3_bind_int64(st, 9, in->id);
    rc = sqlite3_step(st);
    sqlite3_finalize(st);
    return rc == SQLITE_DONE ? SA_OK : err_from_sqlite(rc);
}

sa_err_t sa_db_profile_delete(sa_db_t *db, int64_t id) {
    if (!db || !db->h) return SA_ERR_INVAL;
    sa_profile_t existing = {0};
    sa_err_t e = sa_db_profile_get_by_id(db, id, &existing);
    if (e != SA_OK) return e;
    bool was_immutable = existing.immutable;
    sa_profile_free(&existing);
    if (was_immutable) return SA_ERR_PERM;

    sqlite3_stmt *st = NULL;
    int rc = sqlite3_prepare_v2(db->h,
        "DELETE FROM security_profiles WHERE id = ?", -1, &st, NULL);
    if (rc != SQLITE_OK) { sqlite3_finalize(st); return err_from_sqlite(rc); }
    sqlite3_bind_int64(st, 1, id);
    rc = sqlite3_step(st);
    sqlite3_finalize(st);
    return rc == SQLITE_DONE ? SA_OK : err_from_sqlite(rc);
}

// ---------------------------------------------------------------------------
// Listeners
// ---------------------------------------------------------------------------
#define LISTENER_COLS \
    "id, name, bind_addr, port, enabled, max_sessions, max_sessions_per_user, " \
    "idle_timeout_s, auth_methods, pre_auth_banner, security_profile_id"

static void fill_listener(sqlite3_stmt *st, sa_listener_t *out) {
    memset(out, 0, sizeof(*out));
    out->id                    = sqlite3_column_int64(st, 0);
    out->name                  = col_strdup(st, 1);
    out->bind_addr             = col_strdup(st, 2);
    out->port                  = (uint16_t)sqlite3_column_int(st, 3);
    out->enabled               = sqlite3_column_int(st, 4) != 0;
    out->max_sessions          = sqlite3_column_int64(st, 5);
    out->max_sessions_per_user = sqlite3_column_int64(st, 6);
    out->idle_timeout_s        = sqlite3_column_int64(st, 7);
    out->auth_methods          = col_strdup(st, 8);
    out->pre_auth_banner       = col_strdup(st, 9);
    out->security_profile_id   = sqlite3_column_int64(st, 10);
}

void sa_listener_free(sa_listener_t *l) {
    if (!l) return;
    free(l->name);            l->name = NULL;
    free(l->bind_addr);       l->bind_addr = NULL;
    free(l->auth_methods);    l->auth_methods = NULL;
    free(l->pre_auth_banner); l->pre_auth_banner = NULL;
}

sa_err_t sa_db_listener_get_by_id(sa_db_t *db, int64_t id, sa_listener_t *out) {
    if (!db || !db->h) return SA_ERR_INVAL;
    sqlite3_stmt *st = NULL;
    int rc = sqlite3_prepare_v2(db->h,
        "SELECT " LISTENER_COLS " FROM listeners WHERE id = ?", -1, &st, NULL);
    if (rc != SQLITE_OK) { sqlite3_finalize(st); return err_from_sqlite(rc); }
    sqlite3_bind_int64(st, 1, id);
    sa_err_t r;
    if (sqlite3_step(st) == SQLITE_ROW) {
        if (out) fill_listener(st, out);
        r = SA_OK;
    } else {
        r = SA_ERR_NOENT;
    }
    sqlite3_finalize(st);
    return r;
}

sa_err_t sa_db_listener_get_by_name(sa_db_t *db, const char *name, sa_listener_t *out) {
    if (!db || !db->h || !name) return SA_ERR_INVAL;
    sqlite3_stmt *st = NULL;
    int rc = sqlite3_prepare_v2(db->h,
        "SELECT " LISTENER_COLS " FROM listeners WHERE name = ?", -1, &st, NULL);
    if (rc != SQLITE_OK) { sqlite3_finalize(st); return err_from_sqlite(rc); }
    sqlite3_bind_text(st, 1, name, -1, SQLITE_STATIC);
    sa_err_t r;
    if (sqlite3_step(st) == SQLITE_ROW) {
        if (out) fill_listener(st, out);
        r = SA_OK;
    } else {
        r = SA_ERR_NOENT;
    }
    sqlite3_finalize(st);
    return r;
}

sa_err_t sa_db_listener_iter(sa_db_t *db, sa_listener_cb cb, void *ctx, int *out_cb_rc) {
    if (!db || !db->h || !cb) return SA_ERR_INVAL;
    sqlite3_stmt *st = NULL;
    int rc = sqlite3_prepare_v2(db->h,
        "SELECT " LISTENER_COLS " FROM listeners ORDER BY id", -1, &st, NULL);
    if (rc != SQLITE_OK) { sqlite3_finalize(st); return err_from_sqlite(rc); }
    int cb_rc = 0;
    while (sqlite3_step(st) == SQLITE_ROW) {
        sa_listener_t row;
        fill_listener(st, &row);
        cb_rc = cb(&row, ctx);
        sa_listener_free(&row);
        if (cb_rc != 0) break;
    }
    sqlite3_finalize(st);
    if (out_cb_rc) *out_cb_rc = cb_rc;
    return SA_OK;
}

sa_err_t sa_db_listener_iter_enabled(sa_db_t *db, sa_listener_cb cb, void *ctx, int *out_cb_rc) {
    if (!db || !db->h || !cb) return SA_ERR_INVAL;
    sqlite3_stmt *st = NULL;
    int rc = sqlite3_prepare_v2(db->h,
        "SELECT " LISTENER_COLS " FROM listeners WHERE enabled = 1 ORDER BY id",
        -1, &st, NULL);
    if (rc != SQLITE_OK) { sqlite3_finalize(st); return err_from_sqlite(rc); }
    int cb_rc = 0;
    while (sqlite3_step(st) == SQLITE_ROW) {
        sa_listener_t row;
        fill_listener(st, &row);
        cb_rc = cb(&row, ctx);
        sa_listener_free(&row);
        if (cb_rc != 0) break;
    }
    sqlite3_finalize(st);
    if (out_cb_rc) *out_cb_rc = cb_rc;
    return SA_OK;
}

sa_err_t sa_db_listener_create(sa_db_t *db, const sa_listener_t *in, int64_t *out_id) {
    if (!db || !db->h || !in) return SA_ERR_INVAL;
    sqlite3_stmt *st = NULL;
    int rc = sqlite3_prepare_v2(db->h,
        "INSERT INTO listeners "
        "(name, bind_addr, port, enabled, max_sessions, max_sessions_per_user, "
        " idle_timeout_s, auth_methods, pre_auth_banner, security_profile_id) "
        "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", -1, &st, NULL);
    if (rc != SQLITE_OK) { sqlite3_finalize(st); return err_from_sqlite(rc); }
    sqlite3_bind_text (st, 1, in->name,            -1, SQLITE_STATIC);
    sqlite3_bind_text (st, 2, in->bind_addr,       -1, SQLITE_STATIC);
    sqlite3_bind_int  (st, 3, in->port);
    sqlite3_bind_int  (st, 4, in->enabled ? 1 : 0);
    sqlite3_bind_int64(st, 5, in->max_sessions          ? in->max_sessions          : 100);
    sqlite3_bind_int64(st, 6, in->max_sessions_per_user ? in->max_sessions_per_user : 10);
    sqlite3_bind_int64(st, 7, in->idle_timeout_s        ? in->idle_timeout_s        : 600);
    sqlite3_bind_text (st, 8, in->auth_methods    ? in->auth_methods : "password,publickey", -1, SQLITE_STATIC);
    if (in->pre_auth_banner)
        sqlite3_bind_text(st, 9, in->pre_auth_banner, -1, SQLITE_STATIC);
    else
        sqlite3_bind_null(st, 9);
    sqlite3_bind_int64(st, 10, in->security_profile_id);
    rc = sqlite3_step(st);
    sqlite3_finalize(st);
    if (rc != SQLITE_DONE) return err_from_sqlite(rc);
    if (out_id) *out_id = sqlite3_last_insert_rowid(db->h);
    return SA_OK;
}

sa_err_t sa_db_listener_update(sa_db_t *db, const sa_listener_t *in) {
    if (!db || !db->h || !in) return SA_ERR_INVAL;
    sqlite3_stmt *st = NULL;
    int rc = sqlite3_prepare_v2(db->h,
        "UPDATE listeners SET "
        "  name = ?, bind_addr = ?, port = ?, enabled = ?, "
        "  max_sessions = ?, max_sessions_per_user = ?, idle_timeout_s = ?, "
        "  auth_methods = ?, pre_auth_banner = ?, security_profile_id = ?, "
        "  updated_at = datetime('now') "
        "WHERE id = ?", -1, &st, NULL);
    if (rc != SQLITE_OK) { sqlite3_finalize(st); return err_from_sqlite(rc); }
    sqlite3_bind_text (st, 1,  in->name,         -1, SQLITE_STATIC);
    sqlite3_bind_text (st, 2,  in->bind_addr,    -1, SQLITE_STATIC);
    sqlite3_bind_int  (st, 3,  in->port);
    sqlite3_bind_int  (st, 4,  in->enabled ? 1 : 0);
    sqlite3_bind_int64(st, 5,  in->max_sessions);
    sqlite3_bind_int64(st, 6,  in->max_sessions_per_user);
    sqlite3_bind_int64(st, 7,  in->idle_timeout_s);
    sqlite3_bind_text (st, 8,  in->auth_methods, -1, SQLITE_STATIC);
    if (in->pre_auth_banner)
        sqlite3_bind_text(st, 9, in->pre_auth_banner, -1, SQLITE_STATIC);
    else
        sqlite3_bind_null(st, 9);
    sqlite3_bind_int64(st, 10, in->security_profile_id);
    sqlite3_bind_int64(st, 11, in->id);
    rc = sqlite3_step(st);
    sqlite3_finalize(st);
    return rc == SQLITE_DONE ? SA_OK : err_from_sqlite(rc);
}

sa_err_t sa_db_listener_delete(sa_db_t *db, int64_t id) {
    if (!db || !db->h) return SA_ERR_INVAL;
    sqlite3_stmt *st = NULL;
    int rc = sqlite3_prepare_v2(db->h,
        "DELETE FROM listeners WHERE id = ?", -1, &st, NULL);
    if (rc != SQLITE_OK) { sqlite3_finalize(st); return err_from_sqlite(rc); }
    sqlite3_bind_int64(st, 1, id);
    rc = sqlite3_step(st);
    sqlite3_finalize(st);
    return rc == SQLITE_DONE ? SA_OK : err_from_sqlite(rc);
}

sa_err_t sa_db_listener_set_enabled(sa_db_t *db, int64_t id, bool enabled) {
    if (!db || !db->h) return SA_ERR_INVAL;
    sqlite3_stmt *st = NULL;
    int rc = sqlite3_prepare_v2(db->h,
        "UPDATE listeners SET enabled = ?, updated_at = datetime('now') WHERE id = ?",
        -1, &st, NULL);
    if (rc != SQLITE_OK) { sqlite3_finalize(st); return err_from_sqlite(rc); }
    sqlite3_bind_int  (st, 1, enabled ? 1 : 0);
    sqlite3_bind_int64(st, 2, id);
    rc = sqlite3_step(st);
    sqlite3_finalize(st);
    return rc == SQLITE_DONE ? SA_OK : err_from_sqlite(rc);
}

// ---------------------------------------------------------------------------
// Users
// ---------------------------------------------------------------------------
#define USER_COLS \
    "id, username, enabled, expires_at, password_hash, virtual_root, comment, " \
    "perm_read, perm_write, perm_delete, perm_mkdir, perm_rename, " \
    "bandwidth_kbps, max_open_handles"

static void fill_user(sqlite3_stmt *st, sa_user_t *out) {
    memset(out, 0, sizeof(*out));
    out->id               = sqlite3_column_int64(st, 0);
    out->username         = col_strdup(st, 1);
    out->enabled          = sqlite3_column_int(st, 2) != 0;
    out->expires_at       = col_strdup(st, 3);
    out->password_hash    = col_strdup(st, 4);
    out->virtual_root     = col_strdup(st, 5);
    out->comment          = col_strdup(st, 6);
    out->perm_read        = sqlite3_column_int(st, 7) != 0;
    out->perm_write       = sqlite3_column_int(st, 8) != 0;
    out->perm_delete      = sqlite3_column_int(st, 9) != 0;
    out->perm_mkdir       = sqlite3_column_int(st, 10) != 0;
    out->perm_rename      = sqlite3_column_int(st, 11) != 0;
    out->bandwidth_kbps   = sqlite3_column_int64(st, 12);
    out->max_open_handles = sqlite3_column_int64(st, 13);
}

void sa_user_free(sa_user_t *u) {
    if (!u) return;
    free(u->username);      u->username = NULL;
    free(u->expires_at);    u->expires_at = NULL;
    free(u->password_hash); u->password_hash = NULL;
    free(u->virtual_root);  u->virtual_root = NULL;
    free(u->comment);       u->comment = NULL;
}

sa_err_t sa_db_user_get_by_id(sa_db_t *db, int64_t id, sa_user_t *out) {
    if (!db || !db->h) return SA_ERR_INVAL;
    sqlite3_stmt *st = NULL;
    int rc = sqlite3_prepare_v2(db->h,
        "SELECT " USER_COLS " FROM users WHERE id = ?", -1, &st, NULL);
    if (rc != SQLITE_OK) { sqlite3_finalize(st); return err_from_sqlite(rc); }
    sqlite3_bind_int64(st, 1, id);
    sa_err_t r;
    if (sqlite3_step(st) == SQLITE_ROW) {
        if (out) fill_user(st, out);
        r = SA_OK;
    } else {
        r = SA_ERR_NOENT;
    }
    sqlite3_finalize(st);
    return r;
}

sa_err_t sa_db_user_get_by_username(sa_db_t *db, const char *username, sa_user_t *out) {
    if (!db || !db->h || !username) return SA_ERR_INVAL;
    sqlite3_stmt *st = NULL;
    int rc = sqlite3_prepare_v2(db->h,
        "SELECT " USER_COLS " FROM users WHERE username = ? COLLATE NOCASE",
        -1, &st, NULL);
    if (rc != SQLITE_OK) { sqlite3_finalize(st); return err_from_sqlite(rc); }
    sqlite3_bind_text(st, 1, username, -1, SQLITE_STATIC);
    sa_err_t r;
    if (sqlite3_step(st) == SQLITE_ROW) {
        if (out) fill_user(st, out);
        r = SA_OK;
    } else {
        r = SA_ERR_NOENT;
    }
    sqlite3_finalize(st);
    return r;
}

sa_err_t sa_db_user_iter(sa_db_t *db, sa_user_cb cb, void *ctx, int *out_cb_rc) {
    if (!db || !db->h || !cb) return SA_ERR_INVAL;
    sqlite3_stmt *st = NULL;
    int rc = sqlite3_prepare_v2(db->h,
        "SELECT " USER_COLS " FROM users ORDER BY id", -1, &st, NULL);
    if (rc != SQLITE_OK) { sqlite3_finalize(st); return err_from_sqlite(rc); }
    int cb_rc = 0;
    while (sqlite3_step(st) == SQLITE_ROW) {
        sa_user_t row;
        fill_user(st, &row);
        cb_rc = cb(&row, ctx);
        sa_user_free(&row);
        if (cb_rc != 0) break;
    }
    sqlite3_finalize(st);
    if (out_cb_rc) *out_cb_rc = cb_rc;
    return SA_OK;
}

sa_err_t sa_db_user_create(sa_db_t *db, const sa_user_t *in, int64_t *out_id) {
    if (!db || !db->h || !in || !in->username || !in->virtual_root) return SA_ERR_INVAL;
    sqlite3_stmt *st = NULL;
    int rc = sqlite3_prepare_v2(db->h,
        "INSERT INTO users "
        "(username, enabled, expires_at, password_hash, virtual_root, comment, "
        " perm_read, perm_write, perm_delete, perm_mkdir, perm_rename, "
        " bandwidth_kbps, max_open_handles) "
        "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", -1, &st, NULL);
    if (rc != SQLITE_OK) { sqlite3_finalize(st); return err_from_sqlite(rc); }
    sqlite3_bind_text (st, 1,  in->username,    -1, SQLITE_STATIC);
    sqlite3_bind_int  (st, 2,  in->enabled ? 1 : 0);
    if (in->expires_at)    sqlite3_bind_text(st, 3, in->expires_at,    -1, SQLITE_STATIC);
    else                   sqlite3_bind_null(st, 3);
    if (in->password_hash) sqlite3_bind_text(st, 4, in->password_hash, -1, SQLITE_STATIC);
    else                   sqlite3_bind_null(st, 4);
    sqlite3_bind_text (st, 5,  in->virtual_root, -1, SQLITE_STATIC);
    if (in->comment)       sqlite3_bind_text(st, 6, in->comment, -1, SQLITE_STATIC);
    else                   sqlite3_bind_null(st, 6);
    sqlite3_bind_int  (st, 7,  in->perm_read   ? 1 : 0);
    sqlite3_bind_int  (st, 8,  in->perm_write  ? 1 : 0);
    sqlite3_bind_int  (st, 9,  in->perm_delete ? 1 : 0);
    sqlite3_bind_int  (st, 10, in->perm_mkdir  ? 1 : 0);
    sqlite3_bind_int  (st, 11, in->perm_rename ? 1 : 0);
    sqlite3_bind_int64(st, 12, in->bandwidth_kbps);
    sqlite3_bind_int64(st, 13, in->max_open_handles ? in->max_open_handles : 64);
    rc = sqlite3_step(st);
    sqlite3_finalize(st);
    if (rc != SQLITE_DONE) return err_from_sqlite(rc);
    if (out_id) *out_id = sqlite3_last_insert_rowid(db->h);
    return SA_OK;
}

sa_err_t sa_db_user_update(sa_db_t *db, const sa_user_t *in) {
    if (!db || !db->h || !in) return SA_ERR_INVAL;
    sqlite3_stmt *st = NULL;
    int rc = sqlite3_prepare_v2(db->h,
        "UPDATE users SET "
        "  username = ?, enabled = ?, expires_at = ?, virtual_root = ?, comment = ?, "
        "  perm_read = ?, perm_write = ?, perm_delete = ?, perm_mkdir = ?, perm_rename = ?, "
        "  bandwidth_kbps = ?, max_open_handles = ?, updated_at = datetime('now') "
        "WHERE id = ?", -1, &st, NULL);
    if (rc != SQLITE_OK) { sqlite3_finalize(st); return err_from_sqlite(rc); }
    sqlite3_bind_text (st, 1,  in->username,    -1, SQLITE_STATIC);
    sqlite3_bind_int  (st, 2,  in->enabled ? 1 : 0);
    if (in->expires_at)    sqlite3_bind_text(st, 3, in->expires_at, -1, SQLITE_STATIC);
    else                   sqlite3_bind_null(st, 3);
    sqlite3_bind_text (st, 4,  in->virtual_root, -1, SQLITE_STATIC);
    if (in->comment)       sqlite3_bind_text(st, 5, in->comment, -1, SQLITE_STATIC);
    else                   sqlite3_bind_null(st, 5);
    sqlite3_bind_int  (st, 6,  in->perm_read   ? 1 : 0);
    sqlite3_bind_int  (st, 7,  in->perm_write  ? 1 : 0);
    sqlite3_bind_int  (st, 8,  in->perm_delete ? 1 : 0);
    sqlite3_bind_int  (st, 9,  in->perm_mkdir  ? 1 : 0);
    sqlite3_bind_int  (st, 10, in->perm_rename ? 1 : 0);
    sqlite3_bind_int64(st, 11, in->bandwidth_kbps);
    sqlite3_bind_int64(st, 12, in->max_open_handles);
    sqlite3_bind_int64(st, 13, in->id);
    rc = sqlite3_step(st);
    sqlite3_finalize(st);
    return rc == SQLITE_DONE ? SA_OK : err_from_sqlite(rc);
}

sa_err_t sa_db_user_delete(sa_db_t *db, int64_t id) {
    if (!db || !db->h) return SA_ERR_INVAL;
    sqlite3_stmt *st = NULL;
    int rc = sqlite3_prepare_v2(db->h,
        "DELETE FROM users WHERE id = ?", -1, &st, NULL);
    if (rc != SQLITE_OK) { sqlite3_finalize(st); return err_from_sqlite(rc); }
    sqlite3_bind_int64(st, 1, id);
    rc = sqlite3_step(st);
    sqlite3_finalize(st);
    return rc == SQLITE_DONE ? SA_OK : err_from_sqlite(rc);
}

sa_err_t sa_db_user_set_password_hash(sa_db_t *db, int64_t id, const char *new_hash) {
    if (!db || !db->h) return SA_ERR_INVAL;
    sqlite3_stmt *st = NULL;
    int rc = sqlite3_prepare_v2(db->h,
        "UPDATE users SET password_hash = ?, updated_at = datetime('now') WHERE id = ?",
        -1, &st, NULL);
    if (rc != SQLITE_OK) { sqlite3_finalize(st); return err_from_sqlite(rc); }
    if (new_hash) sqlite3_bind_text(st, 1, new_hash, -1, SQLITE_STATIC);
    else          sqlite3_bind_null(st, 1);
    sqlite3_bind_int64(st, 2, id);
    rc = sqlite3_step(st);
    sqlite3_finalize(st);
    return rc == SQLITE_DONE ? SA_OK : err_from_sqlite(rc);
}
