# How-to guide

Step-by-step walkthroughs for the most common `sftp-loadtest` flows.
Each section uses `[View → Card → Action]` breadcrumbs so you can map the
instructions to the UI.

---

## 1. First-time setup

1. **Pick the SKU.** [Server (web-ui) flavor](../README.md#pre-built-binaries)
   for headless / shared deployments; the [desktop-app flavor](../README.md#pre-built-binaries)
   for client laptops. Both ship the same engine and UI.
2. **Unpack the zip** for your platform and run the binary
   (`./sftp-loadtest-mac-apple-silicon` on macOS, double-click the `.app`
   on the desktop SKU, etc.).
3. **Open the UI.** The web-ui flavor logs
   `sftp-loadtest listening on http://127.0.0.1:8080` — open that URL.
   The desktop SKU launches its own native window.
4. **Land on Configure.** The sidebar's `[Configure]` row is selected by
   default. Type your SFTP host, port, folder, username, and password
   into the **Quick checks** card.
5. **Run a Test connection.** Click `[Configure → Quick checks → Test connection]`.
   On a server you've never connected to before, the result panel shows
   `New host key` with a SHA-256 fingerprint. Verify out-of-band, then
   click **Accept and connect** — the key is pinned for future runs.
6. **Save the connection** (optional). Click `[Configure → Quick checks → Save…]`,
   give it a name, and the sidebar's Connections section will hold it for
   one-click recall on subsequent days.

---

## 2. Running a basic load test (SFTP)

1. In `[Configure → Target]`, fill **Host**, **Port**, **Folder**,
   **Username**, **Password**.
2. In `[Configure → Workload]`, leave **Upload** enabled. Set
   **Files per minute** (e.g. `60`), the **Min file size / Max file size**
   range, and the **Users** CSV (one `user,password,trackid-pattern` per
   line — see the placeholder).
3. In `[Configure → Resource limits → Run]`, set **Run duration (hours)**
   (e.g. `0.05` = 3 min) and **Watcher poll seconds**.
4. Click `[Topbar → Run]` (or press `Cmd+Enter`). The status pill flips
   to `Running`.
5. Watch the upload happen on `[Workbench]`: the Throughput chart, Upload
   latency percentiles, Live activity rows, and Live metrics tile grid
   all stream while the run progresses.
6. When the duration elapses (or you click `[Topbar → Stop]`), the run
   finishes. Click `[Configure → Action zone → Download CSV]` to grab the
   per-file report.

---

## 2a. Running a load test against an FTP server (v0.13.0)

1. In `[Configure → Target → Quick checks]`, click the **FTP** segment of
   the protocol picker. The port snaps to `21` automatically (don't worry —
   if you've already typed a custom port, your value is preserved).
2. Fill **Host**, **Username**, **Password**, and **Folder** as you would
   for SFTP. The **Folder** convention (e.g. `inbox`) matches the FTP
   server's working directory.
3. Click **Test connection**. The probe runs `connect → AUTH (USER/PASS)
   → list folder` and reports per-stage timings.
4. The Workload card, user CSV, files-per-minute, parallelism — every
   knob — works identically to SFTP. The track-id watcher polls the same
   way; servers that already rename `foo.txt` → `foo.txt#<id>` are
   detected without any FTP-specific tweak.
5. Click **Run**. The slim run-summary chip reads `proto: ftp` while
   the run is active.

> The mock at `cmd/mockftpserver` (used by the test suite) is a 100-LOC
> protocol implementation — handy for smoke-running the tool offline.

---

## 2b. Running against an FTPS server with cert TOFU (v0.13.0)

1. In `[Configure → Target → Quick checks]`, click **FTPS** in the
   protocol picker. Two FTPS-specific controls appear:
   - **TLS mode** segmented — **Explicit** (AUTH TLS upgrade on the
     standard port 21) or **Implicit** (TLS from byte 0, port 990). The
     port snaps to the right default automatically.
   - **Server name** — SNI override; defaults to the host you typed.
   - **Trust self-signed cert** toggle — opt-in for lab servers whose
     certs aren't anchored in your system trust store.
2. Click **Test connection**. On success the OK card carries an extra
   **TLS certificate fingerprint** chip with the SHA-256 of the server's
   leaf cert. Verify out-of-band (e.g. against the cert your platform
   team published) before running real load against the box.
3. The fingerprint is also surfaced via `/api/probe` as `tls_fingerprint`,
   so automated CI gates can pin it. The fingerprint store + UI consent
   prompt for cert renewal is on the v0.14.x roadmap; for now the
   "Trust self-signed cert" toggle is the operator's explicit gate.
4. Click **Run**. The slim run-summary chip reads `proto: ftps`.

---

## 3. Using SSH key auth

1. Open `[Configure → Target → Quick checks]`.
2. Click the disclosure **Use SSH private key**.
3. Paste your private key into **Private key (PEM)**. PKCS8 and OpenSSH
   formats (ed25519, RSA, ECDSA) are all accepted. If the key is
   passphrase-protected, fill **Passphrase**.
4. Click **Test connection**. The probe now uses the key; password
   columns in the user CSV are ignored.
5. Click `[Topbar → Run]` to start the load test. Every user in every CSV
   authenticates with the single shared key.

> Tip: the PEM is held in memory only — it never reaches `localStorage`
> unless you explicitly opt-in via `[Schedule → Schedule & config →
> Include passwords in export]` before exporting.

---

## 4. Saving and reusing a connection

1. With the four credential fields filled, click `[Configure → Quick checks → Save…]`.
2. Name the entry (e.g. `acme-prod`). Tick **Save password in this browser**
   if you want the password persisted; leave it blank for "username only,
   I'll re-type the password each run".
3. Click **Save**. The sidebar's `[Connections]` section now lists the
   entry above your auto-tracked recents.
4. To recall: click the saved row in the sidebar — host / port / username
   / password are refilled in one shot.
5. To delete: hover the row, click the **×**, confirm.

---

## 5. Saving a config preset

1. Configure the form the way you like — workload, resource limits,
   download settings, the lot.
2. Press `Cmd+K` to open the command palette.
3. Type `save current config`, press `Enter`, give the preset a name in
   the prompt.
4. The preset now appears in the sidebar's `[Saved configs]` section.
   Click any preset row, or open `Cmd+K` and type `load preset → <name>`,
   to restore the entire form.

---

## 6. Scheduling a future run

1. Configure the form as you would for an immediate run.
2. Click `[Schedule]` in the sidebar.
3. In the **Schedule & config** card, set **Schedule run at** to a future
   time (the input is `datetime-local` — use your local clock).
4. Optionally fill **Note** so future-you remembers why this run was
   queued.
5. Click `[Schedule → Schedule this config]`. A confirm dialog appears;
   accept it. The pending row appears in **Pending schedules** below.
6. To cancel before it fires: click the **Cancel** button in the row.
7. Schedules survive process restarts (persisted as JSON under your
   config directory). Missed-during-downtime schedules are dropped, not
   stampeded.

---

## 7. Cluster fan-out (N workers)

1. Stand up `sftp-loadtest` on each worker machine (web-ui flavor),
   bound to a reachable address.
2. Open the controlling UI and click `[Cluster]` in the sidebar.
3. Click **+ Add worker** in the cluster KPI strip. Provide a name and
   the worker's URL (e.g. `http://10.0.0.5:8080`); add basic-auth creds
   if you launched the worker with `-auth-user` / `-auth-pass`.
4. The worker appears in the sidebar's `[Workers]` section. Toggle it
   on/off via the row's checkbox.
5. Switch to `[Configure]`. In the **Upload** card, tick
   **Distribute load across workers**.
6. Click `[Topbar → Run]`. The controller calls `/api/cluster/start` on
   every enabled worker; each worker runs the same config locally and
   reports its own metrics. Use the toast that pops to inspect run-ids.

---

## 7a. Bootstrapping a remote worker via SSH (v0.11.0)

For hosts where you don't want to expose an extra port, the master can
SSH to the remote, install the `sftp-loadtest` binary, run it bound to
loopback, and tunnel HTTP back through the existing SSH session.

1. `[Cluster] → + Add worker` opens the dual-tab modal. Switch to the
   **SSH bootstrap** tab.
2. Fill **Host**, **Port** (default 22), **User**, and either a
   **Password** or a private key (open the **Use SSH private key**
   disclosure to paste the PEM and an optional passphrase).
3. Pick an **Install method**:
   - **Download from GitHub release** — the remote needs internet access
     to `github.com/roshandubey-cloud/utilities`. Default.
   - **Upload local binary over SSH** — streams the master's own binary
     via the SSH session's SFTP subsystem. No outbound internet needed
     on the remote.
4. Click **Spawn worker**. The modal renders a live spawn log: arch
   detection, orphan reaping, install, smoke test, spawn, tunnel ready.
   Each step gets a ✓ on success or ✗ + the error message on failure.
5. On success the modal closes and the worker shows up in the sidebar
   `[Workers]` section with a `🔗 SSH` badge. The URL is the master-side
   loopback tunnel (`http://127.0.0.1:<random>`).
6. **Distribute load across workers** becomes enabled. Start a run as
   usual — the master fan-outs the per-worker config through the tunnel.
7. Click the row's `×` (sidebar) or **Remove** (Cluster view) to forget
   the worker. For SSH-sourced workers, this also POSTs
   `/api/worker/despawn`, which kills the remote process and closes the
   SSH session — no orphan stays running.

> The master's `Server.Shutdown()` closes every SSH-bootstrapped tunnel
> on a clean exit, so a Ctrl-C on the master also tears down the remote
> workers it started.

---

## 8. Reviewing past runs

1. Click `[Runs]` in the sidebar. The view stacks two sections:
   "About to run" (mirrors the current form) and the past-runs history.
2. Each past-run card shows summary KPIs, latency percentiles, an
   analyser panel with suggestions, the CSV download link, and an
   **Open** button.
3. Click **Open** to drill into the run-detail pane: KPIs, latency bars,
   host peaks, per-user breakdown, and the full Live activity recap for
   that run.
4. Click any sidebar nav row (e.g. `[Workbench]`) to escape the detail
   pane and return to the regular view.

---

## 9. Trusting / forgetting host keys

1. Click `[Trust]` in the sidebar.
2. The list shows every server whose key you've accepted via Test
   connection. Each row carries the host:port and SHA-256 fingerprint.
3. To forget a key: click the row's **×** button, confirm. The next
   connection to that host will prompt you to verify and accept the key
   again (TOFU re-enrollment).
4. If you launched the tool with `-known-hosts <file>`, the Trust view
   shows "Managed externally" — edit the file directly and restart.

---

## 10. Exporting / importing a config

1. To export: click `[Configure → Action zone → Export config]`. A JSON
   file downloads.
2. **Passwords are stripped by default.** Tick `[Schedule → Schedule & config → Include passwords in export]`
   first if you want the file to carry credentials (it triggers a
   confirm dialog as a safety net).
3. To import: click `[Schedule → Schedule & config → Import config]`,
   pick the JSON, the form refills.
4. **Import & Run now** loads the JSON and immediately starts the run —
   ideal for repeatable CLI-style tests where the JSON lives in a
   workspace.

> Security caveat: a config with passwords is a credential. Don't email
> it, don't commit it, don't paste it in a chat. The JSON is plaintext.
