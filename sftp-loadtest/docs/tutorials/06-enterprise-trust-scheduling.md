# Tutorial 06 — Enterprise: FTPS, trust, scheduling, presets

> **Audience:** Enterprise SRE / security teams. Compliance-conscious
> orgs. Anyone whose audit trail needs to show *what was tested, when,
> against which cert*.
> **Duration:** 3:30 (target 3:20–3:40)
> **Goal:** Cover the enterprise-grade affordances that didn't fit
> elsewhere: FTPS implicit + explicit, leaf-cert TOFU pinning, the trust
> store UI, scheduled runs, Export / Import config presets, and the
> command palette as a power-user surface.

After this video the operator should know: when to pick FTPS implicit
vs explicit; how leaf-cert pinning works (and how it differs from SSH
host-key pinning); how to inspect and remove trust entries; how to
schedule a run for off-hours; how to export a preset and re-import it
on a different machine; and how to drive the whole UI from the command
palette without touching the mouse.

---

## Setup before recording

```bash
# Need an FTPS server. The bundled mock server doesn't do FTPS; use a
# real one. vsftpd in a Docker container is the easiest.
docker run -d --name ftps -p 21:21 -p 990:990 -p 30000-30009:30000-30009 \
  -e FTP_USER=ftpsuser -e FTP_PASS=ftpspass \
  fauria/vsftpd

# OR use a proper FTPS server with self-signed cert:
# (full setup script in docs/howto.md)

# Fresh app
rm -rf "$HOME/Library/Application Support/sftp-loadtest" \
       "$HOME/Library/WebKit/com.roshandubey.sftp-loadtest-desktop"
open /path/to/sftp-loadtest-desktop.app
```

If FTPS is too involved for your recording setup, the tutorial still
works against SFTP — just note in the narration that the same TOFU
mechanism handles both protocols.

---

## Storyboard

### 0:00 — 0:30 · FTPS — implicit vs explicit

**Visual.** Configure → Target. Operator clicks the **FTPS** segment in
the Protocol picker. Two new fields slide in: a **TLS mode** segmented
picker (**Explicit | Implicit**) and a **TLS server name** field (SNI).
The port field auto-snaps to 21 (Explicit default) and to 990 if the
operator clicks Implicit.

**VO.**
> *Section one: enterprise protocols. Click FTPS in the protocol
> picker — the form reveals two FTPS-specific affordances. TLS
> mode: Explicit means AUTH TLS upgrade on the plain FTP control
> channel — works on any port, canonical port twenty-one. Implicit
> means TLS from byte zero — entire control channel encrypted from
> connect — works on any port, canonical port nine ninety. The
> port snaps automatically when you flip modes. SNI server name is
> optional and useful when one IP serves multiple cert subjects.
> Skip-verify exists for ephemeral lab testing. For everything
> else, leave skip-verify off and let TOFU pin the leaf cert.*

---

### 0:30 — 1:10 · TOFU pin on first connect

**Visual.** Operator fills FTPS host (`127.0.0.1`), port `990`, mode
Implicit, user `ftpsuser`, password `ftpspass`. TOFU checkbox **on**.
Clicks **Test connection**. The probe runs through TCP → TLS handshake →
FTP login → folder list. A small modal appears showing the leaf cert's
SHA-256 fingerprint, issuer, subject, not-before / not-after dates.
Operator clicks **Trust this cert**.

**VO.**
> *First connect against an FTPS server with TOFU on triggers a
> consent prompt — leaf cert fingerprint, issuer, subject, validity
> dates. Same idea as SSH host-key pinning, applied to TLS leaves.
> Operator approves once; the fingerprint is stored under app data
> as tls-hosts dot json. Subsequent connections verify against
> that pin strictly. A CHANGED leaf cert is refused as MITM
> signal. If your cert is rotating regularly, you flip skip-verify
> on; for stable production servers, the pin gives you the
> integrity guarantee.*

---

### 1:10 — 1:35 · The Trust panel

**Visual.** Sidebar → **Trust**. Two lists side-by-side: SSH host keys
(the entries pinned across all the tutorials) and FTPS leaf certs (just
the one we pinned at 1:00). Each row shows: server, port, fingerprint
(truncated SHA-256), pinned-at timestamp, and a small **Remove** button.

