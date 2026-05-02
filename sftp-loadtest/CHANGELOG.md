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

## [v0.13.11] — 2026-05-01
### Fixed
- **FTPS error messages no longer say "SSH handshake failed".** The
  probe + run paths funnelled FTP/FTPS errors through
  `friendlyProbeError`, which only knew SFTP-flavoured patterns —
  so a TLS handshake error on port 19990 surfaced as
  `SSH handshake failed — check server config, credentials, or network.`
  Added a sibling `friendlyFTPError` that maps TLS / FTP-control-channel
  patterns to FTP language: TLS handshake mismatch hints at implicit
  vs explicit, x509 unknown-authority hints at the new
  `tls_trust_on_first_use` flag, FTP 530 maps to login-incorrect,
  421 to service-not-available, 550 to folder-not-writable, etc.
  Updated the FTPS probe path in `web.handleProbe` to call the new
  helper.

## [v0.13.12] — 2026-05-01
### Fixed
- **Probe with "Trust on first use" actually trusts now.** v0.13.8
  added FTPS auto-TOFU on the runner side but `probeFTP` still set
  `dialOpts.TLSStore` only when `!tofu`. With TOFU enabled the dial
  fell through to standard chain verify and rejected every self-signed
  lab cert before the post-success `tlsStore.Add` block could run —
  surfacing as the "FTPS certificate is not trusted" / "TLS handshake
  failed" message even though the user had ticked the box. probeFTP
  now wires `TLSStore` whenever the store exists and propagates the
  `tofu` flag through `TLSTrustOnFirstUse`, so the same store-backed
  VerifyConnection path the runner uses drives the probe too.
  Pinned by manual run against the local mockftpserver — first probe
  with TOFU pins the cert and returns `stage: complete`; second probe
  without TOFU verifies cleanly against the stored fingerprint.

## [v0.13.13] — 2026-05-01
### Fixed
- **Run button now auto-TOFUs the FTPS cert.** v0.13.8 added the
  runner-side auto-TOFU plumbing and v0.13.12 fixed the probe path,
  but the run form's serializer in `js/legacy.js` never read any TOFU
  control — so hitting Run always sent `tls_trust_on_first_use:false`
  even though the runner was wired to honor it. The unified
  `data-role="tofu"` toggle (existing) now drives BOTH the SFTP
  host-key TOFU on `/api/probe` AND the FTPS leaf-cert TOFU on
  `/api/start`. Toggle defaults checked, so a first run against a
  new FTPS server with a self-signed cert just works — the leaf is
  pinned to the trust store, subsequent runs verify strictly,
  changed certs still refuse (MITM signal).
- Toggle label updated from "Auto-add server key on first connect (TOFU)"
  to "Trust on first connect (TOFU) — pins SSH host key for SFTP, leaf
  certificate for FTPS" so its scope is clear.

### Verified
- Empty `tls-hosts.json` → POST `/api/start` with `tls_trust_on_first_use:true`
  → cert pinned to disk inside ~3 s of run start (fingerprint, subject,
  not_after, added_at all populated). Subsequent run with the toggle
  off still verifies cleanly against the stored fingerprint.

## [v0.13.14] — 2026-05-01
### Fixed
- **"SSH handshake failed" no longer fires on TLS-fronted ports.**
  When the protocol picker is left on SFTP but the operator points
  the probe at an FTPS port (e.g. mockftpserver on 19990), the SSH
  handshake never starts — the underlying error is a TLS handshake
  failure. `friendlyProbeError` previously caught the generic
  `ssh: handshake failed` substring AFTER the TLS error already
  bubbled up, masking the real problem. New TLS/x509 detection
  branches steer the operator to the FTPS picker instead:
  `Server speaks TLS, not SSH — switch the protocol to FTPS (or FTP)
  in the connection form, or point this probe at port 22.`
  The `ssh: handshake failed` branch also gained an FTPS-suggestion
  hint so the message reads useful even on the genuine SSH path.

