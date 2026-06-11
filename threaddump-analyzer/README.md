# threaddump-analyzer

Enterprise-grade JVM thread-dump analyzer with **multi-dump diff**, **frozen-frame hang detection**, and **ranked findings**. Lightweight (single Go binary, embedded web UI, no runtime deps), but goes well beyond jstack-pretty-printer tools — the headline panel tells you what's broken and why, not just a histogram.

## Status

v0.1 — first release. Parser + analyzer + multi-dump session + findings engine + web UI all wired and tested. See [features](#features) for what's in, and [roadmap](#roadmap) for what's coming.

## Features (v0.1)

### Parsing
- HotSpot `jstack` / `jcmd Thread.print` format
- Stack frames, lock acquisitions, AQS-style park targets, daemon / priority / cpu / tid / nid metadata
- Streams without holding the whole file beyond what's necessary; handles 100 MB+ dumps

### Single-dump analysis
- Thread-state histogram (NEW/RUNNABLE/BLOCKED/WAITING/TIMED_WAITING/TERMINATED/UNKNOWN)
- Deadlock cycle detection (Tarjan-style wait-for-graph walk)
- Top contended monitors with holder + waiters
- Pool classification for Tomcat, Jetty, Hikari, c3p0, Netty, Reactor Netty, Kafka, Akka, gRPC, ForkJoin, JVM internals
- Per-pool saturation stats (% BLOCKED)
- Stack-signature deduplication ("47 threads share this exact stack")

### Multi-dump analysis
- Upload N dumps into one **session**; thread continuity is tracked across snapshots
- **Frozen-frame hang detector** — threads whose top-8 stack didn't change across every dump → near-certain proof of no forward progress
- Lifelines API (per-thread state + signature time series)

### Findings engine
- Ranked verdicts: severity × confidence × impact-count
- Built-in kinds: `DEADLOCK`, `POOL_EXHAUSTION`, `CONTENTION`, `HANG_SIGNATURE`, `ANTI_PATTERN` (finalizer-clog, classloading-contention), `SUMMARY`
- Every finding carries an evidence trail — never a black box
- Headline + detail + actionable remediation per finding

### Web UI
- Embedded static UI (no separate bundle, no build step)
- Newspaper-themed, dark-mode aware
- Three-step flow: create session → upload dumps → read findings
- Raw analysis tables hidden behind a disclosure for power users
- Same security envelope as the other utilities here: bound to 127.0.0.1, `X-Requested-With` CSRF guard, body-size cap, `MaxBytesReader`

## Quick start

```bash
# build
cd threaddump-analyzer
go build -o threaddump-analyzer .

# run
./threaddump-analyzer            # listens on http://127.0.0.1:8090

# in another terminal, upload the sample dump and check findings
SID=$(curl -s -X POST -H "X-Requested-With: threaddump-analyzer" http://127.0.0.1:8090/api/session \
  | python3 -c 'import json,sys;print(json.load(sys.stdin)["id"])')
curl -s -X POST -H "X-Requested-With: threaddump-analyzer" \
  --data-binary @examples/deadlock.jstack \
  "http://127.0.0.1:8090/api/session/$SID/upload"
curl -s -H "X-Requested-With: threaddump-analyzer" \
  "http://127.0.0.1:8090/api/session/$SID/findings" | python3 -m json.tool
```

Or just open `http://127.0.0.1:8090` in a browser and use the upload form.

## What it tells you on the sample

Running the bundled `examples/deadlock.jstack` produces (top three findings):

1. **CRITICAL · DEADLOCK · confidence 100%** — Deadlock cycle of 2 threads (`Thread-A` ↔ `Thread-B`).
2. **HIGH · CONTENTION · confidence 90%** — 4 threads contending on a `ConditionObject`; holder is the empty HikariCP pool's `getConnection` queue.
3. **INFO · SUMMARY** — 8 threads in dump, 2 BLOCKED, 4 WAITING.

With **two** dumps showing the same situation: a fourth finding fires — **HIGH · HANG_SIGNATURE · confidence ~88%** — "4 tomcat threads frozen across all 2 dumps in `HikariPool.getConnection`."

## API

| Method | Path | Body | Returns |
|---|---|---|---|
| `POST` | `/api/session` | `{"label":"…"}` | `{id, label}` |
| `GET`  | `/api/sessions` | — | `{sessions:[…]}` |
| `POST` | `/api/session/{id}/upload` | raw jstack | `{ok, threads, dumps, title}` |
| `GET`  | `/api/session/{id}/findings` | — | `{findings:[…]}` |
| `GET`  | `/api/session/{id}/analysis` | — | `{states, deadlocks, contention, pools, sig_groups, lifelines}` |
| `GET`  | `/api/session/{id}/dumps` | — | `{dumps:[…]}` |
| `GET`  | `/healthz` | — | `{ok, sessions, time}` |

All state-changing endpoints require the `X-Requested-With: threaddump-analyzer` header (CSRF guard).

## Roadmap (post-v0.1)

- OpenJ9 javacore parser
- Lock-holder progression view (who held what across dumps)
- Deadlock prediction from partial cycles
- Pattern DSL — operators add their own detection rules
- GC-log overlay; align GC pauses with stack freezes
- `top -H` / `pidstat` ingest for real per-thread CPU%
- Persistence + share links
- SSO (SAML/OIDC/LDAP) + RBAC + audit log
- Air-gapped CVE/library DB
- Kubernetes Operator + `ThreadDumpAnalysis` CRD
- Optional local-model AI assist with citations-only mode

## Tests

```bash
go test ./...
```

The bundled `examples/deadlock.jstack` is checked end-to-end against the parser, deadlock detector, contention ranker, and state histogram.

## License

MIT — same as the rest of `roshandubey-cloud/utilities`.
