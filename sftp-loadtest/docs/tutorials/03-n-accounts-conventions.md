# Tutorial 03 — N accounts via folder & pattern conventions

> **Audience:** Anyone running a load test that simulates many real
> partner accounts at once — typical EDI / VAN / B2B integration loads.
> **Duration:** 3:30 (target 3:20–3:40)
> **Goal:** Drive a 3-account test where every account uploads from its
> own folder of fixtures (or its own filename pattern), with zero
> per-account JSON, using the layout picker.

After this video the operator should know: when to use `flat`, `by-user`,
`by-pattern`, and `by-user-pattern` layouts; how the user CSV's pattern
column drives `by-pattern` filtering; how the per-user **probe matrix**
lets them validate every account's pool resolves before committing to a
30-minute run.

---

## Setup before recording

```bash
# By-user fixture tree: one subdir per account
TREE=/tmp/tut/tree
rm -rf $TREE && mkdir -p $TREE/alice $TREE/bob $TREE/charlie
echo "ALICE-INV-1 content" > $TREE/alice/inv-1.txt
echo "ALICE-INV-2 content" > $TREE/alice/inv-2.txt
echo "BOB-RPT-A content"   > $TREE/bob/rpt-a.txt
echo "BOB-RPT-B content"   > $TREE/bob/rpt-b.txt
echo "BOB-RPT-C content"   > $TREE/bob/rpt-c.txt
echo "CHARLIE-only file"   > $TREE/charlie/only-1.txt

# Mock SFTP with three paired routes (alice→dlA, bob→dlB, charlie→dlC)
/tmp/tut/mocksftp -addr 127.0.0.1:2225 -trackid-delay 1s \
  -pairs "alice=dlA,bob=dlB,charlie=dlC" -persist-content \
  > /tmp/tut/mocksftp.log 2>&1 &

rm -rf "$HOME/Library/Application Support/sftp-loadtest" \
       "$HOME/Library/WebKit/com.roshandubey.sftp-loadtest-desktop"
mkdir -p /tmp/tut/downloads
open /path/to/sftp-loadtest-desktop.app
```

---

## Storyboard

### 0:00 — 0:20 · The problem statement

**Visual.** Cut to a whiteboard graphic (or a simple slide in your editor):
"50 accounts. Each has different files. How do you configure the test?"
Below it: bad example showing 50 lines of repetitive JSON. Then transition
to the app.

**VO.**
> *Real load tests don't have one user. They have fifty. Each customer
> account has its own files — different fixtures, different naming
> patterns, sometimes its own folder. Configuring fifty separate
> JSON overrides by hand is tedious and error-prone. The layout picker
> turns this into convention. Three layouts, zero per-user JSON.*

---

### 0:20 — 0:55 · `by-user` layout — folder per account

**Visual.** Configure → Upload card. Source kind already on Synthetic;
operator clicks **Local directory**, then **Browse…** and picks
`/tmp/tut/tree`. The **layout sub-picker** appears — four segmented
buttons: *Flat (one pool) · By account folder · By filename pattern · By
account + pattern*. Default is **Flat**.

**On-screen action.**
1. Source kind → **Local directory**.
2. Browse → `/tmp/tut/tree`.
3. Layout picker → click **By account folder**.
4. The teaching-copy panel below the picker reads:
   *"Each account pulls from `<root>/<username>/*`. Create one subdir
   per account; if it's missing the run fails for that user."*
5. Users CSV: paste
   ```
   alice,pass,inv-*
   bob,pass,rpt-*
   charlie,pass,only-*
   ```

**VO.**
> *Layout one: by account folder. Point the source at a parent
> directory, and the runner pulls from `root slash username` per
> account. So Alice gets her two invoices, Bob gets his three reports,
> Charlie gets his single file. The per-user CSV is just three rows.
> No per-user override. No JSON. The pattern column — `inv dash star`,
> `rpt dash star` — is still used at upload time to generate
> filenames; the layout is what routes the source files.*

