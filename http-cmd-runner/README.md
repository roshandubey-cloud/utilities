# http-cmd-runner

A small, single-binary HTTP wrapper (Go, stdlib only) that executes Linux
commands and scripts on the host it runs on and returns their output as JSON.

By default it runs in **arbitrary mode** — any shell line you POST is executed
via `sh -c` and the stdout/stderr/exit code come back in the response.

> ⚠️ **There is NO authentication and all commands are allowed.** Anyone who can
> reach the listen address can run any command as the service user. This is
> remote code execution by design. Keep `listen` bound to `127.0.0.1` (the
> default) or a trusted private network, and never expose it to the internet.
> If you later want a lock-down, set `allow_arbitrary: false` and fill in
> `allowlist`.

## Build

```sh
# On the Linux server (or cross-compile from anywhere):
go build -o http-cmd-runner .

# Cross-compile from Windows/macOS for a Linux x86-64 server:
#   PowerShell:  $env:GOOS="linux"; $env:GOARCH="amd64"; go build -o http-cmd-runner .
#   bash:        GOOS=linux GOARCH=amd64 go build -o http-cmd-runner .
```

No external dependencies — pure Go standard library.

## Configure

```sh
cp config.example.json config.json
```

Key config fields (see `config.go` for all):

| Field             | Meaning                                                        |
|-------------------|----------------------------------------------------------------|
| `listen`          | Bind address. Default `127.0.0.1:8080` (localhost only).       |
| `allow_arbitrary` | `true` (default) = any `sh -c` command; `false` = allowlist.   |
| `allowlist`       | Program names allowed when `allow_arbitrary` is `false`.       |
| `default_timeout_sec` / `max_timeout_sec` | Per-request timeout and its cap.      |
| `max_output_bytes`| Caps captured stdout/stderr (default 1 MiB).                   |
| `working_dir`     | Directory commands run in. Empty = process cwd.                |
| `tls_cert_file` / `tls_key_file` | Set both to serve HTTPS directly.               |
| `log_file`        | Append audit lines here. Empty = stdout.                       |

`CMDRUNNER_LISTEN` env var overrides `listen` if set.

## Run

```sh
./http-cmd-runner -config config.json
```

## API

### `GET /healthz`
Returns `200 ok`.

### `POST /exec`
Request body:

```json
{
  "command": "ls -la /var/log | head",
  "stdin": "",
  "timeout_sec": 30
}
```

- **arbitrary mode** (default): `command` is the full shell line.
- **allowlist mode** (`allow_arbitrary: false`): `command` must match an
  allowlist entry and `args` are passed through directly (no shell).

Response — the Linux output parsed back to you as JSON:

```json
{
  "stdout": "...",
  "stderr": "...",
  "exit_code": 0,
  "duration_ms": 12,
  "timed_out": false,
  "truncated": false
}
```

`exit_code` is `-1` when the process could not be started.

## Examples

```sh
# run any command line
curl -s http://127.0.0.1:8080/exec \
  -H "Content-Type: application/json" \
  -d '{"command":"uptime && free -m"}'

# pipes, redirects, scripts — all work in arbitrary mode
curl -s http://127.0.0.1:8080/exec \
  -H "Content-Type: application/json" \
  -d '{"command":"df -h | grep -v tmpfs"}'

# feed stdin to a command
curl -s http://127.0.0.1:8080/exec \
  -H "Content-Type: application/json" \
  -d '{"command":"cat","stdin":"hello\n"}'
```

## Run with Docker / Rancher Desktop

```sh
docker build -t http-cmd-runner .
# bind to 0.0.0.0 inside the container so the mapped port is reachable
docker run -d --name cmdrunner -p 8080:8080 \
  -e CMDRUNNER_LISTEN="0.0.0.0:8080" \
  http-cmd-runner
```

## Deploy as a systemd service

```sh
sudo useradd -r -s /usr/sbin/nologin cmdrunner
sudo install -m 0755 http-cmd-runner /usr/local/bin/
sudo mkdir -p /etc/http-cmd-runner
sudo install -m 0644 config.json /etc/http-cmd-runner/config.json
sudo cp http-cmd-runner.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now http-cmd-runner
sudo systemctl status http-cmd-runner
```

The unit ships with `NoNewPrivileges`, `ProtectSystem=strict`, `PrivateTmp`,
and friends to limit blast radius. Add `ReadWritePaths=` for any directory your
scripts must write to.

## Security notes

Since there is no auth and every command is allowed:

- Keep `listen` on `127.0.0.1` or a trusted private interface — never the internet.
- Run as a dedicated **unprivileged** user; never as root.
- Watch the audit log (`log_file`) — every exec is recorded.
- If you ever need restrictions, flip `allow_arbitrary` to `false` and populate
  `allowlist`; you can also front it with nginx/Caddy for TLS or IP allowlisting.
```
