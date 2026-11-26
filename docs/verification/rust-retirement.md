# Product source retirement

The active application builds from the Cargo workspace. The Rust crate owns the dispatcher guide and skill text, the board handoff template, the Claude native input plugins and the OpenCode UI bridge. The pinned Discord media helper lives under `services/discord-voice`. The `.go` files are historical Hermes and mailbox test helpers. The `.go.fixture` sources build against a separate pinned Go reference for interoperability tests.

Removed from the product branch were the Go application, account switching and login workers, Android and iOS applications, phone and standalone voice control, relay and push services, mobile infrastructure deployment and their publishing scripts. The active shipping comparison is signed Go v0.29.24 at `a716dd50a5faf746ce6f0831fc31d0d73b06ffeb`; the independent Discord-call comparison remains pinned at `da7b9f2dfa7e2c6960bd860a72459c0448845c82`. Historical compatibility fixtures intentionally stay pinned to Go v0.29.22 at `f26648d69086e329132ac7849954d2f29a895387`. Source retirement leaves deployed services, installation files, credentials and operator state in their existing custody.

Run the local checks from the candidate checkout.

```sh
cargo test --locked --workspace
python3 scripts/verification/retired_surfaces.py target/debug/swarm
node --test crates/swarm/assets/opencode/tui.test.mjs
node crates/swarm/assets/claude/plugin_lifecycle_test.mjs crates/swarm/assets/claude/plugin.js
python3 docs/specifications/rust-migration/inventory/generate.py --check
python3 scripts/verification/build_rust_references.py /tmp/swarm-oracles-new
```

On 26 November 2026 the signed `75546e1d` archives passed the retirement witness on Darwin arm64 and Linux x86_64. Each fresh supervisor refused the former mobile, relay, voice app and account-management requests. Discord call help and local owner subscription remained available. Five packaging tests and the inventory check passed. The macOS managed archive also passed its installed media check in private scratch.

The fresh-install probe sends the former remote, pairing, policy, activity and managed-login capability offers over the actual owner socket. It checks that only the requested retained subscription is negotiated, that obsolete command invocations do not create state, and that the Discord call help is still available. The Rust-only checkout passed all 118 ordinary Rust tests on native macOS arm64 and Linux x86_64, the two provider plugin suites, the release-verifier tests, strict Clippy and the pinned inventory check. Both historical Go executables rebuilt from Git and passed their version and Discord surface checks. Release qualification for the complete application is tracked in the [fleet ledger](rust-migration.md).

The local release tool linked four optimized architecture slices from frozen source `73612d56` and built three signed preview archives using a disposable test key. The universal macOS archive SHA-256 was `c77da6385ada32bacd9e1f95614b88f8c3a08854d370c05b3a3c9e6a3350dbf8`, Linux x86_64 was `3d25bbeb1b86660c7443ab703c7f2a7c071d10c6a815690846b2d83038bc2053`, and Linux arm64 was `9582f9df9a2f4c159b9980c19a7807b3c67db7d4871b5ab5f95b2094e2ddad70`. The macOS installation passed a native board attach/reattach trial and both arm64 and translated x86_64 passed bundled media resolution. On Ubuntu, using umask 0002 initially produced group-writable nested media directories and the packaged helper refused to start. Installer correction `85feef29` explicitly creates each directory with mode 0700. Using that verifier on the same signed x86_64 archive under umask 0002 passed media resolution and native board attach/reattach. Linux arm64 archive signature and membership were verified. [Manual release procedure](../operations/rust-preview-release.md).

The refreshed four-target RustSec scan at `adb247c9` reported one informational maintenance advisory. `paste` 1.0.15 is a compile-time macro reached through `fnv_rs` 0.4.4 and `bbolt-rs` 1.3.10, the library used to retain Go-format state. Beads `agents-tracker-8fp1.11.1` tracks its release disposition and native ledger regression evidence. Scan log SHA-256 is `7cb1931eb7cd4b64f06db6c78f204c21289d4229958d8a31da4bc2e2509c85ed`. [RustSec advisory](https://rustsec.org/advisories/RUSTSEC-2024-0436.html)

With pinned Node 24.17.0 on macOS arm64 and Linux x86_64, `adb247c9` passed the Claude plugin lifecycle and 162 OpenCode bridge tests per host. The three separate transformed-source admission cases passed through the explicit Rust opt-in on each native host. Combined Node log SHA-256 is `6ee724ba569b13e2968ccde8b3ced53a744b9167dd0bd99c9243fd6a2548e754` on macOS and `f8ba557321422c890f6fd60062cf0f1877f467783c52c0f487dfad80e61ae601` on Linux.
