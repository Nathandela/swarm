# Installing swarm

`swarm` ships as a single static binary — no runtime dependencies, no
installer. Three ways to get it:

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

Builds from source with the Go toolchain already on your machine. Note: this
path does not run the release `-ldflags` stamp, so `swarm version` reports
`dev` rather than a release tag — functionally identical, just without a
human-readable version string. Prefer Homebrew (macOS) or the static download
below if you want `swarm version` to report something meaningful.

## Static binary download

Download the archive for your platform from the
[GitHub releases page](https://github.com/Nathandela/swarm/releases), matching
one of the 4 artifacts the release pipeline produces (darwin/linux ×
amd64/arm64), then verify it against the published `checksums.txt`:

```sh
shasum -a 256 -c checksums.txt --ignore-missing   # macOS
sha256sum -c checksums.txt --ignore-missing        # Linux
```

Extract the tarball and put `swarm` on your `PATH`.

## Verifying the install

```sh
swarm version
```

prints the build version and Go toolchain version, e.g. `swarm v1.2.3
(go1.24.2)`.

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
