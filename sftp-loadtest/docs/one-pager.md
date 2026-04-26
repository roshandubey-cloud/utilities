# sftp-loadtest

> **End-to-end SFTP load testing. One static binary. No fluff.**

A web-UI-driven SFTP load tester that measures upload throughput, server-side
processing time, and download round-trips — honestly, even on multi-hour
high-FPM runs. Ships as a 10 MB binary for macOS, Linux, and Windows.

---

## What you can test

| Use case | The question it answers |
|---|---|
| **Throughput ceiling** | What sustained MB/s does my SFTP server hold under N concurrent uploaders? |
| **Server-side processing time** | How long until the server tags an uploaded file with a track-id? |
| **End-to-end round-trip** | Upload → processing → download — captured per file. |
| **Concurrent capacity** | How many users × parallel streams before connections are refused? |
| **24h+ soak / stability** | Will the pipeline survive a day of continuous load? |
| **Failure taxonomy** | What error classes (`CREATE / WRITE / CLOSE / TIMEOUT / DOWNLOAD`) appear under stress? |
| **Bad-credential isolation** | Which user accounts silently fail? Auto-disabled at 3 consecutive errors. |
| **Client-side network ceiling** | What MB/s is your client→server link *actually* delivering right now? |

---

## Key features

| | |
|---|---|
| **Lightweight** | 10 MB single static binary · ~8 MB RSS at idle · zero runtime deps |
| **Self-healing** | SSH keepalives + pool reconnect + watcher redial — multi-day runs survive idle drops |
| **Honest metrics** | Per-file rate when timing is reliable, minute-window rate otherwise — never blank, never inflated |
| **Streaming reports** | CSV flushed to disk during the run; RAM stays flat regardless of file count |
| **Scheduler built-in** | Queue runs for later; survives restarts; missed-during-downtime runs dropped, not stampeded |
| **Per-user auto-disable** | Bad credentials taken out of rotation after N consecutive fails, recorded in the report |
| **Pre-flight probe** | One-click TCP → SSH → SFTP → folder-list test with per-stage timings |
| **Live visibility** | `/healthz`, `/api/host`, `/api/status`, `/api/probe`, `/debug/pprof` (opt-in) |
| **Config portability** | Export / import full config (incl. users) as JSON; share + replay tests verbatim |
| **Cross-platform** | macOS arm64/amd64, Linux amd64/arm64, Windows amd64 |

---

## 60-second start

```sh
unzip sftp-loadtest-mac.zip
xattr -cr ./sftp-loadtest-mac-apple-silicon && chmod +x ./sftp-loadtest-mac-apple-silicon
./sftp-loadtest-mac-apple-silicon
# → open http://127.0.0.1:8080
```

Fill the form → **Test connection** → **Start run**.

---

## Footprint

| | Idle | 50 users × 30 streams @ 1k fpm | Over 24 h |
|---|---|---|---|
| **RSS** | ~8 MB | ~250 MB | **flat** |
| **CPU** | ~0% | < 5% | **flat** |
| **Disk** | — | ~500 KB/min CSV | ~720 MB |

---

## Why not k6 / Locust / JMeter

- General HTTP/load tools — **this is SFTP-first**.
- They start at 50–80 MB and pull in JVM/Python/Node — **this is one 10 MB binary**.
- They measure what they send — **this also measures server-side processing time** per file.
- They need scripting for round-trips — **this observes upload → process → download natively**, no pairing config required.

---

## Limitations (be upfront)

Password SFTP auth only · No host-key verification (lab use, not prod data) ·
No HTTPS on the UI (bind to `127.0.0.1` + SSH-tunnel for remote access) ·
No bundled UI authentication · One active run per process.

---

**MIT licensed · open source · screenshots + full docs in [README.md](../README.md)**
**Source:** [github.com/roshandubey-cloud/utilities](https://github.com/roshandubey-cloud/utilities)
