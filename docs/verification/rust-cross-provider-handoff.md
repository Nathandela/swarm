# Cross-provider hands-off handoff

The keyboard board handed a live Claude Code 2.1.329 session to Codex 0.172.0 in the same physical, non-Git checkout. Both installed CLIs completed a real native turn against separate synthetic inference endpoints. The source used its authenticated Claude hook and wrote the original turn to its native transcript. The destination's opening user message named that transcript, the Claude conversation UUID, the source swarm row and provider, and the checkout. It omitted the original user text. The destination rendered its local model's reply.

The board named Claude and Codex in its confirmation. Cancellation and an unconfirmed opening produced no successor or destination request. Confirming created one Codex row with handoff lineage and empty supervision. An unnegotiated request, unknown source, symlink-substituted transcript and a different directory inode at the same checkout path all refused before reservation. The source's owner and user turns stayed intact. The original Claude session then accepted a new input into the same native conversation without a restart.

The reference is Go v0.29.24. Its board prompts before a cross-provider disclosure. Its owner binds the source's directory and five transcript pointers, replaces the caller's opening prompt, records local lineage and leaves the source running. The embedded Go and Rust templates compared byte for byte. Rust also pins the physical checkout identity at reservation. This comparison used the Go implementation and template. The two-provider native run used the Rust owner. [Go owner](https://github.com/Nathandela/swarm/blob/v0.29.24/internal/skeleton/api.go) · [Go board](https://github.com/Nathandela/swarm/blob/v0.29.24/internal/tui/handoff.go)

| Host | Build and boundary |
|---|---|
| tf_nathan Linux x86-64 | Unmodified Rust source at `3d15e157b9c064d888e046560d707e562a163843`. Private PID, mount and network namespaces with loopback only. |
| macOS arm64 | Same source with a fixture-only deterministic ID for the Codex successor. Seatbelt allows just the scratch root, the two model ports and the single derived Codex Unix listener and lock. |

Scratch profiles used synthetic credentials. Both fixtures reaped all their processes. No existing session, provider endpoint or Discord destination participated. The reverse direction covered a live Codex source and a Claude receiving stub. [Codex source witness](rust-codex-resume.md#hands-off-from-a-live-codex-session)

On macOS, point the launcher at the resolved binaries. Its temporary source archive fixes only the Codex successor's row ID so the sandbox can allow the exact redirected Unix socket.

```sh
scripts/verification/run-macos-live-go-codex.sh "$codex" --cross-provider "$claude"
```

On Linux, stage the candidate as `swarm` alongside the witness, `native_fixture_sandbox.py`, `parity_codex.py`, `parity_runtime.py`, `parity_ipc.py`, `rust_claude_worker_input.py`, and `parity/swarm-parity`. Run the launcher with the resolved installed binaries.

```sh
scripts/verification/provider_native_linux.sh "$fixture" "$claude" rust_cross_provider_handoff.py hands-off "$codex"
```
