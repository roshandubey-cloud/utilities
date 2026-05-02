# sftp-loadtest — tutorial library

Six production-ready tutorial scripts. Each is dimensioned for a **~3-minute
recorded video** (target 2:30–3:30) with English voice-over. The scripts are
written so a video editor can shoot them in one take from this document — every
click, value, narration line, and on-screen highlight is specified.

| # | Tutorial | Duration | What it teaches |
|---|---|---|---|
| 01 | [First run in 90 seconds](01-first-run.md) | 3:00 | Synthetic upload against the bundled mock SFTP server. Connection, TOFU pinning, single load, status polling, CSV export. |
| 02 | [Real files end-to-end](02-real-files-end-to-end.md) | 3:00 | `local-dir` source + `local-disk` sink, smart auto-link, byte-for-byte verification. The "no synthetic bytes — actual customer files" demo. |
| 03 | [N accounts via conventions](03-n-accounts-conventions.md) | 3:30 | Layout picker (`by-user` / `by-pattern`) — how 50-account tests stay declarative. Probe matrix preview before commit. |
| 04 | [Round-trip + processing-time capture](04-roundtrip-processing-time.md) | 3:00 | Track-id vs filename match modes, what the server has to do for processing-time to land in the report, latency percentiles, orphan tracking. |
| 05 | [Worker fan-out + cumulative reporting](05-worker-fanout-cluster.md) | 3:30 | SSH wizard → spawn workers → Distribute toggle → unified config broadcast → per-worker CSV archive on Stop. |
| 06 | [Enterprise: FTPS, trust, scheduling, presets](06-enterprise-trust-scheduling.md) | 3:30 | FTPS implicit + explicit, leaf-cert pinning, host-key store, scheduled runs, Export/Import config, command palette. |

Total runtime when shot back-to-back: **~19–20 minutes**.

---

## Production setup

### Recommended capture settings
- **Resolution:** 1920×1200 desktop, capture window-only (1280×820 — the app's default Wails frame)
- **Frame rate:** 30 fps
- **Codec:** H.264, ~6 Mbps
- **Audio:** mono, 48 kHz, -14 LUFS
- **Cursor:** show + click effects, ~1.2× size
- **Theme:** Dark (`Cmd+,` → set to "Dark"). The accent orange + dark canvas is what every screenshot in this repo assumes.

### Voice-over
Three options, in order of customer-grade quality:

1. **A real human VO.** Best quality, ~$50–150/video on Voice123 / Voices.com. Hand them the `## VO script` block from each tutorial.
2. **ElevenLabs** (ai voice, "Adam" or "Rachel" voice models). Paste the VO script block into their dashboard. ~$0.30/video on the Pro plan.
3. **macOS `say`** (free, fallback). One liner: `say -v Daniel -o tutorial-01.aiff -f tutorial-01-vo.txt`. Quality is mid-tier but acceptable for internal use.

### Pre-recording checklist (every tutorial)

1. Tag a clean release of the tool: `git checkout vX.Y.Z; ./scripts/build-desktop.sh` (or use the latest release `.app`).
2. Wipe app data so each tutorial starts from zero state:
   ```bash
   rm -rf "$HOME/Library/Application Support/sftp-loadtest"
   rm -rf "$HOME/Library/WebKit/com.roshandubey.sftp-loadtest-desktop"
   ```
3. Start a fresh `mocksftp` if the tutorial uses it (each tutorial declares its setup).
4. Set theme to Dark, sidebar to expanded, viewport to 1280×820.
5. Disable system notifications during the recording window (System Settings → Focus → Do Not Disturb).
6. Clear browser autocomplete / saved passwords if the tutorial enters credentials.

### Style guide for the editor

- **Cuts:** prefer J-cuts (audio leads video) for transitions between scenes; eyes catch the new screen as the narrator names it.
- **Highlights:** for every value the narrator names, pulse a 4px accent border around the field (1.5s, 0.4 opacity at peak). The CSS `field-pulse` animation already exists in the app — re-use it via DOM injection if you record from a real session, or composite the highlight in post.
- **Speed:** typing is typed at 1× (we want the operator to follow along). Page-load and metric-arrival waits are 4×–8× sped up — DO NOT cut them entirely; the customer needs to see the metrics arrive, just faster.
- **Captions:** burn the VO as captions (SRT, white text on 60%-opaque black, bottom-third). All values that the narrator says are referenced by name in the captions.

### Shared B-roll

Each tutorial calls for some shared cutaway footage. Shoot once, reuse:

- **B-roll #1 — Mock SFTP server log tailing.** A terminal showing `tail -f mocksftp.log` while a run is active. ~10s, used in tutorials 01, 04.
- **B-roll #2 — Activity Monitor / process tree.** macOS Activity Monitor with `sftp-loadtest-desktop` selected, showing CPU + memory rising as a run starts and falling on Stop. ~6s, used in tutorial 05.
- **B-roll #3 — Finder window of fixture directory.** A Finder window showing the `/tmp/sltval/upload-fixtures/` folder with the 4 known fixture files. ~4s, used in tutorial 02.

---

## Gap audit (before recording)

Read [gaps.md](gaps.md) before shooting. It lists features that are claimed
in the CHANGELOG but partially wired in the UI today, and the workarounds the
tutorials use to avoid promising broken features. Three gaps that affect
the scripts:

1. **Recurring schedules** — only one-time schedules are reachable from the
   UI. Tutorial 06 only demonstrates the one-time path.
2. **Per-load-type duration** — all loads share one `duration_hours`.
   Tutorials don't claim per-load duration.
3. **Download orphan itemisation** — orphans are counted, not listed.
   Tutorial 04 calls this out as "the count, not the list."

---

## Customer-facing summary blurb

For each video, paste this into the customer email + the YouTube description:

> **sftp-loadtest** is an MIT-licensed SFTP / FTP / FTPS load tester with a
> Wails-native desktop app. This tutorial walks through `<tutorial-title>` in
> ~3 minutes. Download the latest release at
> <https://github.com/roshandubey-cloud/utilities/releases/latest>.
> Source: <https://github.com/roshandubey-cloud/utilities/tree/main/sftp-loadtest>.
