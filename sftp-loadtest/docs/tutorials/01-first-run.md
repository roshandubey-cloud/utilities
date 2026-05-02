# Tutorial 01 — First run in 90 seconds

> **Audience:** SFTP / EDI engineers, ops teams, anyone evaluating the tool
> for the first time.
> **Duration:** 3:00 (target 2:50–3:10)
> **Goal:** Get from "fresh download of `sftp-loadtest-desktop.app`" to
> "live charts ticking + a CSV in hand" in under three minutes, all from the
> UI, no CLI.

After this video the operator should know: how to point the tool at an SFTP
server, how Trust on First Connect (TOFU) pins the host key, what every
field in the Configure pane does at a high level, what happens when they
click Start, and what the Run history panel and per-run CSV give them.

---

## Setup before recording

Run this once:

```bash
# Build the bundled mock SFTP server (no prior state — clean for the take)
cd ~/Downloads/utilities/sftp-loadtest
go build -o /tmp/tut/mocksftp ./cmd/mockserver

# Start it on a non-default port so the recording shows the operator changing it
/tmp/tut/mocksftp -addr 127.0.0.1:2225 -trackid-delay 1s -persist-content \
  > /tmp/tut/mocksftp.log 2>&1 &

# Wipe app state so the recording opens from a true fresh install
rm -rf "$HOME/Library/Application Support/sftp-loadtest" \
       "$HOME/Library/WebKit/com.roshandubey.sftp-loadtest-desktop"

# Launch the app
open /path/to/sftp-loadtest-desktop.app
```

Window: 1280×820, dark theme, sidebar expanded.

---

## Storyboard

### 0:00 — 0:15 · Cold open: the wordmark + version pill

**Visual.** App opens to the Workbench view. Camera is on the masthead:
the `SFTP Load Test` wordmark, the small `v0.14.18` accent pill, the
status dot pulsing "Idle". Sidebar visible on the left listing
*Workbench / Configure / Schedule / Runs / Cluster / Trust*.

**On-screen action.** None — operator has just launched the app.

**VO.**
> *Welcome to sftp-loadtest. The pill next to the wordmark — `v zero point
> fourteen point eighteen` — tells you exactly which build is running, fetched
> live from the server. Today we're going from a brand-new install to a real
> load run, with a CSV on disk, in under three minutes.*

