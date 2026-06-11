// DB layer tests. Every test creates a fresh on-disk file under
// $TMPDIR (or /tmp) so migrations actually exercise the disk path.
// SQLite is fast enough that this is fine; an in-memory ":memory:"
// path would skip the on-disk WAL behaviour we care about.

#include <setjmp.h>
#include <stdarg.h>
#include <stddef.h>

#include <cmocka.h>

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <time.h>
#include <unistd.h>

#include "sftpadmin/db.h"

static char g_path[512];

// Allocates a unique temp filename in $TMPDIR (or /tmp). We use mkstemp
// then unlink so the file is gone on test exit even if the test crashes.
static int test_setup(void **state) {
    (void)state;
    const char *tmp = getenv("TMPDIR");
    if (!tmp || !*tmp) tmp = "/tmp";
    snprintf(g_path, sizeof(g_path), "%s/sftpadmin-test-XXXXXX.db", tmp);
    int fd = mkstemps(g_path, 3);
    if (fd < 0) return -1;
    close(fd);
    unlink(g_path);  // sqlite3_open_v2 needs absence-or-empty
    return 0;
}

static int test_teardown(void **state) {
    (void)state;
    unlink(g_path);
    // Also clean WAL/shm sidecars that WAL mode may have created.
    char p[600];
    snprintf(p, sizeof(p), "%s-wal", g_path); unlink(p);
    snprintf(p, sizeof(p), "%s-shm", g_path); unlink(p);
    return 0;
}

// ---------------------------------------------------------------------------

static void test_open_migrate_idempotent(void **state) {
    (void)state;
    sa_db_t *db = NULL;
    assert_int_equal(sa_db_open(g_path, &db), SA_OK);
    assert_non_null(db);
    assert_int_equal(sa_db_migrate(db), SA_OK);
    assert_int_equal(sa_db_schema_version(db), 1);
    // Second migrate is a no-op.
    assert_int_equal(sa_db_migrate(db), SA_OK);
    assert_int_equal(sa_db_schema_version(db), 1);
    sa_db_close(db);
}

static int count_profiles_cb(const sa_profile_t *row, void *ctx) {
    (void)row;
    int *n = ctx;
    (*n)++;
    return 0;
}

static void test_seed_profiles(void **state) {
    (void)state;
    sa_db_t *db = NULL;
    assert_int_equal(sa_db_open(g_path, &db), SA_OK);
    assert_int_equal(sa_db_migrate(db), SA_OK);
    assert_int_equal(sa_db_seed_profiles(db), SA_OK);
    // Idempotent.
    assert_int_equal(sa_db_seed_profiles(db), SA_OK);
    int n = 0;
    int cb_rc = 0;
    assert_int_equal(sa_db_profile_iter(db, count_profiles_cb, &n, &cb_rc), SA_OK);
    assert_int_equal(n, 3);

    // Lookup by name.
    sa_profile_t p = {0};
    assert_int_equal(sa_db_profile_get_by_name(db, "Modern (strict)", &p), SA_OK);
    assert_true(p.immutable);
    assert_non_null(p.kex_algorithms);
    assert_non_null(strstr(p.kex_algorithms, "curve25519"));
    // No banned algorithms.
    assert_null(strstr(p.ciphers, "arcfour"));
    assert_null(strstr(p.macs,    "hmac-md5"));
    assert_null(strstr(p.kex_algorithms, "group1"));
    sa_profile_free(&p);

    sa_db_close(db);
}

static void test_immutable_profile_cannot_be_updated(void **state) {
    (void)state;
    sa_db_t *db = NULL;
    assert_int_equal(sa_db_open(g_path, &db), SA_OK);
    assert_int_equal(sa_db_migrate(db), SA_OK);
    assert_int_equal(sa_db_seed_profiles(db), SA_OK);

    sa_profile_t p = {0};
    assert_int_equal(sa_db_profile_get_by_name(db, "Modern (strict)", &p), SA_OK);
    p.rekey_seconds = 60;  // try to weaken rekey
    assert_int_equal(sa_db_profile_update(db, &p), SA_ERR_PERM);
    assert_int_equal(sa_db_profile_delete(db, p.id), SA_ERR_PERM);
    sa_profile_free(&p);
    sa_db_close(db);
}

static void test_listener_lifecycle(void **state) {
    (void)state;
    sa_db_t *db = NULL;
    assert_int_equal(sa_db_open(g_path, &db), SA_OK);
    assert_int_equal(sa_db_migrate(db), SA_OK);
    assert_int_equal(sa_db_seed_profiles(db), SA_OK);

    sa_profile_t modern = {0};
    assert_int_equal(sa_db_profile_get_by_name(db, "Modern (strict)", &modern), SA_OK);

    sa_listener_t in = {
        .name        = "primary",
        .bind_addr   = "0.0.0.0",
        .port        = 2222,
        .enabled     = true,
        .max_sessions= 0,
        .max_sessions_per_user = 0,
        .idle_timeout_s = 0,
        .auth_methods= "password,publickey",
        .pre_auth_banner = NULL,
        .security_profile_id = modern.id,
    };
    int64_t id = 0;
    assert_int_equal(sa_db_listener_create(db, &in, &id), SA_OK);
    assert_true(id > 0);

    // Duplicate name -> SA_ERR_DUPLICATE.
    assert_int_equal(sa_db_listener_create(db, &in, NULL), SA_ERR_DUPLICATE);

    sa_listener_t got = {0};
    assert_int_equal(sa_db_listener_get_by_id(db, id, &got), SA_OK);
    assert_int_equal(got.port, 2222);
    assert_true(got.enabled);
    assert_int_equal(got.max_sessions, 100);  // default applied
    assert_int_equal(got.security_profile_id, modern.id);

    // Update.
    free(got.name);
    got.name = strdup("primary-renamed");
    got.port = 2223;
    assert_int_equal(sa_db_listener_update(db, &got), SA_OK);

    sa_listener_t check = {0};
    assert_int_equal(sa_db_listener_get_by_name(db, "primary-renamed", &check), SA_OK);
    assert_int_equal(check.port, 2223);
    sa_listener_free(&check);
    sa_listener_free(&got);

    // FK RESTRICT: deleting an in-use profile fails.
    assert_int_not_equal(sa_db_profile_delete(db, modern.id), SA_OK);

    // Delete listener.
    assert_int_equal(sa_db_listener_delete(db, id), SA_OK);
    assert_int_equal(sa_db_listener_get_by_id(db, id, NULL), SA_ERR_NOENT);

    sa_profile_free(&modern);
    sa_db_close(db);
}

