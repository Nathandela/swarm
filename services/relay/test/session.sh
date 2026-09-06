#!/bin/sh
set -eu
port=${RELAY_TEST_PORT:?}
scratch=${RELAY_TEST_SCRATCH:?}
wrangler=
stop_worker() {
  [ -n "$wrangler" ] || return 0
  kill "$wrangler" 2>/dev/null || :
  wait "$wrangler" 2>/dev/null || :
  wrangler=
}
trap 'stop_worker' EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
start_worker() {
  state=$1
  retention=$2
  disable_alarms=$3
  challenge_ttl=$4
  rendezvous_ttl=$5
  cost_metrics=$6
  log="$scratch/$state.log"
  XDG_CONFIG_HOME="$scratch/config" WRANGLER_LOG_PATH="$scratch/debug-$state" \
    ./node_modules/.bin/wrangler dev --local --persist-to "$scratch/$state" --port "$port" \
      --inspector-port 0 \
      --var OPERATOR_NAMESPACE:local-test \
      --var ALLOWED_MACHINE_RIDS:88564c8ede170d2ed321e21e61354184 \
      --var CHALLENGE_TTL_MS:"$challenge_ttl" --var RENDEZVOUS_TTL_MS:"$rendezvous_ttl" --var RETENTION_MS:"$retention" \
      --var TEST_DISABLE_ALARMS:"$disable_alarms" \
      --var TEST_COST_METRICS:"$cost_metrics" \
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
start_worker state 60000 0 1000 1000 1
RELAY_HTTP="http://127.0.0.1:$port" node test/protocol.mjs
grep -F 'RELAY_V2_COST' "$log"
if RELAY_V2_HTTP="http://127.0.0.1:$port" \
  go test ../../internal/remote/relayv2 -run '^TestWorkerdNoiseMailboxReconnectReplayAndRevoke$' -count=1 -timeout=30s; then
  :
else
  sed -n '1,200p' "$log"
  exit 1
fi
stop_worker
start_worker discard-state 60000 0 30000 60000 0
if output=$(RELAY_V2_HTTP="http://127.0.0.1:$port" \
  go test ../../internal/remote/relayv2 -run '^TestWorkerdDiscardRetryRecovery$' -count=1 -timeout=30s -v 2>&1); then
  printf '%s\n' "$output"
  case "$output" in *"--- PASS: TestWorkerdDiscardRetryRecovery"*) :;; *) exit 1;; esac
else
  printf '%s\n' "$output"
  sed -n '1,200p' "$log"
  exit 1
fi
stop_worker
start_worker discard-expiry-state 100 0 30000 60000 0
if output=$(RELAY_V2_EXPIRY_HTTP="http://127.0.0.1:$port" \
  go test ../../internal/remote/relayv2 -run '^TestWorkerdDiscardRecoverySurvivesCutoffExpiry$' -count=1 -timeout=30s -v 2>&1); then
	printf '%s\n' "$output"
	case "$output" in *"--- PASS: TestWorkerdDiscardRecoverySurvivesCutoffExpiry"*) :;; *) exit 1;; esac
else
	printf '%s\n' "$output"
	sed -n '1,200p' "$log"
	exit 1
fi
stop_worker
start_worker skeleton-state 60000 0 30000 60000 0
if output=$(SKELETON_RELAY_V2_HTTP="http://127.0.0.1:$port" \
  go test ../../internal/skeleton -run '^TestLoadPairingConfigUsesNativeRelayV2MachineControl$' -count=1 -timeout=30s -v 2>&1); then
  printf '%s\n' "$output"
  case "$output" in *"--- PASS: TestLoadPairingConfigUsesNativeRelayV2MachineControl"*) :;; *) exit 1;; esac
else
  printf '%s\n' "$output"
  sed -n '1,200p' "$log"
  exit 1
fi
if output=$(OPERATOR_RELAY_V2_HTTP="http://127.0.0.1:$port" \
  go test ../../cmd/swarm -run '^TestOperatorRelayV2RevokeAndDeferredRetry$' -count=1 -timeout=45s -v 2>&1); then
	printf '%s\n' "$output"
	case "$output" in *"--- PASS: TestOperatorRelayV2RevokeAndDeferredRetry"*) :;; *) exit 1;; esac
else
	printf '%s\n' "$output"
	sed -n '1,200p' "$log"
	exit 1
fi
stop_worker
start_worker mobile-state 60000 0 30000 60000 0
if output=$(MOBILE_RELAY_V2_HTTP="http://127.0.0.1:$port" \
  go test ../../mobile -run '^Test(PairingUsesNativeRelayV2Transport|WorkerdPairingCommitFailurePublishesNoOutcomeOrAuthorization)$' -count=1 -timeout=45s -v 2>&1); then
	printf '%s\n' "$output"
	case "$output" in *"--- PASS: TestPairingUsesNativeRelayV2Transport"*"--- PASS: TestWorkerdPairingCommitFailurePublishesNoOutcomeOrAuthorization"*) :;; *) exit 1;; esac
else
  printf '%s\n' "$output"
  sed -n '1,200p' "$log"
  exit 1
fi
if output=$(SKELETON_RELAY_V2_HTTP="http://127.0.0.1:$port" \
  go test ../../internal/skeleton -run '^TestS19_' -count=1 -timeout=60s -v 2>&1); then
  printf '%s\n' "$output"
  case "$output" in *"--- PASS: TestS19_ARefusedLeaseReportsTheDaemonsReason"*) :;; *) exit 1;; esac
else
  printf '%s\n' "$output"
  sed -n '1,200p' "$log"
  exit 1
fi
if output=$(OPERATOR_RELAY_V2_HTTP="http://127.0.0.1:$port" \
  go test ../../cmd/swarm -run '^TestRelayDoctorV2UsesConfiguredMachineAndKeepsLiveStream$' -count=1 -timeout=45s -v 2>&1); then
	printf '%s\n' "$output"
	case "$output" in *"--- PASS: TestRelayDoctorV2UsesConfiguredMachineAndKeepsLiveStream"*) :;; *) exit 1;; esac
else
	printf '%s\n' "$output"
	sed -n '1,200p' "$log"
	exit 1
fi
stop_worker
start_worker alarm-state 60000 1 30000 1000 1
RELAY_HTTP="http://127.0.0.1:$port" node test/alarm-cost.mjs
grep -F 'RELAY_V2_ALARM_COST' "$log"
stop_worker
start_worker expiry-state 3000 1 30000 1000 1
RELAY_V2_EXPIRY_HTTP="http://127.0.0.1:$port" \
  go test ../../internal/remote/relayv2 -run TestExpiredReceipt -count=1
stop_worker
start_worker rate-state 60000 0 30000 1000 1
RELAY_HTTP="http://127.0.0.1:$port" node test/rate-limit.mjs
stop_worker
