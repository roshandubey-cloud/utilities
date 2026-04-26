# utilities

A small collection of self-contained engineering utilities. Each tool lives
in its own subdirectory, builds to a single static binary, and ships with
its own README + zipped releases for macOS / Windows / Linux.

## Tools

| Tool | What it does | Languages |
|---|---|---|
| [**sftp-loadtest/**](./sftp-loadtest) | Lightweight, production-grade SFTP load tester with a newspaper-themed web UI: per-file metrics, processing-time tracking, scheduled runs, streaming CSV reports, self-healing connections, per-user auto-disable. ~10 MB binary, sub-15 MB RSS at idle. | Go |

## Conventions across all tools

- **One static binary per platform.** No Python, no Node, no JVM, no
  installer. `chmod +x` (or unblock on Windows) and run.
- **Cross-platform.** Each tool ships pre-built for darwin/arm64,
  darwin/amd64, linux/amd64, linux/arm64, windows/amd64.
- **Web UI on `127.0.0.1` by default.** No bundled authentication —
  expose via SSH tunnel or put nginx in front for remote access.
- **MIT license.** See [LICENSE](./LICENSE).

## Building from source

Each tool has its own `go.mod`. From a clean clone:

```sh
git clone https://github.com/roshandubey-cloud/utilities.git
cd utilities/sftp-loadtest
go build -o sftp-loadtest .
./sftp-loadtest
```

Cross-compile:

```sh
GOOS=linux  GOARCH=amd64 go build -o sftp-loadtest-linux-amd64  .
GOOS=darwin GOARCH=arm64 go build -o sftp-loadtest-mac-arm64    .
GOOS=windows GOARCH=amd64 go build -o sftp-loadtest-win.exe     .
```

## Contributing

Issues, PRs, and feature ideas welcome. Each tool's behaviour is small
enough to fit in your head; please read the tool's `README.md` before
proposing changes.