## [v0.13.15] — 2026-05-01
### Fixed
- **WebKit no longer serves stale JS/HTML after a desktop-app upgrade.**
  Tracking down "even after I rebuilt, the SSH-handshake error still
  fires" surfaced a deeper problem: the static-asset handler returned
  no `Cache-Control` headers, so WKWebView on macOS happily kept the
  old build's JavaScript in its per-app cache (under
  `~/Library/WebKit/com.roshandubey.sftp-loadtest-desktop/` and
  `~/Library/Caches/com.roshandubey.sftp-loadtest-desktop/`) for hours
  after the binary upgrade. The user-visible symptom was a v0.13.13
  binary running v0.13.10's serializer (TOFU never sent, FTPS probe
  always took the SFTP path).
- Static asset handler now wraps `http.FileServer` with a
  `Cache-Control: no-store` shim. Single fetch-per-page-load cost
  buys parity between the binary version and the rendered UI version.
  JSON API endpoints were already non-cacheable.

## [v0.13.16] — 2026-05-01
### Fixed
- **Pre-Run probe in `legacy.js` no longer says "SSH handshake failed"
  on FTPS targets.** The Run button calls `start()`, which calls
  `ensureHostKeyTrusted(host, port)` to confirm SSH host-key trust
  before posting `/api/start`. That helper built its probe body as
  `{host, port, username, password}` — with NO `protocol` field — so
  the server defaulted to SFTP and tried an SSH handshake against the
  user's FTPS port. The handshake failure surfaced as the misleading
  message, even though `/api/start` would have succeeded if it had
  been allowed to fire (and it actually did fire — the function
  returns `true` on non-host-key probe failures).
- `ensureHostKeyTrusted` now early-returns `true` for any non-SFTP
  protocol — host-key consent only makes sense for SSH; FTPS cert
  trust is handled run-side by the v0.13.13 `tls_trust_on_first_use`
  plumbing.
- `probeConnection` (legacy "Test connection" button, still wired in
  the legacy DOM as a fallback) now reads `protocol`, `tls_mode`,
  `tls_server_name`, `tls_insecure_skip_verify` from the form so it
  routes to the correct probe path. Also picks up the unified TOFU
  toggle (`data-role="tofu"`) instead of the legacy `#probe_tofu`
  ID, which was retired in the workbench redesign.

## [v0.13.17] — 2026-05-01
### Fixed
- **THIRD probe path was still SFTP-defaulting on FTPS Run.** v0.13.16
  fixed `legacy.js`'s probe paths but missed `start-preflight.js` —
  a capture-phase click wrapper around `#startBtn` that runs an
  /api/probe BEFORE the real /api/start to handle SSH host-key
  consent / renewal. Its `probeBody` had no `protocol` field, so
  hitting Run on FTPS triggered an SFTP probe → SSH handshake against
  the TLS port → "SSH handshake failed" toast in the masthead → click
  was preventDefault'd → /api/start never fired. The user-visible
  symptom: the same misleading message even after every other fix.
- `mountStartPreflight` now early-returns for any non-SFTP protocol —
  host-key consent is an SSH concept; FTPS cert trust is handled
  run-side. `firstCredential` is no longer called for FTPS clicks.
- For the SFTP path, the preflight body now explicitly sets
  `protocol: 'sftp'` so future protocol-default changes can't trip it.

## [v0.13.18] — 2026-05-01
### Fixed
- **Cluster spawn upload to macOS workers no longer fails with
  "create remote $HOME/sftp-loadtest: file does not exist".** The
  install-path logic set `bin = "$HOME/sftp-loadtest"` as a literal
  string when the remote arch was `darwin-*`. The shell-driven
  download path expanded `$HOME` correctly, but the SFTP-driven
  upload path treated it as a literal directory name — `pkg/sftp`
  never expands shell variables — so `Open` failed.
- Master now resolves the remote home directory at spawn time
  (`printf %s "$HOME"` with a `whoami → /Users/<who>` fallback for
  exotic shells) and uses the absolute path for both upload (SFTP)
  and download (shell). Operators can still override via
  `RemoteBinaryPath`. The resolved path is recorded in the spawn
  log: "Resolved install path: /Users/<user>/sftp-loadtest".
- Test harness updated: `fakeSSHServer` now answers `echo $HOME` /
  `whoami` so the darwin spawn path can be exercised end-to-end.

