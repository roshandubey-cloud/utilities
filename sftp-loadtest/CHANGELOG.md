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

## [v0.13.24] — 2026-05-01
### Added
- **Master archives every cluster run's per-worker reports automatically.**
  Until now, when an operator hit Stop on a cluster run, each worker
  kept its CSV + meta JSON locally and the master UI showed nothing —
  to view per-worker numbers you had to SSH into each box and copy
  the reports yourself. The master now pulls each worker's report at
  Stop time and persists everything under `<reportsDir>/cluster-runs/<id>/`.
- New types in `internal/cluster/archive.go`:
  - `ClusterRunMeta` — aggregated cluster-run record (id, master version,
    started/stopped, summed counters, per-worker pointers).
  - `ClusterWorkerReport` — one worker's row inside a cluster run, with
    relative paths to its `worker-NN.csv` and `worker-NN.json` archives.
- New endpoints:
  - `GET /api/cluster/runs` — list every archived cluster run, newest first.
  - `GET /api/cluster/runs?id=<id>` — full meta for one cluster run.
  - `GET /api/cluster/runs/file?id=<id>&name=<file>` — download one
    of the per-worker artifacts. Strict path-component validation
    (`cluster-<digits>` ids, `worker-NN.{csv,json}` or `meta.json`
    filenames). `..` and slashes refused.
- `Coordinator.ArchiveOnStop(ctx)` is the single entry point. The
  cluster-stop handler calls it after `Coordinator.Stop` succeeds.
  Best-effort: a single unreachable worker doesn't block the others'
  reports from being persisted; the response surfaces an
  `archive_warning` field when archival hit a snag.
- **Runs-history panel now shows cluster runs alongside solo runs.**
  Cluster cards get a `cluster · N workers` badge, a left-edge accent
  bar, and an expand button that reveals a per-worker drawer with
  CSV + meta-JSON download links per worker. Aggregated and worker
  totals are reconciled from each worker's `/api/runs` row before
  the meta is written, so the headline matches the records.
- `runs-history.js` now fetches `/api/runs` and `/api/cluster/runs`
  in parallel and merges by `started_at` so the History timeline is
  unified.

### Verified
- Runtime test `TestCoordinator_ArchiveOnStop` exercises the full
  archive flow against fake worker servers: empty trust → Start →
  Status poll → Stop → ArchiveOnStop produces meta.json + 2×
  worker-NN.{csv,json}; ListClusterRuns reads them back; summed
  counters match (66 files / 1 failed across the two workers).

## [v0.13.25] — 2026-05-01
### Changed
- **Sidebar sections collapse to 3 rows + "Show N more" toggle.**
  Recent runs / Trusted hosts / Saved presets each rendered up to
  10 rows in the sidebar. On smaller laptops a long-running operator
  session ended up pushing primary nav (Workbench / Configure /
  Schedule / Runs / Cluster / Trust) off the bottom of the viewport.
- Each section now caps the visible-by-default count at 3 with a
  `Show N more` (uppercase, sidebar-mute color) toggle below. Click
  expands; click again collapses back to 3. Expanded state survives
  the periodic refresh so a "Show all" choice doesn't snap back.
- Hard cap of 30 rows when fully expanded — the Runs / Trust
  panels still surface the full list with proper scrolling. Sidebar
  is for quick access, not a data table.
- Overflow rows carry `data-overflow="true"` and are gated by a
  parent `data-expanded="false|true"` CSS rule, so JS just toggles
  one attribute instead of rebuilding the DOM.

## [v0.13.26] — 2026-05-01
### Fixed
- v0.13.25's in-place "Show N more" toggle had two real problems
  surfaced by the user:
  1. The toggle text got clipped by the sidebar's left chrome
     ("HOW EWER" instead of "SHOW FEWER") because its CSS padding
     was a sibling, not a child of `.shell-sidebar-row`.
  2. Expanding the list inside the sidebar still pushed primary nav
     off the bottom on smaller laptops — the original problem
     v0.13.25 was supposed to solve.

### Changed
- Sidebar list sections (Recent runs / Trusted hosts / Saved presets)
  now show ONLY the latest 3 entries, full stop. When there are more,
  a footer row reads "View all N runs →" / "View all N trusted hosts →"
  / "View all N presets in ⌘K →" and routes the operator to the
  dedicated panel where the full list lives:
  - Recent runs   → clicks the sidebar's `[data-view="runs"]` row
                    (full History view with latency, analysis, CSV).
  - Trusted hosts → clicks the sidebar's `[data-view="trust"]` row
                    (Trust panel with fingerprints + bulk Add/Remove).
  - Saved presets → opens the command palette filtered to presets
                    (canonical "load preset" surface with descriptions).
