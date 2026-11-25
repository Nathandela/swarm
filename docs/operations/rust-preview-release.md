# Manual Rust preview release

The Rust preview uses separate `swarm-rust_…` archive names. The Go release channel and Homebrew tap continue to own existing installations. Keep the preview on a separate distribution channel and leave the Go release as the repository's latest stable release. Before making any public tag, inspect its commit and the destination's default branch for GitHub Actions release workflows. Use a disposable test installation until the state-transition gates in the [Rust transition plan](../specifications/rust-migration/transition.md) pass.

## Freeze and build

Use a clean, exact source commit, Cargo's locked dependency graph, and the compiler build pinned by `scripts/release-rust.py`. Have Python 3.11+, Xcode with a Darwin SDK, both Darwin Rust targets, and both Linux musl Rust targets and their C compilers/linkers available. Record `rustc --version --verbose`, `xcodebuild -version`, `xcrun --show-sdk-version`, the linker versions, operating system versions, and the dependency/license review in the release evidence. Fetch Cargo dependencies before the frozen build.

Obtain the four official Node 24.17.0 archives for Darwin and Linux arm64 and x64 as `.tar.xz` files. Verify their upstream signed checksum list in the Node release channel independently. The builder compares each archive to the pinned digest. The extracted Node and its license ship with each target's lock-resolved npm dependencies. npm installation takes place in scratch with lifecycle scripts and binary links disabled. The application embeds the resulting media digests. The Linux application is built for musl, while the bundled Node and DAVE addon require a glibc-based system.

Run these gates locally:

```sh
cargo fmt --all -- --check
cargo clippy --locked --workspace --all-targets -- -D warnings
cargo test --locked --workspace
python3 scripts/release-rust.py build \
  --source "$(git rev-parse HEAD)" --version 0.1.0 \
  --node-archives /path/to/verified/node-archives \
  --target-dir /path/to/scratch/cargo-target --output /path/to/new/artifacts
```

The builder obtains the release card from the compiled CLI. It makes one universal Darwin archive and one for each Linux architecture. The archive contains `swarm`, the release card, member digests, media pins, license and the architecture-specific helper tree. Use the same frozen toolchain, SDK, linker, and dependencies to compare repeat build digests. Inspect each Mach-O slice and ELF binary's minimum OS/kernel, architecture and linked libraries. Run both the terminal and packaged media checks on each target. The published archives require a separate Gatekeeper download trial on macOS. Ad hoc Mach-O signatures and Ed25519 archive signatures provide different guarantees.

## Sign and verify

Signing and verification use Python 3.10+ and OpenSSL 3 with Ed25519 support. Set `SWARM_OPENSSL` to its absolute path if your system's default command lacks it. Transfer the artifact set to your offline signing environment. Verify its hashes and compare them to the recorded build evidence before signing:

```sh
python3 scripts/release-rust.py sign /path/to/artifacts --private-key /offline/release-ed25519.pem
```

The signing command reads a PEM Ed25519 private key and writes the legacy wire format, base64 of the raw signature over `checksums.txt`. Use the trusted verifier from your independently authenticated source checkout and obtain the public key from a trusted channel. A verifier downloaded beside an archive cannot authenticate itself.

```sh
python3 scripts/release-rust.py verify /path/to/artifacts swarm-rust_0.1.0_darwin_all.tar.gz \
  --public-key /trusted/release-ed25519-public.pem
```

On each target, repeat verification with `--prefix /path/to/new/preview --check-media` to install the matching archive and run its codec, cipher and DAVE checks through the packaged executable's helper resolver. The destination parent must exist and the prefix must be new. Use a private parent owned by your test user. Linux asset suffixes are `linux_amd64` and `linux_arm64`. Use the installed `/path/to/new/preview/swarm` explicitly and a fresh isolated home and daemon state directory for testing. The installed application can run terminal workflows without loading Node. A call with no configured helper path uses the installed architecture-specific bundle. A configured absolute external helper path continues to select the explicit helper.

## First hop and rollback

This preview install command refuses any existing prefix, including a package-managed path or symlink. It creates no services, touches no provider or Swarm state, and leaves the old command on PATH. For a disposable preview, stop its own test processes and return to the previous explicit executable and state directory. Leave its directory in place if it contains evidence or test work you want to preserve.

The old Go updater looks for `swarm_<version>_darwin_all.tar.gz`, `swarm_<version>_linux_amd64.tar.gz`, or `swarm_<version>_linux_arm64.tar.gz`. It ignores media files even if presented with a compatible-looking archive. The Rust names intentionally yield no match for the old updater, including single-binary installations. Never alias the assets to the Go filenames. Verify actual stable and preview release metadata before publishing. Keep Brew, dpkg, rpm and Cargo ownership intact. A Rust source build from the frozen commit can be compiled with `cargo build --locked --release --bin swarm` and used from its target directory with separate state. Its default media bundle is absent until built and installed through the release tool. The historical Go source install stays pinned to its old release tag.

Promotion requires actual cask and package-owner transition rehearsals, service/writer custody and state rollback proof, a native PTY smoke on each architecture, an encrypted call using shipped bytes, and a Gatekeeper trial. Run them on disposable hosts as specified in the transition plan. A draft can be uploaded manually after qualification. Compare downloaded draft bytes, run the opt-in canary, and use the exact verified bytes for promotion and public redownload verification. The local tool does not publish releases or manage services.
