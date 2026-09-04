#!/bin/sh
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
if [ -z "${JAVA_HOME:-}" ]; then
  if command -v java >/dev/null 2>&1 && java -version >/dev/null 2>&1; then
    : # Firebase can discover java from PATH.
  elif [ -x /usr/local/opt/openjdk@21/bin/java ]; then
    JAVA_HOME=/usr/local/opt/openjdk@21
    export JAVA_HOME
  else
    echo "Java 21+ is required (set JAVA_HOME or put java on PATH)" >&2
    exit 2
  fi
fi
export FIREBASE_EMULATORS_PATH="$ROOT/.cache/firebase-emulators"
export XDG_CONFIG_HOME="$ROOT/.cache/xdg"
export npm_config_cache="$ROOT/.cache/npm"
export CI=1
cd "$ROOT"
node run-bounded.mjs 120 npm ci --ignore-scripts --no-audit --no-fund
# emulators:exec normally owns teardown; the process-group deadline also kills its Java
# descendant if the CLI or probe hangs.
node run-bounded.mjs 150 npm run probe
