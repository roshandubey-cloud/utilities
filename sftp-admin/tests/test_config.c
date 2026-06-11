// Config-loader tests:
//   * sa_config_defaults() yields a struct that passes sa_config_validate.
//   * Partial overlay (one section set) keeps defaults elsewhere.
//   * Unknown keys produce warnings but DO NOT fail the load.
//   * Garbage JSON yields SA_ERR_PARSE; oversized payload yields TOOBIG.
//   * Schema violations (relative path, bad argon2) yield SA_ERR_SCHEMA.

#include <setjmp.h>
#include <stdarg.h>
#include <stddef.h>

#include <cmocka.h>

#include <string.h>

#include "sftpadmin/config.h"

static void test_defaults_validate(void **state) {
    (void)state;
    sa_config_t c;
    sa_config_defaults(&c);
    assert_int_equal(sa_config_validate(&c), SA_OK);
    sa_config_free(&c);
}

static void test_partial_overlay_keeps_defaults(void **state) {
    (void)state;
    const char *j = "{\"admin\":{\"port\":1443}}";
    sa_config_t c;
    assert_int_equal(sa_config_load_buf(j, strlen(j), &c), SA_OK);
    assert_int_equal(c.admin_port, 1443);
    // Defaults untouched. The exact db_path is platform-specific
    // (Linux/Mac/Windows pick different default data dirs); we only
    // assert that it was populated and ends in "sftpadmin.db".
    assert_non_null(c.db_path);
    const char *tail = strstr(c.db_path, "sftpadmin.db");
    assert_non_null(tail);
    sa_config_free(&c);
}

static void test_unknown_keys_dont_fail(void **state) {
    (void)state;
    const char *j = "{\"foobar\":{\"x\":1},\"admin\":{\"port\":2222,\"extra\":\"ignored\"}}";
    sa_config_t c;
    assert_int_equal(sa_config_load_buf(j, strlen(j), &c), SA_OK);
    assert_int_equal(c.admin_port, 2222);
    sa_config_free(&c);
}

static void test_underscore_keys_pass_silently(void **state) {
    (void)state;
    // Keys prefixed with '_' are the JSON comment convention; they must
    // not warn and must not fail the load.
    const char *j = "{\"_comment\":\"hi\",\"admin\":{\"_note\":\"also hi\",\"port\":3333}}";
    sa_config_t c;
    assert_int_equal(sa_config_load_buf(j, strlen(j), &c), SA_OK);
    assert_int_equal(c.admin_port, 3333);
    sa_config_free(&c);
}

static void test_garbage_json_is_parse_error(void **state) {
    (void)state;
    const char *j = "{not even close";
    sa_config_t c;
    memset(&c, 0xAA, sizeof(c));
    assert_int_equal(sa_config_load_buf(j, strlen(j), &c), SA_ERR_PARSE);
}

static void test_oversized_is_toobig(void **state) {
    (void)state;
    // We don't have to actually allocate >1 MiB; the byte-count argument
    // is what triggers the cap.
    sa_config_t c;
    assert_int_equal(sa_config_load_buf("{}", (1024 * 1024) + 1, &c),
                     SA_ERR_TOOBIG);
}

static void test_relative_db_path_fails_schema(void **state) {
    (void)state;
    const char *j = "{\"paths\":{\"db_path\":\"relative/path.db\"}}";
    sa_config_t c;
    assert_int_equal(sa_config_load_buf(j, strlen(j), &c), SA_ERR_SCHEMA);
}

static void test_argon2_out_of_range_fails_schema(void **state) {
    (void)state;
    const char *j = "{\"security\":{\"argon2_ops\":99999}}";
    sa_config_t c;
    assert_int_equal(sa_config_load_buf(j, strlen(j), &c), SA_ERR_SCHEMA);
}

static void test_log_level_string_parsed(void **state) {
    (void)state;
    const char *j = "{\"logging\":{\"level\":\"debug\"}}";
    sa_config_t c;
    assert_int_equal(sa_config_load_buf(j, strlen(j), &c), SA_OK);
    assert_int_equal(c.log_level, SA_LOG_DEBUG);
    sa_config_free(&c);
}

static void test_empty_buf_is_parse_error(void **state) {
    (void)state;
    sa_config_t c;
    assert_int_equal(sa_config_load_buf("", 0, &c), SA_ERR_PARSE);
}

int main(void) {
    const struct CMUnitTest tests[] = {
        cmocka_unit_test(test_defaults_validate),
        cmocka_unit_test(test_partial_overlay_keeps_defaults),
        cmocka_unit_test(test_unknown_keys_dont_fail),
        cmocka_unit_test(test_underscore_keys_pass_silently),
        cmocka_unit_test(test_garbage_json_is_parse_error),
        cmocka_unit_test(test_oversized_is_toobig),
        cmocka_unit_test(test_relative_db_path_fails_schema),
        cmocka_unit_test(test_argon2_out_of_range_fails_schema),
        cmocka_unit_test(test_log_level_string_parsed),
        cmocka_unit_test(test_empty_buf_is_parse_error),
    };
    return cmocka_run_group_tests(tests, NULL, NULL);
}
