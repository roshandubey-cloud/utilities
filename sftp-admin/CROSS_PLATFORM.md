# CROSS_PLATFORM

How sftpadmind runs on Linux, macOS, and Windows; what's the same, what
diverges, and how the release artefacts are organised.

---

## Goal

> Backend in C; UI optional (web UI served by the backend, OR a native
> desktop app frame that wraps the same web UI). Releases ship per
> (OS × UI) so a user picks the bundle that fits their machine.

## Distribution matrix

Three OS targets × two UI flavours = six release bundles per version.

| Bundle | OS | UI | What's inside |
|---|---|---|---|
| `sftpadmind-<ver>-linux-x64-cli.tar.gz` | Linux x64 | none | daemon binary + example config |
| `sftpadmind-<ver>-linux-x64-webui.tar.gz` | Linux x64 | web | daemon binary (UI embedded), open `https://localhost:9443` in your browser |
| `sftpadmind-<ver>-linux-x64-app.tar.gz` | Linux x64 | desktop | daemon + Go/Wails wrapper opening the embedded UI in a native WebKitGTK window |
| `sftpadmind-<ver>-macos-arm64-cli.tar.gz` | macOS arm64 | none | same shape as Linux |
| `sftpadmind-<ver>-macos-arm64-webui.tar.gz` | macOS arm64 | web | same |
| `sftpadmind-<ver>-macos-arm64-app.dmg` | macOS arm64 | desktop | daemon + WKWebView wrapper, signed .dmg |
| `sftpadmind-<ver>-windows-x64-cli.zip` | Windows x64 | none | daemon `.exe` + example config |
| `sftpadmind-<ver>-windows-x64-webui.zip` | Windows x64 | web | daemon `.exe`, browser UI |
| `sftpadmind-<ver>-windows-x64-app.zip` | Windows x64 | desktop | daemon `.exe` + Go/Wails wrapper opening the embedded UI in a WebView2 window |

**Phase 1 ships the `cli` variant only.** The `webui` variant is
unblocked by Phase 8 (admin API + embedded UI). The `app` variant
layers a Go/Wails wrapper on top of the `webui` daemon and is
delivered alongside it.

## Why three UI flavours?

* **`cli`** — for ops teams that drive the daemon from `sftpadminctl`
  and `systemctl` (or Windows Services). No UI dependencies, smallest
  attack surface.
* **`webui`** — for teams who want the admin UI but don't want a
  desktop app installed. Embedded server + browser; no extra
  processes.
* **`app`** — for individual developers / single-machine deployments.
  Double-click to launch; the embedded UI shows up in a native
  window. No browser, no public port, no firewall surprises.

All three share the same C daemon binary. The `app` variant adds a Go
sidecar that talks to the daemon over localhost.

## Platform divergence inside the C code

The whole project is plain C11 with one tiny abstraction header
([`include/sftpadmin/portable.h`](include/sftpadmin/portable.h)). Per
platform, the only diverging primitives are:

| Concern | Linux / BSD | macOS | Windows |
|---|---|---|---|
| Mutex | `pthread_mutex_t` | `pthread_mutex_t` | `CRITICAL_SECTION` |
| Process id | `getpid()` | `getpid()` | `GetCurrentProcessId()` |
| ISO-8601 UTC time | `clock_gettime` + `gmtime_r` | same | `GetSystemTimePreciseAsFileTime` + `gmtime_s` |
| Default data dir | `$XDG_DATA_HOME/sftpadmin` or `~/.local/share/sftpadmin` | `~/Library/Application Support/sftpadmin` | `%APPDATA%\sftpadmin` |
| Signal handlers | `sigaction(SIGTERM/SIGINT)` + ignore `SIGPIPE` | same | `signal(SIGTERM/SIGINT)` + `SetConsoleCtrlHandler` |
| Wait-for-term | `pause()` loop | same | `Sleep(100)` loop |
| Path separator | `/` | `/` | `\` |
| Abs-path check | starts with `/` | starts with `/` | drive-letter + colon + slash, OR `\\` UNC |
| Syslog sink | `syslog()` | `syslog()` (legacy; works) | not available — logs go to stderr + file only |
| Hardening flags | `-fPIE -fstack-protector-strong -D_FORTIFY_SOURCE=2 -Wl,-z,relro -Wl,-z,now` | same | `/sdl /W4 /WX /permissive-` (and the linker's `/DYNAMICBASE /HIGHENTROPYVA` defaults) |

For phases beyond 1, the platform divergence grows in a few places:

* **Phase 7 (supervisor)** — POSIX uses `fork()` per listener for the
  privilege-separation security model the spec demands. Windows
  doesn't have fork. We'll use one-process + thread-pool with
  per-listener Job Objects on Windows; the privilege-separation
  guarantee is weaker on Windows and that's documented as a known
  difference, not a bug.
* **Phase 8 (admin API)** — libmicrohttpd builds on all three. TLS
  cert paths follow the per-OS data-dir convention.

## Build matrix

CI builds via `.github/workflows/sftp-admin-build.yml`:

| Runner | Compiler | Notes |
|---|---|---|
| `ubuntu-22.04` | gcc 11 | strict flags, sanitiser-clean target available |
| `macos-14` (arm64) | Apple clang 15 | same strict flags |
| `windows-2022` | MSVC 19.x | `/W4 /WX /permissive-` |

Every push that touches `sftp-admin/**` runs the full build × test on
all three. A tag matching `sa-vX.Y.Z` packages the artefacts and
attaches them to the GitHub Release.

## Local builds

### POSIX (Linux / macOS / WSL2)

```sh
cmake -B build -DCMAKE_BUILD_TYPE=Release
cmake --build build -j
ctest --test-dir build --output-on-failure
./build/sftpadmind --config examples/sftpadmin.conf
```

### Windows

```pwsh
cmake -B build -DCMAKE_BUILD_TYPE=Release -G "Visual Studio 17 2022" -A x64
cmake --build build --config Release -j
ctest --test-dir build -C Release --output-on-failure
.\build\Release\sftpadmind.exe --config examples\sftpadmin.conf
```

### Docker (any host)

```sh
./scripts/build-in-docker.sh
```

Builds inside `gcc:13`, drops `./build/sftpadmind` for Linux.

## Default paths per OS (Phase 1 config)

The daemon defaults to a per-user data directory so a fresh install
just works:

| OS | Data dir |
|---|---|
| Linux / BSD | `$XDG_DATA_HOME/sftpadmin` → `$HOME/.local/share/sftpadmin` |
| macOS | `$HOME/Library/Application Support/sftpadmin` |
| Windows | `%APPDATA%\sftpadmin` (typically `C:\Users\<user>\AppData\Roaming\sftpadmin`) |

Inside that dir we put `sftpadmin.db`, `hostkeys/`, `master.key`,
`run/`, `admin-cert.pem`, `admin-key.pem`. The operator can override
every path via `paths.*` in the JSON config.

## Open items

* **macOS code-signing** for the `.dmg`-packaged `app` variant.
  Doable but needs Apple Developer credentials; deferred to whatever
  release first requires it.
* **Windows installer (`.msi`)** for the `app` variant. The current
  CI ships a plain `.zip`. WiX or `dotnet wixsdk` adds an installer
  in a Phase 8+ pass.
