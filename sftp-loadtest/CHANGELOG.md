# Changelog

All notable changes to **sftp-loadtest** are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/).

Releases ship two SKUs at every tag — the `webui` CLI/server binary and the
`desktop-app` Wails native window — for mac-apple-silicon, mac-intel,
linux-amd64, linux-arm64 (webui only for now), and windows-amd64. Asset URLs
follow the `releases/latest/download/<asset>` pattern so README links
self-update.

## [v0.13.7] — 2026-05-01
### Security
- **CVE fix:** Bumped `golang.org/x/crypto` from `v0.33.0` to `v0.50.0` —
  closes `GO-2025-3487` (DoS in `golang.org/x/crypto`) which `govulncheck`
  flagged in the keepalive loop, sftpx dial, mocksftp, and runExec call
  paths. Bumped Go module to `1.25` (required by the new x/crypto).
  Also bumped `x/net 0.35.0 → 0.52.0`, `x/sys 0.30.0 → 0.43.0`,
  `x/text 0.22.0 → 0.36.0` for transitive consistency.
- CI: bumped Go runtime from `1.22` to `1.25` on every release + test
  job (matches the new `go.mod` requirement).

### Added (enterprise hardening)
- `SECURITY.md` — coordinated-disclosure path + supported-version
  table.
- `.github/dependabot.yml` — weekly Go module + GitHub Actions updates.
- `govulncheck` step in the `test` workflow — every push fails CI if a
  *called* vulnerability appears.
- SBOM generation (CycloneDX) in `release.yml` — `sftp-loadtest-sbom-<tag>.cdx.json`
  uploaded as a release asset on every tag.
- `Dockerfile` (multi-stage, distroless base) + `.dockerignore` —
  `docker run roshandubey-cloud/sftp-loadtest` works against the same
  webui binary the GitHub release ships. Image build runs in CI on
  every tag; published to GHCR.

### Fixed (hygiene)
- Removed unused `disablePolicy.allUploadDisabled` (staticcheck U1000).
- Replaced deprecated `io/ioutil` import in `tunnel_test.go` with
  `os` / `io` (staticcheck SA1019).

## [v0.13.8] — 2026-05-01
Three follow-ups from the v0.13.7 validation pass.

### Added
- **FTPS auto-TOFU on first contact.** New `tls_trust_on_first_use`
  field on `/api/start` (and `RunConfig.TLSTrustOnFirstUse`). When set,
  the runner records an unknown FTPS server's leaf certificate to the
  trust store on first contact instead of failing the run with "unknown
  host" — same semantics as SFTP host-key TOFU. The web Server's
  TLSStore is now plumbed through `runner.StartWithPersistAndTLS` to
  the protocol layer; subsequent runs against the same (host, port)
  verify strictly, and a *changed* cert still refuses (operator must
  consent through the probe + TLS-renewal modal). Pinned by
  `TestFTPS_TLSStore_TrustOnFirstUse` (3-stage: TOFU dial → strict
  verify → fresh-store refuse).
- **Wails desktop-app bundle stamps the release version.** New
  `stamp wails.json with release version` step before each `wails build`
  in all four desktop-app jobs (mac arm64+intel, linux arm64, windows).
  Replaces the hardcoded `0.4.0-dev` with `${VERSION#v}` so
  `CFBundleVersion` / `CFBundleShortVersionString` track the release tag
  on every build. Closes the v0.13.5 validation finding.
- **Homebrew formula + auto-bump on release.** `Formula/sftp-loadtest.rb`
  installs the webui SKU on macOS / Linux (arm64 + intel). New
  `update-homebrew-formula` job in `release.yml` rewrites version + all
  four SHA256s and commits to main after build-mac + build-linux finish,
  so `brew tap roshandubey-cloud/utilities && brew install sftp-loadtest`
  always points at the latest tag.

### Changed
- `protocol.Dial` now sets `tls.Config.InsecureSkipVerify=true` whenever
  `TLSStore != nil`, so the store-backed `VerifyConnection` callback
  owns trust decisions end-to-end. Without this, Go's chain verifier
  rejected self-signed certs before TOFU could fire. Strictly safer
  than `InsecureSkipVerify` alone — unknown certs without TOFU still
  refuse.

