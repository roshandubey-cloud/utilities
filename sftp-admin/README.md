# sftpadmind

Cross-platform enterprise SFTP server administration suite. A single
C daemon that supervises N independent SFTP listeners, manages users /
SSH keys / security profiles centrally, and ships in three UI flavours
(headless `cli`, browser `webui`, native desktop `app`) on Linux,
macOS, and Windows.

**This is Phase 1 of 10** (portable foundation). Phase 1 brings the
foundation online: logger, error subsystem, config loader, supervisor
skeleton — all cross-platform. Listener fork, auth, SFTP engine,
admin UI etc. land in subsequent phases. See [GAPS.md](GAPS.md) for
what's pending and which phase delivers it. See
[CROSS_PLATFORM.md](CROSS_PLATFORM.md) for how each OS / UI bundle is
built and what diverges between them.

## Status

| Layer | Status |
|---|---|
| Build (CMake, strict flags) | ✅ Phase 1 |
| Logger (JSON-lines, syslog, fork-safe) | ✅ Phase 1 |
| Error subsystem | ✅ Phase 1 |
| Config loader (cJSON, schema validation) | ✅ Phase 1 |
| Daemon entrypoint (args, signals, banner) | ✅ Phase 1 |
| SQLite data layer | ⏳ Phase 2 |
| First listener end-to-end + 5 GB transfer | ⏳ Phase 3 |
| Full SFTP handler + path-jail fuzz | ⏳ Phase 4 |
| Security profiles + multi-hostkey | ⏳ Phase 5 |
| Pubkey auth + brute-force defense | ⏳ Phase 6 |
| Supervisor + control socket + drain | ⏳ Phase 7 |
| Admin API + sftpadminctl | ⏳ Phase 8 |
| Web UI + SSE | ⏳ Phase 9 |
| Observability + systemd packaging | ⏳ Phase 10 |

## Build

Cross-platform from day one. Same source tree builds on Linux, macOS,
and Windows. See [CROSS_PLATFORM.md](CROSS_PLATFORM.md) for the full
matrix and the per-OS build commands.

### Linux / macOS / BSD

```sh
cmake -B build -DCMAKE_BUILD_TYPE=Release
cmake --build build -j
ctest --test-dir build --output-on-failure
```

### Windows (MSVC)

```pwsh
cmake -B build -DCMAKE_BUILD_TYPE=Release -G "Visual Studio 17 2022" -A x64
cmake --build build --config Release -j
ctest --test-dir build -C Release --output-on-failure
```

### Docker (any host without a local C toolchain)

```sh
./scripts/build-in-docker.sh
```

Bind-mounts the repo at `/work`, runs cmake + ctest inside `gcc:13`,
writes build artefacts back to `./build/`.

### Build flags

The CMake `SFTPADMIN_STRICT` option (default **ON**) compiles every
sftpadmin source with the full hardening suite — `-Wall -Wextra
-Werror -Wconversion -fstack-protector-strong -D_FORTIFY_SOURCE=2
-fPIE`. Don't turn it off in CI.

`SFTPADMIN_ASAN=ON` rebuilds with AddressSanitizer + UBSan for the
test target.

## Run (Phase 1)

```sh
./build/sftpadmind --config examples/sftpadmin.conf
```

In Phase 1 the daemon loads config, validates it, initialises the
logger, prints a startup banner, and waits for SIGTERM/SIGINT. The
listener supervisor and admin API are not yet wired — that's Phase 7+.

## Library dependencies

Limited to the spec's allowed list:

* **libssh** — SSH/SFTP server (Phase 3+)
* **sqlite3** — config + audit store (Phase 2+)
* **libsodium** — argon2id, secretbox for host-key encryption (Phase 5/6)
* **libmicrohttpd** — admin HTTPS server (Phase 8)
* **cJSON** — config + admin-API JSON (already in use)

All UI is static HTML/CSS/vanilla JS served from libmicrohttpd. No
JavaScript bundler, no Node toolchain.

## Where things are

```
sftp-admin/
├── CMakeLists.txt              build entry
├── src/                        implementation
│   ├── err.c                   central error subsystem
│   ├── log.c                   structured logger
│   ├── config.c                config loader
│   └── main.c                  daemon entrypoint
├── include/sftpadmin/          public headers
├── tests/                      CMocka unit tests
├── examples/sftpadmin.conf     sample config
├── scripts/build-in-docker.sh  hermetic build
├── DECISIONS.md                durable design log
├── GAPS.md                     phase-by-phase status
└── SECURITY.md                 security posture + decisions
```

## License

MIT — same as the rest of `roshandubey-cloud/utilities`.
