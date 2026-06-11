# sftpadmind — enterprise SFTP server administration suite
#
# Phase 1 portable build. Compiles on Linux, macOS, BSD, and Windows
# (MSVC or MinGW). Strict warning + hardening flags scoped to OUR
# targets only.
cmake_minimum_required(VERSION 3.22)

project(sftpadmin
    VERSION 0.1.0
    DESCRIPTION "Enterprise SFTP server administration suite (portable C)"
    LANGUAGES C)

set(CMAKE_C_STANDARD 11)
set(CMAKE_C_STANDARD_REQUIRED ON)
set(CMAKE_C_EXTENSIONS OFF)
set(CMAKE_POSITION_INDEPENDENT_CODE ON)

include(FetchContent)

# 64-bit file offsets on POSIX; harmless on Windows where the C library
# is 64-bit-clean anyway. NOT a warning flag so it's safe for everyone.
if(NOT WIN32)
    add_compile_definitions(_DEFAULT_SOURCE _FILE_OFFSET_BITS=64)
endif()
if(APPLE)
    add_compile_definitions(_DARWIN_C_SOURCE)
endif()

# Cross-platform threads. On POSIX this resolves to -lpthread; on Windows
# (MSVC) it's a no-op because the C runtime already provides what we need.
set(THREADS_PREFER_PTHREAD_FLAG ON)
find_package(Threads REQUIRED)

option(SFTPADMIN_STRICT "Enable strict warning + hardening flags for our code (default ON)" ON)
option(SFTPADMIN_ASAN   "Build with AddressSanitizer + UBSan" OFF)
option(SFTPADMIN_BUILD_TESTS "Build the test suite" ON)

# ----------------------------------------------------------------------------
# Strict / hardening flags packaged as an INTERFACE library.  Linked into
# OUR targets only — third-party (cJSON / cmocka) keep their own warning
# conventions. The flag list is intentionally compiler-aware because MSVC
# doesn't accept -Werror, -Wconversion, etc.
# ----------------------------------------------------------------------------
add_library(sftpadmin_strict INTERFACE)
if(SFTPADMIN_STRICT)
    if(MSVC)
        target_compile_options(sftpadmin_strict INTERFACE
            /W4 /WX /permissive- /sdl
        )
        # Modern CRT functions like _CRT_SECURE_NO_WARNINGS are not the
        # right answer; we use the _s variants explicitly via portable.c.
    else()
        target_compile_options(sftpadmin_strict INTERFACE
            -Wall -Wextra -Werror -Wconversion -Wshadow -Wpointer-arith
            -Wcast-qual -Wstrict-prototypes -Wmissing-prototypes
            -Wformat=2 -Wformat-security
            -fstack-protector-strong
            -fno-common
        )
        target_compile_definitions(sftpadmin_strict INTERFACE _FORTIFY_SOURCE=2)
        # PIE is a UNIX-toolchain concern; Windows handles ASLR
        # differently and doesn't take -fPIE.
        if(NOT WIN32)
            target_compile_options(sftpadmin_strict INTERFACE -fPIE)
        endif()
    endif()
endif()

# Link-time hardening (POSIX toolchains only — MSVC has its own /GS /DYNAMICBASE).
if(NOT WIN32 AND NOT MSVC)
    if(CMAKE_BUILD_TYPE STREQUAL "Release")
        add_link_options(-Wl,-z,relro -Wl,-z,now -pie)
    else()
        # PIE in debug too so behaviour matches release.
        add_link_options(-pie)
    endif()
endif()

if(SFTPADMIN_ASAN AND NOT MSVC)
    add_compile_options(-fsanitize=address,undefined -fno-omit-frame-pointer)
    add_link_options(-fsanitize=address,undefined)
endif()

# ----------------------------------------------------------------------------
# cJSON via FetchContent
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
# Core static library (everything except main())
# ----------------------------------------------------------------------------
add_library(sftpadmin_core STATIC
    src/err.c
    src/log.c
    src/config.c
    src/portable.c
)
target_include_directories(sftpadmin_core
    PUBLIC ${CMAKE_CURRENT_SOURCE_DIR}/include
)
target_include_directories(sftpadmin_core PRIVATE ${cjson_SOURCE_DIR})
target_link_libraries(sftpadmin_core
    PUBLIC  cjson Threads::Threads
    PRIVATE sftpadmin_strict
)
if(WIN32)
    # Windows: kernel32 (CreateProcess, console-ctrl, FILETIME, etc.)
    # — usually linked implicitly, but be explicit so MinGW builds work
    # without surprise.
    target_link_libraries(sftpadmin_core PUBLIC kernel32)
endif()

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
# Install
# ----------------------------------------------------------------------------
include(GNUInstallDirs)
install(TARGETS sftpadmind
        RUNTIME DESTINATION ${CMAKE_INSTALL_BINDIR})
install(FILES examples/sftpadmin.conf
        DESTINATION ${CMAKE_INSTALL_SYSCONFDIR}/sftpadmin
        RENAME sftpadmin.conf.example)

message(STATUS "sftpadmind ${PROJECT_VERSION}  strict=${SFTPADMIN_STRICT} asan=${SFTPADMIN_ASAN} tests=${SFTPADMIN_BUILD_TESTS}")
message(STATUS "  platform: ${CMAKE_SYSTEM_NAME}/${CMAKE_SYSTEM_PROCESSOR}  compiler: ${CMAKE_C_COMPILER_ID}")
