# Rust preview release rehearsal

Built the opt-in preview from `e6afc69682a15923db4355fe48e4ce6b5c3ff92b` at package version `0.1.0`. This was a local release-lane rehearsal with a disposable Ed25519 key. Qualification of the integrated candidate requires a new build at its own frozen SHA. The archived preview, scripts and receipt do not change the public Go release or the Homebrew tap.

## Frozen artifacts

| Asset | Size in bytes | SHA-256 | Regular members |
|---|---:|---|---:|
| `swarm-rust_0.1.0_darwin_all.tar.gz` | 92,329,981 | `677d8b7ff8c0cc901c0557c383866cdddfa8963957bc5f935504bb5776be98a0` | 2,911 |
| `swarm-rust_0.1.0_linux_amd64.tar.gz` | 51,177,888 | `49ffeef6593402301154c358c65d85a20da36c6c84639e4cf33b56f2d7b5c359` | 1,458 |
| `swarm-rust_0.1.0_linux_arm64.tar.gz` | 50,795,226 | `17e60acc49ebba1822248cde7e5d766b4f1c72ad88120177e785d35d6f7620ca` | 1,458 |

The three signed archives contain one Rust app each. The manifest hashes the regular members, including the compatibility card and the platform media files. The embedded media-manifest SHA-256 is `d16ac740c25afe3ba4f22c060c560c4aad7274ed5824e9f6ebe06ff62ac7cc00`. The compiled app digests are `e5d55f4503032e07799259eb53f260bab536c708274d287f3e5141d0851765b1` for Darwin, `c6df13c9dad5a7146342a1903cccd6a6bc9fd2c3954b96652c7c0a5c0f4f4d15` for Linux amd64 and `a4837fc4b299b87e9303c05805c40cc403278153a05f737ce47af528adac1984` for Linux arm64.

The active preview build has one Cargo binary target and zero GitHub Actions workflows. The package inventories have no Go app, remote gateway, push gateway, mobile installer, account-rotation tool, standalone voice app or upgrade service unit. The retired GoReleaser build graph, Homebrew spec and upgrader unit templates remain available through the pinned Go history. Historical Go release assertions that expected those files belong to that history.

Environment was Apple Silicon macOS 27.2.1 build 26B131, Apple Clang 15.0.0, Darwin SDK 14.4, Python 3.11.2, Rust 1.99.1 `58cf8f9c35b355199727627d07ae34a4e2846d67` and LLVM 23.1.1. Cargo linked Darwin with the Apple tools, compiled musl C objects with Zig 0.17.1 and linked Linux with the Rust toolchain's LLD. The Linux linker adapter dropped C driver flags `-m64`, `-nostartfiles` and `-nodefaultlibs`, unpacked `-Wl,` flags, and retained `--fix-cortex-a53-843419`. Rust's x86_64 musl link retried as static when the driver lacked `-static-pie`. The command used the documented `python3 scripts/release-rust.py build` with external `CARGO_TARGET_*_LINKER`, `CC_*` and `AR_*` set to those tools.

Node 24.17.0's upstream checksums verified under the project-published release signer `5BE8A3F6C8A5C01D106C0AD820B1A390B168D356`. All four Node archive digests matched. Each media bundle contains the main Node executable, Node license, the original helper source and dependency lock, and its lock-resolved production dependencies. [Node archive checksums](https://nodejs.org/dist/v24.17.0/SHASUMS256.txt) · [Node release keys](https://github.com/nodejs/release-keys)

The second build used fresh npm and packaging scratch, reused the Cargo dependency cache and recompiled all four app targets. All three archives, the installer, its instructions and the checksum document matched byte for byte. The freeze also refused both a wrong SHA and a package-version mismatch before creating any artifact directory.

## Installed-byte checks

