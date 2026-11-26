# Product source retirement

The active application builds from the Cargo workspace. The Rust crate owns the dispatcher guide and skill text, the board handoff template, the Claude native input plugins and the OpenCode UI bridge. The pinned Discord media helper remains under `services/discord-voice`. The only Go source in the candidate tree consists of two historical test helpers, for Hermes native admission and Go mailbox interoperability. Both run against the separately pinned Go source.

Removed from the product branch were the Go application, account switching and login workers, Android and iOS applications, phone and standalone voice control, relay and push services, mobile infrastructure deployment and their publishing scripts. Operators can still inspect the exact original source and its release evidence through the [Go reference tree](https://github.com/Nathandela/swarm/tree/f26648d69086e329132ac7849954d2f29a895387) and the [Discord call reference](https://github.com/Nathandela/swarm/tree/da7b9f2dfa7e2c6960bd860a72459c0448845c82). Source retirement leaves deployed services, installation files, credentials and operator state in their existing custody.

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