## [v0.13.9] — 2026-05-01
### Fixed
- CI: linux-arm64-desktop job lost both v0.13.7 and v0.13.8 to a stale
  `ports.ubuntu.com` index (404s on `gstreamer1.0-plugins-base`,
  `libgstreamer-gl1.0-0`, etc. when the apt mirror was mid-refresh).
  apt install now uses `--no-install-recommends` (drops the gstreamer
  plugin set Wails doesn't need at build time) + `--fix-missing` and a
  3-attempt retry with linear backoff. Same fix applied to the linux
  amd64 job for symmetry. Also bumped Go to 1.25.

## [v0.13.10] — 2026-05-01
### Changed
- **UI: cross-platform Mac-glossy finish.** The Wails Windows desktop
  was reading "flat" against the Wails Mac desktop because the previous
  matte-only design tokens left platform-native depth (Aqua / Mica) as
  the only source of polish. Added six gloss tokens — `--gloss-top-edge`,
  `--gloss-bottom-edge`, `--gloss-panel-sheen`, `--gloss-button-bg`,
  `--gloss-halo-{accent,success,danger}`, `--shadow-panel` — and
  applied them to the topbar (frosted-glass top edge), sidebar (inner
  right highlight), `.panel`/`.card` (top-edge sheen overlay + layered
  shadow), `.btn-primary` + `.btn-secondary` (gradient + halo on hover),
  `.segmented` (lifted pill), and the active sidebar row (accent halo).
  All tokens are platform-agnostic — same depth in the web UI, the Mac
  .app, the Windows .exe, the Linux AppImage. Light theme has its own
  hand-tuned variants (white sheen on top edges, soft shadow underneath).
  Closes the user's "Windows looks flat next to Mac" finding.

## [Unreleased]

(no changes since v0.13.10)

## [v0.13.6] — 2026-05-01
### Fixed
- CI: forced `FORCE_JAVASCRIPT_ACTIONS_TO_NODE24=true` at workflow level
  so `softprops/action-gh-release@v2` (still Node 20) keeps working past
  the June 2 2026 deadline. Drops once upstream ships Node-24.
- CI: pinned `cache-dependency-path: sftp-loadtest/go.sum` on all four
  release-pipeline `setup-go@v6` invocations — ends the "Dependencies
  file is not found" cache miss on every release run.

## [v0.13.5] — 2026-05-01
First release with full dual-SKU parity (5 platforms × 2 SKUs = 20
assets). Cluster reliability + UI workbench scaffolding.

### Added
- Cluster: SSH KeepAlive on every spawned worker tunnel. Pings
  `keepalive@openssh.com` every 30s; after 3 consecutive failures the
  tunnel closes proactively so `/api/cluster/status` flips to
  "unreachable" within ~90s. Runtime test
  `TestKeepAlive_ClosesTunnelOnSessionLoss`.
- Cluster: worker version negotiation. `/healthz?detail=1` reports
  master's platformVersion; cluster status JSON exposes `master_version`,
  per-worker `version`, and `version_mismatch`. Pinned by
  `TestCoordinator_VersionNegotiation`.
- CI: new `build-linux-arm64-desktop` job on `ubuntu-22.04-arm` —
  closes dual-SKU parity gap (webui already had linux-arm64;
  desktop-app was amd64-only).
- UI: token-ified the first batch of inline px styles in `index.html`
  (`8px → var(--sp-2)` at 5 sites). First step toward v0.9.0 workbench.

### Changed
- CI: pinned `wails` CLI to `v2.12.0` (matches `go.mod`) + 3-attempt
  install retry on every runner (mac/linux/linux-arm64/windows).
  Closes the `proxy.golang.org` timeout class that lost 8 of 20 assets
  on v0.13.3.
- CI: bumped `actions/checkout@v4 → v5` and `actions/setup-go@v5 → v6`
  for the Node.js 20 deprecation (enforcement 2026-06-02).

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

[Unreleased]: https://github.com/roshandubey-cloud/utilities/compare/v0.13.10...HEAD
[v0.13.10]: https://github.com/roshandubey-cloud/utilities/releases/tag/v0.13.10
[v0.13.9]: https://github.com/roshandubey-cloud/utilities/releases/tag/v0.13.9
[v0.13.8]: https://github.com/roshandubey-cloud/utilities/releases/tag/v0.13.8
[v0.13.7]: https://github.com/roshandubey-cloud/utilities/releases/tag/v0.13.7
[v0.13.6]: https://github.com/roshandubey-cloud/utilities/releases/tag/v0.13.6
[v0.13.5]: https://github.com/roshandubey-cloud/utilities/releases/tag/v0.13.5
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