| Target | Observation |
|---|---|
| Darwin arm64, native | Installed from the signed universal archive with `verify --prefix … --check-media`. The bundled Node, DAVE, cipher and Opus check passed. `version` reported 0.1.0 (Rust). An absent-daemon `doctor --json` kept the state directory absent. The board witness launched, attached and resized a worker, killed the daemon, reattached to the same process and restored terminal modes. |
| Darwin x86_64, Rosetta | `arch -x86_64` ran the installed universal app's x86_64 version and verified its pinned x86_64 media. The board witness also passed when launched from a translated Python process. Native Intel macOS hardware was unavailable. |
| Linux x86_64, native | On Ubuntu kernel 5.15.0-203, glibc 2.35 and Python 3.10.12, the signed native install passed the packaged helper check, version, read-only doctor and the same PTY/process-identity witness. |
| Linux arm64, QEMU | Archive signature and member digests passed. The released Rust executable reported 0.1.0 and the shipped Node/helper's direct codec, cipher and DAVE check passed under QEMU 6.2.0 with signature-verified Ubuntu Base 22.04.5 ARM64 userspace. The PRoot attempt at the Rust-to-Node resolver stopped on its ownership check. Native install, resolver and PTY execution remain open. |

Both Darwin app slices declare macOS 12.0 and link `libSystem.B.dylib` and `libiconv.2.dylib`. The ad hoc signature passed `codesign --verify --strict --all-architectures`. Both Node slices declare macOS 13.5 and use system CoreFoundation, Security, libc++ and libSystem. Both Linux app binaries are static ELF with no dynamic section. Linux Node and DAVE require the system loader, glibc and standard C++ runtime. Node's dynamic symbols reach GLIBC 2.28. The declared minimum versions were inspected in headers and versioned symbols. Native execution at those floors remains open.

A wrong Ed25519 key and a flipped archive byte each refused before creating an installation prefix. The isolated Rust process refused both a modified installed helper dependency and a missing helper dependency. Its terminal executable still reported its version. The Python archive tests also reject missing/wrong helper files, a rehashed compatibility card with the wrong version, a retired executable and traversal. Rust formatting and Clippy with warnings denied passed, as did 92 default tests with two qualified environment tests ignored. The four-target RustSec audit reported an existing unmaintained `paste` proc-macro through `bbolt-rs`. npm's production lock audit returned zero advisories. The Rust crate-license inventory was read from Cargo metadata. Distribution review of all third-party notices remains open. [RustSec advisory](https://rustsec.org/advisories/RUSTSEC-2024-0436)

## Go first hop

On 2026-11-26 the public latest release and the published cask both reported stable Go v0.29.22. The v0.29.22 Darwin and Linux amd64 archive bytes matched their Ed25519-signed checksums under the pinned Go release key. The Darwin archive contains `swarm`, `swarm-remote`, `compat.json` and README. The old Go extractor selects bare app, remote and card members, so extending that archive would omit the media assets in an unattended update. [Published release](https://github.com/Nathandela/swarm/releases/tag/v0.29.22) · [Published cask](https://github.com/Nathandela/homebrew-swarm/blob/HEAD/Casks/swarm.rb) · [Pinned Go resolver](https://github.com/Nathandela/swarm/blob/v0.29.22/internal/upgrade/resolve.go)

A disposable local release server redirected the old upgrade implementation to a hypothetical higher version with preview-prefixed assets. A fixture with only the old Go executable requested the legacy filename, received 404 and failed download without any staged executable. The preview verifier independently refused to install over a private prefix containing the genuine v0.29.22 executable. Its byte hash stayed identical and invoking it explicitly reported Go v0.29.22. Installing the Rust candidate to a new prefix is the verified opt-in route. Returning to the prior executable and state directory is the verified rollback for that isolated preview, before it writes to shared state.

Production signing, native Intel and Linux arm64 qualification, downloaded Gatekeeper behavior, a live encrypted Discord call from these archive bytes, Brew/dpkg/rpm transitions, real updater/timer/daemon writer custody, and restoration of post-cutover work all remain promotion gates. No public tag, release, tap, system service or production state was changed.