**VO.**
> *Sidebar — Trust. Two columns. SSH host keys, with their SHA-two-
> fifty-six fingerprints; FTPS leaf certs with theirs. Pin time
> stamped. Remove button per row — clicking it un-pins. The next
> connection to that server triggers TOFU again, or fails strictly
> if TOFU is off. This panel is your audit trail for the security
> team: who authorised which cert, when. The underlying JSON files
> live under app-data slash sftp-loadtest — hosts dot json for SSH,
> tls-hosts dot json for FTPS — both are diff-able and back-up-able
> by your fleet management.*

---

### 1:35 — 2:10 · Scheduled runs

**Visual.** Configure pane filled with a typical run — same as Tutorial
01. Operator hits **Cmd+K** to open the command palette. Types
*"schedule"*. The palette shows a **Schedule this config…** entry. Click.
A modal opens with: a date-time picker, a name field (auto-filled with
"after-hours soak"), a "Cancel previous schedules" checkbox.

**On-screen action.**
1. Pick a time 5 minutes from now.
2. Name: `after-hours soak`.
3. Click **Schedule**.
4. Sidebar → **Schedule** view: the queued run appears in the table
   with its trigger time and a small **Cancel** button.

**VO.**
> *Schedule. Cmd-K — palette — schedule. Pick a time five minutes
> from now, name it after-hours soak, save. Sidebar — Schedule —
> shows the queue. The schedule fires on a fifteen-second tick;
> when the trigger time arrives, the master starts the same run
> you just configured, the form snapshot is locked at schedule
> time so later edits don't affect already-queued runs. While a
> scheduled run is active, a sticky banner appears at the top of
> every view with a CSV link — you don't have to be on the Runs
> panel to grab it. One-time schedules today; recurring schedules
> are reachable through the slash a-p-i slash schedule endpoint
> directly, the UI doesn't expose the cron picker yet.*

---

### 2:10 — 2:35 · Export / Import config

**Visual.** Sidebar → Schedule → bottom of the page or via Cmd+K.
Operator clicks **Export config** (in the legacy actions row).
Wails native save dialog. Operator picks Desktop, names the file
`prod-soak.json`. Done.

Cut to a Finder showing the JSON file. Quick zoom on its contents in a
text editor — readable, all fields present, **passwords blanked**.

Then operator clicks **Import config** in the same actions row, picks
the JSON, the form repopulates. Or **Import & Run now** which posts
/api/start immediately.

**VO.**
> *Export config dumps the current form state to a JSON file —
> every field, including the v0.14 sources and sinks, plus
> resource limits. Passwords are blanked by default; check
> 'include passwords' if you really want them in the file, e.g.
> for an automated runner. Import does the inverse — file picker,
> form repopulates. Import & Run goes one step further — the run
> kicks off immediately, ideal for CLI-style repeatable tests.
> Versioned in your repo, this file is your scenario library.*

---

### 2:35 — 3:00 · Saved presets via the palette

**Visual.** Operator hits **Cmd+S**. Modal: *"Save current config as a
preset."* Name field, description field. Operator types
`production-edi-soak`, *"30-min FTPS soak against prod-edi-1, 5 EDI
accounts, sink to disk for cert validation."*. Saves. Then hits **Cmd+K**,
types `prod`, finds the preset in the palette, hits Enter — form
repopulates instantly.

**VO.**
> *Saved presets are localStorage-backed — they survive across app
> restarts but live only on this machine. Cmd-S to save the
> current form. Cmd-K to recall — the palette searches preset
> names and descriptions. Enter loads. For sharing across the team,
> use Export — that produces a real JSON file you check into git
> alongside your runbooks.*

---

### 3:00 — 3:30 · Closing — the audit story

**Visual.** Final overlay graphic. A timeline:
- 09:00 — Pinned cert (Trust panel)
- 09:05 — Saved preset `production-edi-soak`
- 18:00 — Schedule fires (banner)
- 18:30 — Run completes (Runs panel; CSV per-run; cluster archive if multi-worker)
- 19:00 — Exported config (audit attachment)

**VO.**
> *That's enterprise. Pinned cert with a fingerprint your security
> team can audit. Named preset versionable in git. Off-hours schedule
> the operator doesn't have to babysit. Per-run CSV your dashboard
> ingests. Per-cluster archive if you fanned out across boxes.
> Every artefact reproducible, every config diff-able, every cert
> change refused as a man-in-the-middle signal. Six tutorials,
> twenty minutes total. Source and releases at github dot com slash
> roshandubey-cloud slash utilities. Built honestly, MIT licensed.*

