#!/usr/bin/env bash
# Before/after benchmark for v0.19.0 perf changes.
# Runs a 60-second SFTP upload/download against an in-process mocksftp
# at HEAD and at the previous tag, then prints the delta.
#
# Honest framing: the mock is localhost (RTT ≈ 0), so the
# UseConcurrentWrites win is suppressed (there's no per-packet
# round-trip to amortise). The CSV-flush win shows up regardless.
# For a real-world latency measurement, point at a remote SFTP
# instead.
#
# Usage:  ./scripts/bench-v019.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="${TMPDIR:-/tmp}/slt-bench"
mkdir -p "$WORK"

build_at() {
  local label="$1" rev="$2"
  echo "==> building $label ($rev)..."
  ( cd "$ROOT" && git checkout -q "$rev" )
  ( cd "$ROOT" && go build -o "$WORK/slt-$label" . )
  ( cd "$ROOT" && go build -o "$WORK/mock-$label" ./cmd/mockserver )
}

run_bench() {
  local label="$1"
  local mock_pid web_pid reports
  reports=$(mktemp -d)
  echo "==> running 60s SFTP bench under $label (reports=$reports)"
  "$WORK/mock-$label" -addr 127.0.0.1:23030 -trackid-delay 0 > "$WORK/mock-$label.log" 2>&1 &
  mock_pid=$!
  "$WORK/slt-$label" -addr 127.0.0.1:18181 -insecure-host-key -reports-dir "$reports" \
      > "$WORK/web-$label.log" 2>&1 &
  web_pid=$!
  # Wait for healthz.
  for i in 1 2 3 4 5 6 7 8 9 10; do
    curl -fsS http://127.0.0.1:18181/healthz >/dev/null 2>&1 && break
    sleep 0.5
  done
  curl -s -X POST -H 'X-Requested-With: sftp-loadtest' -H 'Content-Type: application/json' \
    -d '{
      "host":"127.0.0.1","port":23030,"protocol":"sftp",
      "upload_folder":"inbox","parallel_streams":4,
      "duration_hours":0.0167,
      "poll_seconds":3,"track_id_timeout_seconds":300,
      "normal_enabled":true,"files_per_minute":3000,
      "normal_min_mb":1,"normal_max_mb":1,
      "normal_users_csv":"u1,p,f-*\nu2,p,f-*\nu3,p,f-*\nu4,p,f-*"
    }' \
    http://127.0.0.1:18181/api/start > "$WORK/start-$label.json"
  # Wait for run to finish (~60s + drain).
  sleep 65
  curl -s -X POST -H 'X-Requested-With: sftp-loadtest' http://127.0.0.1:18181/api/stop \
    > "$WORK/stop-$label.json" 2>&1 || true
  sleep 5
  # Pull the meta JSON for the run that just finished.
  local meta
  meta=$(ls -t "$reports"/*.json 2>/dev/null | head -1 || true)
  if [ -z "$meta" ]; then
    echo "  no meta JSON produced"
  else
    python3 - "$meta" <<'PY'
import json, sys
m = json.load(open(sys.argv[1]))
print(f"  total_files          {m.get('total_files', 0):,}")
print(f"  total_bytes          {m.get('total_bytes', 0):,}")
print(f"  overall_mbps         {m.get('overall_mbps', 0):.2f}")
print(f"  peak_window_mbps     {m.get('peak_window_mbps', 0):.2f}")
print(f"  failed_files         {m.get('failed_files', 0)}")
print(f"  dispatch_skips       {m.get('dispatch_skips', 0)}")
lat = (m.get('latency') or {}).get('upload') or {}
def ms(ns): return (ns or 0) / 1e6
print(f"  upload p50 ms        {ms(lat.get('p50_ns')):.2f}")
print(f"  upload p95 ms        {ms(lat.get('p95_ns')):.2f}")
print(f"  upload p99 ms        {ms(lat.get('p99_ns')):.2f}")
PY
  fi
  kill "$web_pid" "$mock_pid" 2>/dev/null || true
  sleep 1
}

# Capture HEAD ref and previous tag for back-comparison.
ORIGINAL_REF=$(cd "$ROOT" && git rev-parse --abbrev-ref HEAD)
PREV_TAG=$(cd "$ROOT" && git describe --tags --abbrev=0)
trap '(cd "$ROOT" && git checkout -q "$ORIGINAL_REF")' EXIT

build_at "before" "$PREV_TAG"
run_bench "before"
build_at "after" "$ORIGINAL_REF"
run_bench "after"

echo
echo "==> delta:"
echo "    BEFORE = $PREV_TAG"
echo "    AFTER  = HEAD ($ORIGINAL_REF)"
