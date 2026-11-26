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

The fresh-install probe sends the former remote, pairing, policy and activity capability offers over the actual owner socket. It checks that only the requested retained subscription is negotiated, that obsolete command invocations do not create state, and that the Discord call help is still available. The Rust-only checkout passed all 118 ordinary Rust tests on native macOS arm64, the two provider plugin suites, the release-verifier tests, strict Clippy and the pinned inventory check. Both historical Go executables rebuilt from Git and passed their version and Discord surface checks. Release qualification for the complete application is tracked in the [fleet ledger](rust-migration.md).
