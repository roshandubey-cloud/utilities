# Tutorial 02 — Real files end-to-end

> **Audience:** EDI gateway engineers, integration teams, anyone whose
> "load test" has to use real customer fixtures (PDFs, EDIFACT, X12, CSVs)
> instead of synthetic random bytes.
> **Duration:** 3:00 (target 2:50–3:10)
> **Goal:** Upload actual files from disk, save the round-tripped downloads
> back to disk, and verify byte-for-byte that what came out matches what
> went in.

After this video the operator should know: how `local-dir` source works,
how the smart auto-link wires the sink root from the source dir, what the
sink template variables produce, and how to verify byte fidelity using
SHA-256 outside the tool.

---

## Setup before recording

```bash
# Build a fixture directory with four real, distinct files
mkdir -p /tmp/tut/fixtures
echo "INVOICE-A real fixture content"  > /tmp/tut/fixtures/invoice-A.txt
head -c 524288  /dev/urandom > /tmp/tut/fixtures/payload-512k.bin
head -c 1048576 /dev/urandom > /tmp/tut/fixtures/payload-1m.bin
head -c 2097152 /dev/urandom > /tmp/tut/fixtures/payload-2m.bin

# Capture the SHA-256s for the on-camera comparison at 2:30
shasum -a 256 /tmp/tut/fixtures/* | tee /tmp/tut/fixture-hashes.txt

# Mock SFTP MUST run with -persist-content for byte-faithful round-trip
# (without it, downloads come back zero-filled — see CHANGELOG v0.14.2)
/tmp/tut/mocksftp -addr 127.0.0.1:2225 -trackid-delay 1s \
  -pairs "up1=dl1" -persist-content \
  > /tmp/tut/mocksftp.log 2>&1 &

# Fresh app state
rm -rf "$HOME/Library/Application Support/sftp-loadtest" \
       "$HOME/Library/WebKit/com.roshandubey-sftp-loadtest-desktop"
mkdir -p /tmp/tut/downloads
open /path/to/sftp-loadtest-desktop.app
```

---

## Storyboard

### 0:00 — 0:15 · Cold open: the fixtures on disk

