# DECISIONS

The durable memory the operating rules in section 8 of the build spec ask
me to keep. Every entry has a date, the choice, and the reason. New
entries go at the top.

---

## 2026-06-11 · Phase 1 — portability pivot

### Cross-platform from day one (Linux + macOS + Windows)

The original spec said "Linux target (systemd)". The product owner has
since clarified the user experience: "the tool should be like an app
that I can run on Windows, Linux, or any macOS directly … web page or
app." That puts cross-platform in the foundation rather than as a
later port.

The backend stays in C (the spec's "Backend 100% C" constraint is
intact). The desktop "app" variant gets a Go/Wails wrapper layered on
top, talking to the same C daemon over localhost. CI builds for
Linux x64, macOS arm64, and Windows x64 (MSVC) on every push.

This is recorded in detail in CROSS_PLATFORM.md.

### Two-line abstraction (`portable.h`/`portable.c`)

Every divergence is funnelled through one tiny header. Five primitives
total: mutex, getpid, ISO-8601 time, default-data-dir, signal handler
install + wait. The rest of the codebase is identical across OSes.
Adding a sixth would require a deliberate decision logged here.

### MSVC + gcc/clang share one CMakeLists

The strict-flag interface library detects MSVC and switches to
`/W4 /WX /permissive- /sdl` instead of the gcc/clang set. Both gates
are equally tight; neither is relaxed.

### Phase 7 windows divergence is documented, not hidden

Windows has no fork. The privilege-separation security model the spec
demands relies on POSIX fork. On Windows we'll use one-process +
thread-pool with per-listener Job Objects in Phase 7. The result is
weaker isolation than the POSIX build. We log this as a known
limitation rather than try to emulate fork on Windows (which is what
Cygwin does, and it's terrible).

---

## 2026-06-11 · Phase 1 — earlier entries

### Build host = Windows; build path = gcc:13 Docker container

The Windows 11 box I'm developing on cannot natively compile a Linux
daemon that depends on libssh/libsodium/etc. Rather than fight WSL or
MinGW for what will eventually be a Linux-only artefact, the build
script (`scripts/build-in-docker.sh`) shells into `gcc:13` and invokes
CMake there. The repo is bind-mounted; the host filesystem still owns
all sources.

**Cost:** the systemd / privilege-drop integration suite cannot run on
this developer box — those need a real Linux host (a fresh Ubuntu 24
VM is the spec target). The unit test suite runs in the container
just fine.

### cJSON is allowed; TOML is not

The spec's allowed-library list names cJSON. The config loader uses
JSON on disk, with `_comment` keys for human notes (cJSON ignores
unknown keys). I considered libtoml or a hand-rolled parser; both
add a dependency or surface area for no gain.

### Logger format is JSON-lines with a fixed key set

`ts level subsys msg pid` are always emitted, plus a flat key/value
tail. No nested objects in log lines — production log search tools
key on flat scalars. Caller-supplied keys go through identical
escaping so a user-controlled value can't break the line.

### Logger record cap is 4 KiB; config file cap is 1 MiB

Both are conservative anti-flood caps. A caller can hand us a 1 MB
SQL string and we'll truncate the line rather than burn memory on a
log message. A config file > 1 MiB is treated as malformed and
refused — no real config approaches that size.

### Error codes never leak `errno`

Every syscall failure is mapped through `sa_err_from_errno()` at the
syscall site; the raw value is discarded. This guarantees we never
end up in a situation where an audit log entry mixes "errno=13" and
"errno=EACCES" depending on which translator ran where.

### CMocka via FetchContent, not system package

Pulling CMocka through CMake's FetchContent means the test target is
buildable on any box with internet, no apt install dance. The cost
is a one-time clone; the build is otherwise hermetic.

### Strict flags compile entire codebase; cJSON keeps its own

cJSON's source has shadow-name and conversion patterns that don't
clear our `-Wconversion -Werror`. Rather than fork it, we apply the
strict warnings only to `sftpadmin_core` sources via
`target_compile_options(... PRIVATE ...)`. cJSON compiles with its
own defaults inside its own subdir.

### `_FILE_OFFSET_BITS=64` is set globally

The spec is explicit: 64-bit offsets everywhere, no signed 32-bit
length/offset in a data path. Setting the macro at CMake-compile-
options level means every translation unit gets it, including any
later third-party header we drag in.
