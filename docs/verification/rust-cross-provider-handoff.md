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