- The new "View all" rows reuse the existing `.shell-sidebar-row`
  template so padding / alignment can never drift; they're styled
  italic + sidebar-mute with an arrow on the right.

## [v0.13.27] — 2026-05-01
### Fixed
- **TLS-mode label "Implicit (port 990)" implied the port was fixed.**
  990 is the canonical implicit-TLS port but any port works (the
  local mockftpserver runs on 19990, real customer servers commonly
  on 21021, 4990, etc.). Renamed the label to "Implicit (TLS from
  byte 0)" so it describes the protocol semantic, not a specific
  port. Hover title still notes "Any port works (canonical 990)" as
  a hint. The Explicit button got a parallel title hint.
- Port auto-snap behaviour (already correct, just clarifying):
  switching from SFTP → FTPS implicit pre-fills the port field with
  990 as a courtesy. The instant the operator types a custom port,
  `userEditedPort` flips and we stop touching the field. Custom
  ports stick across protocol-mode flips.

## [v0.13.28] — 2026-05-01
### Fixed
- **Stop button stays clickable after a run ends.** The status poller
  reconciles `tbRun.disabled` / `tbStop.disabled` from `/api/status`'s
  `active` flag every 2 s, so after Stop fired the Stop button stayed
  enabled for up to two seconds — long enough for a frustrated double-
  click during a slow stop. Same problem on the Run side: clicking Run
  left it enabled while POST /api/start was in flight, so a quick
  double-click submitted twice (the second was a 409 but the operator
  saw a confusing toast).
- The topbar handlers now optimistically flip the disabled state on
  click: clicking Run immediately disables Run + enables Stop;
  clicking Stop does the inverse. The 2-second status poll keeps
  doing its reconciliation job, so if the optimistic state turns out
  to be wrong (e.g., /api/start failed validation) it's corrected
  within one tick.

## [v0.13.29] — 2026-05-01
### Fixed
- **CSV `download_available_at` column was a duplicate of
  `track_id_detected_at`.** `buildRow` wrote `r.TrackIDAt` into both
  columns — silent data corruption: two columns, identical values.
  Added `FileRecord.DownloadAvailableAt`, populated from
  `DownloadResult.AvailableAt` in all three merge sites
  (`AttachDownload*` paths). The column now distinguishes "download
  worker first observed the file in the outbox" from "track-id
  watcher detected the upload landed" — the two can diverge when
  the download worker has its own queue depth or pacing.
- **Analyzer's "downloads stalled" suggestion fired when downloads
  were disabled.** Without the gate, a non-zero `DownloadStalled`
  from a stale counter would surface a nonsensical
  recommendation. Now requires `m.DownloadEnabled && DownloadStalled > 0`.
  Pinned by `TestSuggest_DownloadsStalled_DisabledIsSkipped`.
- **Live `Downloads` / `Leftover (prior runs)` tiles always
  rendered with `0` even when the run had no downloads
  configured.** Operators read this as "downloads ran but failed."
  Tiles now carry a `.dl-only[data-dl-enabled="false"]` CSS hide,
  driven by a new `download_enabled` flag on `/api/status`.

### Added
- `RunMeta.NormalEnabled` and `RunMeta.LargeEnabled` capture the
  workload toggles at seal time. `DownloadEnabled` was already
  there; the symmetric pair lets historical reports + the UI
  branch correctly on what the run actually exercised.
- `/api/status` now surfaces `normal_enabled`, `large_enabled`,
  `download_enabled` so live UI tiles can hide irrelevant rows.
- Runs-history cards (Previous-runs panel) gain workload-shape
  badges in the subtitle: `normal`, `large`, `download · trackid`
  / `download · filename`. A normal-only run and a normal + large
  + download run now look different at a glance.

## [v0.13.30] — 2026-05-01
### Fixed
- **Sidebar Search input read as broken — only fired on Enter.**
  Clicking the box and typing produced no visible feedback because
  the handler waited for an Enter keypress before dispatching
  `sftpl:open-cmdk`. There was no hint Enter was required and
  Spotlight-trained users gave up after one keystroke.
- The Search input now opens the command palette **on the first
  keystroke**, forwarding the typed character as the initial query.
  The palette steals focus immediately so subsequent keystrokes go
  to its (live-filtering) input. Same Esc / arrow / Enter mechanics
  the palette already supports.
- Enter still works as a keyboard-only path: tab into the box, press
  Enter, the palette opens empty-handed (curated First-steps view).
