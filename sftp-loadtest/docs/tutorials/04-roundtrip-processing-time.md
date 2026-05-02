# Tutorial 04 — Round-trip + processing-time capture

> **Audience:** EDI / B2B engineers who care about *end-to-end* latency,
> not just upload throughput. SLA owners. Anyone whose customer
> contract says *"99% of files processed within N seconds."*
> **Duration:** 3:00 (target 2:50–3:10)
> **Goal:** Configure a round-trip run that captures upload latency,
> server processing time, AND download latency separately — and explain
> exactly what the server has to do for processing time to land in the
> CSV.

After this video the operator should know: the difference between the two
match modes (track-id suffix vs filename pattern); what a track-id
actually is and where it appears in the report; how to read p50 / p95 /
p99 percentiles; what an "orphaned download" means and how to spot one;
and the contract the server must honour for processing-time to be
non-zero.

---

## Setup before recording

```bash
# Mock with paired routing + persist-content + 1s trackid delay
# (1s delay simulates a server that takes ~1s to "process" a file)
/tmp/tut/mocksftp -addr 127.0.0.1:2225 -trackid-delay 1s \
  -pairs "up1=dl1" -persist-content \
  > /tmp/tut/mocksftp.log 2>&1 &

rm -rf "$HOME/Library/Application Support/sftp-loadtest" \
       "$HOME/Library/WebKit/com.roshandubey.sftp-loadtest-desktop"
open /path/to/sftp-loadtest-desktop.app
```

---

## Storyboard

### 0:00 — 0:20 · Why processing time matters

**Visual.** Slide / overlay graphic: three timelines stacked.
- *Upload latency* (client → server): green segment.
- *Server processing time* (after PUT, before file appears in outbox):
  yellow segment.
- *Download latency* (server outbox → client): blue segment.
- Annotation: "Total round-trip = upload + processing + download."

**VO.**
> *Throughput tells you how fast you can push bytes. It doesn't tell
> you whether the file got processed. Real EDI workflows have three
> distinct phases: upload latency — bytes leave the client until the
> server acks; processing time — the server applies its business
> rules, validates the envelope, transforms, routes; and download
> latency — the result file lands in an outbox the client polls. SLA
> contracts are written against the SUM of all three. The tool
> captures each independently if the server is configured properly.
> Let me show you exactly what "configured properly" means.*

---

### 0:20 — 0:50 · The two match modes

**Visual.** Configure → Download card. The new compact segmented control:
**Track-id suffix (default) | Filename pattern (no track-id needed)**.

**On-screen action.** Hover each segment briefly so the tooltip text shows
on screen.

**VO.**
> *The Download card has one critical setting: round-trip tracking
> mode. Two options. Track-id suffix — the server appends a hash mark
> followed by a unique identifier to each processed file's name.
> `invoice123.txt` becomes `invoice123.txt#a1b2c3d4`. The runner
> watches for the renamed version. When it appears in the outbox,
> the runner downloads it, computes the round-trip latency, and
> attributes it back to the original upload row. This requires
> server cooperation — the server must add the suffix. OFTP and most
> EDI gateways do this natively. The second mode — filename pattern
> — works without server cooperation: the tool injects a marker
> like underscore-s-l-t-underscore followed by twelve random
> characters into the upload filename. The server preserves the
> marker. The tool finds the file in the outbox by substring match.*

---

### 0:50 — 1:10 · Track-id mode setup

**Visual.** Operator selects **Track-id suffix**. Fills:
- Connection: 127.0.0.1, 2225, up1 / pass.
- Upload card: FPM 30, min/max 1 MB, users `up1,pass,demo*`, folder `inbox`.
- Download card: folder `outbox`, parallel streams `2`,
  match mode `track-id suffix`, users `dl1,pass,*`.

**VO.**
> *I'm setting up a small run — thirty files per minute, one
> megabyte each, against the mock with a one-second trackid delay
> simulating a slow server. The mock APPENDS the trackid suffix —
> that's why this works in track-id mode out of the box.*

---

### 1:10 — 1:30 · Resource limits + start

**Visual.** Operator sets duration `0.01` (~36 seconds). Clicks Start.

**On-screen action.**
1. Run header bar appears.
2. Status flips to green.

**VO.**
> *Thirty-six second duration. Start. Run header pops in.*

---

### 1:30 — 2:30 · Reading the live metrics

**Visual.** Records panel and live charts. Three latency series
visible: **upload p50 / p95 / p99** in green, **download p50 / p95 /
p99** in blue. The records table columns include: filename, size,
**upload_ms**, **trackid**, **available_at_ms**, **download_ms**.

**On-screen action.** Pause and zoom on the table. Highlight the
trackid column (12 hex chars). Highlight the `available_at` and
`download_ms` columns.

**VO.**
> *The table tells the whole story. Each row is one upload. Filename
> on the left. Upload underscore m-s — how long the SFTP PUT took.
> Track-id — the unique twelve-hex-character marker the server
> assigned. Available underscore at — the moment the renamed file
> first showed up in the outbox; subtracting upload completion from
> that gives you SERVER PROCESSING TIME. Download underscore m-s —
> how long the runner took to pull the file back. Sum the three for
> the end-to-end round-trip per file. Notice the available-at column
> is roughly one second after the upload completed — that's the
> mock's configured trackid-delay; against your real server it'll be
> however long your business rules take to run. If your server
> takes thirty seconds to validate an EDIFACT envelope, you'll see
> thirty-second values here.*

**Highlight overlays.**
- Pulse "upload_ms" column header.
- Pulse "trackid" column header.
- Pulse "available_at_ms" column.
- Pulse "download_ms" column.
- Show a small tooltip: `processing_time_ms = available_at_ms - (upload_start_ms + upload_ms)`.

