#!/usr/bin/env bash
# demo-walking-skeleton.sh — the GG-1 / E8.7 walking-skeleton acceptance demo.
#
# Proves the WHOLE architecture end-to-end before any real CLI adapter exists:
#
#   launch fake agent  ->  grouped in the general view  ->  attach (snapshot paints)
#   ->  type  ->  detach  ->  kill -9 the daemon  ->  every agent/shim still alive
#   ->  restart daemon  ->  session reconnected, nothing lost  ->  re-attach.
#
# The demo IS the Go end-to-end test internal/e2e.TestE2E_WalkingSkeleton_GG1: it
# drives a REAL `swarm daemon` subprocess through the real client protocol against
# swarm-fake-agent. This wrapper runs it verbosely so the sequence is legible, and
# is what CI invokes and what Nathan runs for human acceptance (E8.7).
#
# Exit status is the test's: 0 = the walking skeleton stands up end-to-end.
set -euo pipefail

cd "$(dirname "$0")/.."

echo "==> swarm walking-skeleton demo (GG-1 / E8.7)"
echo "==> building + driving a real swarm daemon subprocess against the fake agent"
echo

# -count=1 defeats the test cache so the demo always actually runs the sequence.
# -v surfaces each step; the test log narrates launch/attach/kill-9/restart.
go test -run '^TestE2E_WalkingSkeleton_GG1$' -count=1 -v ./internal/e2e/

echo
echo "==> walking skeleton PASSED: launch -> attach -> detach -> kill -9 -> restart -> reconnect, nothing lost."
