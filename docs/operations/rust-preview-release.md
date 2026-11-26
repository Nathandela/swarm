# Manual Rust preview release

The Rust preview uses separate `swarm-rust_…` archive names. Native managed releases use the signed `rust-native-preview` channel. The Go release channel and Homebrew tap continue to own existing installations. Keep the preview on a separate distribution channel and leave the Go release as the repository's latest stable release. Before making any public tag, inspect its commit and the destination's default branch for GitHub Actions release workflows. Use a disposable test installation until the state-transition gates in the [Rust transition plan](../specifications/rust-migration/transition.md) pass.

## Freeze and build

Use a clean, exact source commit, Cargo's locked dependency graph, and the compiler build pinned by `scripts/release-rust.py`. Have Python 3.11+, the Xcode command line tools with a Darwin SDK, both Darwin Rust targets, and both Linux musl Rust targets and their C compilers/linkers available. Record `rustc --version --verbose`, `xcodebuild -version` or the Command Line Tools and Apple Clang versions, `xcrun --show-sdk-version`, the linker versions, operating system versions, and the dependency/license review in the release evidence. Fetch Cargo dependencies before the frozen build.

Obtain the four official Node 24.17.0 archives for Darwin and Linux arm64 and x64 as `.tar.xz` files. Verify their upstream signed checksum list in the Node release channel independently. The builder compares each archive to the pinned digest. The extracted Node and its license ship with each target's lock-resolved npm dependencies. npm installation takes place in scratch with lifecycle scripts and binary links disabled. The application embeds the resulting media digests. The Linux application is built for musl, while the bundled Node and DAVE addon require a glibc-based system.

Obtain the [DAVE 0.2.0 source archive](https://codeload.github.com/Snazzah/davey/tar.gz/72d1c6d081f4993d618806990f1d3a1f6f53ed67)
as a separate build input. Its SHA-256 is pinned by the builder. Unpack a scratch copy and run
`cargo fetch --locked --manifest-path /path/to/davey-source/Cargo.toml` before the frozen build.
The builder includes notices for each locked Swarm and DAVE dependency, the upstream DAVE MIT license,
the locked DAVE source revision and Cargo lock, and the installed Rust standard-library notices.
Published npm assets retain their accompanying notices. The DAVE producer's exact compiler and
enabled-feature attestation still needs separate upstream evidence.

Run these gates locally:

```sh
python3 scripts/verification/test_release_rust.py
cargo fmt --all -- --check
cargo clippy --locked --workspace --all-targets -- -D warnings
cargo test --locked --workspace
python3 scripts/release-rust.py build \
  --source "$(git rev-parse HEAD)" --version 0.1.0 \
  --node-archives /path/to/verified/node-archives \
  --davey-source /path/to/davey-source.tar.gz \
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

## Managed Rust updates

Use `verify --prefix /path/to/new/preview --managed` with a native-channel archive for a self-updating preview. The installed updater is native Rust. Python and OpenSSL are only needed for offline packaging and bootstrap installation. The trusted public key is pinned at installation. Keep using `/path/to/new/preview/swarm` as the launcher and in user services. Existing unmanaged copies keep the installer, Brew, dpkg, rpm, Go or Cargo as their owner and are refused by self-update. Use a new private prefix if you explicitly choose a managed install. The previous Python-managed preview uses a separate channel and installation marker. Its administrator cannot switch a native installation, and native staging accepts native release cards only.

```sh
swarm upgrade --json --dist /path/to/local/signed-release
swarm upgrade --stage --dist /path/to/local/signed-release --check-media
swarm upgrade --activate
swarm upgrade status
swarm upgrade --rollback
```

A default check verifies the checksums signature and the checksum-covered `rust-native-preview.json` channel, version and source record. It reads those files from the dedicated native preview tag and changes no files. A missing or unpublished preview channel is an offline result. The updater never consults the stable Go latest endpoint. `--dist` selects a local signed artifact set. Point `SWARM_UPGRADE_DIST` at a previously obtained signed Rust artifact directory to skip the option on repeat runs. Stage verifies the pinned signature, selected archive and members, CLI identity and release ABI. Stage works with live processes. Activation requires the installation's exclusive writer lease and verified immutable generations. A single pointer switch installs the app and its matching media, with the prior generation recorded in that same route. An interrupted switch leaves either complete route addressable. `swarm upgrade --unattended` combines stage and safe activation for an explicit wrapper. It defers while managed writers are live. It neither launches services nor restarts a daemon. An older signed release needs `--allow-downgrade` for manual staging and cold activation.

A potentially effectful command creates a durable generation marker. After that, an older release may only replace the current one when the current signed build explicitly lists the target source in its qualified rollback sources. Session stores, provider data, worktrees and externally published effects remain in place. A killed shim, connector or schedule writer leaves an unfinished identity record. Status shows it, and activation and rollback defer until writer custody and any external effects have been reconciled offline. The current allowlist is empty pending a tested comparable reader pair, so rollback works before the new generation admits writes. The rollback route holds the abandoned release version until another release is selected. A read-only doctor, version or upgrade check does not mark potential effects.

## Go first hop and rollback

This preview install command refuses any existing prefix, including a package-managed path or symlink. It creates no services, touches no provider or Swarm state, and leaves the old command on PATH. For a disposable preview, stop its own test processes and return to the previous explicit executable and state directory. Leave its directory in place if it contains evidence or test work you want to preserve.

The old Go updater looks for `swarm_<version>_darwin_all.tar.gz`, `swarm_<version>_linux_amd64.tar.gz`, or `swarm_<version>_linux_arm64.tar.gz`. It ignores media files even if presented with a compatible-looking archive. The Rust names intentionally yield no match for the old updater, including single-binary installations. Never alias the assets to the Go filenames. Verify actual stable and preview release metadata before publishing. Keep Brew, dpkg, rpm and Cargo ownership intact. A Rust source build from the frozen commit can be compiled with `cargo build --locked --release --bin swarm` and used from its target directory with separate state. Its default media bundle is absent until built and installed through the release tool. The historical Go source install stays pinned to its old release tag.

Promotion requires actual cask and package-owner transition rehearsals, service/writer custody and state rollback proof, a native PTY smoke on each architecture, an encrypted call using shipped bytes, and a Gatekeeper trial. Run them on disposable hosts as specified in the transition plan. A draft can be uploaded manually after qualification. Compare downloaded draft bytes, run the opt-in canary, and use the exact verified bytes for promotion and public redownload verification. The local tool does not publish releases or manage services.