---

### 0:55 — 1:25 · Probe matrix — preview every account's pool

**Visual.** Click **Probe source**. The probe matrix renders below: one
row per (user, pattern) pair, showing per-account file count and total
bytes.

**On-screen action.** Click Probe source. Wait ~500ms.

**Expected on screen.**
```
Account    Pattern    Files    Total
alice      inv-*      2        72 B
bob        rpt-*      3        54 B
charlie    only-*     1        18 B
```

**VO.**
> *Probe source — and now the matrix. Three rows, one per CSV user.
> Alice resolves to two files in her subdir, totalling seventy-two
> bytes. Bob to three files, fifty-four bytes. Charlie to one. If
> any subdir is missing or empty, that row would highlight red with
> the friendly error inline — fix it before you commit to a real
> run. This is local validation only; nothing has been uploaded yet.*

---

### 1:25 — 2:00 · `by-pattern` — single root, glob filter

**Visual.** Operator clears the source dir, picks a flat folder
`/tmp/tut/flat` (set up beforehand with `inv-*`, `rpt-*`, `ack-*`), and
clicks **By filename pattern**.

**On-screen action.**
1. Browse → `/tmp/tut/flat`.
2. Layout → **By filename pattern**.
3. Probe source.

**Setup beforehand for this beat:**
```bash
mkdir -p /tmp/tut/flat
echo "INV-A" > /tmp/tut/flat/inv-A.txt
echo "INV-B" > /tmp/tut/flat/inv-B.txt
echo "RPT-1" > /tmp/tut/flat/rpt-1.txt
echo "RPT-2" > /tmp/tut/flat/rpt-2.txt
echo "RPT-3" > /tmp/tut/flat/rpt-3.txt
echo "ACK-X" > /tmp/tut/flat/ack-X.txt
```

**Expected matrix:**
```
Account    Pattern    Files    Total
alice      inv-*      2        14 B
bob        rpt-*      3        21 B
charlie    only-*     0        ERROR: no files match
```

**VO.**
> *Layout two: by filename pattern. Same root for everyone, but each
> account's CSV pattern filters the pool. Alice's `inv dash star`
> picks invoice files. Bob's `rpt dash star` picks reports. Charlie's
> `only dash star` matches nothing — and the matrix flags that row
> red. Fix the pattern, or move charlie out of this load. The
> validation happens before any network call.*

**On-screen action (recovery).** Edit charlie's pattern to `ack-*`.
Re-probe. Charlie now shows 1 file, 6 B.

---

### 2:00 — 2:30 · `by-user-pattern` — both axes

**Visual.** Brief slide / overlay graphic:
- `by-user`: `<root>/<user>/*`
- `by-pattern`: `<root>/* matched by pattern`
- `by-user-pattern`: `<root>/<user>/* matched by pattern`
- `flat`: `<root>/*` (all users share)

**VO.**
> *Layout three: by account plus pattern. The strictest. Each account
> has its own subdir AND its files have to match its CSV pattern.
> Use this when accounts share a parent root but have distinct file
> types — Alice's folder has invoices and credit memos, only invoices
> get uploaded. Layout four — flat — everyone shares the top-level
> files; the pattern column reverts to filename generation only. Pick
> the layout that matches how YOUR fixture folder is organised; the
> runner does the routing.*

---

### 2:30 — 3:00 · Run + result

**Visual.** Switch back to **By account folder** layout. Enable Download
card with three users `dlA / dlB / dlC`, sink set to local disk root
`/tmp/tut/downloads`, template `{user}/{filename}`. Duration `0.005`.
Start.

**Speed up post-production: 4× from 2:35 to 2:55.**

**Expected during run.** Records panel shows uploads from alice/bob/charlie
interleaved. Downloads land in `/tmp/tut/downloads/dlA/`,
`dlB/`, `dlC/`.

