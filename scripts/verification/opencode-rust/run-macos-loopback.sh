#!/bin/bash
set -euo pipefail
umask 077
if [ "$#" -lt 2 ] || [ ! -x "$2" ]; then
  printf '%s\n' 'usage: run-macos-loopback.sh /private/tmp/oc.RANDOM/fresh-stage /absolute/path/to/rust-swarm [loopback flags]' >&2
  exit 2
fi
root=$1
test_dir=${root%/*}
[[ "$test_dir" =~ ^/private/tmp/oc\.[A-Za-z0-9]+$ ]]
[[ "${root##*/}" =~ ^[A-Za-z0-9][A-Za-z0-9_-]*$ ]]
[ "$(cd -- "$test_dir" && pwd -P)" = "$test_dir" ]
[ "$(stat -f '%u %Lp' "$test_dir")" = "$(id -u) 700" ]
native=${SWARM_OPENCODE_TEST_EXECUTABLE:-$(command -v opencode)}
case "$native" in /*) ;; *) exit 2 ;; esac
test -x "$native"
mkdir -m 700 "$root"
mkdir -p "$test_dir/bin"
[[ ! -L "$test_dir/bin" && ! -L "$test_dir/bin/opencode" && ! -d "$test_dir/bin/opencode" ]]
launcher=$(mktemp "$test_dir/bin/.opencode.XXXXXX")
# The shipping Go and Rust child allowlists omit TMPDIR. Keep Bun's scratch in this stage.
# shellcheck disable=SC2016 # Preserve the runtime variable expansions in the generated wrapper.
printf '#!/bin/bash\nset -eu\nexport TMPDIR=${XDG_DATA_HOME%%/data}\nexec %q "$@"\n' "$native" >"$launcher"
chmod 700 "$launcher"
mv -f "$launcher" "$test_dir/bin/opencode"
profile=$test_dir/${root##*/}.sb
[[ ! -e "$profile" && ! -L "$profile" ]]
cat >"$profile" <<PROFILE
(version 1)
(allow default)
(deny file-write*)
(allow file-write* (subpath "$root") (literal "/dev/null") (literal "/dev/tty") (literal "/dev/ptmx") (regex #"^/dev/tty[sS][0-9]+$"))
(deny network-outbound (remote ip "*:*"))
(allow network-outbound (remote ip "localhost:*"))
(allow network* (local ip "localhost:*"))
(allow network-bind (local unix-socket (subpath "$root")))
(allow network-outbound (remote unix-socket (subpath "$root")))
(allow network-inbound (local unix-socket (subpath "$root")))
PROFILE
fixture_dir=$(cd -- "$(dirname -- "$0")" && pwd -P)
exec /usr/bin/env -i PATH="$PATH" TMPDIR="$root"   /usr/bin/sandbox-exec -f "$profile" "$(command -v python3)" -B "$fixture_dir/loopback.py" "$@" --native "$native"
