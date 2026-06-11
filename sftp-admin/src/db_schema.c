// Schema migrations, compiled into the binary as string literals so the
// daemon never depends on a sidecar SQL directory at runtime.
//
// Rules:
//   * Each migration is idempotent (CREATE IF NOT EXISTS, etc.) so a
//     half-applied migration on a previous crash can re-run safely.
//   * Every migration ends with `INSERT OR REPLACE INTO schema_version`
//     so the row count is exactly one per applied version.
//   * Migrations are NEVER edited after release. Schema changes go in a
//     new migration with a higher version number.
//
// Pragmas (WAL, busy_timeout, foreign_keys) are set on every open in
// db.c rather than in a migration — they're connection state, not
// schema state.

#include "sftpadmin/db_schema.h"

const char *const SA_MIGRATIONS[] = {
    [0] = NULL, // sentinel; version 0 means "empty database"

    // -------------------------------------------------------------------
    // Migration 1 — initial schema
    // -------------------------------------------------------------------
    [1] =
"CREATE TABLE IF NOT EXISTS schema_version ("
"  version    INTEGER PRIMARY KEY,"
"  applied_at TEXT NOT NULL DEFAULT (datetime('now'))"
");"

"CREATE TABLE IF NOT EXISTS security_profiles ("
"  id                     INTEGER PRIMARY KEY,"
"  name                   TEXT NOT NULL UNIQUE,"
"  is_immutable           INTEGER NOT NULL DEFAULT 0,"
"  kex_algorithms         TEXT NOT NULL,"
"  ciphers                TEXT NOT NULL,"
"  macs                   TEXT NOT NULL,"
"  hostkey_algorithms     TEXT NOT NULL,"
"  pubkey_accepted_algos  TEXT NOT NULL,"
"  rekey_mb               INTEGER NOT NULL DEFAULT 1024,"
"  rekey_seconds          INTEGER NOT NULL DEFAULT 3600,"
"  created_at             TEXT NOT NULL DEFAULT (datetime('now')),"
"  updated_at             TEXT NOT NULL DEFAULT (datetime('now'))"
");"

"CREATE TABLE IF NOT EXISTS listeners ("
"  id                       INTEGER PRIMARY KEY,"
"  name                     TEXT NOT NULL UNIQUE,"
"  bind_addr                TEXT NOT NULL,"
"  port                     INTEGER NOT NULL CHECK (port BETWEEN 1 AND 65535),"
"  enabled                  INTEGER NOT NULL DEFAULT 1,"
"  max_sessions             INTEGER NOT NULL DEFAULT 100,"
"  max_sessions_per_user    INTEGER NOT NULL DEFAULT 10,"
"  idle_timeout_s           INTEGER NOT NULL DEFAULT 600,"
"  auth_methods             TEXT NOT NULL DEFAULT 'password,publickey',"
"  pre_auth_banner          TEXT,"
"  security_profile_id      INTEGER NOT NULL"
"    REFERENCES security_profiles(id) ON DELETE RESTRICT,"
"  created_at               TEXT NOT NULL DEFAULT (datetime('now')),"
"  updated_at               TEXT NOT NULL DEFAULT (datetime('now'))"
");"
"CREATE INDEX IF NOT EXISTS idx_listeners_enabled ON listeners(enabled);"

"CREATE TABLE IF NOT EXISTS host_keys ("
"  id                  INTEGER PRIMARY KEY,"
"  listener_id         INTEGER NOT NULL"
"    REFERENCES listeners(id) ON DELETE CASCADE,"
"  algo                TEXT NOT NULL,"
"  encrypted_key       BLOB NOT NULL,"
"  public_fingerprint  TEXT NOT NULL,"
"  created_at          TEXT NOT NULL DEFAULT (datetime('now'))"
");"

"CREATE TABLE IF NOT EXISTS users ("
"  id                INTEGER PRIMARY KEY,"
"  username          TEXT NOT NULL UNIQUE COLLATE NOCASE,"
"  enabled           INTEGER NOT NULL DEFAULT 1,"
"  expires_at        TEXT,"
"  password_hash     TEXT,"
"  virtual_root      TEXT NOT NULL,"
"  comment           TEXT,"
"  perm_read         INTEGER NOT NULL DEFAULT 1,"
"  perm_write        INTEGER NOT NULL DEFAULT 1,"
"  perm_delete       INTEGER NOT NULL DEFAULT 1,"
"  perm_mkdir        INTEGER NOT NULL DEFAULT 1,"
"  perm_rename       INTEGER NOT NULL DEFAULT 1,"
"  bandwidth_kbps    INTEGER NOT NULL DEFAULT 0,"
"  max_open_handles  INTEGER NOT NULL DEFAULT 64,"
"  created_at        TEXT NOT NULL DEFAULT (datetime('now')),"
"  updated_at        TEXT NOT NULL DEFAULT (datetime('now'))"
");"

"CREATE TABLE IF NOT EXISTS user_ssh_keys ("
"  id                  INTEGER PRIMARY KEY,"
"  user_id             INTEGER NOT NULL"
"    REFERENCES users(id) ON DELETE CASCADE,"
"  label               TEXT NOT NULL,"
"  key_type            TEXT NOT NULL,"
"  key_blob            TEXT NOT NULL,"
"  sha256_fingerprint  TEXT NOT NULL,"
"  enabled             INTEGER NOT NULL DEFAULT 1,"
"  created_at          TEXT NOT NULL DEFAULT (datetime('now'))"
");"
"CREATE UNIQUE INDEX IF NOT EXISTS idx_user_keys_unique"
"  ON user_ssh_keys(user_id, sha256_fingerprint);"
"CREATE INDEX IF NOT EXISTS idx_user_keys_fpr"
"  ON user_ssh_keys(sha256_fingerprint);"

"CREATE TABLE IF NOT EXISTS user_listener_assignments ("
"  user_id                INTEGER NOT NULL"
"    REFERENCES users(id) ON DELETE CASCADE,"
"  listener_id            INTEGER NOT NULL"
"    REFERENCES listeners(id) ON DELETE CASCADE,"
"  virtual_root_override  TEXT,"
"  perm_write_override    INTEGER,"
"  PRIMARY KEY (user_id, listener_id)"
");"

"CREATE TABLE IF NOT EXISTS bans ("
"  id            INTEGER PRIMARY KEY,"
"  listener_id   INTEGER"
"    REFERENCES listeners(id) ON DELETE CASCADE,"
"  ip            TEXT NOT NULL,"
"  failure_count INTEGER NOT NULL DEFAULT 1,"
"  first_seen    TEXT NOT NULL DEFAULT (datetime('now')),"
"  last_seen     TEXT NOT NULL DEFAULT (datetime('now')),"
"  banned_until  TEXT"
");"
"CREATE INDEX IF NOT EXISTS idx_bans_ip_listener ON bans(listener_id, ip);"

"CREATE TABLE IF NOT EXISTS audit_log ("
"  id        INTEGER PRIMARY KEY,"
"  ts        TEXT NOT NULL DEFAULT (datetime('now')),"
"  actor     TEXT NOT NULL,"
"  category  TEXT NOT NULL,"
"  action    TEXT NOT NULL,"
"  target    TEXT,"
"  success   INTEGER NOT NULL DEFAULT 0,"
"  detail    TEXT"
");"
"CREATE INDEX IF NOT EXISTS idx_audit_ts ON audit_log(ts);"
"CREATE INDEX IF NOT EXISTS idx_audit_category ON audit_log(category);"

"INSERT OR REPLACE INTO schema_version(version) VALUES (1);",
};

const size_t SA_MIGRATIONS_COUNT = sizeof(SA_MIGRATIONS) / sizeof(SA_MIGRATIONS[0]);