- Placeholder updated from `Search…` to
  `Search commands, presets, runs…` so the affordance is obvious
  before the operator clicks.
- `opening` debounce flag prevents a fast typist from
  double-dispatching the event during the input → focus transfer
  window.

## [v0.14.11] — 2026-05-02
### UI — minimalist label pattern + drop "Implicit (TLS from byte 0)"

**Eyebrow pattern.** The `1 · Target` / `2 · Workload` / `3 · Resource
limits` markers picked up a subtle leading accent rule that fades to
transparent — small visual flourish that ties the three sections into
one typographic system without shouting. Weight dialled back from
semibold → medium, letter-spacing widened from 0.08em → 0.14em so
the small caps read as a refined pattern rather than a heading.

**Label vocabulary unified.** `.label-inline` (used by the Target
card's Protocol / Host / Port / Username / Password mini-labels)
now shares the same uppercase-tertiary-medium family — same
letter-spacing scale, no leading rule (one rung quieter than the
section eyebrow). One vocabulary for "here's a category" across
the form.

**Dropped enterprise-grade-explanations.** The TLS-mode segmented
buttons used to read *"Explicit (AUTH TLS)"* / *"Implicit (TLS
from byte 0)"*. Now just *"Explicit"* / *"Implicit"*. The full
explanation lives in the button title (hover tooltip) for the
operator who actually needs it. Enterprise-grade products don't
explain themselves in the chrome.

### Internal
- `main.go` `platformVersion` → `0.14.11`; `wails.json` synced.

## [v0.14.10] — 2026-05-02
### UI — drop chatty section titles in Configure

Removed three question-style `<h2 class="cfg-section-title">` lines:
- *"Where am I targeting?"*
- *"What's the workload shape?"*
- *"How long, how aggressively?"*

Each section now reads as `[1 · Target]` eyebrow + a short
descriptive subtitle. Same information density, less voice. Also
trimmed the Target subtitle to drop "folder" (folder lives on the
Upload card since v0.14.6) and renamed "Quick checks" → "Test
connection" to match the button.

### Internal
- `main.go` `platformVersion` → `0.14.10`; `wails.json` synced.

## [v0.14.9] — 2026-05-02
### No click is ever silently ignored

Operator-driven UX pass. Every primary CTA now either does its job or
tells the operator exactly what's missing — no more "I clicked Test
connection / Start run / Probe and nothing happened."

A subagent audit found six silent-ignore patterns across the four
busiest screens. All closed.

### New helper — `internal/web/static/js/guidance.js`
Shared module exporting:
- **`guideRequiredFields([{el, label}, …], { action })`** — when any
  entry is empty, focuses the first one, pulses an accent ring
  around all of them for ~1.5s, and toasts
  *"Fill in Host and Port to test the connection."* (or whatever the
  action verb-phrase is). Returns `false` so the calling handler can
  bail.
- **`guideCondition(ok, message, { focusEl })`** — predicate variant
  for "no workload enabled" / "users CSV empty" rules.
- **`pulseField(el)`** — primitive used by both helpers; pure-CSS
  `@keyframes sftpl-field-pulse` animation.
- Bridged onto `window.__guide` so the legacy non-module
  `legacy.js` can use it without an import.

### Wiring (six gaps closed)

| Where | Before | After |
|---|---|---|
| **Test connection** ([connection.js:386](internal/web/static/js/connection.js)) | Silent return on empty Host/Port; field focused but no toast | `guideRequiredFields([Host, Port], action: 'test the connection')` — focus + pulse + toast |
| **Start run** ([legacy.js:start()](internal/web/static/js/legacy.js)) | Silently POSTed to `/api/start`; backend 400 surfaced far from the field | Pre-flight `startRunGuidance()`: gates Host/Port, no-workload-enabled, empty users CSV per enabled workload |
| **Probe source** (sources-sinks.js) | Local-files / local-dir picked but textarea/dir empty → fired with null cfg, opaque backend response | `guideRequiredFields` on the right field per kind |
| **Probe sink** (sources-sinks.js) | Local-disk picked but root empty → opaque "root is required" response | `guideRequiredFields` on Root directory |
| **SSH wizard URL / S1 / S2 / Probe TCP** (cluster-ui.js) | Silent returns or toast-only without focus jump | All four steps: focus + pulse + toast naming the missing field |

### `Test connection` got a gentle accent

