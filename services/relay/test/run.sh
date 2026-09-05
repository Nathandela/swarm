#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
node test/deployment-config.test.mjs
node test/admission.mjs
port=${RELAY_TEST_PORT:-8790}
case "$port" in *[!0-9]*|'') echo "RELAY_TEST_PORT must be numeric" >&2; exit 2;; esac
budget=${RELAY_TEST_BUDGET_SECONDS:-180}
case "$budget" in *[!0-9]*|'') echo "RELAY_TEST_BUDGET_SECONDS must be numeric" >&2; exit 2;; esac
if curl -fsS "http://127.0.0.1:$port/" >/dev/null 2>&1; then
  echo "relay test port $port is already occupied" >&2
  exit 2
fi
scratch=$(mktemp -d "${TMPDIR:-/tmp}/swarm-relay-v2.XXXXXX")
trap 'rm -rf "$scratch"' EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
RELAY_TEST_PORT="$port" RELAY_TEST_SCRATCH="$scratch" \
  node test/run-bounded.mjs "$budget" sh test/session.sh