---

## VO script (paste-ready)

```
Section one: enterprise protocols. Click FTPS in the protocol picker —
the form reveals two FTPS-specific affordances. TLS mode: Explicit
means AUTH TLS upgrade on the plain FTP control channel — works on
any port, canonical port twenty-one. Implicit means TLS from byte
zero — entire control channel encrypted from connect — works on any
port, canonical port nine ninety. The port snaps automatically when
you flip modes. SNI server name is optional and useful when one IP
serves multiple cert subjects. Skip-verify exists for ephemeral lab
testing. For everything else, leave skip-verify off and let TOFU pin
the leaf cert.

First connect against an FTPS server with TOFU on triggers a consent
prompt — leaf cert fingerprint, issuer, subject, validity dates.
Same idea as SSH host-key pinning, applied to TLS leaves. Operator
approves once; the fingerprint is stored under app data as tls-hosts
dot json. Subsequent connections verify against that pin strictly. A
CHANGED leaf cert is refused as MITM signal. If your cert is rotating
regularly, you flip skip-verify on; for stable production servers,
the pin gives you the integrity guarantee.

Sidebar — Trust. Two columns. SSH host keys, with their SHA-two-fifty-
six fingerprints; FTPS leaf certs with theirs. Pin time stamped.
Remove button per row — clicking it un-pins. The next connection to
that server triggers TOFU again, or fails strictly if TOFU is off.
This panel is your audit trail for the security team: who authorised
which cert, when. The underlying JSON files live under app-data slash
sftp-loadtest — hosts dot json for SSH, tls-hosts dot json for FTPS
— both are diff-able and back-up-able by your fleet management.

Schedule. Cmd-K — palette — schedule. Pick a time five minutes from
now, name it after-hours soak, save. Sidebar — Schedule — shows the
queue. The schedule fires on a fifteen-second tick; when the trigger
time arrives, the master starts the same run you just configured, the
form snapshot is locked at schedule time so later edits don't affect
already-queued runs. While a scheduled run is active, a sticky banner
appears at the top of every view with a CSV link — you don't have to
be on the Runs panel to grab it. One-time schedules today; recurring
schedules are reachable through the slash a-p-i slash schedule
endpoint directly, the UI doesn't expose the cron picker yet.

Export config dumps the current form state to a JSON file — every
field, including the v zero point fourteen sources and sinks, plus
resource limits. Passwords are blanked by default; check 'include
passwords' if you really want them in the file, e.g. for an automated
runner. Import does the inverse — file picker, form repopulates.
Import and Run goes one step further — the run kicks off immediately,
ideal for CLI-style repeatable tests. Versioned in your repo, this
file is your scenario library.

Saved presets are localStorage-backed — they survive across app
restarts but live only on this machine. Cmd-S to save the current
form. Cmd-K to recall — the palette searches preset names and
descriptions. Enter loads. For sharing across the team, use Export —
that produces a real JSON file you check into git alongside your
runbooks.

That's enterprise. Pinned cert with a fingerprint your security team
can audit. Named preset versionable in git. Off-hours schedule the
operator doesn't have to babysit. Per-run CSV your dashboard ingests.
Per-cluster archive if you fanned out across boxes. Every artefact
reproducible, every config diff-able, every cert change refused as a
man-in-the-middle signal. Six tutorials, twenty minutes total. Source
and releases at github dot com slash roshandubey-cloud slash
utilities. Built honestly, MIT licensed.
```

Word count: ~640 ≈ 4:16 at 150 wpm. Land at 3:30 by trimming the
schedule-queue beat (2:00 area) and the audit-overlay closing.

---

## Talking points to call out on screen

- **0:25** — "Explicit = port 21, AUTH TLS. Implicit = port 990, TLS from byte 0"
- **0:55** — "TOFU pin → CHANGED cert refused as MITM"
- **1:25** — "tls-hosts.json + hosts.json are diff-able and back-up-able"
- **1:55** — "Form snapshot locked at schedule time — later edits don't affect queued runs"
- **2:30** — "Passwords blanked in export by default"
- **3:10** — "Audit-trail timeline: pin → preset → schedule → CSV → export"
