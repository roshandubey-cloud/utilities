# sftpadmind — enterprise SFTP server administration suite
#
# Phase 1: lays the foundation (logger, errors, config, entrypoint).
# Later phases bring SQLite, libssh, libsodium, libmicrohttpd in under
# conditional find_package() so the early build doesn't fight the whole
# dependency chain — Phase 1 only needs a C compiler + cJSON + cmocka.
#
# Hardening flags are scoped to OUR targets only (sftpadmin_core,
# sftpadmind, tests) via an INTERFACE library. We DELIBERATELY do not
# pollute third-party builds (cJSON, cmocka) with our strict warnings —
# they have their own conventions and would not clear -Werror -Wconversion.
cmake_minimum_required(VERSION 3.22)

project(sftpadmin
    VERSION 0.1.0
    DESCRIPTION "Enterprise SFTP server administration suite"
    LANGUAGES C)

set(CMAKE_C_STANDARD 11)
set(CMAKE_C_STANDARD_REQUIRED ON)
set(CMAKE_C_EXTENSIONS OFF)
set(CMAKE_POSITION_INDEPENDENT_CODE ON)

include(FetchContent)

# ----------------------------------------------------------------------------
# Definitions that MUST be global: 64-bit file offsets and the modern-POSIX
# feature macro. These are not warning flags so they don't break third-party
# code; they enable required behaviour everywhere.
# ----------------------------------------------------------------------------
add_compile_definitions(_DEFAULT_SOURCE _FILE_OFFSET_BITS=64)

# ----------------------------------------------------------------------------
# Toggles
# ----------------------------------------------------------------------------
option(SFTPADMIN_STRICT "Enable strict warning + hardening flags for our code (default ON)" ON)
option(SFTPADMIN_ASAN   "Build with AddressSanitizer + UBSan" OFF)
option(SFTPADMIN_BUILD_TESTS "Build the test suite" ON)

# Strict / hardening flags packaged as an INTERFACE library. We link this
# into OUR targets only — never globally — so cJSON / cmocka / future deps
# keep their own warning conventions.
add_library(sftpadmin_strict INTERFACE)
if(SFTPADMIN_STRICT)
    target_compile_options(sftpadmin_strict INTERFACE
        -Wall -Wextra -Werror -Wconversion -Wshadow -Wpointer-arith
        -Wcast-qual -Wstrict-prototypes -Wmissing-prototypes
        -Wformat=2 -Wformat-security
        -fstack-protector-strong
        -fno-common
        -fPIE
    )
    target_compile_definitions(sftpadmin_strict INTERFACE _FORTIFY_SOURCE=2)
endif()

# Hardening link flags. These are safe globally — they only affect link
# behaviour, not source semantics.
if(CMAKE_BUILD_TYPE STREQUAL "Release")
    add_link_options(-Wl,-z,relro -Wl,-z,now -pie)
else()
    add_link_options(-pie)
endif()

if(SFTPADMIN_ASAN)
    # ASan/UBSan instrumentation legitimately needs to cross third-party
    # boundaries — link order issues otherwise — so this one goes global.
    add_compile_options(-fsanitize=address,undefined -fno-omit-frame-pointer)
    add_link_options(-fsanitize=address,undefined)
endif()

# ----------------------------------------------------------------------------
# cJSON via FetchContent. We pin a tag for reproducibility. Tests + the
# cJSON utils binary are turned off so the configure runs quietly.
# ----------------------------------------------------------------------------
FetchContent_Declare(
    cjson
    GIT_REPOSITORY https://github.com/DaveGamble/cJSON.git
    GIT_TAG        v1.7.18
    GIT_SHALLOW    TRUE
)
set(ENABLE_CJSON_TEST OFF CACHE BOOL "" FORCE)
set(ENABLE_CJSON_UTILS OFF CACHE BOOL "" FORCE)
set(BUILD_SHARED_LIBS OFF CACHE BOOL "" FORCE)
set(BUILD_SHARED_AND_STATIC_LIBS OFF CACHE BOOL "" FORCE)
set(CJSON_OVERRIDE_BUILD_SHARED_LIBS ON CACHE BOOL "" FORCE)
FetchContent_MakeAvailable(cjson)

# ----------------------------------------------------------------------------
# Core library
# ----------------------------------------------------------------------------
add_library(sftpadmin_core STATIC
    src/err.c
    src/log.c
    src/config.c
)
target_include_directories(sftpadmin_core
    PUBLIC ${CMAKE_CURRENT_SOURCE_DIR}/include
)
target_link_libraries(sftpadmin_core
    PUBLIC  cjson
    PRIVATE sftpadmin_strict
)
# cJSON's FetchContent target doesn't export its header dir cleanly for
# consumers; pull it in explicitly so #include <cJSON.h> works without
# relying on a system install.
target_include_directories(sftpadmin_core PRIVATE ${cjson_SOURCE_DIR})

# ----------------------------------------------------------------------------
# Daemon binary
# ----------------------------------------------------------------------------
add_executable(sftpadmind src/main.c)
target_link_libraries(sftpadmind PRIVATE sftpadmin_core sftpadmin_strict)

# ----------------------------------------------------------------------------
# Tests
# ----------------------------------------------------------------------------
if(SFTPADMIN_BUILD_TESTS)
    enable_testing()
    FetchContent_Declare(
        cmocka
        GIT_REPOSITORY https://git.cryptomilk.org/projects/cmocka.git
        GIT_TAG        cmocka-1.1.7
        GIT_SHALLOW    TRUE
    )
    set(WITH_STATIC_LIB ON CACHE BOOL "" FORCE)
    set(WITH_EXAMPLES OFF CACHE BOOL "" FORCE)
    set(UNIT_TESTING OFF CACHE BOOL "" FORCE)
    FetchContent_MakeAvailable(cmocka)

    add_subdirectory(tests)
endif()

# ----------------------------------------------------------------------------
# Install (placeholder; expanded in Phase 10)
# ----------------------------------------------------------------------------
include(GNUInstallDirs)
install(TARGETS sftpadmind
        RUNTIME DESTINATION ${CMAKE_INSTALL_SBINDIR})
install(FILES examples/sftpadmin.conf
        DESTINATION ${CMAKE_INSTALL_SYSCONFDIR}/sftpadmin
        RENAME sftpadmin.conf.example)

message(STATUS "sftpadmind ${PROJECT_VERSION}  strict=${SFTPADMIN_STRICT} asan=${SFTPADMIN_ASAN} tests=${SFTPADMIN_BUILD_TESTS}")
