# threaddump-analyzer

Enterprise-grade JVM thread-dump analyzer with **multi-dump diff**, **frozen-frame hang detection**, **deadlock prediction**, a **user-extensible pattern DSL**, **GC-log overlay**, **`top -H` ingest**, and **ranked findings**. Single static Go binary, embedded UI, no runtime deps.

Goes well beyond jstack pretty-printers — the headline panel tells the operator what's broken and why, not just a histogram.

## Status

**v0.2.0** — second release. Adds OpenJ9 javacore, lock-holder progression, deadlock prediction, pattern DSL, GC-log overlay, CPU ingest, file-based persistence. All packages have tests; all green.

See [roadmap](#roadmap) for what's still deferred.

## Features (v0.2)

### Parsing
- **HotSpot `jstack` / `jcmd Thread.print`** — frames, locks, AQS-style park targets, daemon, priority, cpu / tid / nid metadata
- **OpenJ9 / IBM javacore** — full thread block with Java + native stacks, blocked-on lock with owner, vmstate → ThreadState mapping
- **Auto-detection** — upload either format, we route to the right parser

### Single-dump analysis
- State histogram (NEW/RUNNABLE/BLOCKED/WAITING/TIMED_WAITING/TERMINATED)
- **Tarjan-style deadlock cycle detection** — full cycles
- Top-contended monitors with holder + waiter count
- Pool classification: Tomcat, Jetty, HikariCP, c3p0, Netty, Reactor Netty, Kafka, Akka, gRPC, ForkJoin, JVM internals
- Per-pool saturation stats (% BLOCKED)
- Stack-signature deduplication ("47 threads share this top-6 stack")

### Multi-dump session analysis
- Thread continuity across snapshots
- **Frozen-frame hang detector** — top-N stack unchanged across all N dumps
- **Lock-holder progression** — who held lock X in dump 1, dump 2, …, with stable-holder flag (proves stuck monitor across time)
- **Deadlock prediction** — partial wait-for chains (≥3 threads, no cycle yet), with candidate "closer" thread identification

### Pattern DSL — user-extensible detection
- JSON-described rules: regex on stack frames, regex on thread name, required state, min-threads threshold
- Built-in catalog ships with: HikariCP saturation, Tomcat I/O stall, Log4j 1.x appender contention
- Operator drops their own `*.json` rules into a directory at startup; loaded at boot
- Returns operator-supplied severity, headline (with `{N}` placeholder), detail, remediation

### Findings engine
- Ranked verdicts: severity × confidence × impact-count
- Kinds: `DEADLOCK`, `POOL_EXHAUSTION`, `CONTENTION`, `HANG_SIGNATURE`, `ANTI_PATTERN`, plus everything from pattern DSL
- Every finding carries an evidence trail — never a black box
- Headline + detail + actionable remediation per finding

### Auxiliary inputs
- **GC log** (Unified Logging / pre-9 PrintGCDetails) — pause count, total stop-the-world time, max pause, Full/Mixed/Young breakdown
- **`top -H` / `pidstat -t`** — per-thread CPU% joined to dump threads by NID (hex↔decimal); surfaces the actually hot threads

### Persistence
- Sessions, raw dumps, GC log, CPU sample all persist to `--data-dir`
- 0o700 dir / 0o600 files — owner-only on shared hosts
- Atomic writes (temp + rename)
- Crash-resume on startup — process restart reloads sessions transparently

### Web UI
- Embedded static SPA (no separate bundle, no build step)
- Newspaper-themed, dark-mode aware
- Three-step flow: create session → upload artefacts (tabs for dumps / GC log / CPU) → read findings
- Raw analysis tables hidden behind a disclosure for power users
- Same security envelope as the other utilities here: bind 127.0.0.1, `X-Requested-With` CSRF guard, body-size cap, `MaxBytesReader`

## Quick start

```bash
# build
cd threaddump-analyzer
go build -o threaddump-analyzer .

# run
./threaddump-analyzer            # listens on http://127.0.0.1:8090
# or with a persistent data dir + extra pattern rules
./threaddump-analyzer --data-dir ~/.tda --patterns-dir ~/.tda/patterns

# end-to-end demo via curl
SID=$(curl -s -X POST -H "X-Requested-With: threaddump-analyzer" http://127.0.0.1:8090/api/session \
  | python3 -c 'import json,sys;print(json.load(sys.stdin)["id"])')
curl -s -X POST -H "X-Requested-With: threaddump-analyzer" \
  --data-binary @examples/deadlock.jstack \
  "http://127.0.0.1:8090/api/session/$SID/upload"
curl -s -X POST -H "X-Requested-With: threaddump-analyzer" \
  --data-binary @examples/sample.gclog \
  "http://127.0.0.1:8090/api/session/$SID/upload-gclog"
curl -s -X POST -H "X-Requested-With: threaddump-analyzer" \
  --data-binary @examples/sample.top \
  "http://127.0.0.1:8090/api/session/$SID/upload-cpu"
curl -s -H "X-Requested-With: threaddump-analyzer" \
  "http://127.0.0.1:8090/api/session/$SID/findings" | python3 -m json.tool
```

Or just open `http://127.0.0.1:8090` in a browser.

## API

| Method | Path | Body | Returns |
|---|---|---|---|
| `POST` | `/api/session` | `{"label":"…"}` | `{id, label}` |
| `GET`  | `/api/sessions` | — | `{sessions:[…]}` |
| `GET`  | `/api/patterns` | — | `{patterns:[…]}` |
| `POST` | `/api/session/{id}/upload` | raw jstack / javacore | `{ok, threads, dumps, title}` |
| `POST` | `/api/session/{id}/upload-gclog` | raw GC log | `{ok, stats}` |
| `POST` | `/api/session/{id}/upload-cpu` | raw top/pidstat | `{ok, rows, source}` |
| `GET`  | `/api/session/{id}/findings` | — | `{findings:[…]}` |
| `GET`  | `/api/session/{id}/analysis` | — | `{states, deadlocks, predictions, progressions, contention, pools, sig_groups, lifelines, gc_stats, cpu_top}` |
| `GET`  | `/api/session/{id}/progressions` | — | `{progressions:[…]}` |
| `GET`  | `/api/session/{id}/predictions` | — | `{predictions:[…]}` |
| `GET`  | `/api/session/{id}/patterns` | — | `{matches:[…]}` |
| `GET`  | `/api/session/{id}/dumps` | — | `{dumps:[…]}` |
| `GET`  | `/healthz` | — | `{ok, sessions, patterns, data_dir, time}` |

All state-changing endpoints require `X-Requested-With: threaddump-analyzer` (CSRF guard).

## Authoring a custom pattern rule

Drop a JSON file at `--patterns-dir/<id>.json`:

```json
{
  "id": "my-internal-rpc-stuck",
  "kind": "RPC_STALL",
  "severity": "high",
  "confidence": 80,
  "headline": "{N} workers stuck inside InternalRpcClient.invoke",
  "detail": "These threads are awaiting the response from internal-rpc-gateway. The gateway is the upstream cause; this JVM is not the bug.",
  "remediation": "Open the internal-rpc-gateway dashboard. If gateway is healthy, check the connection-pool size on this side.",
  "stack_includes_any": ["com\\.acme\\.rpc\\.InternalRpcClient\\.invoke"],
  "states": ["RUNNABLE", "TIMED_WAITING"],
  "min_threads": 3
}
```

Rules are reloaded on process restart. Add as many as you want.

## Roadmap (deferred)

These are real engineering items but each needs infrastructure decisions that go beyond a single-binary tool — they'll land as separate work when the deployment context calls for it:

- **SSO (SAML/OIDC/LDAP) + RBAC + audit log** — needs IdP integration decisions per deployment
- **Air-gapped CVE / known-bug library DB** — needs curated dataset + update bundle pipeline
- **Kubernetes Operator + `ThreadDumpAnalysis` CRD** — separate codebase + Helm chart
- **Local-model AI assist with citations-only mode** — needs model + RAG corpus decisions

The everyday-engineer roadmap items are all in v0.2.

## Tests

```bash
go test ./...
```

Drives the bundled `examples/*` fixtures end-to-end across every package. All green as of v0.2.0.

## License

MIT — same as the rest of `roshandubey-cloud/utilities`.
