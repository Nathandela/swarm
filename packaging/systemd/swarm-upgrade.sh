#!/bin/sh
# swarm nightly auto-upgrade, the Linux half of auto-upgrade-plan.md L1 (the macOS half
# is packaging/launchd/com.swarm.upgrade.plist, where brew is the fetcher). Linux has no
# cask, so the fetcher is the GitHub release itself: when the latest published tag is
# newer than the installed binary, download the linux tarball, verify it against the
# release's checksums.txt, and install it over SWARM_BIN -- staged beside the target and
# moved into place, so the running daemon's inode is never truncated under it.
#
# Then ALWAYS converge, fetched or not, exactly as the plist joins its steps with ";"
# and not "&&": a restart deferred one night must be retried the next, and a binary
# upgraded by hand must be converged too. The job's exit status is `--unattended`'s
# (ADR-020: 0 converged or nothing to do, 1 failed, 2 deferred, 3 refused); the unit
# treats 2 and 3 as success because a deferral is the design working, not the run failing.
#
# Environment: SWARM_BIN (default /usr/local/bin/swarm, the documented tarball install
# path) and SWARM_REPO (default Nathandela/swarm). A systemd user service carries no
# owner PATH and no credentials; nothing here needs either -- the converge spawns the
# replacement daemon from the daemon.env the daemon saved at its last interactive start.
set -u

REPO="${SWARM_REPO:-Nathandela/swarm}"
SWARM_BIN="${SWARM_BIN:-/usr/local/bin/swarm}"

fetch() {
	installed=$("$SWARM_BIN" version 2>/dev/null | awk '{print $2}')
	latest=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" |
		sed -n 's/^ *"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1)
	if [ -z "$latest" ]; then
		echo "upgrade: cannot read the latest release tag for $REPO; keeping ${installed:-nothing}" >&2
		return 1
	fi
	if [ "v$installed" = "$latest" ]; then
		echo "upgrade: $installed is current"
		return 0
	fi
	ver=${latest#v}
	case "$(uname -m)" in
	x86_64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*)
		echo "upgrade: unsupported architecture $(uname -m)" >&2
		return 1
		;;
	esac
	tmp=$(mktemp -d) || return 1
	trap 'rm -rf "$tmp"' EXIT
	tarball="swarm_${ver}_linux_${arch}.tar.gz"
	base="https://github.com/$REPO/releases/download/$latest"
	curl -fsSL -o "$tmp/$tarball" "$base/$tarball" || {
		echo "upgrade: download failed: $tarball" >&2
		return 1
	}
	curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || {
		echo "upgrade: download failed: checksums.txt" >&2
		return 1
	}
	(cd "$tmp" && sha256sum --check --ignore-missing --quiet checksums.txt) || {
		echo "upgrade: checksum mismatch for $tarball; not installing" >&2
		return 1
	}
	tar -xzf "$tmp/$tarball" -C "$tmp" || return 1
	dst_dir=$(dirname "$SWARM_BIN")
	as_root=""
	[ -w "$dst_dir" ] || as_root="sudo -n"
	# swarm always; swarm-remote (the gateway, shipped in the same tarball) only where
	# one is already installed, so a gateway machine never runs a stale one and a
	# machine without a gateway gains no binary it never asked for.
	for bin in swarm swarm-remote; do
		dst="$dst_dir/$bin"
		[ "$bin" = swarm ] && dst="$SWARM_BIN"
		[ "$bin" = swarm ] || [ -e "$dst" ] || continue
		if ! $as_root install -m 0755 "$tmp/$bin" "$dst.new" || ! $as_root mv -f "$dst.new" "$dst"; then
			echo "upgrade: cannot install $dst (dir not writable and no passwordless sudo?)" >&2
			return 1
		fi
	done
	echo "upgrade: installed $latest over ${installed:-nothing} at $SWARM_BIN"
}

fetch || true
exec "$SWARM_BIN" daemon restart --unattended
