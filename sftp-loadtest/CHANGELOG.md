# Changelog

All notable changes to **sftp-loadtest** are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/).

Releases ship two SKUs at every tag — the `webui` CLI/server binary and the
`desktop-app` Wails native window — for mac-apple-silicon, mac-intel,
linux-amd64, linux-arm64 (webui only for now), and windows-amd64. Asset URLs
follow the `releases/latest/download/<asset>` pattern so README links
self-update.

## [Unreleased]

### Added
- Cluster: SSH KeepAlive on every spawned worker tunnel. The master pings
  `keepalive@openssh.com` every 30s; after 3 consecutive failures the tunnel
  closes proactively so `/api/cluster/status` flips to "unreachable" within
  ~90s instead of silently aggregating zero progress. Runtime test
  `TestKeepAlive_ClosesTunnelOnSessionLoss` exercises the failure path.
- Cluster: worker version negotiation. `/healthz?detail=1` now reports the
  master's platform version; `/api/cluster/start` captures each worker's
  version and the cluster status JSON exposes `master_version`,
  per-worker `version`, and a `version_mismatch` flag. Workers that don't
  expose version (older releases) leave Version empty without blocking the
  run. Pinned by `TestCoordinator_VersionNegotiation`.
- CI: new `build-linux-arm64-desktop` job on `ubuntu-22.04-arm` ships the
  desktop-app for linux/arm64 — closes the dual-SKU parity gap (webui
  already had linux-arm64; desktop-app was amd64-only).
- UI: token-ified the first batch of inline px styles in `index.html`
  (`margin-bottom:8px`, `padding:8px 0 0`, `gap:8px` → `var(--sp-2)`).
  First step toward the v0.9.0 workbench refactor.

### Changed
- Bumped `platformVersion` to `0.13.5` so the tester can verify the build
  the cluster status UI surfaces this in `master_version`.
- CI: pinned `wails` CLI to `v2.12.0` (matches `go.mod`) and wrapped install
  with a 3-attempt retry across mac / linux / windows / linux-arm64 runners.
  Closes the `proxy.golang.org` timeout class that lost 8 of 20 assets on
  v0.13.3.
- CI: bumped `actions/checkout@v4 → v5` and `actions/setup-go@v5 → v6` (Node.js
  20 deprecation, enforcement 2026-06-02).

## [v0.13.5] — 2026-05-01
Same content as Unreleased above; tag was cut on 2026-05-01 to ship the
cluster keepalive + version negotiation + linux-arm64 desktop SKU + UI
token-ify batch.

### Fixed (post-release CI hygiene, applied on `main` after the tag)
- Forced `FORCE_JAVASCRIPT_ACTIONS_TO_NODE24=true` at the workflow level
  so `softprops/action-gh-release@v2` (still Node 20) keeps working past
  the June 2 deadline.
- Pinned `cache-dependency-path: sftp-loadtest/go.sum` on all four
  release-pipeline `setup-go@v6` invocations — ends the "Dependencies
  file is not found" cache miss on every release run.

## [v0.13.4] — 2026-05-01
### Added
- Cluster: NDJSON streaming spawn with real-time per-step status events to the
  UI, replacing the previous batch result.
- macOS install hardening for spawned workers (sudo-free path, robust shell
  detection).

## [v0.13.3] — 2026-04-30
### Added
- Cluster: real-time situation-aware guidance during SSH spawn — surfaces
  actionable hints (auth, connectivity, install, runtime) instead of raw error
  strings.
### Known issue
- macOS release job failed on `wails@latest` install (network timeout). Linux
  and Windows assets shipped; mac assets missing on this tag. Closed in
  `Unreleased` by pinning + retry.

## [v0.13.2] — 2026-04-29
### Added
- Cluster: closed 3 worker validation gaps — preflight check, live probe,
  sidebar LED.

## [v0.13.1] — 2026-04-28
### Added
- FTPS: certificate TOFU consent + renewal modal.

## [v0.13.0] — 2026-04-27
### Added
- Multi-protocol: FTP and FTPS join SFTP under a single run model.

## [v0.12.0] — 2026-04-25
### Changed
- UI: cross-platform visual parity — bundle Inter + JetBrains Mono, drop
  Windows-only tone overrides.

## [v0.11.4] — 2026-04-24
### Fixed
- Desktop / Windows: restore system close / minimise / maximise controls.

## [v0.11.3] — 2026-04-24
### Changed
- UI: deep Windows dark-theme refinement — proper tier hierarchy, true gloss.

## [v0.11.2] — 2026-04-23
### Changed
- UI: glossier Windows feel — ambient glow, richer sheen, halo accents.

## [v0.11.1] — 2026-04-23
### Changed
- Desktop: Windows visual parity with macOS via Mica + CustomTheme.

## [v0.11.0] — 2026-04-22
### Added
- Cluster: SSH-bootstrapped worker spawn — controller installs and starts
  remote `sftp-loadtest` workers over SSH, fans out a single run across N
  workers, aggregates metrics back to the controller UI.

## [v0.10.x] — 2026-04-19 → 2026-04-21
- v0.10.7: `Save preset…` button in Configure prelude.
- v0.10.6: disable Distribute toggle when zero workers enabled.
- v0.10.5: close 3 gaps from v0.10.4 validation pass.
- v0.10.4: validation-pass docs.
- v0.10.3: sidebar nav no longer escapes the run-detail pane.
- v0.10.2: live-metrics tiles no longer freeze during an active run.
- v0.10.1: saved connections — named host/port/user/pass entries.
- v0.10.0: SSH public-key auth — shared key at the run level.

## [v0.9.x] — 2026-04-15 → 2026-04-18
- v0.9.10: hide redundant Export config in Schedule card.
- v0.9.9: hoist Import config to top prelude.
- v0.9.8: compact action toolbar — consistent buttons + icons.
- v0.9.7: slim Run-summary into sticky pill with inline play/stop.
- v0.9.6: densify Configure — 4 fields per row.
- v0.9.5: split Resource limits into Upload / Download / Run sub-groups.
- v0.9.4: Configure redesign + global glassy polish.
- v0.9.3: host-key consent in UI, results to Workbench, Runs view.
- v0.9.2: Schedule + Review in sidebar, Cluster scope fix.
- v0.9.1: Apple-TV sidebar, view switcher, modal system.
- v0.9.0: polish + 75-spec Playwright lock-down.

[Unreleased]: https://github.com/roshandubey-cloud/utilities/compare/v0.13.4...HEAD
[v0.13.4]: https://github.com/roshandubey-cloud/utilities/releases/tag/v0.13.4
[v0.13.3]: https://github.com/roshandubey-cloud/utilities/releases/tag/v0.13.3
[v0.13.2]: https://github.com/roshandubey-cloud/utilities/releases/tag/v0.13.2
[v0.13.1]: https://github.com/roshandubey-cloud/utilities/releases/tag/v0.13.1
[v0.13.0]: https://github.com/roshandubey-cloud/utilities/releases/tag/v0.13.0
[v0.12.0]: https://github.com/roshandubey-cloud/utilities/releases/tag/v0.12.0
[v0.11.4]: https://github.com/roshandubey-cloud/utilities/releases/tag/v0.11.4
[v0.11.3]: https://github.com/roshandubey-cloud/utilities/releases/tag/v0.11.3
[v0.11.2]: https://github.com/roshandubey-cloud/utilities/releases/tag/v0.11.2
[v0.11.1]: https://github.com/roshandubey-cloud/utilities/releases/tag/v0.11.1
[v0.11.0]: https://github.com/roshandubey-cloud/utilities/releases/tag/v0.11.0
