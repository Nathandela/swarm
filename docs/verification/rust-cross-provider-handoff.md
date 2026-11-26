# Cross-provider hands-off handoff

The keyboard board passed hands-off in both directions between installed Claude Code 2.1.329 and Codex 0.172.0 on the same physical, non-Git checkout. Each run used an authenticated live source and a real native turn at each provider, with separate synthetic inference endpoints. The destination's opening user message carried the exact five pointers to the source transcript, provider conversation UUID, checkout, provider and swarm row. All destination user-content parts omitted the original user's text and the source model's reply. The destination rendered its local model's reply. In the Codex-to-Claude run the Claude hook identified a distinct native conversation whose transcript recorded the opening user turn under the source's checkout.

The board named Claude and Codex in its confirmation. Cancellation and an unconfirmed opening produced no successor or destination request. Confirming created one Codex row with handoff lineage and empty supervision. An unnegotiated request, unknown source, symlink-substituted transcript and a different directory inode at the same checkout path all refused before reservation. The source's owner and user turns stayed intact. The original session then accepted a new input into the same native conversation and sent it to its loopback endpoint.

The reference is Go v0.29.24. Its board prompts before a cross-provider disclosure. Its owner binds the source's directory and five transcript pointers, replaces the caller's opening prompt, records local lineage and leaves the source running. The embedded Go and Rust templates compared byte for byte. Rust also pins the physical checkout identity at reservation. This comparison used the Go implementation and template. The two-provider native run used the Rust owner. [Go owner](https://github.com/Nathandela/swarm/blob/v0.29.24/internal/skeleton/api.go) · [Go board](https://github.com/Nathandela/swarm/blob/v0.29.24/internal/tui/handoff.go)

| Host | Build and boundary |
|---|---|
| tf_nathan Linux x86-64 | Unmodified Rust source at `3d15e157b9c064d888e046560d707e562a163843`. Private PID, mount and network namespaces with loopback only. |
| macOS arm64 | Same source with a fixture-only deterministic ID for the Codex row. Seatbelt restricts writes to scratch state and the single derived Codex listener and lock. Network access is limited to the fixture Unix sockets and two local model ports. |

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
