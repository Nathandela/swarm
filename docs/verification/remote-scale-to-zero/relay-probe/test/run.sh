#!/bin/sh
set -eu
scratch_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
log="$scratch_dir/test/wrangler-test.log"
wrangler_bin=${RELAY_PROBE_WRANGLER:-"$scratch_dir/node_modules/.bin/wrangler"}
rm -f "$log"
XDG_CONFIG_HOME="$scratch_dir/.xdg" WRANGLER_LOG_PATH="$scratch_dir/.wrangler-logs" \
  "$wrangler_bin" dev --local --port 8788 >"$log" 2>&1 &
pid=$!
cleanup() { kill "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true; }
trap cleanup EXIT INT TERM
i=0
while ! grep -q "Ready on" "$log"; do
  i=$((i + 1))
  [ "$i" -lt 80 ] || { sed -n '1,160p' "$log"; exit 1; }
  sleep 0.1
done
node "$scratch_dir/test/negative.mjs"
RELAY_URL="ws://127.0.0.1:8788/v2/ws" node "$scratch_dir/test/integration.mjs"
