# LinkedIn announcement post

> Image to attach: `feature-card.png` (1200×1200, in this folder)

---

## Post copy (paste this in)

Most SFTP load testers measure what *you* send.
They don't measure what your server actually *does* with the file.

So I built one that does.

**sftp-loadtest** — a lightweight, single-binary, web-UI driven SFTP load tester for production systems. Open source, MIT licensed.

What's different:

→ Captures *server-side processing time* per file (not just upload throughput)
→ End-to-end: upload → server processing → download round-trip, observed natively
→ 10 MB single binary · ~8 MB RSS at idle · macOS / Linux / Windows
→ Self-healing SSH (keepalives, pool reconnect, watcher redial) — multi-day runs survive idle drops
→ Streams CSV to disk during the run — RAM stays flat at 1k+ files/min for 24h+
→ Per-user auto-disable when credentials silently fail mid-run
→ Test-connection probe before committing to a real run
→ Built-in scheduler, /healthz, pprof, host-capacity panel

Why? Because k6 / Locust / JMeter are general-purpose. They start at 50–80 MB. They measure what they send, not what your server processes. And round-trips need scripting.

This was built for engineers who need an answer to "is my SFTP pipeline ready for production load?" — without a 4-hour install or a JVM.

🔗 github.com/roshandubey-cloud/utilities

Stars + feedback welcome. Especially if you've fought your own SFTP-load battle.

#SFTP #LoadTesting #Golang #DevOps #SRE #OpenSource #Performance #Engineering

---

## Notes

- ~210 word body (LinkedIn sweet spot: 150–300)
- First two lines are the hook — survive the "see more" truncation
- Bullets use `→` (renders cleanly on every platform; LinkedIn strips `*` and `-` formatting)
- One emoji (🔗) for visual scan; everything else is plain text
- Tags ordered by relevance; LinkedIn weights the first 3 highest
- CTA invites engagement ("if you've fought your own SFTP-load battle") rather than a pure ask