Switched from `btn-secondary` (neutral grey) to a new `btn-accent-soft`
variant — soft accent tint background, accent foreground, gloss halo
on hover. Same visual vocabulary the v0.14.8 segmented selection
update introduced. Reads as *"clearly an action, clearly tied to the
workflow"* without competing with Start run's filled-accent
`btn-primary`. CSS:

```css
.btn-accent-soft {
  background-color: var(--sidebar-active-bg);
  background-image: var(--gloss-button-bg);
  color: var(--sidebar-active-fg);
  box-shadow: var(--gloss-top-edge);
}
```

### Internal
- New `internal/web/static/js/guidance.js`.
- `app.js` adds a side-effect import so `window.__guide` is wired
  before `legacy.js` (loaded after as a non-module script) reaches
  for it.
- `connection.js`, `sources-sinks.js`, `cluster-ui.js` import the
  helper directly.
- `legacy.js` adds `startRunGuidance()` near `start()`.
- `components.css` adds `.btn-accent-soft` + `@keyframes
  sftpl-field-pulse` + `.field-pulse`.
- `main.go` `platformVersion` → `0.14.9`; `wails.json` synced.

## [v0.14.8] — 2026-05-02
### Two visual-polish fixes from operator feedback

- **Bottom status-bar version is dynamic.** The `v0.9.4` you see in
  the bottom-right cell was a hardcoded literal in `shell.js` from
  the v0.9.4 redesign — it never got wired to `/api/version` when
  that endpoint shipped in v0.14.5. Now `masthead.js` updates BOTH
  the masthead pill AND the status-bar cell from one fetch. Hidden
  until the response arrives so we never flash a placeholder. Same
  `Cache-Control: no-store` chain so an upgraded binary's real
  version shows up on the very next page load.

- **Segmented `[aria-pressed="true"]` now matches the sidebar's
  selected-row treatment.** Previously the chosen Protocol /
  TLS mode / Source kind / Layout / Pick mode / Sink kind /
  Theme button used a neutral grey-on-grey gradient while the
  sidebar's "Configure / Records / Runs" picked row used the
  accent tint + accent foreground + soft halo. Two visual
  vocabularies for the same semantic. Unified via:

  ```css
  .segmented button[aria-pressed="true"] {
    background-color: var(--sidebar-active-bg);   /* accent tint */
    background-image: var(--gloss-button-bg);     /* keep lifted-pill gloss */
    color: var(--sidebar-active-fg);              /* accent foreground */
    font-weight: var(--fw-semibold);
    box-shadow: var(--gloss-top-edge), var(--shadow-1), var(--gloss-halo-accent);
  }
  ```

  Operators now read both *"this is the current view"* (sidebar)
  and *"this is the active option"* (segmented) with one accent
  vocabulary. Applies uniformly across every segmented control in
  the app — protocol picker, TLS mode, source/sink kind, layout
  picker, mode picker, theme switcher, etc.

### Internal
- `main.go` `platformVersion` → `0.14.8`; `wails.json` synced.

## [v0.14.7] — 2026-05-02
### UX — drop context-mismatched Download CSV from hero actions

Operator feedback: a Download CSV link sat permanently in the
configure-pane action row next to Start / Stop, even before any run
had happened. It pointed at `/api/report.csv` (the "last completed
run") with no indication of which run that was, and duplicated the
per-row CSV button each runs-history entry already carries.

- **Removed `<a id="csvBtn">` from the hero actions row.** CSV
  consumption now lives in two contextual homes only: each row of
  the runs-history panel (per-run download), and the scheduled-run
  banner that appears when a scheduled run is in progress. No more
  always-visible "the CSV" with ambiguous referent.
- **`legacy.js` guarded** — the live-status loop no longer assumes
  `#csvBtn` is in the DOM, so a stale cached HTML against the new
  binary doesn't throw.

### What I checked but kept

- Sched banner CSV (`#sched_banner_csv`) — only renders when a
  scheduled run is in progress; **contextual, kept**.
- Per-row CSV in runs history — the right home; **kept**.
- Stop button greyed-out next to Start — pre-run-affordance value
  outweighs noise; the sticky run-header has its own Stop that
  takes over once a run is active. **Kept.**

### Internal
- `main.go` `platformVersion` → `0.14.7`; `wails.json` synced.

## [v0.14.6] — 2026-05-02
### UI — Target section redesign

Operator feedback: the Target card was overly complex — three labeled
fields per row stacked over a full-width TOFU toggle stacked over an
empty "no recent connections" hint, burning ~440px even with empty
inputs. The Folder field shouldn't have been there at all (it's a
workload concern; the upload card already owns it).