**VO.**
> *Back to by-account-folder, add three download users — dlA, dlB,
> dlC, paired in the mock — sink to local disk, template
> user-slash-filename. Eighteen-second run. Records show uploads
> interleaving across the three accounts; downloads fan out into
> three subfolders.*

---

### 3:00 — 3:30 · Byte isolation check

**Visual.** Terminal:
```bash
$ shasum -a 256 /tmp/tut/tree/alice/* | awk '{print $1}' | sort -u
$ shasum -a 256 /tmp/tut/downloads/dlA/* | awk '{print $1}' | sort -u
# (results match)
```

Repeat for bob → dlB, charlie → dlC. All three pairs show identical
hash sets. Cross-comparison: dlA's hashes are NOT in dlB's set.

**VO.**
> *Final check. Hash every alice fixture, hash every dlA download —
> identical. Same for bob to dlB, charlie to dlC. And critically,
> dlA's hashes don't appear in dlB's set; the layout picker enforced
> per-account isolation end to end. Zero cross-leakage. This is what
> a fifty-account test feels like with one source kind, one CSV, and
> one layout choice.*

---

## VO script (paste-ready)

```
Real load tests don't have one user. They have fifty. Each customer
account has its own files — different fixtures, different naming
patterns, sometimes its own folder. Configuring fifty separate JSON
overrides by hand is tedious and error-prone. The layout picker turns
this into convention. Three layouts, zero per-user JSON.

Layout one: by account folder. Point the source at a parent
directory, and the runner pulls from root slash username per account.
So Alice gets her two invoices, Bob gets his three reports, Charlie
gets his single file. The per-user CSV is just three rows. No per-
user override. No JSON. The pattern column — inv dash star, rpt dash
star — is still used at upload time to generate filenames; the
layout is what routes the source files.

Probe source — and now the matrix. Three rows, one per CSV user.
Alice resolves to two files in her subdir, totalling seventy-two
bytes. Bob to three files, fifty-four bytes. Charlie to one. If any
subdir is missing or empty, that row would highlight red with the
friendly error inline — fix it before you commit to a real run. This
is local validation only; nothing has been uploaded yet.

Layout two: by filename pattern. Same root for everyone, but each
account's CSV pattern filters the pool. Alice's inv dash star picks
invoice files. Bob's rpt dash star picks reports. Charlie's only dash
star matches nothing — and the matrix flags that row red. Fix the
pattern, or move charlie out of this load. The validation happens
before any network call.

Layout three: by account plus pattern. The strictest. Each account
has its own subdir AND its files have to match its CSV pattern. Use
this when accounts share a parent root but have distinct file types
— Alice's folder has invoices and credit memos, only invoices get
uploaded. Layout four — flat — everyone shares the top-level files;
the pattern column reverts to filename generation only. Pick the
layout that matches how your fixture folder is organised; the
runner does the routing.

Back to by-account-folder, add three download users — dlA, dlB,
dlC, paired in the mock — sink to local disk, template user-slash-
filename. Eighteen-second run. Records show uploads interleaving
across the three accounts; downloads fan out into three subfolders.

Final check. Hash every alice fixture, hash every dlA download —
identical. Same for bob to dlB, charlie to dlC. And critically,
dlA's hashes don't appear in dlB's set; the layout picker enforced
per-account isolation end to end. Zero cross-leakage. This is what
a fifty-account test feels like with one source kind, one CSV, and
one layout choice.
```

Word count: ~620 words ≈ 4:08 at 150 wpm. Trim by speeding the run-execution
beats to land at 3:30.

---

## Talking points to call out on screen

- **0:30** — "One subdir per account = one CSV row per account"
- **1:10** — "Probe matrix is local-only — no network I/O"
- **1:55** — "Failed rows highlight red with the per-account error inline"
- **3:10** — "Cross-account isolation enforced — alice's hashes are NOT in dlB's set"
