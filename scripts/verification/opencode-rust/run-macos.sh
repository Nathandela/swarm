#!/bin/bash
set -euo pipefail
if [ "${OPENCODE_RUST_FIXTURE_CLEAN:-}" != 1 ]; then
  exec /usr/bin/env -i PATH="$PATH" OPENCODE_RUST_FIXTURE_CLEAN=1 /bin/bash "$0" "$@"
fi
if [ "$#" -ne 1 ] || [ ! -x "$1" ]; then
  printf '%s\n' 'usage: run-macos.sh /absolute/path/to/rust-swarm' >&2
  exit 2
fi
bin=$1
case "$bin" in /*) ;; *) exit 2 ;; esac
fixture_dir=$(cd -- "$(dirname -- "$0")" && pwd -P)
test_root=$(mktemp -d /private/tmp/oct.XXXXXX)
export HOME="$test_root/home"
export SWARM_DAEMON_STATE="$test_root/state"
export SWARM_DAEMON_SOCK="$test_root/daemon.sock" SWARM_DAEMON_LOCK="$test_root/daemon.lock"
unset SWARM_SESSION_ID SWARM_MANAGER_VM_CLI
export OPENCODE_DISABLE_MODELS_FETCH=1 OPENCODE_DISABLE_AUTOUPDATE=1 OPENCODE_DISABLE_DEFAULT_PLUGINS=1 OPENCODE_DISABLE_PROJECT_CONFIG=1
export TERM=xterm-256color TMPDIR="$test_root"
mkdir -m 700 "$HOME" "$SWARM_DAEMON_STATE" "$test_root/project"
cat >"$test_root/guard.sb" <<PROFILE
(version 1)
(allow default)
(allow file-write* (subpath "$test_root") (literal "/dev/null") (literal "/dev/tty") (literal "/dev/ptmx") (regex #"^/dev/tty[sS][0-9]+$"))
(deny network-outbound (remote ip "*:*"))
(allow network* (remote ip "localhost:*"))
(allow network* (local ip "localhost:*"))
(allow network-bind (local unix-socket (subpath "$test_root")))
(allow network-outbound (remote unix-socket (subpath "$test_root")))
(allow network-inbound (local unix-socket (subpath "$test_root")))
PROFILE
[ "$(/usr/bin/sandbox-exec -f "$test_root/guard.sb" "$(command -v opencode)" --version)" = 1.19.3 ]
/usr/bin/sandbox-exec -f "$test_root/guard.sb" "$(command -v python3)" - <<'PYGUARD'
import errno,socket
for family, address in [(socket.AF_INET, "192.0.2.1"), (socket.AF_INET6, "2001:db8::1")]:
    with socket.socket(family, socket.SOCK_STREAM) as connection:
        connection.settimeout(0.2)
        try:
            connection.connect((address,80))
        except OSError as error:
            assert error.errno in (errno.EPERM, errno.EACCES), error
        else:
            raise AssertionError("offline guard admitted external egress")
PYGUARD
/usr/bin/sandbox-exec -f "$test_root/guard.sb" "$bin" daemon >"$test_root/daemon.log" 2>&1 &
daemon=$!
endpoint_verified=false
kill_daemon() {
  if [ "${1:-}" = cleanup ] && [ "$endpoint_verified" = true ] && [ -S "$SWARM_DAEMON_SOCK" ]; then "$bin" ls --json 2>/dev/null | python3 -c 'import json,sys; print("\n".join(r["id"] for r in json.load(sys.stdin) if r["status"]["Process"] == "running"))' | while IFS= read -r row; do [ -z "$row" ] || "$bin" kill "$row" >/dev/null 2>&1 || true; done; fi
  kill "$daemon" 2>/dev/null || true
  wait "$daemon" 2>/dev/null || true
}
trap 'kill_daemon cleanup' EXIT
for i in {1..50}; do [ -S "$SWARM_DAEMON_SOCK" ] && break; sleep .05; done
echo "$test_root"
python3 "$fixture_dir/launch.py"
endpoint_verified=true
result=$(python3 "$fixture_dir/launch.py" "$test_root/project") || {
  find "$test_root/state" -name opencode-launch.json -exec python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("reason", "ready"))' {} \;
  cat "$test_root/daemon.log"
  find "$test_root/state" -name observation.json -exec python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["status"])' {} \;
  exit 1
}
echo "$result"
row=$(printf '%s' "$result" | python3 -c 'import json,sys; print(json.load(sys.stdin)["session"]["id"])')
idle() {
  "$bin" ls --json | python3 -c 'import json,sys; r=next(row for row in json.load(sys.stdin) if row["id"]==sys.argv[1]); sys.exit(not (r["status"]["Turn"]=="idle" and r["status"]["Process"]=="running"))' "$row"
}
for i in {1..28}; do idle && break; sleep .25; done
idle
"$bin" history "$row" --limit 1 >"$test_root/before.json"
"$bin" requests "$row"
kill_daemon
/usr/bin/sandbox-exec -f "$test_root/guard.sb" "$bin" daemon >"$test_root/restart.log" 2>&1 &
daemon=$!
for i in {1..50}; do [ -S "$SWARM_DAEMON_SOCK" ] && python3 "$fixture_dir/launch.py" 2>/dev/null && break; sleep .05; done
python3 "$fixture_dir/launch.py"
"$bin" history "$row" --limit 1 >"$test_root/rejoined.json"
cmp "$test_root/before.json" "$test_root/rejoined.json"
"$bin" kill "$row"
for i in {1..24}; do
  [ "$("$bin" ls --json | python3 -c 'import json,sys; print(json.load(sys.stdin)[0]["status"]["Process"])')" = exited ] && break
  sleep .25
done
next=$("$bin" resume "$row" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
"$bin" history "$next" --limit 1 >"$test_root/after.json"
python3 - "$test_root" <<'PYTEST'
import json,sys
from pathlib import Path
root=Path(sys.argv[1])
old=json.loads((root/'before.json').read_text())
new=json.loads((root/'after.json').read_text())
assert old['native_session_id']==new['native_session_id']
assert old['native_created_at_ms']==new['native_created_at_ms']
assert old['session_instance']!=new['session_instance']
print('confirmed exact native root resume with a fresh incarnation')
PYTEST
"$bin" kill "$next"
sleep 2.2
