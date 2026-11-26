#!/bin/sh
# Run a native provider witness on a VM with only the installed executable and
# the synthetic loopback model available to the worker.
set -eu
if [ "$#" -lt 3 ] || [ "$#" -gt 4 ]; then
  echo 'usage: provider_native_linux.sh STAGED_FIXTURE PROVIDER_EXECUTABLE WITNESS.py [personal|accepted|revoked|board|worktree-board|external|external-go|capacity]' >&2
  exit 2
fi
fixture_dir=$(realpath "$1")
native_exe=$(realpath "$2")
mounted=/native/claude
witness=$3
case "$witness" in
  rust_agy_schedule.py)
    test "$#" -eq 3
    mounted=/native/agy
    set -- /fixture/swarm "$mounted" ;;
  rust_claude_resume.py)
    case "${4:-direct}" in
      direct|internal) set -- ;;
      external) set -- --external ;;
      external-go) set -- --external --go-owner /fixture/go-owner ;;
      capacity) set -- --external --capacity ;;
      board) set -- --board ;;
      worktree-board) set -- --worktree --board ;;
      *) exit 2 ;;
    esac
    set -- --candidate /fixture/swarm --provider /native/claude "$@" ;;
  rust_claude_schedule.py)
    test "$#" -eq 3
    set -- ;;
  rust_claude_worker_input.py)
    test -x "$fixture_dir/producer"
    case "${4:-personal}" in
      personal) set -- /fixture/producer ;;
      accepted|revoked) set -- /fixture/producer --guild "$4" ;;
      *) exit 2 ;;
    esac ;;
  *) exit 2 ;;
esac
if [ "$witness" = rust_claude_worker_input.py ] || [ "$witness" = rust_claude_schedule.py ]; then
  set -- /fixture/swarm /native/claude "$@"
fi
test -f "$fixture_dir/$witness"
test -x "$fixture_dir/swarm"
test -x "$native_exe"
netns=$(stat -Lc %i /proc/self/ns/net)
exec bwrap --unshare-user --uid 0 --gid 0 --unshare-net --unshare-pid --unshare-ipc \
  --cap-add CAP_NET_ADMIN --cap-add CAP_SETPCAP --die-with-parent --new-session --proc /proc --dev /dev \
  --tmpfs /tmp --ro-bind /usr /usr --ro-bind /bin /bin --ro-bind /lib /lib \
  --ro-bind /lib64 /lib64 --ro-bind /etc/passwd /etc/passwd --ro-bind /etc/hosts /etc/hosts \
  --ro-bind /etc/ld.so.cache /etc/ld.so.cache --ro-bind /etc/terminfo /etc/terminfo \
  --ro-bind /usr/share/zoneinfo/Etc/UTC /etc/localtime --ro-bind /etc/timezone /etc/timezone \
  --ro-bind "$fixture_dir" /fixture --ro-bind "$native_exe" "$mounted" --remount-ro / \
  --clearenv --setenv PATH /usr/sbin:/usr/bin:/bin --setenv HOME /tmp \
  --setenv SWARM_NATIVE_FIXTURE_OS_ISOLATED 1 --setenv SWARM_NATIVE_FIXTURE_WRITABLE_ROOT /tmp \
  --setenv SWARM_NATIVE_FIXTURE_HOST_NETNS "$netns" --setenv PYTHONDONTWRITEBYTECODE 1 \
  /bin/sh -ec 'ip link set lo up; exec setpriv --bounding-set=-all -- "$@"' sh \
  /usr/bin/python3 -B "/fixture/$witness" "$@"
