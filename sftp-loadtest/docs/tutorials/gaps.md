# Gaps — what to fix before recording, what to call out, what to fix later

Read this **before shooting** any of the six tutorials. The scripts have
been written to avoid promising features that don't fully work today.
This document lists every gap I found while writing the scripts,
classified by severity, with the workaround the script uses (if any).

---

## P0 — Fix before recording

These would make a customer say *"the demo lied"*. None found in the
last full audit (2026-05-02). The six tutorial scripts route around
every limitation listed below without claiming anything broken.

---

## P1 — Notable limitations the scripts call out explicitly

### G1. Recurring schedules — UI doesn't expose the cron picker

- **Backend status:** the scheduler accepts cron-syntax bodies on `POST
  /api/schedule` and fires recurring runs correctly.
- **UI status:** only one-time schedules are reachable. The modal has
  date+time, no frequency picker.
- **Tutorial 06 handles it:** the narration explicitly says *"one-time
  schedules today; recurring schedules are reachable through the
  /api/schedule endpoint directly, the UI doesn't expose the cron
  picker yet."*
- **Fix priority:** medium. Ship a frequency picker (Daily / Weekly /
  Monthly + cron-expert mode) in a future v0.15.x release.

### G2. Per-load-type duration — single shared field

- **Backend status:** `RunConfig.DurationHours` is one float; Normal,
  Large, and Download all share it.
- **Use case missed:** "run Normal load for 30 min, but only kick off
  Large every hour" — operators have to compose multiple runs.
- **Tutorial 04 handles it:** the script doesn't claim per-load
  duration. The Large card's `interval_minutes` field controls cadence,
  not duration.
- **Fix priority:** low. Operators can chain runs via the scheduler.

### G3. Download orphan itemisation — count, not list

- **What is shown:** orphan count in the run summary and in the live
  status payload.
- **What is missing:** which specific files were orphaned. Operators
  who hit a non-zero count have to grep the CSV.
- **Tutorial 04 handles it:** the narration says *"orphans are counted
  but not itemised; if your count is non-zero, your server either
  dropped the file, took too long to process, or stripped the trackid
  suffix."* No claim that the UI lists them.
- **Fix priority:** medium. A small `Orphans (N)` badge in the run
  card that expands to a list would close this. Backend already has the
  data in the per-file records.

### G4. Per-user / per-load-type latency chart

- **What is shown:** aggregate p50/p95/p99 across all uploads.
- **What is missing:** "show me alice's p99 vs bob's p99" — requires
  CSV pivot today.
- **Tutorial 04 handles it:** narration explicitly says *"open this in
  a pivot table — group by user, sort by p99 upload — that's your
  slowest user."*
- **Fix priority:** low. Most analyst workflows are CSV-based anyway.

---

## P2 — Mild rough edges (the scripts ignore these)

### R1. Saved-config localStorage is per-machine

- **Tutorial 06 handles it.** Narration calls out: *"Saved presets are
  localStorage-backed — they survive across app restarts but live only
  on this machine. For sharing across the team, use Export."*
- Could add: cloud sync via a customer-supplied JSON-blob endpoint. Not
  a today-priority.

### R2. SSH wizard — first spawn uses password auth in the demo

- **Tutorial 05 handles it.** Narration: *"Password auth for the
  demo; in production you'd paste an SSH private key."*
- The wizard's S3 step exposes both auth methods; the demo just
  picks password for simplicity.

### R3. Worker version skew — warn but don't block

- **Tutorial 05 handles it.** Narration: *"a yellow chip would warn of
  skew; version skew isn't blocked, but you'll know."*
- Intentional design — letting a v0.14.18 master coordinate v0.14.17
  workers is acceptable for compatible features.

### R4. The "Distribute" toggle row is hidden when zero workers

- This is the **fix** from v0.14.14, not a bug — it was previously
  always visible with a noisy "no workers enabled" warning. Tutorial
  05 makes the appearance dynamic (*"appeared the moment the first
  worker was added"*).

### R5. Wizard URL parser is strict about `ssh://user[:pass]@host[:port]`

- **Workaround in tutorials:** scripts use the SSH path (which prompts
  fields individually), not the URL paste path.
- Could improve: accept terminal-style `user@host` as a shortcut.

### R6. Run ID collision on rapid reruns within 1 second

- `run-<UNIX_SECONDS>` collides if you Stop and Start within the same
  second. Rare; tutorials don't exercise this.

### R7. Status poller doesn't backoff on 503

- If the server restarts during a run, the poller hammers it at the
  configured interval (default 3s). Not noticeable in a 30-second
  tutorial run.

---

## P3 — Documentation gaps the tutorials close

### D1. Server contract for processing-time capture

- Tutorial 04 spells out the four-line server contract that the
  CHANGELOG glosses over. Customers shipping their own EDI gateway
  can use this checklist to verify their server cooperates with
  track-id mode.

### D2. Smart auto-link source→sink

- Tutorial 02 demonstrates the v0.14.12 auto-link feature that's
  documented in the CHANGELOG but never shown in a customer-grade
  walkthrough.

### D3. Per-worker CSV archive on Stop

- Tutorial 05 shows the v0.13.24+ feature where the master pulls each
  worker's CSV through the reverse SSH tunnel on Stop. Not previously
  demoed in any video.

### D4. Trust panel as audit surface

- Tutorial 06 frames `hosts.json` and `tls-hosts.json` as audit
  artefacts diff-able and back-up-able by fleet management. Currently
  the README mentions the JSON files but doesn't position them this
  way.

---

## Pre-recording validation script

Run this before each shoot — it confirms the tool is in the state the
tutorial assumes:

```bash
#!/bin/bash
set -e

cd ~/Downloads/utilities/sftp-loadtest

# 1. Build is clean
go vet ./...
go build ./...
go test ./cmd/desktop/... -count=1   # version pin test

# 2. Mock SFTP works with persist-content
go build -o /tmp/mocksftp ./cmd/mockserver
/tmp/mocksftp -addr 127.0.0.1:9999 -persist-content > /dev/null 2>&1 &
PID=$!
sleep 1
kill $PID

# 3. Probe endpoints respond
go build -o /tmp/sftp-loadtest .
/tmp/sftp-loadtest -addr 127.0.0.1:8088 \
  -reports-dir /tmp/reports -insecure-host-key > /tmp/webui.log 2>&1 &
PID=$!
sleep 1

curl -sf http://127.0.0.1:8088/api/version > /dev/null
curl -sf -X POST -H 'Content-Type: application/json' \
  -H 'X-Requested-With: sftp-loadtest' \
  --data '{"kind":"synthetic"}' \
  http://127.0.0.1:8088/api/probe-source > /dev/null
curl -sf -X POST -H 'Content-Type: application/json' \
  -H 'X-Requested-With: sftp-loadtest' \
  --data '{"kind":"discard"}' \
  http://127.0.0.1:8088/api/probe-sink > /dev/null

kill $PID
echo "ALL GREEN — safe to record"
```

If any line fails, fix the failure before recording — the script
assumes those endpoints work.

---

## What's NOT on this list (deliberate)

- Features that work fine and don't need a callout.
- Internal architectural choices (Go modules, Wails framework choice,
  etc.) — not customer-facing.
- Future-roadmap items that aren't claimed today.

The list is intentionally brutal. If a customer hits any of the P1
items, they should not be surprised — the script told them.
