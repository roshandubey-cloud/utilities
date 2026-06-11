# Phase 1 tests. Each test is its own executable, linked against
# sftpadmin_core (which carries the PUBLIC include dir for our headers)
# plus cmocka. We deliberately do NOT link sftpadmin_strict into the
# test targets — cmocka's expansion macros generate code that wouldn't
# clear our -Wconversion/-Wshadow gates, and we'd rather keep the gates
# strict on OUR code than relax them everywhere.

set(SFTPADMIN_TESTS
    test_err
    test_log
    test_config
    test_db
)

foreach(t ${SFTPADMIN_TESTS})
    add_executable(${t} ${t}.c)
    target_link_libraries(${t} PRIVATE sftpadmin_core cmocka)
    # Minimal flags on test glue; the implementation under test was
    # compiled with the full strict set already.
    target_compile_options(${t} PRIVATE -Wall -Wextra)
    add_test(NAME ${t} COMMAND ${t})
endforeach()