---

### 2:30 — 2:50 · Orphaned downloads + the SLA picture

**Visual.** Run completes. Cut to the Runs panel. Operator hovers the
just-finished run; the card shows totals: 18 uploads, 18 downloads, **0
orphans**. Then operator clicks the per-run CSV button.

**Cut to the CSV opened in a spreadsheet.** Columns visible:
`run_id, user, kind, filename, start_time, end_time, expected_size,
size_bytes, speed_mbps, error_code, error, track_id, filename_id,
download_user, download_available_at, download_start, download_end,
download_size, download_error_code`.

**VO.**
> *Run finishes. Eighteen uploads, eighteen downloads, zero orphans.
> An orphan is a file the runner uploaded that never reappeared in
> the outbox within the trackid timeout — section three's track-id
> timeout setting. If that count is non-zero, your server either
> dropped the file, took too long to process, or stripped the
> trackid suffix. The CSV column ordering: every upload row carries
> the matched download's columns suffixed `download underscore`.
> Open this in a pivot table — group by upload size, plot
> available-at minus end-time — that's your processing-time
> distribution by file size. Group by user, sort by p ninety-nine
> upload — that's your slowest user. The CSV is the analyst's tool;
> the live charts are the operator's eyes.*

---

### 2:50 — 3:00 · The server contract

**Visual.** Final overlay graphic: a checklist titled "What the server
must do for processing-time capture."
- [✓] Accept SFTP/FTP/FTPS PUT.
- [✓] Apply business rules.
- [✓] **Rename the file** in-place to add `#<unique-id>` (track-id mode).
- [✓] Place a copy in the download user's outbox path.
- [✓] Within the trackid-timeout window.

**VO.**
> *The server contract for track-id mode: receive the upload, apply
> your rules, rename the result with the hash-id suffix, drop a
> copy in the download user's outbox. Within the timeout. If your
> server can't do this — most B2B gateways can — switch to filename
> mode. The end-to-end measurement is what your customer's SLA
> cares about. Now you have it.*

---

## VO script (paste-ready)

```
Throughput tells you how fast you can push bytes. It doesn't tell you
whether the file got processed. Real EDI workflows have three distinct
phases: upload latency — bytes leave the client until the server acks;
processing time — the server applies its business rules, validates the
envelope, transforms, routes; and download latency — the result file
lands in an outbox the client polls. SLA contracts are written against
the SUM of all three. The tool captures each independently if the
server is configured properly. Let me show you exactly what "configured
properly" means.

The Download card has one critical setting: round-trip tracking mode.
Two options. Track-id suffix — the server appends a hash mark followed
by a unique identifier to each processed file's name. invoice 123 dot
txt becomes invoice 123 dot txt hash a1b2c3d4. The runner watches for
the renamed version. When it appears in the outbox, the runner
downloads it, computes the round-trip latency, and attributes it back
to the original upload row. This requires server cooperation — the
server must add the suffix. OFTP and most EDI gateways do this
natively. The second mode — filename pattern — works without server
cooperation: the tool injects a marker like underscore-s-l-t-
underscore followed by twelve random characters into the upload
filename. The server preserves the marker. The tool finds the file in
the outbox by substring match.

I'm setting up a small run — thirty files per minute, one megabyte
each, against the mock with a one-second trackid delay simulating a
slow server. The mock APPENDS the trackid suffix — that's why this
works in track-id mode out of the box.

Thirty-six second duration. Start. Run header pops in.

The table tells the whole story. Each row is one upload. Filename on
the left. Upload underscore m-s — how long the SFTP PUT took. Track-
id — the unique twelve-hex-character marker the server assigned.
Available underscore at — the moment the renamed file first showed up
in the outbox; subtracting upload completion from that gives you
SERVER PROCESSING TIME. Download underscore m-s — how long the runner
took to pull the file back. Sum the three for the end-to-end round-
trip per file. Notice the available-at column is roughly one second
after the upload completed — that's the mock's configured trackid-
delay; against your real server it'll be however long your business
rules take to run. If your server takes thirty seconds to validate
an EDIFACT envelope, you'll see thirty-second values here.

Run finishes. Eighteen uploads, eighteen downloads, zero orphans. An
orphan is a file the runner uploaded that never reappeared in the
outbox within the trackid timeout — section three's track-id timeout
setting. If that count is non-zero, your server either dropped the
file, took too long to process, or stripped the trackid suffix. The
CSV column ordering: every upload row carries the matched download's
columns suffixed download underscore. Open this in a pivot table —
group by upload size, plot available-at minus end-time — that's your
processing-time distribution by file size. Group by user, sort by p
ninety-nine upload — that's your slowest user. The CSV is the
analyst's tool; the live charts are the operator's eyes.

The server contract for track-id mode: receive the upload, apply your
rules, rename the result with the hash-id suffix, drop a copy in the
download user's outbox. Within the timeout. If your server can't do
this — most B2B gateways can — switch to filename mode. The end-to-
end measurement is what your customer's SLA cares about. Now you
have it.
```

Word count: ~620 ≈ 4:08 at 150 wpm. Trim with 2× speed-ups during the live
run (1:30–2:30) and the closing checklist beat.

---

## Talking points to call out on screen

- **0:30** — "Track-id mode requires server cooperation; filename mode doesn't"
- **1:50** — `processing_time = available_at - (upload_start + upload_ms)` formula overlay
- **2:35** — "Orphans count = files that never re-appeared within the timeout"
- **2:55** — Server contract checklist (4 line items)