Cuts:
- **Folder removed from Target.** Folder lives on the Upload card via
  `upload-restructure.js`. The probe still reads from the legacy
  hidden `#folder` input that the upload card writes through to —
  same wire format, just no duplicate field.
- **`(optional)` labels dropped.** One shared caption beneath the
  credentials row reads *"Used by Test connection only — the real
  run uses each load's user CSV."* The mental model is communicated
  once instead of stamped on every label.
- **TOFU full-width toggle → compact inline checkbox** next to Reset
  / Save / Test. Carries the same protocol-aware label ("Auto-add
  server key" for SFTP, "Trust this server cert" for FTPS).
- **Empty-state for Recent connections hidden** until the operator
  has at least one. The placeholder copy was adding a row of noise
  on a fresh install.
- **Panel header tightened** to one line: *"Target — host, port,
  credentials. Validated by Test connection."* (was a two-line
  title/subtitle pair).

Adds:
- **Password show/hide toggle.** Eye-icon button overlays the right
  edge of the password input; click flips `type=password ↔ text`
  in place. Operators can verify what they typed without retyping.
- **Compact label-inline style** — fs-11 uppercase tertiary labels
  above each input cut row height from ~110px to ~62px.

Net result: same information surface, ~280px shorter, fewer labels
to read, no duplicate concerns.

### Internal
- `internal/web/static/index.html` — Target section rewritten with
  new `.target-row`, `.target-protocol/host/port/user/pass` flex
  cells, `.input-pass-wrap`, `.target-actions`, `.check-inline`.
- `internal/web/static/js/connection.js` — `folderEl` reads the
  legacy `#folder` directly; reset no longer wipes folder; password
  toggle handler.
- `internal/web/static/styles/components.css` — new layout rules
  for the redesigned Target card.
- `main.go` `platformVersion` → `0.14.6`; `wails.json` synced.

## [v0.14.5] — 2026-05-02
### Live platform version visible in the UI

Operator-reported gap: there was no place in the running app or web UI
to see the platform version. The macOS About dialog showed
`0.4.0-dev` because `cmd/desktop/wails.json` had drifted from
`main.go`'s `platformVersion` const for ~14 releases.

- New unauthenticated **`GET /api/version`** returns
  `{version, started_at}`. Sets `Cache-Control: no-store` so the
  WebKit per-app cache on macOS doesn't pin the value across an
  app upgrade — every fresh page load reads the running binary's
  real version.
- **Masthead pill.** A small monospace `vX.Y.Z` next to the
  "SFTP Load Test" wordmark, fetched on mount. Hidden until the
  response arrives so we never flash a placeholder.
- **`wails.json` productVersion → `0.14.5`** (was stuck at
  `0.4.0-dev`). Drives the macOS About dialog.
- **CI guard against drift:** new
  `TestWailsProductVersionMatchesPlatform` in `cmd/desktop/main_test.go`
  reads `wails.json` and the `platformVersion` literal in `main.go`,
  fails the test when they differ. Bumping one without the other
  now breaks the build.

## [v0.14.4] — 2026-05-02
### Sources scale to N users / N files

The v0.14.0 `local-dir` source was a single shared pool — fine for one
user with one set of fixtures, awkward for real load tests with 50
accounts each owning their own files. v0.14.4 adds a `Layout` knob to
`local-dir` so operators can use familiar conventions instead of
hand-rolling per-user JSON overrides.

- **`layout: "flat"`** — default, identical to v0.14.0–3 (one pool,
  every user picks from `<root>/*`).
- **`layout: "by-user"`** — `<root>/<username>/*` per account. Drop
  fixture files into `<root>/alice/`, `<root>/bob/`, `<root>/charlie/`
  and the runner routes each upload accordingly. Missing subdirs fail
  the upload with a friendly error instead of falling back to a
  shared pool.
- **`layout: "by-pattern"`** — `<root>/*` filtered by
  `filepath.Match` against each user's CSV pattern column. Different
  accounts carve up the same flat directory by their patterns
  (`invoice-*`, `report-*`, `ack-*`).
- **`layout: "by-user-pattern"`** — both axes:
  `<root>/<username>/*` further filtered by pattern. The strictest
  layout for accounts that have their own subdir AND distinct file
  types within it.

A new `internal/source.LocalTree` type backs the three non-flat
layouts. Per-(user, pattern) pools are resolved lazily and cached for
the rest of the run, so a 50-account test against
`<root>/<account>/*` is cheap on startup.

### `/api/probe-source` upgraded to a per-user matrix

The probe endpoint now accepts a `{ source, users[{username, pattern}] }`
envelope (the legacy bare-config shape still works) and returns one
row per (user, pattern) pair when the layout is non-flat:

