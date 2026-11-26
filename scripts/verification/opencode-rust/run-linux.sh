#!/bin/sh
set -eu
umask 077
if [ "$#" -lt 2 ]; then
  printf '%s\n' 'usage: run-linux.sh /tmp/oc.RANDOM or /dev/shm/oc.RANDOM fresh-run-name [loopback flags]' >&2
  exit 2
fi
probe_root=$1
case "$probe_root" in /tmp/oc.*|/dev/shm/oc.*) ;; *) exit 2 ;; esac
native=$(readlink -f "${SWARM_OPENCODE_TEST_EXECUTABLE:-$HOME/.local/bin/opencode}")
test -x "$native"
run_name=$2
case "$run_name" in ""|.*|*/*) exit 2 ;; esac
mkdir -p "$probe_root/bin"
shift 2
mkdir -m 700 "$probe_root/$run_name"
test -x "$probe_root/swarm"
test -f "$probe_root/loopback.py"
exec /usr/bin/bwrap --unshare-net --unshare-pid --unshare-ipc --die-with-parent \
  --proc /proc --dev /dev --tmpfs /tmp --ro-bind /usr /usr --ro-bind /bin /bin \
  --ro-bind /lib /lib --ro-bind /lib64 /lib64 --ro-bind /etc/terminfo /etc/terminfo --ro-bind /etc/ld.so.cache /etc/ld.so.cache --ro-bind /usr/share/zoneinfo/Etc/UTC /etc/localtime \
  --bind "$probe_root" /probe --ro-bind "$native" /probe/bin/opencode \
  --clearenv --setenv PATH /probe/bin:/usr/bin:/bin --setenv PYTHONUNBUFFERED 1 \
  /usr/bin/python3 -B /probe/loopback.py "/probe/$run_name" /probe/swarm "$@"
