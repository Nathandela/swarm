#!/bin/sh
set -eu
port=${RELAY_TEST_PORT:?}
scratch=${RELAY_TEST_SCRATCH:?}
start_worker() {
  state=$1
  retention=$2
  disable_alarms=$3
  challenge_ttl=$4
  log="$scratch/$state.log"
  XDG_CONFIG_HOME="$scratch/config" WRANGLER_LOG_PATH="$scratch/debug-$state" \
    ./node_modules/.bin/wrangler dev --local --persist-to "$scratch/$state" --port "$port" \
      --inspector-port 0 \
      --var OPERATOR_NAMESPACE:local-test \
      --var ALLOWED_MACHINE_RIDS:88564c8ede170d2ed321e21e61354184 \
      --var CHALLENGE_TTL_MS:"$challenge_ttl" --var RENDEZVOUS_TTL_MS:1000 --var RETENTION_MS:"$retention" \
      --var TEST_DISABLE_ALARMS:"$disable_alarms" \
      --var TEST_COST_METRICS:1 \
      >"$log" 2>&1 &
  wrangler=$!
  i=0
  until curl -fsS "http://127.0.0.1:$port/" >/dev/null 2>&1; do
    i=$((i + 1))
    if ! kill -0 "$wrangler" 2>/dev/null || [ "$i" -ge 100 ]; then
      sed -n '1,200p' "$log"
      exit 1
    fi
    sleep 0.1
  done
}

# Only the protocol expiry control needs an accelerated authentication lifetime.
start_worker state 60000 0 1000
RELAY_HTTP="http://127.0.0.1:$port" node test/protocol.mjs
grep -F 'RELAY_V2_COST' "$log"
RELAY_V2_HTTP="http://127.0.0.1:$port" \
  go test ../../internal/remote/relayv2 -run TestWorkerd -count=1
kill "$wrangler"
wait "$wrangler" || :
start_worker alarm-state 60000 1 30000
RELAY_HTTP="http://127.0.0.1:$port" node test/alarm-cost.mjs
grep -F 'RELAY_V2_ALARM_COST' "$log"
kill "$wrangler"
wait "$wrangler" || :
start_worker expiry-state 3000 1 30000
RELAY_V2_EXPIRY_HTTP="http://127.0.0.1:$port" \
  go test ../../internal/remote/relayv2 -run TestExpiredReceipt -count=1
kill "$wrangler"
wait "$wrangler" || :
start_worker rate-state 60000 0 30000
RELAY_HTTP="http://127.0.0.1:$port" node test/rate-limit.mjs
kill "$wrangler"
wait "$wrangler" || :