```json
{ "ok": true, "kind": "local-dir", "layout": "by-user",
  "users": [
    { "username": "alice", "pattern": "inv-*", "files": [...], "total_bytes": 72 },
    { "username": "bob",   "pattern": "rpt-*", "files": [...], "total_bytes": 54 },
    { "username": "nobody","pattern": "*",     "ok": false, "error": "no such directory" }
  ]
}
```

Operators can verify all 50 accounts resolve to non-empty pools before
they start a long run — and a missing subdir or a typoed pattern
shows up immediately as an in-line per-row error.

### UI surfaces the convention

- New **layout sub-picker** inside the Local-directory disclosure
  (Normal load, Large load): four segmented buttons with live
  teaching copy that explains what `<root>/<user>/*` means in each
  mode.
- **Probe button now forwards CSV users.** Reads the same
  `normal_users` / `large_users` textareas the run will use, sends
  them with the probe, and renders a compact matrix of
  Account / Pattern / Files / Total under the Probe output. Failed
  rows highlight red with the per-account error.
- **Round-trip through Export/Import** — `readSource` emits
  `layout` only when non-default; `applySource` restores both the
  picker state and the right teaching-copy span on import.

### Validated end-to-end

- `by-user`: 3 accounts (alice/bob/charlie) with distinct subdirs,
  each routed through paired download users. Per-account hash sets
  matched their source subdir exactly — zero cross-account leakage.
  35 downloads in total, all byte-identical to source fixtures.
- `by-pattern`: 3 accounts sharing one flat root with patterns
  `inv-*`, `rpt-*`, `ack-*`. Per-pattern hash sets matched the
  filtered globs exactly — alice (inv-*) got only inv-A/inv-B,
  bob (rpt-*) got rpt-1/rpt-2/rpt-3, charlie (ack-*) got ack-X.

### Internal
- `internal/config.SourceConfig.Layout` field with enum validation
  in `Validate()`.
- `internal/source.LocalTree` (lazy per-(user, pattern) pool cache)
  + `Layout`, `LayoutFlat`, `LayoutByUser`, `LayoutByPattern`,
  `LayoutByUserAndPattern` constants + `FilesFor(user, pattern)` for
  the probe API.
- `internal/runner.buildSourceLeaf` routes layout!="flat" → LocalTree.
- `main.go` `platformVersion` → `0.14.4`.

## [v0.14.3] — 2026-05-01
### UX — sources & sinks usability pass

A real-user audit of v0.14 surfaced seven gaps where the new feature
worked but felt thin. v0.14.3 closes them in one coherent shipment.

- **Native folder / file pickers (Wails desktop).** Two new
  `App.PickDirectory(title)` and `App.PickFiles(title)` bindings drive
  OS-native dialogs. JS detects the binding at mount time and reveals
  Browse buttons next to every path field — local-files textarea,
  local-dir input, sink root. Web build leaves the buttons hidden so
  hand-typed paths remain the only affordance there (browsers can't
  open arbitrary local FS).
- **Live template preview.** A monospace path under the sink template
  re-renders on every keystroke against a sample upload
  (`{user}=dl1, {filename}=doc-12345.pdf, {trackid}=a1b2c3d4`,
  current `{date}`/`{datetime}`). Operators see exactly what the
  runner will write before they commit.
- **Variable chips.** Eight clickable chips
  (`{user} {filename} {basename} {ext} {trackid} {run_id} {date}
  {datetime}`) under the template input insert at the caret position,
  so the operator never has to remember the variable list.
- **Inline misconfiguration warning.** Picking `local-files` with an
  empty textarea (or `local-dir` with an empty path, or `local-disk`
  with an empty root) now surfaces a yellow live-updating banner —
  *"the run will silently use synthetic random bytes"* /
  *"downloads will be discarded silently"*. v0.14.0 silently fell
  back to defaults; that's no longer invisible.
- **Inline JSON parse error.** The advanced per-user / per-pattern
  textarea parses on every input and shows the JSON error inline in a
  red strip instead of `console.warn`-only.
- **`/api/probe-source` + `/api/probe-sink`.** Two new local-only
  endpoints validate the operator's source/sink config before the
  real run starts. Source returns the resolved file list with sizes
  (or a friendly error per file); sink confirms the root is writable
  via a probe-create-delete dance. Each disclosure has a Probe
  button that shows the result inline. No network I/O — instant
  feedback instead of "did my path actually resolve?" anxiety.