## [v0.13.19] — 2026-05-01
### Changed
- **Cmd+K command palette is now guide-class.** The previous palette
  rendered single-line entries (label + tiny hint), nothing on an
  empty query, and silently vanished on any miss. Operators rightly
  read this as "search shows nothing." Replaced with a richer shape:
  - Every entry has an icon, primary label, one-line description,
    optional keyboard shortcut, and optional meta (host, time-since,
    "active" badge).
  - Empty-query view shows curated **First steps**, all run controls,
    the top 2 most-recent presets / connections / runs, plus inline
    Help cards — never blank.
  - Per-section empty states show a CTA instead of disappearing
    ("No presets yet — Cmd+S to save the current config").
  - Token-based subsequence matching: every space-delimited query
    token must hit somewhere in `label + description + section +
    keywords + meta`. Matched substrings render highlighted.
  - **New sections:** Connections (saved host:port:user via
    `saved-connections.js`), Recent runs (last 6 from `/api/runs`),
    Help (TOFU explainer, cluster mode, fpm meaning, protocol
    picker, where reports live).
  - Onboarding banner above the input on first open
    ("Type to filter · ↑↓ navigate · ↵ run · Esc close — searches
    commands, presets, saved connections, recent runs, and help.").
    Dismissed via the × button; localStorage flag
    `sftpl-cmdk-onboard-v2` remembers the choice.
- New CSS for the rich row template (`.cmdk-result-rich`,
  `.cmdk-result-icon`, `.cmdk-result-body`, `.cmdk-result-desc`,
  `.cmdk-result-trail`, `.cmdk-banner*`). Backwards compatible — old
  thin rows still render identically if some code path emits them.

## [v0.13.20] — 2026-05-01
### Fixed
- **Desktop Trust panel can now manage SSH host keys from the UI.**
  Previously the desktop app only wired file-mode (`sftpx.UseKnownHosts`
  + `srv.SetKnownHostsPath(~/.ssh/known_hosts)`) so the Trust panel
  fell back to its read-only "managed externally" message —
  operators were told to edit `~/.ssh/known_hosts` directly and
  restart. The CLI tool defaulted to store mode (UI-managed JSON);
  the desktop app didn't.
- Desktop now opens `<dataDir>/hosts.json` via `hostkeys.Open` (mirrors
  the existing `tls-hosts.json` wiring), calls
  `srv.SetHostKeyStore(store)` to make it the live trust authority, and
  binds `sftpx.SetHostKeyCallback(store.StrictCallback())` for the
  runner. The Trust panel now shows entries with Add / Remove buttons
  and a "store mode" badge instead of the file-mode banner.
- `~/.ssh/known_hosts` is still ensured (so terminal `ssh` keeps
  working alongside the app) but the file-mode callback is NOT bound
  when store mode is active — silently double-trusting via two
  authorities was the wrong default.
- New `Server.HostKeyStoreActive() bool` helper so callers wiring
  trust at startup can decide which mode is live without poking at
  internals.
- Graceful degradation: if `hostkeys.Open` fails (read-only volume,
  etc.) the desktop falls through to the legacy file-mode wiring so
  trust is still honoured.

## [v0.13.21] — 2026-05-01
### Changed
- **Help in the command palette is now enterprise-grade.** The previous
  Help cards were one-line tooltips ("First connect captures the server
  fingerprint to a local store. Future runs verify against it…") —
  surface-level, no actionable depth.
- New right-pane detail layout (Raycast-style): list on the left, full
  guide on the right. Each entry can carry a structured `detail`
  block — `{ title, lede, body[], links[] }` — rendered with proper
  headings, key/value tables, bullet lists, code blocks, and external
  links. Selecting (or hovering) any row updates the right pane in
  ~30 ms.
- Eleven Help guides written from scratch, each with what / why /
  when-to-use / when-NOT / pitfalls / commands sections:
  - Trust on First Use (TOFU) — full reference
  - Cluster mode — full reference
  - files-per-minute — sizing guide
  - Protocol picker — SFTP / FTP / FTPS
  - Run reports — schema + retention + access
  - Reading latency percentiles (p50 / p95 / p99 / cor)
  - Scheduling runs — when + how it survives restarts
  - HTTP API — every endpoint, what it does
  - Performance tuning — fd limits, parallel streams, memory
  - Security posture — what protects this tool
  - Spawning workers via SSH — what each step does
- Non-help entries also get a mini detail card (icon + label + section
  + description + shortcut) so the right pane is never blank.
- Palette width grown 600 → 980 px to fit the two-pane layout.
  Collapses to a stacked list-above-detail under 760 px viewport.

## [v0.13.22] — 2026-05-01
### Fixed
- **Help row clicks no longer surprise-navigate or push misleading
  toasts.** Hangover from the v0.13.19 thin-card era: every Help entry
  carried either a `pushToast('See README §...')` (purely useless once
  the detail pane shipped) or a `clickSidebarRow('configure'|'trust'|...)`
  that yanked the user into a sidebar panel they didn't ask for. With
  the right-pane guide now carrying the value, the hidden side-effect
  on row click read as a bug — clicking *Performance tuning* navigated
  to Configure and toasted "See README §HTTP API".
- All eleven Help entry actions are now no-ops. Row click closes the
  palette; the detail stays in the user's head.
- Each Help entry that has a meaningful destination panel now
  surfaces an explicit `cta` button in the detail header
  ("Open Trust panel", "Open Cluster panel", "Open Runs panel",
  "Open Schedule panel"). Clicking the button is the only way to
  navigate — no more accidental sidebar swaps from a row click.
- Detail card layout updated to make space for the CTA: header
  becomes a flex row with title group on the left, button on the
  right.

## [v0.13.23] — 2026-05-01
### Fixed
- **Three latent fragilities of the same class as the v0.13.22 toast bug.**
  An audit for "row/button click handlers whose target relies on text
  matching" surfaced three more callsites that work today but would
  bite the next time the UI label changes:
  1. `clickSidebarRow` matched sidebar entries by `textContent.startsWith(name)`.
     Today the labels (Workbench / Configure / Schedule / Runs / Cluster
     / Trust) are unique-prefix; a future "Trust this certificate…" row
     would silently steal `clickSidebarRow('trust')`. Fixed: matcher now
     queries `[data-view="<name>"]` directly. Sidebar rows already carry
     this attribute (set by `shell.js`).
  2. The "Test connection" command-palette action used a regex over
     every button's `textContent` — it grabbed the first match in DOM
     order. Replaced with a stable `[data-component="connection"]
     [data-role="submit"]` selector (the Quick-Checks submit button)
     with a fallback to any `[data-role="submit"]`.
  3. `legacy.js` `strSet(id, v)` (the import-config form populator)
     set `el.value` without firing `input` / `change` events. The
     configure-redesign mirrors and saved-config dirty flag listened
     for those events, so a freshly-imported config could leave a
     mirror field stale until the operator clicked into it. Now
     dispatches both events with `bubbles:true`.

### Audited (no fix required)
- All recent probe / start callsites correctly include the `protocol`
  field (the v0.13.16 / 17 fixes held).
- All `pushToast(...)` calls after fetches gate on `res.ok` —
  no false-success or wrong-content toast paths found.

## [Unreleased]

(no changes since v0.13.23)

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

[Unreleased]: https://github.com/roshandubey-cloud/utilities/compare/v0.13.23...HEAD
[v0.13.23]: https://github.com/roshandubey-cloud/utilities/releases/tag/v0.13.23
[v0.13.22]: https://github.com/roshandubey-cloud/utilities/releases/tag/v0.13.22
[v0.13.21]: https://github.com/roshandubey-cloud/utilities/releases/tag/v0.13.21
[v0.13.20]: https://github.com/roshandubey-cloud/utilities/releases/tag/v0.13.20
[v0.13.19]: https://github.com/roshandubey-cloud/utilities/releases/tag/v0.13.19
[v0.13.18]: https://github.com/roshandubey-cloud/utilities/releases/tag/v0.13.18
[v0.13.17]: https://github.com/roshandubey-cloud/utilities/releases/tag/v0.13.17
[v0.13.16]: https://github.com/roshandubey-cloud/utilities/releases/tag/v0.13.16
[v0.13.15]: https://github.com/roshandubey-cloud/utilities/releases/tag/v0.13.15
[v0.13.14]: https://github.com/roshandubey-cloud/utilities/releases/tag/v0.13.14
[v0.13.13]: https://github.com/roshandubey-cloud/utilities/releases/tag/v0.13.13
[v0.13.12]: https://github.com/roshandubey-cloud/utilities/releases/tag/v0.13.12
[v0.13.11]: https://github.com/roshandubey-cloud/utilities/releases/tag/v0.13.11
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
