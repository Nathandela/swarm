#!/bin/sh
set -eu
fixture_root=$1
fixture_python=$2
fixture_hermes=$3
fixture_node=$4
fixture_source=$5
case "$fixture_root" in /dev/shm/swarm-hermes-qual-*) ;; *) echo 'expected owned RAM fixture root' >&2; exit 2;; esac
host_netns=$(stat -Lc %i /proc/self/ns/net)
exec bwrap --unshare-user --uid 0 --gid 0 --unshare-net --unshare-pid --cap-add CAP_NET_ADMIN --die-with-parent \
  --ro-bind / / --ro-bind /home/ubuntu/data /home/ubuntu/data --proc /proc --dev /dev \
  --ro-bind /dev/shm /dev/shm --bind "$fixture_root" "$fixture_root" --chdir "$fixture_root" \
  --setenv SWARM_NATIVE_FIXTURE_OS_ISOLATED 1 \
  --setenv SWARM_NATIVE_FIXTURE_WRITABLE_ROOT "$fixture_root" \
  --setenv SWARM_NATIVE_FIXTURE_HOST_NETNS "$host_netns" \
  --setenv PYTHONDONTWRITEBYTECODE 1 \
  /bin/sh -ec 'ip link set lo up; exec "$@"' sh \
  "$fixture_python" "$fixture_root/hermes_native_admission.py" --root "$fixture_root" \
  --source "$fixture_source" --hermes "$fixture_hermes" --node "$fixture_node" \
  --swarm "$fixture_root/hermes-fixture-swarm-linux" --probe "$fixture_root/hermes-native-probe-linux"