- **Discoverability.** Disclosure summaries now read
  *"…click to upload real files from disk"* / *"…click to save every
  download under a folder you pick"* instead of dry default-state
  copy, with a small `v0.14` pill so first-time viewers know the
  feature is recent.

### Internal
- `internal/source/source.go` — added `(*LocalFiles).Files()` accessor
  so `/api/probe-source` can list pool members without re-walking.
- `internal/web/web.go` — two new handlers + `/api/probe-source` and
  `/api/probe-sink` routes.
- `cmd/desktop/app.go` — two new Wails-bound methods.
- `internal/web/static/js/sources-sinks.js` — Browse / Probe / chip /
  preview / warning / JSON-error wiring; client-side
  `renderTemplateClient` mirrors `internal/sink/sink.go`'s
  `renderTemplate`.
- `internal/web/static/styles/components.css` — new `.input-with-action
  .src-warn .src-advanced-error .sink-preview .sink-var-chips
  .badge-mini` rules.
- Validated end-to-end: 17 downloads through a
  `local-files` source ↔ `local-disk` sink with template
  `{user}/{date}/{trackid}_{filename}` came back byte-identical to
  the fixture and laid out as `dl1/2026-05-01/<trackid>_<filename>`.
- `main.go` `platformVersion` → `0.14.3`.

## [v0.14.2] — 2026-05-01
### Added — mocksftp `-persist-content`
End-to-end byte-fidelity validation for the v0.14 sources & sinks
feature was impossible against the bundled mock: it stored only
`{size, completedAt, trackID}` and synthesised zero-filled bytes on
download. Sizes round-tripped, content didn't.

- New `cmd/mockserver -persist-content` flag (and
  `mocksftp.Options.PersistContent`). When set, `writeHandle` stores
  uploaded bytes in `fileState.content`; `Fileread` returns
  `bytes.NewReader(st.content)`; `promoteInboxesLocked` shares the
  slice into the routed outbox and sender's sent/ entries.
- Defaults **off** — high-throughput throughput-only runs keep their
  zero-allocation download path. Turn ON for load tests that exercise
  `local-files` / `local-dir` upload sources or `local-disk` download
  sinks and want to checksum the round-trip.

Validated locally against this build: 26 downloads from a
`local-dir` source ↔ `local-disk` sink came back byte-identical to
the source fixtures (4/4 distinct hashes match), and a `per_user`
override for `alice` produced 18 `alice-special` downloads in dlA
with zero leakage into dlB's 17 default-pool downloads.

### Internal
- `main.go` `platformVersion` → `0.14.2`.

## [v0.14.1] — 2026-05-01
### Added — sources & sinks UI (Phase 2)
The v0.14.0 backend already accepted `normal_source` / `large_source` /
`download_sink` in the JSON payload. v0.14.1 surfaces those fields in
the Configure form so operators can build the config without
hand-editing JSON.

- **Three new disclosure panels** inside the Normal load, Large load,
  and Download cards. Closed-by-default summary (`— defaults to
  synthetic random bytes` / `— defaults to discard`) keeps the v0.13
  visual silhouette intact for operators who don't care.
- **Segmented kind picker** per panel with `aria-pressed` state:
  `synthetic / local-files / local-dir` for sources, `discard /
  local-disk` for the sink.
- **Kind-specific field groups** gated by `[hidden]`: file-list
  textarea, dir input, pick-mode picker (`round-robin / random /
  sequential`), sink root + template + overwrite toggle. The default
  template `{user}/{filename}` is pre-filled so a single `local-disk`
  click yields a sensible layout.
- **Per-user / per-pattern overrides** live in a nested advanced JSON
  textarea — keeps the most-specific resolution path available
  without forcing a complex inline editor.
- **Wire-format parity:** `readSource` / `readSink` return `null` when
  the picker is on the v0.13 default, so a freshly loaded v0.14.1 UI
  posts the same `/api/start` payload a v0.13 client would. No
  silent payload churn for unchanged configs.
- **Round-trip through Export / Import:** `applySource` / `applySink`
  populate the new disclosures from a saved JSON, including expanding
  the panel when a non-default kind is present.

### Internal
- New ES module `internal/web/static/js/sources-sinks.js` —
  exports mounted on `window.__srcSink` so the non-module
  `legacy.js` form serializer + import path can call into it
  without bundler changes.
- `components.css` gets `.source-disclosure` / `.source-body` /
  `.src-fields` / `.sink-fields` / `.source-advanced` rules — quieter
  chrome than `.disclosure` so the source panel reads as a secondary
  control inside its host card, not a second card.
- `main.go` `platformVersion` → `0.14.1`.