**Visual.** B-roll #3 — Finder window showing
`/tmp/tut/fixtures/` with the four files visible: `invoice-A.txt` (52 B),
`payload-512k.bin`, `payload-1m.bin`, `payload-2m.bin`. Beside it, a
terminal showing `shasum -a 256 /tmp/tut/fixtures/*` output (the four hex
hashes that we'll verify against at 2:30).

**VO.**
> *Synthetic random bytes are great for throughput tests. They're useless
> when your gateway has to validate EDIFACT envelopes, parse X12 transaction
> sets, or stamp checksums on PDFs. For those tests you need real
> customer fixtures. Here are four files we'll use: an invoice, and
> three random binaries at five-twelve K, one meg, and two meg. Their
> SHA-two-fifty-six hashes are pinned in the terminal — we'll compare
> against them after the run.*

---

### 0:15 — 0:50 · Configure → Target → Test connection

**Visual.** Configure pane. Target section.

**On-screen action.**
1. Host `127.0.0.1`, Port `2225`.
2. Username `up1`, Password `pass`.
3. Click **Test connection**. (TOFU pins the host key.)

**VO.**
> *I'll keep the connection setup brief — we covered it in tutorial one.
> Mock server, port twenty-two-twenty-five, user up one, TOFU on,
> Test connection — green badge — host key pinned. Same as before.
> The interesting part starts in the workload card.*

---

### 0:50 — 1:30 · Upload card → Source kind → Local directory

**Visual.** Upload card. Operator clicks the **Upload source** disclosure
(the small `v0.14` badge to its right) — it expands.

**On-screen action.**
1. Click the disclosure to expand.
2. **Source kind** segmented picker — click **Local directory**.
3. **Root directory** field appears. Click the **Browse…** button to its
   right (Wails desktop only).
4. Native macOS folder picker. Navigate to `/tmp/tut/fixtures`. Click
   **Open**.
5. The path now reads `/tmp/tut/fixtures` in the input.
6. Click **Probe source**.

**VO.**
> *Section: Upload source. By default the runner streams synthetic
> random bytes — fast, but exactly the bytes your server has never
> seen. Switching to Local directory points the runner at a folder
> on this machine. Browse opens the native macOS picker — that's a
> Wails binding the desktop SKU exposes; the web SKU still works,
> the operator just types the path. I'll point at the four-file
> fixtures we set up. Probe source confirms what the runner will
> actually see — without doing any network I-O. Four files, three
> point six six megabytes total, exact paths and sizes printed.*

**Expected on screen.** The Probe output below the button reads:
```
OK — 4 files, 3.5 MB total
  /tmp/tut/fixtures/invoice-A.txt (52 B)
  /tmp/tut/fixtures/payload-1m.bin (1.0 MB)
  /tmp/tut/fixtures/payload-2m.bin (2.0 MB)
  …and 1 more
```

---

### 1:30 — 2:00 · Smart auto-link: source → sink

**Visual.** Operator scrolls to the **Download (round-trip)** card. Toggle
it **On**. Then opens the **Save downloads to disk** disclosure (sink). The
sink kind picker shows Discard / Local disk; operator clicks **Save to
local disk**.

**On-screen action.**
1. Toggle Download card **On**.
2. Open the sink disclosure.
3. Click **Save to local disk**.
4. Watch the **Root directory** field auto-fill with
   `/tmp/tut/fixtures-downloads` — the smart auto-link.

**VO.**
> *Now the symmetric move: turn on Download to round-trip the files
> back, and switch the sink to Save to local disk. Watch the Root
> field — it auto-fills with `/tmp/tut/fixtures-downloads`. That's the
> smart auto-link. The tool noticed I configured a local source and
> derived the sink root as a sibling directory. Anything I type
> overrides this; it only fills empty fields. The template stays at
> the default — `user slash filename` — which is exactly what we want
> for a fan-out by download user.*

**Highlight.** Pulse the Root directory field as it auto-fills.

---

### 2:00 — 2:20 · Users + sink probe + Start

**Visual.** The Download users CSV, then Probe sink, then Start.

**On-screen action.**
1. **Download users (CSV)**: paste `dl1,pass,*`.
2. Click **Probe sink**. Output reads `OK — /tmp/tut/fixtures-downloads is writable`.
3. **Resource limits → Duration:** `0.005` hours (~18 seconds).
4. Click **Start run**.

**VO.**
> *One download user, dl one, passwords, pull-everything pattern.
> Probe sink — that does a write probe on the sink root, creates the
> directory if it doesn't exist, drops a tiny test file, and removes
> it. Confirms the run has write permission before we commit. Eighteen
> seconds duration. Start.*

---

### 2:20 — 2:40 · Live + Stop + downloads on disk

**Visual.** ~18 seconds of live charts (4× speed in post). When the run
ends, cut to a Finder window showing
`/tmp/tut/downloads/dl1/`. Files visible — every uploaded file came back.

**On-screen action.** Wait for natural stop, then Sidebar → Runs to
confirm the run record.

**VO.**
> *The run hits the cadence — sixty files per minute means roughly one
> per second from the upload user — and the download user is polling
> the outbox every three seconds and pulling matched files back. After
> eighteen seconds, run finishes. Cut to the Finder: dl one's download
> root is full of files. Every uploaded file round-tripped successfully.*

---

### 2:40 — 3:00 · Byte verification

**Visual.** Cut to terminal:
```bash
$ shasum -a 256 /tmp/tut/downloads/dl1/* | awk '{print $1}' | sort -u
1652aae4d0fb02f143230194df403a608765f8cc9004ebdb18dc106f9a72025d
3b0789d040e31803da175bee0b243d77b9ce7afb423af8bf473cf0b876110ea1
b483e31189851dd7d98abb5a865af8e10cee27b8f3b72ac201fba85e6e37b170
b6fdb3d48ad42cbb81d6225dc7a7006ecffb094e2912cd3421049643c51aa089
```

Then a `diff` against the fixture hash file from 0:00.

**VO.**
> *Final verification. Hash every download, dedupe — four distinct
> hashes. Diff against the fixtures we hashed at the start —
> identical. Every file came back byte-for-byte. The mock server is
> honouring the persist-content flag we set; against a real EDI
> gateway the same comparison tells you whether the gateway's
> content-validation rules altered any byte.*

---

## VO script (paste-ready)

```
Synthetic random bytes are great for throughput tests. They're useless when
your gateway has to validate EDIFACT envelopes, parse X12 transaction sets,
or stamp checksums on PDFs. For those tests you need real customer
fixtures. Here are four files we'll use: an invoice, and three random
binaries at five-twelve K, one meg, and two meg. Their SHA-two-fifty-six
hashes are pinned in the terminal — we'll compare against them after the
run.

I'll keep the connection setup brief — we covered it in tutorial one. Mock
server, port twenty-two-twenty-five, user up one, TOFU on, Test
connection — green badge — host key pinned. Same as before. The
interesting part starts in the workload card.

Section: Upload source. By default the runner streams synthetic random
bytes — fast, but exactly the bytes your server has never seen.
Switching to Local directory points the runner at a folder on this
machine. Browse opens the native macOS picker — that's a Wails binding
the desktop SKU exposes; the web SKU still works, the operator just
types the path. I'll point at the four-file fixtures we set up. Probe
source confirms what the runner will actually see — without doing any
network I-O. Four files, three point six six megabytes total, exact
paths and sizes printed.

Now the symmetric move: turn on Download to round-trip the files back,
and switch the sink to Save to local disk. Watch the Root field — it
auto-fills with `slash tmp slash tut slash fixtures dash downloads`.
That's the smart auto-link. The tool noticed I configured a local
source and derived the sink root as a sibling directory. Anything I
type overrides this; it only fills empty fields. The template stays at
the default — user slash filename — which is exactly what we want for
a fan-out by download user.

One download user, dl one, password, pull-everything pattern. Probe sink
— that does a write probe on the sink root, creates the directory if
it doesn't exist, drops a tiny test file, and removes it. Confirms the
run has write permission before we commit. Eighteen seconds duration.
Start.

The run hits the cadence — sixty files per minute means roughly one per
second from the upload user — and the download user is polling the
outbox every three seconds and pulling matched files back. After
eighteen seconds, run finishes. Cut to the Finder: dl one's download
root is full of files. Every uploaded file round-tripped successfully.

Final verification. Hash every download, dedupe — four distinct hashes.
Diff against the fixtures we hashed at the start — identical. Every
file came back byte-for-byte. The mock server is honouring the
persist-content flag we set; against a real EDI gateway the same
comparison tells you whether the gateway's content-validation rules
altered any byte.
```

Word count: ~520 words. ≈ 3:28 at 150 wpm — trim Finder/terminal beats with
2× speed-ups in post to land at 3:00.

---

## Talking points to call out on screen (caption overlays)

- **0:08** — the four fixture sizes (52 B, 512 KB, 1 MB, 2 MB)
- **0:55** — "Probe source — local-only, no network" callout
- **1:45** — "Smart auto-link — fills empty fields, never overrides"
- **2:05** — "Probe sink writes a tiny test file and removes it"
- **2:55** — "Byte-for-byte verification — what went in came out"
