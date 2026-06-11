// Verifies the err.c table stays in lockstep with the enum. If someone
// adds a new SA_ERR_* but forgets the string entry, the corresponding
// row in SA_ERR_NAMES is implicit-zero (NULL) and sa_err_str() returns
// the BUG fallback — this test catches that.

#include <setjmp.h>  // cmocka prerequisite
#include <stdarg.h>  // cmocka prerequisite
#include <stddef.h>  // cmocka prerequisite

#include <cmocka.h>
#include <string.h>

#include "sftpadmin/err.h"

static void test_every_code_has_non_empty_string(void **state) {
    (void)state;
    for (int i = 0; i < (int)SA_ERR__COUNT; i++) {
        const char *s = sa_err_str((sa_err_t)i);
        assert_non_null(s);
        assert_true(s[0] != '\0');
        // "internal invariant violated" is the fallback for the BUG slot;
        // it must not appear for any other slot, otherwise we have a hole
        // in the table.
        if (i != SA_ERR_BUG) {
            assert_string_not_equal(s, "internal invariant violated");
        }
    }
}

static void test_out_of_range_returns_bug(void **state) {
    (void)state;
    assert_string_equal(sa_err_str((sa_err_t)(SA_ERR__COUNT + 99)),
                        "internal invariant violated");
    assert_string_equal(sa_err_str((sa_err_t)-1),
                        "internal invariant violated");
}

static void test_errno_mapping(void **state) {
    (void)state;
    assert_int_equal(sa_err_from_errno(0),       SA_OK);
    // ENOMEM and EACCES exist on every Linux platform.
    extern int errno;
    (void)errno;
    // Use the names directly rather than rely on numeric values that vary.
    assert_int_equal(sa_err_from_errno(12 /* ENOMEM */), SA_ERR_NOMEM);
    assert_int_equal(sa_err_from_errno(13 /* EACCES */), SA_ERR_PERM);
    assert_int_equal(sa_err_from_errno(2  /* ENOENT */), SA_ERR_NOENT);
    // An exotic errno we don't map specifically should decay to IO.
    assert_int_equal(sa_err_from_errno(0xBEEF), SA_ERR_IO);
}

static void test_expected_classification(void **state) {
    (void)state;
    assert_true(sa_err_is_expected(SA_OK));
    assert_true(sa_err_is_expected(SA_ERR_AUTH));
    assert_true(sa_err_is_expected(SA_ERR_BANNED));
    assert_false(sa_err_is_expected(SA_ERR_BUG));
    assert_false(sa_err_is_expected(SA_ERR_DB_OPEN));
}

int main(void) {
    const struct CMUnitTest tests[] = {
        cmocka_unit_test(test_every_code_has_non_empty_string),
        cmocka_unit_test(test_out_of_range_returns_bug),
        cmocka_unit_test(test_errno_mapping),
        cmocka_unit_test(test_expected_classification),
    };
    return cmocka_run_group_tests(tests, NULL, NULL);
}