## [v0.14.0] — 2026-05-01
### Added — sources & sinks
Major new feature surface. Until now uploads always sent random bytes
(fast, throughput-only) and downloads always drained into io.Discard
(no on-disk persistence). v0.14 lets the operator swap either side
for real files — without sacrificing the synthetic default.

**Upload sources** (`NormalLoad.Source`, `LargeFileLoad.Source`):
- `kind: "synthetic"` — random bytes of the configured size. **Default.**
- `kind: "local-files"` — pool of explicit file paths.
- `kind: "local-dir"` — walks a directory ONCE at run start; every
  top-level regular file becomes a pool member (dotfiles + subdirs
  skipped for determinism).
- `mode: "round-robin" | "random" | "sequential"` — pick policy.
- `per_user[username]` — most-specific override; wins outright.
- `per_pattern[pattern]` — second-most-specific override.

Resolution hierarchy: PerUser → PerPattern → top-level Source → Synthetic.

**Download sinks** (`DownloadLoad.Sink`):
- `kind: "discard"` — `io.Discard`. **Default.**
- `kind: "local-disk"` — writes to `<root>/<rendered template>` with
  mode 0600. `mkdir -p` is automatic.
- `template` — path template with substitutions: `{user}`, `{filename}`,
  `{basename}`, `{ext}`, `{trackid}`, `{run_id}`, `{date}`, `{datetime}`.
  Default: `{user}/{filename}`.
- `overwrite: false` (default) → `O_EXCL`, errors if the file exists.
  `overwrite: true` → clobber.

**Security:** path-component sanitisation escapes `/` and `..` in
template variables to `_`. Defence-in-depth `filepath.Rel` check
confirms the resolved path stays under `root`. A hostile remote
filename can never punch out.

**New packages:**
- `internal/source` — `FileSource`, `Synthetic`, `LocalFiles`, `LocalDir`,
  `Resolver` (per-user/per-pattern/default chain).
- `internal/sink` — `FileSink`, `Discard`, `LocalDisk` (template renderer).

**Wire-up:**
- `config.SourceConfig` + `config.SinkConfig` with `Validate()` methods.
- `RunConfig.Validate()` rejects malformed combinations at run start.
- `runner.uploadOne` resolves the source per (user, pattern, kind).
  Real-file sources override the requested size with the actual file
  size and update `ExpectedSize` so the wire-level transfer + record
  agree.
- Download fetch path replaces `protocol.Drain` with `protocol.DrainTo`,
  copying bytes into the sink-supplied `WriteCloser`.
- `/api/start` accepts `normal_source` / `large_source` / `download_sink`
  fields. Nil keeps the v0.13.x defaults exactly.

### Added — examples + docs
- `examples/sources-and-sinks.json` — local-dir upload pool with a
  per-user override + local-disk download with the
  `{user}/{trackid}_{filename}` template.
- New ⌘K Help guide: "Real-file uploads + on-disk downloads
  (v0.14 sources & sinks)" — full reference for kinds, modes,
  template variables, override hierarchy, security guards.

### Verified
- New unit suites: `internal/source` (round-robin / sequential /
  resolver hierarchy / directory rejection / dotfile skip),
  `internal/sink` (discard / template rendering / traversal
  rejection / O_EXCL semantics).
- All 13 packages green under `go test -race -timeout 5m ./...`.

### Compatibility
- Pure additive: existing configs without source/sink keys behave
  identically to v0.13.30.
- Phase 2 (UI form sections so operators can build these configs
  without hand-editing JSON) lands in a follow-up.

## [Unreleased]

(no changes since v0.14.0)

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

[Unreleased]: https://github.com/roshandubey-cloud/utilities/compare/v0.14.0...HEAD
[v0.14.0]: https://github.com/roshandubey-cloud/utilities/releases/tag/v0.14.0
[v0.13.30]: https://github.com/roshandubey-cloud/utilities/releases/tag/v0.13.30
[v0.13.29]: https://github.com/roshandubey-cloud/utilities/releases/tag/v0.13.29
[v0.13.28]: https://github.com/roshandubey-cloud/utilities/releases/tag/v0.13.28
[v0.13.27]: https://github.com/roshandubey-cloud/utilities/releases/tag/v0.13.27
[v0.13.26]: https://github.com/roshandubey-cloud/utilities/releases/tag/v0.13.26
[v0.13.25]: https://github.com/roshandubey-cloud/utilities/releases/tag/v0.13.25
[v0.13.24]: https://github.com/roshandubey-cloud/utilities/releases/tag/v0.13.24
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