**Highlight.** Pulse the version pill at 0:08–0:10. Pulse the sidebar's
*Configure* row at 0:13–0:15 (operator's about to click it).

---

### 0:15 — 0:35 · Configure → Target

**Visual.** Click sidebar → Configure. Camera follows to the **Target**
section — the small caps `1 · TARGET` eyebrow with a thin accent rule, the
Protocol picker (SFTP / FTP / FTPS), Host, Port.

**On-screen action.**
1. Click **SFTP** segment (already selected by default — pulse it once for emphasis).
2. Click in the **Host** field, type `127.0.0.1`.
3. Click in the **Port** field, clear it, type `2225`.

**VO.**
> *Section one: Target. This is where we're connecting. The protocol picker
> snaps the port default — twenty-two for SFTP, twenty-one for FTP, nine
> ninety for FTPS implicit — but we're hitting the bundled mock server on
> port twenty-two-twenty-five, so I'll override the port. Notice the
> credentials live one row down — they're used by Test connection only.
> The actual run draws users from the CSV in section two.*

**Recording note.** When typing the host, type at human speed (~5 chars/sec).
Don't auto-fill; let the operator see each keystroke.

---

### 0:35 — 0:55 · Test connection + TOFU

**Visual.** Operator types `up1` into Username, `pass` into Password. The
**eye-icon** appears — operator clicks it once to reveal `pass`, clicks
again to re-mask. Then the small accent-pill **Test connection** button.

**On-screen action.**
1. Click **Username**, type `up1`.
2. Click **Password**, type `pass`.
3. Click the eye-icon (password reveals → re-clicks → masks).
4. Confirm the **Trust on first connect (TOFU)** checkbox is checked
   (default).
5. Click **Test connection**.

**VO.**
> *I'll enter creds the mock server accepts — `up one` and any password.
> The eye-icon shows the password without forcing me to retype if I want to
> verify what I keyed. Trust on first connect — TOFU — is on by default;
> the first time we touch this server, its SSH host key is pinned to a
> JSON store under app data. Subsequent runs verify against that fingerprint
> strictly. A CHANGED host key after pinning is refused as a man-in-the-
> middle signal. Clicking Test connection now…*

**Expected on screen.** A small spinner replaces the button label, then a
green badge appears: `OK · TCP 0ms · SSH+SFTP 6ms`. A tiny chip says
`host key pinned`.

**VO continues (immediately on success badge).**
> *…three stages succeeded — TCP dial, SSH handshake, SFTP subsystem — in
> single-digit milliseconds against a local mock. Against a real production
> server you'll see the WAN round-trip dominate the SSH stage. The host
> key was added to the trust store; we'll see it listed in section six.*

---

### 0:55 — 1:30 · Configure → Workload (Normal upload)

**Visual.** Scroll into the **Upload** card (small caps `2 · WORKLOAD`).
The card shows: enable toggle (already On), Folder field, Files per minute,
Min/Max size, Content, Users CSV.

**On-screen action.**
1. Confirm Upload toggle is **On**.
2. Click in **Folder (remote)**, type `inbox`.
3. **Files per minute** is `60` by default — leave it.
4. **Min size** = `1`, **Max size** = `5` — leave defaults.
5. **Content** dropdown — leave on `binary`.
6. Click into **Users (CSV)**, paste:
   ```
   up1,pass,demo*
   ```

**VO.**
> *Section two: Workload. The Upload card is on by default — `On` here
> means this load contributes to the run. The Folder is the remote path
> the SFTP user has write access to; on the mock that's `inbox`. Files
> per minute is the cadence — sixty means one upload per second per user.
> Min and Max size — between one and five megabytes per file, randomised
> within range. Content type stamps the bytes for any server-side
> classifier. The Users CSV is `username comma password comma pattern`;
> the trailing star in `demo star` is replaced with a unique nanos
> timestamp at upload time — `demo` becomes `demo` followed by a
> nineteen-digit timestamp dot text — so the server sees a stream of
> distinct filenames.*

**Highlight.** As the narrator says "trailing star", the `*` in the
textarea pulses.

---

### 1:30 — 1:50 · Resource limits + Start

**Visual.** Scroll to **3 · RESOURCE LIMITS** — the small caps eyebrow
with the same accent rule. Three sub-groups: Upload, Download, Run controls.

**On-screen action.**
1. **Run controls** group → **Duration (hours)** — clear, type `0.01`.
2. Click **Start run**. The button is the big primary accent CTA.

**VO.**
> *Section three: Resource limits. Parallel streams per user — two SSH
> connections per upload user. Polling interval — three seconds, both
> for our own status updates and for the SFTP outbox-listing cadence
> when downloads are on. Duration: I'll set zero point oh one hours
> — that's thirty-six seconds — so this tutorial doesn't run forever.
> The `Start run` button is the only filled-accent button in the form
> — that's deliberate. It's the headline action. Clicking now…*

**Expected on screen.** Start button disables (greyed). A sticky
**Run header bar** slides in from the top: *Run in progress · run-1700000000
· elapsed 0s · 0 files · 0 Mbps · [Stop run]*. The status dot in the
masthead flips to pulsing green.

---

### 1:50 — 2:30 · Live metrics

**Visual.** Records panel auto-scrolls into view. **Live charts**: throughput
in Mbps over time; latency p50 / p95 / p99; per-file activity tail. The
charts are accent-orange against the dark canvas.

**On-screen action.** Operator does NOT click anything for ~25 seconds.
**Speed up post-production: 4× from 2:00 to 2:25** so the metrics arrive
visibly but quickly.

**VO.**
> *Live metrics are now ticking. The throughput chart shows instantaneous
> megabits per second; the latency chart shows the per-file upload time
> percentiles — fiftieth, ninety-fifth, and ninety-ninth — refreshed
> every two seconds. Below the charts, a tail of the last two hundred
> per-file rows scrolls by — filename, size, upload start, latency,
> status, and error code if any. Notice every file's latency is
> single-digit milliseconds against the mock. Against a real server,
> p ninety-nine is the number your SLA cares about — that's the
> tail you'd alert on.*

**Highlight.** When the narrator says "p ninety-nine", flash an
accent border around the p99 line in the chart legend.

---

### 2:30 — 2:50 · Stop + CSV

**Visual.** Run completes naturally at ~36 seconds (or operator clicks
Stop in the run header). The run header collapses, status dot returns to
grey "Idle". Sidebar → **Runs** view.

**On-screen action.**
1. Wait for run to finish (or click Stop).
2. Sidebar → **Runs**.
3. Hover the most recent run card; click the small **CSV** button.
4. Native macOS save dialog appears (Wails); operator picks Desktop, clicks
   Save.

**VO.**
> *The run finished on its own at thirty-six seconds. Sidebar — Runs —
> shows every run this app has executed, newest first, with the totals
> and a per-run download. Clicking the CSV button on the most recent row
> opens a native save dialog because we're on the desktop SKU; the file
> contains every individual upload with timestamps, latencies, error
> codes, and the track-id. That CSV is what you hand to the analyst —
> or what you pivot in Excel to find your slow user, your slow file
> size, or your peak FPM ceiling.*

---

### 2:50 — 3:00 · Closing CTA

**Visual.** Cut to the GitHub release page or the project README.

**VO.**
> *That's the first run. Five tutorials follow — real-file uploads,
> n-account routing, round-trip processing-time capture, multi-worker
> fan-out, and enterprise security and scheduling. Download links and
> source are at github dot com slash roshandubey-cloud slash utilities.
> Built with care, MIT licensed.*

---

## VO script (paste-ready for ElevenLabs / human VO)

```
Welcome to sftp-loadtest. The pill next to the wordmark — v zero point
fourteen point eighteen — tells you exactly which build is running, fetched
live from the server. Today we're going from a brand-new install to a real
load run, with a CSV on disk, in under three minutes.

Section one: Target. This is where we're connecting. The protocol picker
snaps the port default — twenty-two for SFTP, twenty-one for FTP, nine
ninety for FTPS implicit — but we're hitting the bundled mock server on
port twenty-two-twenty-five, so I'll override the port. Notice the
credentials live one row down — they're used by Test connection only.
The actual run draws users from the CSV in section two.

I'll enter creds the mock server accepts — up one and any password. The
eye-icon shows the password without forcing me to retype if I want to
verify what I keyed. Trust on first connect — TOFU — is on by default;
the first time we touch this server, its SSH host key is pinned to a
JSON store under app data. Subsequent runs verify against that
fingerprint strictly. A CHANGED host key after pinning is refused as a
man-in-the-middle signal. Clicking Test connection now…

Three stages succeeded — TCP dial, SSH handshake, SFTP subsystem — in
single-digit milliseconds against a local mock. Against a real
production server you'll see the WAN round-trip dominate the SSH
stage. The host key was added to the trust store; we'll see it listed
in section six.

Section two: Workload. The Upload card is on by default — On here means
this load contributes to the run. The Folder is the remote path the
SFTP user has write access to; on the mock that's inbox. Files per
minute is the cadence — sixty means one upload per second per user.
Min and Max size — between one and five megabytes per file,
randomised within range. Content type stamps the bytes for any
server-side classifier. The Users CSV is username comma password comma
pattern; the trailing star in demo star is replaced with a unique
nanos timestamp at upload time — demo becomes demo followed by a
nineteen-digit timestamp dot text — so the server sees a stream of
distinct filenames.

Section three: Resource limits. Parallel streams per user — two SSH
connections per upload user. Polling interval — three seconds, both
for our own status updates and for the SFTP outbox-listing cadence
when downloads are on. Duration: I'll set zero point oh one hours —
that's thirty-six seconds — so this tutorial doesn't run forever. The
Start run button is the only filled-accent button in the form —
that's deliberate. It's the headline action. Clicking now…

Live metrics are now ticking. The throughput chart shows instantaneous
megabits per second; the latency chart shows the per-file upload time
percentiles — fiftieth, ninety-fifth, and ninety-ninth — refreshed
every two seconds. Below the charts, a tail of the last two hundred
per-file rows scrolls by — filename, size, upload start, latency,
status, and error code if any. Notice every file's latency is
single-digit milliseconds against the mock. Against a real server, p
ninety-nine is the number your SLA cares about — that's the tail
you'd alert on.

The run finished on its own at thirty-six seconds. Sidebar — Runs —
shows every run this app has executed, newest first, with the totals
and a per-run download. Clicking the CSV button on the most recent
row opens a native save dialog because we're on the desktop SKU; the
file contains every individual upload with timestamps, latencies,
error codes, and the track-id. That CSV is what you hand to the
analyst — or what you pivot in Excel to find your slow user, your
slow file size, or your peak FPM ceiling.

That's the first run. Five tutorials follow — real-file uploads, n-
account routing, round-trip processing-time capture, multi-worker
fan-out, and enterprise security and scheduling. Download links and
source are at github dot com slash roshandubey-cloud slash utilities.
Built with care, MIT licensed.
```

Word count: ~620 words. At 150 wpm reading pace = 4:08; trim with cuts +
faster delivery on the metric-arrival montage to land at 3:00.