static int find_alice_cb(const sa_user_t *u, void *ctx) {
    int *found = ctx;
    if (u->username && !strcasecmp(u->username, "alice")) *found = 1;
    return 0;
}

static void test_user_lifecycle(void **state) {
    (void)state;
    sa_db_t *db = NULL;
    assert_int_equal(sa_db_open(g_path, &db), SA_OK);
    assert_int_equal(sa_db_migrate(db), SA_OK);

    sa_user_t in = {
        .username          = "alice",
        .enabled           = true,
        .virtual_root      = "/home/alice/sftp",
        .password_hash     = "$argon2id$test$placeholder",
        .perm_read         = true,
        .perm_write        = true,
        .perm_delete       = false,
        .perm_mkdir        = true,
        .perm_rename       = true,
        .bandwidth_kbps    = 0,
        .max_open_handles  = 0,
    };
    int64_t id = 0;
    assert_int_equal(sa_db_user_create(db, &in, &id), SA_OK);
    assert_true(id > 0);

    // Case-insensitive username conflict.
    sa_user_t dup = in;
    dup.username = "ALICE";
    assert_int_equal(sa_db_user_create(db, &dup, NULL), SA_ERR_DUPLICATE);

    sa_user_t got = {0};
    assert_int_equal(sa_db_user_get_by_username(db, "ALICE", &got), SA_OK);
    assert_string_equal(got.username, "alice");
    assert_true (got.perm_write);
    assert_false(got.perm_delete);
    assert_int_equal(got.max_open_handles, 64);  // default
    sa_user_free(&got);

    // Update password hash.
    assert_int_equal(sa_db_user_set_password_hash(db, id, "$argon2id$test$updated"), SA_OK);
    assert_int_equal(sa_db_user_get_by_id(db, id, &got), SA_OK);
    assert_string_equal(got.password_hash, "$argon2id$test$updated");
    sa_user_free(&got);

    // Iter finds it.
    int found = 0, cb_rc = 0;
    assert_int_equal(sa_db_user_iter(db, find_alice_cb, &found, &cb_rc), SA_OK);
    assert_int_equal(found, 1);

    // Delete.
    assert_int_equal(sa_db_user_delete(db, id), SA_OK);
    assert_int_equal(sa_db_user_get_by_id(db, id, NULL), SA_ERR_NOENT);

    sa_db_close(db);
}

static void test_listener_set_enabled(void **state) {
    (void)state;
    sa_db_t *db = NULL;
    assert_int_equal(sa_db_open(g_path, &db), SA_OK);
    assert_int_equal(sa_db_migrate(db), SA_OK);
    assert_int_equal(sa_db_seed_profiles(db), SA_OK);

    sa_profile_t p = {0};
    assert_int_equal(sa_db_profile_get_by_name(db, "Modern (strict)", &p), SA_OK);
    sa_listener_t in = {
        .name = "l1", .bind_addr = "0.0.0.0", .port = 2222, .enabled = true,
        .auth_methods = "password", .security_profile_id = p.id,
    };
    int64_t id = 0;
    assert_int_equal(sa_db_listener_create(db, &in, &id), SA_OK);
    assert_int_equal(sa_db_listener_set_enabled(db, id, false), SA_OK);

    sa_listener_t got = {0};
    assert_int_equal(sa_db_listener_get_by_id(db, id, &got), SA_OK);
    assert_false(got.enabled);
    sa_listener_free(&got);

    sa_profile_free(&p);
    sa_db_close(db);
}

int main(void) {
    const struct CMUnitTest tests[] = {
        cmocka_unit_test_setup_teardown(test_open_migrate_idempotent,             test_setup, test_teardown),
        cmocka_unit_test_setup_teardown(test_seed_profiles,                        test_setup, test_teardown),
        cmocka_unit_test_setup_teardown(test_immutable_profile_cannot_be_updated,  test_setup, test_teardown),
        cmocka_unit_test_setup_teardown(test_listener_lifecycle,                   test_setup, test_teardown),
        cmocka_unit_test_setup_teardown(test_user_lifecycle,                       test_setup, test_teardown),
        cmocka_unit_test_setup_teardown(test_listener_set_enabled,                 test_setup, test_teardown),
    };
    return cmocka_run_group_tests(tests, NULL, NULL);
}
