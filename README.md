# utilities

> A small collection of self-contained engineering utilities. Each tool
> lives in its own subdirectory, builds to a single static binary, and
> ships pre-built for macOS / Linux / Windows.

[![Latest release](https://img.shields.io/github/v/release/roshandubey-cloud/utilities?label=latest&color=ee5b21)](https://github.com/roshandubey-cloud/utilities/releases/latest)
[![License](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

![sftp-loadtest workbench — live throughput, latency percentiles, per-file activity tail](sftp-loadtest/docs/screenshots/workbench-active-dark.png)

<table>
  <tr>
    <td width="33%"><a href="sftp-loadtest/docs/screenshots/configure-dark.png"><img src="sftp-loadtest/docs/screenshots/configure-dark.png" alt="Configure"></a><br><sub><b>Configure</b> — Target, Workload (Upload + Large + Download), Limits</sub></td>
    <td width="33%"><a href="sftp-loadtest/docs/screenshots/runs-dark.png"><img src="sftp-loadtest/docs/screenshots/runs-dark.png" alt="Runs"></a><br><sub><b>Runs</b> — Per-run history with CSV download. Cluster runs expand to per-worker rows.</sub></td>
    <td width="33%"><a href="sftp-loadtest/docs/screenshots/cluster-with-workers-dark.png"><img src="sftp-loadtest/docs/screenshots/cluster-with-workers-dark.png" alt="Cluster"></a><br><sub><b>Cluster</b> — Multi-worker fan-out via SSH. No preinstalled agent.</sub></td>
  </tr>
  <tr>
    <td><a href="sftp-loadtest/docs/screenshots/schedule-dark.png"><img src="sftp-loadtest/docs/screenshots/schedule-dark.png" alt="Schedule"></a><br><sub><b>Schedule</b> — Queue runs for later, fires automatically.</sub></td>
    <td><a href="sftp-loadtest/docs/screenshots/trust-dark.png"><img src="sftp-loadtest/docs/screenshots/trust-dark.png" alt="Trust"></a><br><sub><b>Trust</b> — SSH host keys + FTPS leaf certs (TOFU pinning).</sub></td>
    <td><a href="sftp-loadtest/docs/screenshots/cmdk-palette-open-dark.png"><img src="sftp-loadtest/docs/screenshots/cmdk-palette-open-dark.png" alt="Command palette"></a><br><sub><b>Cmd+K</b> — Every action reachable by typing.</sub></td>
  </tr>
</table>

<sub>All screenshots in dark theme; <a href="sftp-loadtest/docs/screenshots/">light variants in `sftp-loadtest/docs/screenshots/`</a>.</sub>

---

## Tools

| Tool | What it does | Languages |
|---|---|---|
| [**sftp-loadtest/**](./sftp-loadtest) | Production-grade SFTP / FTP / FTPS load tester. Wails desktop app + CLI/server SKU sharing one engine. Real-file uploads + byte-faithful round-trip downloads. Multi-worker fan-out via SSH with cumulative reporting. ~10 MB binary, sub-15 MB RSS at idle. | Go |

## 6 video tutorials

[Production-ready scripts](sftp-loadtest/docs/tutorials/) for ~3-minute customer-grade walkthrough videos. Storyboards include exact UI actions, values to type, and word-for-word VO scripts:

| | Tutorial | What it teaches |
|---|---|---|
| 01 | [First run in 90 seconds](sftp-loadtest/docs/tutorials/01-first-run.md) | Synthetic upload + TOFU + CSV export |
| 02 | [Real files end-to-end](sftp-loadtest/docs/tutorials/02-real-files-end-to-end.md) | local-dir source + local-disk sink + SHA-256 verification |
| 03 | [N accounts via conventions](sftp-loadtest/docs/tutorials/03-n-accounts-conventions.md) | Layout picker + per-user probe matrix |
| 04 | [Round-trip + processing time](sftp-loadtest/docs/tutorials/04-roundtrip-processing-time.md) | Track-id / filename modes + p50/p95/p99 |
| 05 | [Worker fan-out](sftp-loadtest/docs/tutorials/05-worker-fanout-cluster.md) | SSH wizard + Distribute toggle + cumulative reporting |
| 06 | [Enterprise: FTPS, trust, scheduling](sftp-loadtest/docs/tutorials/06-enterprise-trust-scheduling.md) | TOFU + Trust panel as audit surface + scheduled runs |

---

## Major release highlights

Every `v0.X.0` is a meaningful feature drop. Patch releases (`v0.X.Y` for Y > 0)
are bug fixes / UX polish — see the [full CHANGELOG](sftp-loadtest/CHANGELOG.md) for those.

| Version | Headline | Release notes |
|---|---|---|
| **v0.14.x** | **Sources & sinks** — real files from disk in / round-trip back to disk out. Layout picker (`flat` / `by-user` / `by-pattern` / `by-user-pattern`) for n-account tests. Smart auto-link source → sink. Server-rendered version pill. Full export/import round-trip parity. | [v0.14.20](https://github.com/roshandubey-cloud/utilities/releases/tag/v0.14.20) |
| **v0.13.x** | **Multi-protocol** — FTP + FTPS join SFTP under one run model. Protocol picker auto-snaps the port; FTPS supports both Explicit (AUTH TLS, port 21) and Implicit (TLS from byte 0, port 990). FTPS leaf-cert TOFU pinning. | [v0.13.30](https://github.com/roshandubey-cloud/utilities/releases/tag/v0.13.30) |
| **v0.12.0** | **Cross-platform visual parity** — bundle Inter + JetBrains Mono, drop Windows-only tone overrides. macOS / Linux / Windows render identically. | [v0.12.0](https://github.com/roshandubey-cloud/utilities/releases/tag/v0.12.0) |
| **v0.11.0** | **SSH-bootstrapped cluster spawn** — the controller installs + starts remote `sftp-loadtest` workers over SSH, fans out a single run across N workers, aggregates metrics back to the controller UI. No preinstalled agent. | [v0.11.0](https://github.com/roshandubey-cloud/utilities/releases/tag/v0.11.0) |
| **v0.10.0** | **SSH public-key auth** — paste a PEM in the connection card; every user authenticates with that key (shared-key model for v1). | [v0.10.0](https://github.com/roshandubey-cloud/utilities/releases/tag/v0.10.0) |
| **v0.9.0** | **Workbench redesign** — sidebar nav + view switcher (Workbench / Configure / Schedule / Runs / Cluster / Trust), Cmd+K palette, slim run-summary bar with live chips, dark/light themes. 8 waves, 75 specs. | [v0.9.0](https://github.com/roshandubey-cloud/utilities/releases/tag/v0.9.0) |
| **v0.8.0** | **Cluster mode MVP** — breaks the single-host ceiling. Add `sftp-loadtest` worker URLs, enable the Distribute toggle, one Start fans out via `/api/cluster/start`. | [v0.8.0](https://github.com/roshandubey-cloud/utilities/releases/tag/v0.8.0) |
| **v0.7.0** | **Tool-managed SSH trust store** — all key state lives in the UI. Trust panel lists pinned SSH host keys with SHA-256 fingerprints; click × to forget. | [v0.7.0](https://github.com/roshandubey-cloud/utilities/releases/tag/v0.7.0) |
| **v0.6.0** | **Post-run analysis** — host-capacity peaks (CPU / FD / network) cross-referenced with the slowdown timeline; config suggestions based on what bottlenecked. | [v0.6.0](https://github.com/roshandubey-cloud/utilities/releases/tag/v0.6.0) |
| **v0.5.0** | **Host-key consent during runs** — Test connection probe surfaces an unknown host's fingerprint in a modal and pins it on approval. Simplified users editor. | [v0.5.0](https://github.com/roshandubey-cloud/utilities/releases/tag/v0.5.0) |
| **v0.4.0** | **Desktop SKU + Grafana-style UI redesign** — Wails-based native app for macOS / Linux / Windows alongside the CLI/server SKU. Per-asset release pipeline; one binary per zip. | [v0.4.0](https://github.com/roshandubey-cloud/utilities/releases/tag/v0.4.0) |
| **v0.3.0** | **TOFU host-key enrollment** via the Test connection button. Pinned in `~/.ssh/known_hosts` automatically; subsequent runs verify strictly. | [v0.3.0](https://github.com/roshandubey-cloud/utilities/releases/tag/v0.3.0) |
| **v0.2.0** | **Self-updating release links** — README and CI use `releases/latest/download/<asset>` pattern so download links don't go stale on every tag. | [v0.2.0](https://github.com/roshandubey-cloud/utilities/releases/tag/v0.2.0) |
| **v0.1.0** | **Initial public release** — first tagged build with the LinkedIn launch artefacts. | [v0.1.0](https://github.com/roshandubey-cloud/utilities/releases/tag/v0.1.0) |

[**All releases →**](https://github.com/roshandubey-cloud/utilities/releases)

---

## Conventions across all tools

- **One static binary per platform.** No Python, no Node, no JVM, no
  installer. `chmod +x` (or unblock on Windows) and run.
- **Cross-platform.** Each tool ships pre-built for darwin/arm64,
  darwin/amd64, linux/amd64, linux/arm64, windows/amd64.
- **Web UI on `127.0.0.1` by default.** No bundled authentication —
  expose via SSH tunnel or put nginx in front for remote access.
- **Two SKUs per tool where relevant.** A CLI / server binary plus a
  Wails-native desktop app. Same code, same UI, two transport shells.
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

Desktop SKU (Wails) build:

```sh
cd cmd/desktop
wails build
# Output: cmd/desktop/build/bin/sftp-loadtest-desktop.app  (macOS)
#         cmd/desktop/build/bin/sftp-loadtest-desktop      (Linux)
#         cmd/desktop/build/bin/sftp-loadtest-desktop.exe  (Windows)
```

## Contributing

Issues, PRs, and feature ideas welcome. Each tool's behaviour is small
enough to fit in your head; please read the tool's `README.md` before
proposing changes.
