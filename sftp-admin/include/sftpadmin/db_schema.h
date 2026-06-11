// Private header — only sftp-admin internals include this. Exposes
// the compiled-in migration list so db.c can iterate them.
#ifndef SFTPADMIN_DB_SCHEMA_H
#define SFTPADMIN_DB_SCHEMA_H

#include <stddef.h>

// Indexed by schema version number. SA_MIGRATIONS[0] is NULL (no
// migration corresponds to "version zero"); SA_MIGRATIONS[1] applies
// the initial schema and bumps schema_version to 1.
extern const char *const SA_MIGRATIONS[];
extern const size_t      SA_MIGRATIONS_COUNT;

#endif
