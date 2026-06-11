#!/usr/bin/env bash
# Phase 1 smoke test: daemon starts cleanly with the example config,
# survives a SIGTERM, and exits 0. Run via docker; the host doesn't need
# anything other than docker.
set -euo pipefail

/work/build/sftpadmind --config /work/examples/sftpadmin.conf >/tmp/out.log 2>&1 &
PID=$!
sleep 1
if ! kill -0 "$PID" 2>/dev/null; then
    echo "FAILED: daemon did not stay up"
    cat /tmp/out.log
    exit 1
fi
echo "==> daemon running pid=$PID"
kill -TERM "$PID"
wait "$PID" || rc=$?
rc="${rc:-0}"
echo "==> exit code: $rc"
echo "==> log:"
cat /tmp/out.log
test "$rc" -eq 0
