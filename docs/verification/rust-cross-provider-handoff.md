# Cross-provider hands-off handoff

The keyboard board passed hands-off in both directions between installed Claude Code 2.1.329 and Codex 0.172.0 on the same physical, non-Git checkout. Each run used an authenticated live source and a real native turn at each provider, with separate synthetic inference endpoints. The destination's opening user message carried the exact five pointers to the source transcript, provider conversation UUID, checkout, provider and swarm row. All destination user-content parts omitted the original user's text and the source model's reply. The destination rendered its local model's reply. In the Codex-to-Claude run the Claude hook identified a distinct native conversation whose transcript recorded the opening user turn under the source's checkout.

The board named Claude and Codex in its confirmation. Cancellation and an unconfirmed opening produced no successor or destination request. Confirming created one successor row with handoff lineage and empty supervision. An unnegotiated request, unknown source, symlink-substituted transcript and a different directory inode at the same checkout path all refused before reservation. The source's owner and user turns stayed intact. The original session then accepted a new input into the same native conversation and sent it to its loopback endpoint.

Go v0.29.24 also passed both provider directions on the same two native hosts. Pressing `n` and reopening the confirmation produced no successor or destination model request. The confirmed native turn contained the same five pointers and left the original session identity and transcript in use for a follow-up. The embedded Go and Rust templates compared byte for byte. The Rust owner additionally verified the physical checkout identity before reservation. The first Linux Go observer waited for the Rust picker glyph, then waited for the full target name after a Go incremental repaint that emitted only its changed suffix. Both observations were corrected by checking Go's provider-naming disclosure and the actual destination request. The visual form and disclosure styling are a separate board comparison. [Go owner](https://github.com/Nathandela/swarm/blob/v0.29.24/internal/skeleton/api.go) · [Go board](https://github.com/Nathandela/swarm/blob/v0.29.24/internal/tui/handoff.go)

| Host | Build and boundary |
|---|---|
| tf_nathan Linux x86-64 | Unmodified Rust source at `3d15e157b9c064d888e046560d707e562a163843`. Private PID, mount and network namespaces with loopback only. |
| macOS arm64 | Same source with a fixture-only deterministic ID for the Codex row. Seatbelt restricts writes to scratch state and the single derived Codex listener and lock. Network access is limited to the fixture Unix sockets and two local model ports. |

| Go v0.29.24 oracle | Executable SHA-256 | Two-direction log SHA-256 |
| --- | --- | --- |
| Signed Linux x86_64 release | `3eac509227792e1ef4812860ea74a7d3c56f9a2ade5a8eff26b30b6f4861f6d8` | `e8196373887abd937f29da50ba43f28444c4a9e03e74389b28a97a78c44cb79e` |
| Darwin arm64 source build with one fixed Codex fixture ID for the exact Seatbelt listener | `f3e6e63da8e51b75c8ae0e1e3e1cc9aac694faece5e98b078aecf2b8622b8bfa` | `af7fe05aa069f80df1308bff3d69a5c01dfed97fea8fff9882d50ae27090fcba` |

Scratch profiles used synthetic credentials. Both fixtures reaped all their processes. No existing session, hosted provider endpoint or Discord destination participated. The earlier Codex continuity test separately covered a real Codex source and a Claude receiving stub during owner restart and subsequent Codex resume. [Codex source witness](rust-codex-resume.md#hands-off-from-a-live-codex-session)

## Paired handoff form and consent

The `62a1a28c` board lane compared the signed Go v0.29.24 board and Rust against the same Go owner on each native host. All 49,028 styled cells in 24 frames matched across wide, compact and tiny geometry. Visible terminal modes matched. The raw receipts retain 15 differences in hidden cursor coordinates per host. They cover initial focus, target cycling, model suggestions, supervision and method changes, the running-checkout warning, the provider-naming disclosure, scrolling to read it, cancel, and the compact reading reset on target change.

Rust credits approval only after the current readable decision is painted. A buffered `y` before that paint, a resize, an unreconciled older reading position after a source rename, or a changed target cannot approve the prior decision. The native Rust fixture repeated both provider directions on each host with the same production checks and zero survivors. The Mac binary used its fixture-only deterministic Codex ID for Seatbelt.

| Evidence | SHA-256 |
| --- | --- |
| macOS paired grid | `55cd27697d7c5619bf7164d375dec4359c5f50cb8976254dd578cf1cfdd9b8f0` |
| Linux paired grid | `9e37fa5ee51beaa452a9fcd5a1881f79bdc2310dddd39fad82a144da00e419ff` |
| Both native provider directions on both hosts | `5d785e1ded9b58b7104e8b9612ad96f2f3c4a866081c4cd55325f2b33bcbaf1e` |

On macOS, point the launcher at the resolved binaries. Its temporary source archive fixes only the Codex row ID so the sandbox can allow the exact redirected Unix socket. The patched fixture executable builds into the archive's private target directory.

```sh
sh scripts/verification/run-macos-live-go-codex.sh "$codex" --cross-provider "$claude"
sh scripts/verification/run-macos-live-go-codex.sh "$codex" --cross-provider-codex "$claude"
```

On Linux, stage the candidate as `swarm` alongside the witness, `native_fixture_sandbox.py`, `parity_codex.py`, `parity_runtime.py`, `parity_ipc.py`, `rust_claude_worker_input.py`, and `parity/swarm-parity`. Run the launcher with the resolved installed binaries.

```sh
sh scripts/verification/provider_native_linux.sh "$fixture" "$claude" rust_cross_provider_handoff.py hands-off "$codex"
sh scripts/verification/provider_native_linux.sh "$fixture" "$claude" rust_cross_provider_handoff.py hands-off-codex "$codex"
```
