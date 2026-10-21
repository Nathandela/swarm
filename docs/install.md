# Installing swarm

The `swarm` terminal app is a static binary with no runtime dependencies. Remote
control also needs the companion `swarm-remote` binary beside it; the release
archives and Homebrew cask install both. Three ways to install the terminal app:

## Homebrew (macOS only)

```sh
brew install --cask Nathandela/swarm/swarm
```

This pulls from the `Nathandela/homebrew-swarm` tap, published by the release
pipeline (`.goreleaser.yaml`'s `homebrew_casks:` section) alongside every
tagged release. See [`packaging/homebrew/swarm.rb`](../packaging/homebrew/swarm.rb)
for an example of the formula the pipeline generates.

Homebrew casks only install on macOS (`brew install --cask` errors on Linux —
"Installing casks is supported only on macOS"). On Linux, use `go install` or
the static binary download below.

## `go install`

```sh
go install github.com/Nathandela/swarm/cmd/swarm@latest
```

Builds the terminal app from source with the Go toolchain already on your
machine. This installs only `swarm`, so it supports terminal use but not
`swarm remote init` or remote control. A source build reports `dev` because it
does not use the release version stamp; the managed self-upgrader refuses dev
builds, so `go install` owns updates for this installation. Release archives
also include a `compat.json` card that source builds do not contain.

For a remote-capable source install, build both commands from the same checkout
and install them together:

```sh
go install ./cmd/swarm ./cmd/swarm-remote
```

Or install both at the same release tag by replacing `vX.Y.Z` with that tag:

```sh
go install github.com/Nathandela/swarm/cmd/swarm@vX.Y.Z
go install github.com/Nathandela/swarm/cmd/swarm-remote@vX.Y.Z
```

## Static binary download

Download the Swarm archive for your platform from the
[GitHub releases page](https://github.com/Nathandela/swarm/releases). The
release has three Swarm archives: one universal macOS archive and Linux
`amd64` and `arm64` archives. Each contains `swarm`, `swarm-remote`, and the
release compatibility card. Verify the archive against `checksums.txt`:

```sh
shasum -a 256 -c checksums.txt --ignore-missing   # macOS
sha256sum -c checksums.txt --ignore-missing        # Linux
```

Extract the tarball and put both `swarm` and `swarm-remote` on your `PATH`.

Releases also publish a detached Ed25519 signature for `checksums.txt`.
`swarm upgrade` verifies that signature against its built-in release key and
checks the archive hash before staging an update. A standalone checksum check
detects a mismatch with the downloaded checksum file; it does not verify that
file's signature.

## Verifying the install

```sh
swarm version
```

prints the build version and Go toolchain version, e.g. `swarm v0.19.0
(go1.26.5)`. Source builds report `dev`.

## Upgrading: the version/`swarm daemon restart` note (D-8)

`swarm` auto-starts a background daemon on first use, and that daemon keeps
running (independent of your terminal) across upgrades of the `swarm` binary
on disk. After upgrading via any of the methods above, a client from the new
build and an already-running daemon from the old build can end up speaking
different build versions — the same wire protocol (so nothing breaks), but
different code. The client<->daemon hello handshake carries each side's build
version (`swarm version`'s value) precisely so this is detectable; to bring
the daemon itself up to the new build, run:

```sh
swarm daemon restart
```

This is safe: every running session survives the restart and is reconnected
by the replacement daemon — nothing is lost.
